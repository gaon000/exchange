//go:build darwin && cgo

package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type nativeRecoveryFixture struct {
	dir     string
	genesis wal.Genesis
	want    *engine.Engine
	docs    map[d.Int]snapshot.Document
	ends    map[d.Int]int64
}

func nativeRecovery(t *testing.T) nativeRecoveryFixture {
	t.Helper()
	f := nativeRecoveryFixture{dir: filepath.Join(t.TempDir(), "run"), docs: make(map[d.Int]snapshot.Document), ends: make(map[d.Int]int64)}
	s, err := wal.Init(f.dir, fake(t).g.Config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	f.genesis = s.Genesis()
	f.want, err = engine.New(f.genesis.RunID, f.genesis.Config)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range recoveryCommands(f.genesis.RunID) {
		record := wal.Record{Seq: d.Int(i + 1), Command: c}
		if err := s.Append([]wal.Record{record}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.want.Apply(record.Seq, c); err != nil {
			t.Fatal(err)
		}
		f.ends[record.Seq] = s.Recovery().EndOffset
		if record.Seq == 3 || record.Seq == 7 || record.Seq == 11 {
			doc := snapshot.Document{Identity: snapshotIdentity(f.genesis), SnapshotSeq: record.Seq, State: f.want.Capture()}
			f.docs[record.Seq] = doc
			if err := snapshot.Publish(f.dir, doc); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f nativeRecoveryFixture) snapshotPath(seq d.Int) string {
	name, _ := snapshot.Filename(seq)
	return filepath.Join(f.dir, name)
}
func replaceSnapshot(t *testing.T, path string, doc snapshot.Document) {
	t.Helper()
	var b bytes.Buffer
	if err := snapshot.Write(&b, doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}
func corruptFile(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 1
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNativeSnapshotRecoveryAndFallback(t *testing.T) {
	for _, mode := range []string{"latest", "previous", "Genesis", "invalid state", "WAL input mismatch", "torn tail"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeRecovery(t)
			selected := d.Int(11)
			switch mode {
			case "previous":
				corruptFile(t, f.snapshotPath(11))
				selected = 7
			case "Genesis":
				for seq := range f.docs {
					corruptFile(t, f.snapshotPath(seq))
				}
				selected = 0
			case "invalid state":
				doc := f.docs[11]
				doc.State.Accounts[0].AvailableBaseLots++
				replaceSnapshot(t, f.snapshotPath(11), doc)
				selected = 7
			case "WAL input mismatch":
				doc := f.docs[11]
				for i := range doc.State.Results {
					r := &doc.State.Results[i]
					if r.Response.RequestID == "missing" {
						r.Command.Cancel.ClientOrderID = "different"
					}
				}
				replaceSnapshot(t, f.snapshotPath(11), doc)
				selected = 7
			case "torn tail":
				log, err := os.OpenFile(filepath.Join(f.dir, "commands.wal"), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = log.Write([]byte("EXGRP001"))
				if err := errors.Join(err, log.Close()); err != nil {
					t.Fatal(err)
				}
			}
			// Leftovers cannot become recovery candidates, even with arbitrary bytes.
			if err := os.WriteFile(filepath.Join(f.dir, ".snapshot-leftover.tmp"), []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			walBefore, _ := os.ReadFile(filepath.Join(f.dir, "commands.wal"))
			r, err := Open(f.dir, DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Recovery(); got.SnapshotSeq != selected || got.ReplayedCommands != 11-selected {
				t.Fatalf("wrong recovery source: %+v", got)
			}
			for _, c := range recoveryCommands(f.genesis.RunID) {
				want, _, _ := f.want.Lookup(c)
				got, err := r.Submit(ctx(t), c)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("first response differs: %v", err)
				}
			}
			finish(t, r)
			if !reflect.DeepEqual(r.engine.State(), f.want.State()) {
				t.Fatal("snapshot+tail differs from Genesis full replay")
			}
			walAfter, _ := os.ReadFile(filepath.Join(f.dir, "commands.wal"))
			if !bytes.Equal(walAfter, walBefore[:f.ends[11]]) {
				t.Fatal("WAL changed beyond permitted incomplete tail removal")
			}
			// Restore may not reseed accounts, overwrite first responses, or lose
			// the ability to apply a new command after a fallback scan.
			r, err = Open(f.dir, DefaultOptions())
			if err != nil {
				t.Fatal(err)
			}
			c := cancel("after-restart", "future")
			c.RunID = f.genesis.RunID
			want, err := f.want.Apply(12, c)
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.Submit(ctx(t), c)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("post-recovery cancel: %v", err)
			}
			finish(t, r)
			if !reflect.DeepEqual(r.engine.State(), f.want.State()) {
				t.Fatal("post-recovery state differs")
			}
		})
	}
}

func TestNativeSnapshotCannotHideWALOrIdentityMismatch(t *testing.T) {
	for _, mode := range []string{"old WAL corruption", "short valid WAL", "run", "config", "rules", "response", "format"} {
		t.Run(mode, func(t *testing.T) {
			f := nativeRecovery(t)
			want := snapshot.ErrIdentity
			switch mode {
			case "old WAL corruption":
				path := filepath.Join(f.dir, "commands.wal")
				b, _ := os.ReadFile(path)
				i := bytes.Index(b, []byte("EXGRP001"))
				if i < 0 {
					t.Fatal("no first group")
				}
				b[i+36+4] ^= 1
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				want = wal.ErrCorrupt
			case "short valid WAL":
				if err := os.Truncate(filepath.Join(f.dir, "commands.wal"), f.ends[7]); err != nil {
					t.Fatal(err)
				}
			default:
				path := f.snapshotPath(11)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "run":
					b = bytes.Replace(b, []byte(f.genesis.RunID), []byte(runID), 1)
				case "config":
					hash := f.genesis.ConfigHash
					replacement := []byte(hash)
					replacement[0] = '0'
					if replacement[0] == hash[0] {
						replacement[0] = '1'
					}
					b = bytes.Replace(b, []byte(hash), replacement, 1)
				case "rules":
					b = bytes.Replace(b, []byte(`"rulesVersion":1`), []byte(`"rulesVersion":2`), 1)
				case "response":
					b = bytes.Replace(b, []byte(`"responseVersion":1`), []byte(`"responseVersion":2`), 1)
				case "format":
					b[8] = 2
				}
				digest := sha256.Sum256(b[:len(b)-sha256.Size])
				copy(b[len(b)-sha256.Size:], digest[:])
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			r, err := Open(f.dir, DefaultOptions())
			if r != nil || !errors.Is(err, want) {
				if r != nil {
					finish(t, r)
				}
				t.Fatalf("fatal recovery error not enforced: %v", err)
			}
			// The failed Open must release its lock even when bytes were valid
			// enough to select or begin restoring a snapshot.
			if mode != "old WAL corruption" {
				s, err := wal.Open(f.dir)
				if err != nil {
					t.Fatal(err)
				}
				_ = s.Close()
			}
		})
	}
}

func TestNativeZeroSequenceSnapshot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s, err := wal.Init(dir, fake(t).g.Config)
	if err != nil {
		t.Fatal(err)
	}
	g := s.Genesis()
	e, _ := engine.New(g.RunID, g.Config)
	doc := snapshot.Document{Identity: snapshotIdentity(g), State: e.Capture()}
	if err := snapshot.Publish(dir, doc); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	r, err := Open(dir, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Recovery().SnapshotFile == "" || r.Recovery().SnapshotSeq != 0 || r.Recovery().ReplayedCommands != 0 {
		t.Fatal("seq=0 snapshot confused with missing snapshot")
	}
	finish(t, r)
	got, _ := json.Marshal(r.engine.Capture())
	want, _ := json.Marshal(doc.State)
	if !bytes.Equal(got, want) {
		t.Fatal("initial balance changed")
	}
}
