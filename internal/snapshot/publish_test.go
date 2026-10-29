package snapshot

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

// Failure tests use actual temporary bytes but fake durability. Native tests
// separately exercise APFS, F_FULLFSYNC and exclusive rename.
type fileTrace struct {
	events  []string
	failAt  int
	handles int
}

func (s *fileTrace) event(name string) error {
	s.events = append(s.events, name)
	if len(s.events) == s.failAt {
		return errDisk
	}
	return nil
}

type tracedFile struct {
	*os.File
	s    *fileTrace
	kind string
}

func (f *tracedFile) Read(b []byte) (int, error) {
	if err := f.s.event("read " + f.kind); err != nil {
		return 0, err
	}
	return f.File.Read(b)
}
func (f *tracedFile) Write(b []byte) (int, error) {
	if err := f.s.event("write " + f.kind); err != nil {
		n, _ := f.File.Write(b[:len(b)/2])
		return n, err
	}
	return f.File.Write(b)
}
func (f *tracedFile) Stat() (os.FileInfo, error) {
	if err := f.s.event("stat " + f.kind); err != nil {
		return nil, err
	}
	return f.File.Stat()
}
func (f *tracedFile) Close() error {
	err := f.File.Close()
	f.s.handles--
	return errors.Join(err, f.s.event("close "+f.kind))
}
func (s *fileTrace) ops(dir string) fileOps {
	return fileOps{
		checkFS: func(string) error { return s.event("check filesystem") },
		open: func(path string) (file, error) {
			kind := "existing"
			if path == dir {
				kind = "directory"
			}
			if err := s.event("open " + kind); err != nil {
				return nil, err
			}
			f, err := os.Open(path)
			if err != nil {
				return nil, err
			}
			s.handles++
			return &tracedFile{f, s, kind}, nil
		},
		temp: func(dir string) (file, error) {
			if err := s.event("create temp"); err != nil {
				return nil, err
			}
			f, err := os.CreateTemp(dir, ".snapshot-*.tmp")
			if err != nil {
				return nil, err
			}
			s.handles++
			return &tracedFile{f, s, "temp"}, nil
		},
		sync: func(f file) error { return s.event("sync " + f.(*tracedFile).kind) },
		rename: func(from, to string) error {
			if err := s.event("rename"); err != nil {
				return err
			}
			// Test-only exclusive name operation; never a production fallback.
			if err := os.Link(from, to); err != nil {
				return err
			}
			return os.Remove(from)
		},
		remove: func(path string) error {
			if err := s.event("remove temp"); err != nil {
				return err
			}
			return os.Remove(path)
		},
	}
}

func oldDocument(t testing.TB, doc Document, c d.Config) Document {
	t.Helper()
	e, err := engine.New(doc.RunID, c)
	if err != nil {
		t.Fatal(err)
	}
	doc.SnapshotSeq, doc.State = 0, e.Capture()
	return doc
}
func snapshotPath(t testing.TB, dir string, seq d.Int) string {
	t.Helper()
	name, err := Filename(seq)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}
func readDocument(t testing.TB, path string, doc Document) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(f, info.Size(), doc.Identity, doc.SnapshotSeq)
	if err != nil || !reflect.DeepEqual(got, doc) {
		t.Fatalf("published file differs: %v", err)
	}
}

func TestPublicationOrderAndIdempotentConfirmation(t *testing.T) {
	doc, _, wire := fixture(t)
	dir := t.TempDir()
	s := &fileTrace{}
	if err := publish(dir, doc, s.ops(dir), MaxPayloadBytes); err != nil {
		t.Fatal(err)
	}
	want := []string{"check filesystem", "open directory", "stat directory", "open existing", "create temp", "write temp", "write temp", "write temp", "sync temp", "close temp", "rename", "sync directory", "close directory"}
	if !reflect.DeepEqual(s.events, want) || s.handles != 0 {
		t.Fatalf("publication order/handles: %v / %d", s.events, s.handles)
	}
	path := snapshotPath(t, dir, 3)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot permissions: %v", info.Mode())
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, wire) {
		t.Fatal("published bytes differ")
	}
	s = &fileTrace{}
	if err := publish(dir, doc, s.ops(dir), MaxPayloadBytes); err != nil {
		t.Fatal(err)
	}
	gotInfo, _ := os.Stat(path)
	if !os.SameFile(info, gotInfo) || s.handles != 0 {
		t.Fatal("idempotent publication replaced file/leaked handle")
	}
	events := strings.Join(s.events, ",")
	if strings.Contains(events, "temp") || strings.Contains(events, "rename") || !strings.HasSuffix(events, "sync existing,close existing,sync directory,close directory") {
		t.Fatalf("idempotent confirmation: %s", events)
	}
}

