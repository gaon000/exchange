package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

func TestCandidateSelectionAndLoadBoundaries(t *testing.T) {
	doc, _, wire := fixture(t)
	dir := t.TempDir()
	for _, name := range []string{".snapshot-leftover.tmp", "genesis.bin", "commands.wal", "snapshot-not-a-candidate.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("ignored"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, seq := range []int{2, 10, 3} {
		name, _ := Filename(d.Int(seq))
		if err := os.WriteFile(filepath.Join(dir, name), wire, 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidates, err := List(dir)
	if err != nil || len(candidates) != 3 || candidates[0].Seq != 10 || candidates[1].Seq != 3 || candidates[2].Seq != 2 {
		t.Fatalf("candidate selection: %+v / %v", candidates, err)
	}
	got, err := Load(dir, candidates[1], doc.Identity, 3)
	if err != nil || !reflect.DeepEqual(got, doc) {
		t.Fatalf("load: %v", err)
	}
	if _, err := Load(dir, candidates[0], doc.Identity, 10); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("filename mismatch: %v", err)
	}
	if _, err := Load(dir, Candidate{Name: "../outside", Seq: 3}, doc.Identity, 3); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid filename admitted")
	}
	if _, err := Load(dir, candidates[1], doc.Identity, 2); !errors.Is(err, ErrIdentity) {
		t.Fatalf("snapshot past retained WAL: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "snapshot-3.bin"), wire, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("ambiguous filename silently ignored: %v", err)
	}
}

func TestCandidateMustBeRegularAndPresent(t *testing.T) {
	doc, _, wire := fixture(t)
	dir := t.TempDir()
	name, _ := Filename(3)
	c := Candidate{Name: name, Seq: 3}
	if _, err := Load(dir, c, doc.Identity, 3); !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing file downgraded: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, c, doc.Identity, 3); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "unrelated.bin")
	if err := os.WriteFile(target, wire, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, c, doc.Identity, 3); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("symlink candidate followed: %v", err)
	}
}

func TestConfirmationNeverRecreatesAndPropagatesEveryFailure(t *testing.T) {
	doc, _, wire := fixture(t)
	dir := t.TempDir()
	s := &fileTrace{}
	if err := confirm(dir, doc, s.ops(dir)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("missing confirmation: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("confirmation recreated missing file")
	}
	if err := os.WriteFile(snapshotPath(t, dir, 3), wire, 0600); err != nil {
		t.Fatal(err)
	}
	s = &fileTrace{}
	if err := confirm(dir, doc, s.ops(dir)); err != nil {
		t.Fatal(err)
	}
	for i, stage := range s.events {
		f := &fileTrace{failAt: i + 1}
		if err := confirm(dir, doc, f.ops(dir)); !errors.Is(err, errDisk) || f.handles != 0 {
			t.Fatalf("stage %s: %v, handles %d", stage, err, f.handles)
		}
	}
}
