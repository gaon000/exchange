// civerify rejects incomplete or skipped CI test runs using go list and go test JSON.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const module = "github.com/gaon000/exchange/"
const childHelper = module + "internal/snapshot/TestSnapshotPublisherChild"

type packageInfo struct {
	Dir, ImportPath           string
	TestGoFiles, XTestGoFiles []string
}

type event struct {
	Action, Package, Test string
}

type expectations map[string]map[string]bool

func main() {
	mode := flag.String("mode", "", "portable or native")
	packages := flag.String("packages", "", "go list -json ./... output")
	events := flag.String("events", "", "go test -json ./... output")
	flag.Parse()
	if err := run(*mode, *packages, *events); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(mode, packages, events string) error {
	if flag.NArg() != 0 || mode != "portable" && mode != "native" {
		return errors.New("expected -mode portable|native -packages file -events file")
	}
	f, err := os.Open(packages)
	if err != nil {
		return err
	}
	defer f.Close()
	expected, err := discover(f)
	if err != nil {
		return err
	}
	if err := requireCoverage(expected, mode); err != nil {
		return err
	}
	log, err := os.Open(events)
	if err != nil {
		return err
	}
	defer log.Close()
	passed, skipped, err := verify(log, expected)
	if err == nil {
		fmt.Printf("Verified %d tests across %d packages; %d explicit child-helper skip.\n", passed, len(expected), skipped)
	}
	return err
}

// Discover only test files selected by go list for the current GOOS/CGO mode.
// AST inspection avoids a hardcoded count that silently ignores new packages.
func discover(r io.Reader) (expectations, error) {
	result := expectations{}
	decoder := json.NewDecoder(r)
	for {
		var p packageInfo
		if err := decoder.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if p.ImportPath == "" || result[p.ImportPath] != nil {
			return nil, errors.New("empty or repeated package in manifest")
		}
		tests := map[string]bool{}
		for _, name := range append(p.TestGoFiles, p.XTestGoFiles...) {
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(p.Dir, name), nil, 0)
			if err != nil {
				return nil, err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv != nil || !isTest(fn.Name.Name) {
					continue
				}
				tests[fn.Name.Name] = true
			}
		}
		result[p.ImportPath] = tests
	}
	if len(result) == 0 {
		return nil, errors.New("empty package manifest")
	}
	return result, nil
}

func isTest(name string) bool {
	if name == "TestMain" || !strings.HasPrefix(name, "Test") {
		return false
	}
	r, _ := utf8.DecodeRuneInString(strings.TrimPrefix(name, "Test"))
	return !unicode.IsLower(r)
}

// These anchors prevent a wrong build mode or a missing integration package from
// passing merely because every remaining test succeeded. All discovered tests,
// not just these anchors, must subsequently run and pass.
func requireCoverage(expected expectations, mode string) error {
	required := map[string]string{
		"internal/wal":       "TestUnsupportedStorageDoesNotCreateFiles",
		"internal/snapshot":  "TestUnsupportedPublicationDoesNotCreateFiles",
		"cmd/exchange":       "TestInitUnsupportedDoesNotCreateRun",
		"tools/storageprobe": "TestUnsupportedPlatformFailsClosed",
	}
	if mode == "native" {
		required = map[string]string{
			"internal/wal":       "TestNativeStore",
			"internal/snapshot":  "TestNativeProcessKillAtPublicationBoundaries",
			"internal/runtime":   "TestNativeHTTPProcessBoundaryRecovery",
			"internal/api":       "TestNativeHTTPTradingSnapshotsAndRestart",
			"cmd/exchange":       "TestNativeCLIProcessKillRecoversAcknowledgedResult",
			"cmd/loadgen":        "TestNativeCLIProducesReplayableHTTPArtifacts",
			"cmd/experiment":     "TestNativeExperimentBundleAndFailureCleanup",
			"tools/storageprobe": "TestProbePreservesParent",
		}
	}
	for pkg, test := range required {
		if !expected[module+pkg][test] {
			return fmt.Errorf("missing %s coverage: %s/%s", mode, pkg, test)
		}
	}
	return nil
}

func verify(r io.Reader, expected expectations) (passed, skipped int, err error) {
	running, terminal, packages := map[string]bool{}, map[string]string{}, map[string]string{}
	decoder := json.NewDecoder(r)
	for {
		var e event
		if err := decoder.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, 0, err
		}
		key := e.Package + "/" + e.Test
		switch e.Action {
		case "fail", "build-fail":
			return 0, 0, fmt.Errorf("failed event: %s", key)
		case "run":
			running[key] = true
		case "pass", "skip":
			if e.Test == "" {
				packages[e.Package] = e.Action
				continue
			}
			if e.Action == "skip" && key != childHelper {
				return 0, 0, fmt.Errorf("unexpected skipped test: %s", key)
			}
			terminal[key] = e.Action
		}
	}
	for pkg, tests := range expected {
		if packages[pkg] != "pass" && !(len(tests) == 0 && packages[pkg] == "skip") {
			return 0, 0, fmt.Errorf("package did not complete: %s", pkg)
		}
		for test := range tests {
			key := pkg + "/" + test
			if !running[key] || terminal[key] != "pass" && !(key == childHelper && terminal[key] == "skip") {
				return 0, 0, fmt.Errorf("test did not run and complete: %s", key)
			}
			if terminal[key] == "skip" {
				skipped++
			} else {
				passed++
			}
		}
	}
	if passed == 0 {
		return 0, 0, errors.New("no passing tests")
	}
	return passed, skipped, nil
}