func TestEveryPublicationAndConfirmationFailure(t *testing.T) {
	doc, c, _ := fixture(t)
	old := oldDocument(t, doc, c)
	for _, existing := range []bool{false, true} {
		baselineDir := t.TempDir()
		if existing {
			s := &fileTrace{}
			if err := publish(baselineDir, doc, s.ops(baselineDir), MaxPayloadBytes); err != nil {
				t.Fatal(err)
			}
		}
		baseline := &fileTrace{}
		if err := publish(baselineDir, doc, baseline.ops(baselineDir), MaxPayloadBytes); err != nil {
			t.Fatal(err)
		}
		for i, stage := range baseline.events {
			t.Run(fmt.Sprintf("existing=%t/%02d-%s", existing, i, stage), func(t *testing.T) {
				dir := t.TempDir()
				setup := &fileTrace{}
				if err := publish(dir, old, setup.ops(dir), MaxPayloadBytes); err != nil {
					t.Fatal(err)
				}
				if existing {
					if err := publish(dir, doc, setup.ops(dir), MaxPayloadBytes); err != nil {
						t.Fatal(err)
					}
				}
				s := &fileTrace{failAt: i + 1}
				if err := publish(dir, doc, s.ops(dir), MaxPayloadBytes); !errors.Is(err, errDisk) {
					t.Fatalf("failure swallowed: %v", err)
				}
				if s.handles != 0 {
					t.Fatalf("%d handles leaked", s.handles)
				}
				readDocument(t, snapshotPath(t, dir, 0), old)
				if _, err := os.Stat(snapshotPath(t, dir, 3)); err == nil {
					readDocument(t, snapshotPath(t, dir, 3), doc)
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				// Retrying starts fresh, preserves existing files and re-confirms
				// durability even after an earlier post-rename failure.
				retry := &fileTrace{}
				if err := publish(dir, doc, retry.ops(dir), MaxPayloadBytes); err != nil {
					t.Fatal(err)
				}
				readDocument(t, snapshotPath(t, dir, 3), doc)
			})
		}
	}
}

func TestExistingDestinationNeverOverwritten(t *testing.T) {
	doc, _, wire := fixture(t)
	for _, late := range []bool{false, true} {
		for _, same := range []bool{false, true} {
			dir := t.TempDir()
			path := snapshotPath(t, dir, 3)
			contents := bytes.Clone(wire)
			contents[len(contents)/2] ^= 1 // same length, but different contents
			if same {
				contents = wire
			}
			s := &fileTrace{}
			ops := s.ops(dir)
			create := func() {
				if err := os.WriteFile(path, contents, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if late {
				rename := ops.rename
				ops.rename = func(from, to string) error { create(); return rename(from, to) }
			} else {
				create()
			}
			err := publish(dir, doc, ops, MaxPayloadBytes)
			if same && err != nil || !same && !errors.Is(err, ErrExistsMismatch) {
				t.Fatalf("late=%v same=%v: %v", late, same, err)
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, contents) || s.handles != 0 {
				t.Fatal("existing file replaced/handle leaked")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatalf("owned temp not cleaned: %v", entries)
			}
		}
	}
}

func TestPostRenameFailureAndCleanupFailure(t *testing.T) {
	doc, _, _ := fixture(t)
	dir := t.TempDir()
	s := &fileTrace{}
	ops := s.ops(dir)
	rename := ops.rename
	ops.rename = func(from, to string) error {
		if err := rename(from, to); err != nil {
			return err
		}
		return errDisk // operation happened but acknowledgment failed
	}
	if err := publish(dir, doc, ops, MaxPayloadBytes); !errors.Is(err, errDisk) {
		t.Fatalf("post-rename error: %v", err)
	}
	readDocument(t, snapshotPath(t, dir, 3), doc)
	dir = t.TempDir()
	s = &fileTrace{}
	ops = s.ops(dir)
	ops.rename = func(string, string) error { return errDisk }
	cleanup := errors.New("cleanup failed")
	ops.remove = func(string) error { return cleanup }
	if err := publish(dir, doc, ops, MaxPayloadBytes); !errors.Is(err, errDisk) || !errors.Is(err, cleanup) {
		t.Fatalf("lost primary or cleanup error: %v", err)
	}
	if _, err := os.Stat(snapshotPath(t, dir, 3)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed rename published file")
	}
}

func TestInvalidExistingFileShapeIsPreserved(t *testing.T) {
	doc, _, wire := fixture(t)
	for _, kind := range []string{"short", "trailing", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := snapshotPath(t, dir, 3)
			contents := wire[:headerBytes]
			if kind == "trailing" {
				contents = append(bytes.Clone(wire), 0)
			}
			var err error
			if kind == "directory" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.WriteFile(path, contents, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(path)
			s := &fileTrace{}
			if err := publish(dir, doc, s.ops(dir), MaxPayloadBytes); !errors.Is(err, ErrExistsMismatch) {
				t.Fatal(err)
			}
			after, _ := os.Stat(path)
			if !os.SameFile(before, after) || s.handles != 0 {
				t.Fatal("existing entry replaced or file leaked")
			}
		})
	}
}

func TestPublicationInputLimitsBeforeFilesystemAccess(t *testing.T) {
	doc, _, wire := fixture(t)
	s := &fileTrace{}
	dir := filepath.Join(t.TempDir(), "uncreated")
	if err := publish(dir, doc, s.ops(dir), int64(len(wire)-53)); !errors.Is(err, ErrTooLarge) || len(s.events) != 0 {
		t.Fatalf("size limit touched filesystem: %v", err)
	}
	doc.SnapshotSeq = -1
	if err := publish(dir, doc, s.ops(dir), MaxPayloadBytes); !errors.Is(err, ErrInvalid) || len(s.events) != 0 {
		t.Fatalf("invalid input touched filesystem: %v", err)
	}
	for _, seq := range []d.Int{0, 3, math.MaxInt64} {
		name, err := Filename(seq)
		if err != nil || len(name) != len("snapshot-")+20+len(".bin") {
			t.Fatalf("filename %d: %q / %v", seq, name, err)
		}
	}
	if _, err := Filename(-1); !errors.Is(err, ErrInvalid) {
		t.Fatal("negative sequence name accepted")
	}
}

func TestComparingWriterRetainsIOError(t *testing.T) {
	w := &comparingWriter{r: errorReader{}}
	if _, err := w.Write([]byte("x")); !errors.Is(err, errDisk) {
		t.Fatal(err)
	}
	w.r = strings.NewReader("")
	if _, err := w.Write([]byte("x")); !errors.Is(err, ErrExistsMismatch) {
		t.Fatal(err)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errDisk }
