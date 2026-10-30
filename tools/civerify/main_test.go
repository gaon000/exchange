package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func events(items ...event) *bytes.Buffer {
	var b bytes.Buffer
	for _, e := range items {
		_ = json.NewEncoder(&b).Encode(e)
	}
	return &b
}

func TestVerifyCompleteRun(t *testing.T) {
	expected := expectations{"p": {"TestOne": true}, "empty": {}}
	log := events(
		event{"run", "p", "TestOne"},
		event{"run", "p", "TestOne/subtest"},
		event{"pass", "p", "TestOne/subtest"},
		event{"pass", "p", "TestOne"},
		event{"pass", "p", ""},
		event{"skip", "empty", ""},
	)
	passed, skipped, err := verify(log, expected)
	if err != nil || passed != 1 || skipped != 0 {
		t.Fatalf("passed=%d skipped=%d error=%v", passed, skipped, err)
	}
}

func TestVerifyRejectsMissingFailedAndSkippedEvidence(t *testing.T) {
	expected := expectations{"p": {"TestOne": true}}
	cases := map[string][]event{
		"empty":              {},
		"package only":       {{"pass", "p", ""}},
		"test not finished":  {{"run", "p", "TestOne"}, {"pass", "p", ""}},
		"package unfinished": {{"run", "p", "TestOne"}, {"pass", "p", "TestOne"}},
		"cached/no run":      {{"pass", "p", "TestOne"}, {"pass", "p", ""}},
		"skipped package":    {{"skip", "p", ""}},
		"skipped test":       {{"run", "p", "TestOne"}, {"skip", "p", "TestOne"}, {"pass", "p", ""}},
		"skipped subtest":    {{"skip", "p", "TestOne/unsupported"}, {"pass", "p", "TestOne"}, {"pass", "p", ""}},
		"failed subtest":     {{"fail", "p", "TestOne/bad"}, {"pass", "p", "TestOne"}, {"pass", "p", ""}},
		"failed package":     {{"run", "p", "TestOne"}, {"pass", "p", "TestOne"}, {"fail", "p", ""}},
		"build failure":      {{"build-fail", "p", ""}},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := verify(events(items...), expected); err == nil {
				t.Fatal("incomplete or failed evidence accepted")
			}
		})
	}
	complete := events(event{"run", "p", "TestOne"}, event{"pass", "p", "TestOne"}, event{"pass", "p", ""})
	complete.WriteString(`{"Action":`)
	if _, _, err := verify(complete, expected); err == nil {
		t.Fatal("truncated trailing JSON accepted")
	}
}

func TestVerifyOnlyAllowsTheExactChildHelperSkip(t *testing.T) {
	pkg := module + "internal/snapshot"
	expected := expectations{pkg: {"TestSnapshotPublisherChild": true, "TestNativeProcessKillAtPublicationBoundaries": true}}
	for _, helper := range []string{"TestSnapshotPublisherChild", "TestSnapshotPublisherChild/hidden"} {
		log := events(
			event{"run", pkg, "TestSnapshotPublisherChild"}, event{"skip", pkg, helper},
			event{"run", pkg, "TestNativeProcessKillAtPublicationBoundaries"},
			event{"pass", pkg, "TestNativeProcessKillAtPublicationBoundaries"}, event{"pass", pkg, ""},
		)
		passed, skipped, err := verify(log, expected)
		if helper == "TestSnapshotPublisherChild" {
			if err != nil || passed != 1 || skipped != 1 {
				t.Fatalf("helper exception: %d %d %v", passed, skipped, err)
			}
		} else if err == nil {
			t.Fatal("exception extended to a different test")
		}
	}
}

func TestDiscoverSelectedFilesAndRejectBadManifest(t *testing.T) {
	dir := t.TempDir()
	source := `package example
func TestMain() {}
func TestInternal() {}
func TestlowerHelper() {}
func FuzzInput() {}
type T struct{}
func (T) TestMethod() {}
`
	if err := os.WriteFile(filepath.Join(dir, "selected_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "external_test.go"), []byte("package example_test\nfunc TestExternal() {}"), 0600); err != nil {
		t.Fatal(err)
	}
	var manifest bytes.Buffer
	p := packageInfo{Dir: dir, ImportPath: "example", TestGoFiles: []string{"selected_test.go"}, XTestGoFiles: []string{"external_test.go"}}
	if err := json.NewEncoder(&manifest).Encode(p); err != nil {
		t.Fatal(err)
	}
	data := manifest.String()
	got, err := discover(strings.NewReader(data))
	if err != nil || len(got["example"]) != 2 || !got["example"]["TestInternal"] || !got["example"]["TestExternal"] {
		t.Fatalf("selected tests: %v, %v", got, err)
	}
	for _, invalid := range []string{"", "{}", "{", data + data, data + "{"} {
		if _, err := discover(strings.NewReader(invalid)); err == nil {
			t.Fatalf("invalid manifest accepted: %q", invalid)
		}
	}
	if err := os.Remove(filepath.Join(dir, "selected_test.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(strings.NewReader(data)); err == nil {
		t.Fatal("missing source accepted")
	}
}

func TestCoverageRequiresModeSpecificIntegrationTests(t *testing.T) {
	portable := expectations{
		module + "internal/wal":       {"TestUnsupportedStorageDoesNotCreateFiles": true},
		module + "internal/snapshot":  {"TestUnsupportedPublicationDoesNotCreateFiles": true},
		module + "cmd/exchange":       {"TestInitUnsupportedDoesNotCreateRun": true},
		module + "tools/storageprobe": {"TestUnsupportedPlatformFailsClosed": true},
	}
	if err := requireCoverage(portable, "portable"); err != nil {
		t.Fatal(err)
	}
	if err := requireCoverage(portable, "native"); err == nil {
		t.Fatal("portable tests accepted as native evidence")
	}
	delete(portable, module+"internal/wal")
	if err := requireCoverage(portable, "portable"); err == nil {
		t.Fatal("missing fail-closed coverage accepted")
	}
}
