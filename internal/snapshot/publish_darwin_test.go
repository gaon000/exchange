//go:build darwin && cgo

package snapshot

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/durable"
)

func lockRun(t testing.TB, dir string) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := durable.Lock(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	return f
}

func TestNativePublishAndExclusiveRename(t *testing.T) {
	doc, c, _ := fixture(t)
	dir := t.TempDir()
	lock := lockRun(t, dir)
	defer lock.Close()
	old := oldDocument(t, doc, c)
	for _, checkpoint := range []Document{old, doc, doc} {
		if err := Publish(dir, checkpoint); err != nil {
			t.Fatal(err)
		}
		readDocument(t, snapshotPath(t, dir, checkpoint.SnapshotSeq), checkpoint)
	}
	readDocument(t, snapshotPath(t, dir, 0), old)
	destination := snapshotPath(t, dir, doc.SnapshotSeq)
	before, _ := os.ReadFile(destination)
	source := filepath.Join(dir, "unrelated.tmp")
	if err := os.WriteFile(source, []byte("unrelated content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := durable.RenameExclusive(source, destination); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination must be protected: %v", err)
	}
	after, _ := os.ReadFile(destination)
	kept, _ := os.ReadFile(source)
	if !bytes.Equal(before, after) || string(kept) != "unrelated content" {
		t.Fatal("exclusive rename modified a source or destination on EEXIST")
	}
	if err := durable.RenameExclusive(source, filepath.Join(dir, "new-name")); err != nil {
		t.Fatal(err)
	}
	if err := durable.RenameExclusive("bad\x00name", destination); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("NUL path: %v", err)
	}
}

type killFile struct {
	*os.File
	stage  string
	hook   func(string)
	writes int
}

func (f *killFile) Write(b []byte) (int, error) {
	n, err := f.File.Write(b)
	if err == nil {
		f.writes++
		f.hook([]string{"write-header", "write-payload", "write-digest"}[f.writes-1])
	}
	return n, err
}
func (f *killFile) Close() error {
	err := f.File.Close()
	if err == nil && f.stage == "file" {
		f.hook("close-file")
	}
	return err
}

func TestSnapshotPublisherChild(t *testing.T) {
	stage := os.Getenv("EXCHANGE_SNAPSHOT_KILL_STAGE")
	if stage == "" {
		t.Skip("child process helper")
	}
	dir, marker := os.Getenv("EXCHANGE_SNAPSHOT_KILL_DIR"), os.Getenv("EXCHANGE_SNAPSHOT_KILL_MARKER")
	lock := lockRun(t, dir)
	defer lock.Close()
	doc, _, _ := fixture(t)
	hook := func(at string) {
		if stage == at {
			if err := os.WriteFile(marker, []byte(at), 0600); err != nil {
				t.Fatal(err)
			}
			<-time.After(time.Hour) // parent kills this process at the observed boundary
		}
	}
	ops := nativeFileOps()
	open, temp, rename := ops.open, ops.temp, ops.rename
	ops.open = func(path string) (file, error) {
		f, err := open(path)
		if err != nil {
			return nil, err
		}
		return &killFile{File: f.(*os.File), stage: "directory", hook: hook}, nil
	}
	ops.temp = func(dir string) (file, error) {
		f, err := temp(dir)
		if err != nil {
			return nil, err
		}
		return &killFile{File: f.(*os.File), stage: "file", hook: hook}, nil
	}
	ops.sync = func(f file) error {
		x := f.(*killFile)
		err := durable.Sync(x.File)
		if err == nil {
			hook("sync-" + x.stage)
		}
		return err
	}
	ops.rename = func(from, to string) error {
		err := rename(from, to)
		if err == nil {
			hook("rename")
		}
		return err
	}
	if err := publish(dir, doc, ops, MaxPayloadBytes); err != nil {
		t.Fatal(err)
	}
	t.Fatal("child did not reach requested failure boundary")
}

func TestNativeProcessKillAtPublicationBoundaries(t *testing.T) {
	doc, c, _ := fixture(t)
	old := oldDocument(t, doc, c)
	for _, stage := range []string{"write-header", "write-payload", "write-digest", "sync-file", "close-file", "rename", "sync-directory"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			lock := lockRun(t, dir)
			if err := Publish(dir, old); err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "reached")
			cmd := exec.Command(os.Args[0], "-test.run=^TestSnapshotPublisherChild$")
			cmd.Env = append(os.Environ(), "EXCHANGE_SNAPSHOT_KILL_STAGE="+stage, "EXCHANGE_SNAPSHOT_KILL_DIR="+dir, "EXCHANGE_SNAPSHOT_KILL_MARKER="+marker)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					waited = true
					t.Fatalf("child boundary timeout: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			waited = true
			var exit *exec.ExitError
			if !errors.As(err, &exit) || !strings.Contains(exit.Error(), "killed") {
				t.Fatalf("expected killed child: %v, %s", err, output.String())
			}
			lock = lockRun(t, dir) // OS released the child lifetime lock
			defer lock.Close()
			readDocument(t, snapshotPath(t, dir, 0), old)
			path := snapshotPath(t, dir, 3)
			if stage == "rename" || stage == "sync-directory" {
				readDocument(t, path, doc)
			} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unpublished file visible: %v", err)
			}
			// A fresh publisher either creates the missing file or verifies and
			// strictly syncs the complete named one. Previous snapshots survive.
			if err := Publish(dir, doc); err != nil {
				t.Fatal(err)
			}
			readDocument(t, path, doc)
			readDocument(t, snapshotPath(t, dir, 0), old)
		})
	}
}
