package wal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

// Test logical I/O boundaries on all platforms. Actual strict sync and lifetime
// process lock tests are separate in store_darwin_test.go; these are not durability evidence.
func fakeOps() fileOps {
	o := nativeOps()
	o.checkFS = func(string) error { return nil }
	o.lock = func(*os.File) error { return nil }
	o.sync = func(*os.File) error { return nil }
	return o
}
func mustInit(t *testing.T, dir string, o fileOps) *Store {
	t.Helper()
	s, err := initStore(dir, testConfig(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func mustOpen(t *testing.T, dir string, o fileOps) *Store {
	t.Helper()
	s, err := openStore(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func bind(r Record, g Genesis) Record { r.Command.RunID = g.RunID; return r }
func appendFile(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = writeAll(f, b)
	err = errors.Join(err, f.Close())
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreReplayReconstructsEngineWithoutReseeding(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s := mustInit(t, dir, fakeOps())
	g := s.Genesis()
	e, err := engine.New(g.RunID, g.Config)
	if err != nil {
		t.Fatal(err)
	}
	a := bind(place(1, "alice", "buy"), g)
	b := bind(place(2, "bob", "sell"), g)
	b.Command.Place.Side = d.Sell
	b.Command.Place.QuantityLots = 1
	b.Command.Place.PriceTicks = 90
	c := Record{Seq: 3, Command: d.Command{RunID: g.RunID, AccountID: "alice", Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: "cancel", ClientOrderID: "buy"}}}
	// Business rejections must also survive logging and replay.
	rejected := bind(place(4, "alice", "large"), g)
	rejected.Command.Place.QuantityLots = d.MaxQuantityLots
	rejected.Command.Place.PriceTicks = d.MaxPriceTicks
	rs := []Record{a, b, c, rejected}
	var results []d.Result
	if err := s.Append(rs); err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		res, err := e.Apply(r.Seq, r.Command)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, res)
	}
	if s.DurableSeq() != 4 || s.Recovery().LastSeq != 4 {
		t.Fatal("wrong durable sequence")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 3; restart++ {
		s = mustOpen(t, dir, fakeOps())
		if s.DurableSeq() != 0 {
			t.Fatal("open inferred old fsync")
		}
		if err := s.Append([]Record{bind(place(5, "alice", "new"), g)}); !errors.Is(err, ErrNotReady) {
			t.Fatal(err)
		}
		recovered, err := engine.New(g.RunID, g.Config)
		if err != nil {
			t.Fatal(err)
		}
		var replayResults []d.Result
		err = s.Replay(func(r Record) error {
			res, err := recovered.Apply(r.Seq, r.Command)
			if err == nil && recovered.AppliedSeq() != r.Seq {
				return errors.New("replay did not advance")
			}
			replayResults = append(replayResults, res)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e.State(), recovered.State()) || !reflect.DeepEqual(results, replayResults) {
			t.Fatal("replay state or first results differ")
		}
		if err := recovered.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		if err := s.ConfirmRecovery(); err != nil {
			t.Fatal(err)
		}
		if s.DurableSeq() != 4 {
			t.Fatal("recovery not confirmed")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecoveryOnlyTruncatesAfterConfirmation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s := mustInit(t, dir, fakeOps())
	g := s.Genesis()
	r := bind(place(1, "alice", "a"), g)
	if err := s.Append([]Record{r}); err != nil {
		t.Fatal(err)
	}
	end := s.Recovery().EndOffset
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "commands.wal")
	tail := group(t, bind(place(2, "bob", "b"), g))
	appendFile(t, path, tail[:len(tail)-1])
	original, _ := os.ReadFile(path)
	s = mustOpen(t, dir, fakeOps())
	if !s.Recovery().TornTail || s.Recovery().EndOffset != end {
		t.Fatalf("%+v", s.Recovery())
	}
	var records []Record
	if err := s.Replay(func(r Record) error { records = append(records, r); return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if !bytes.Equal(before, original) || len(records) != 1 {
		t.Fatal("Open/Replay mutated tail")
	}
	if err := s.ConfirmRecovery(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != end || s.DurableSeq() != 1 || s.Recovery().TornTail {
		t.Fatalf("%v %v", info, err)
	}
	if err := s.Append([]Record{bind(place(2, "bob", "next"), g)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Replay(nil); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCorruptionPreservesAllBytes(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, string){
		"Genesis trailing": func(t *testing.T, dir string) { appendFile(t, filepath.Join(dir, "genesis.bin"), []byte("!")) },
		"Genesis truncated": func(t *testing.T, dir string) {
			if err := os.Truncate(filepath.Join(dir, "genesis.bin"), 20); err != nil {
				t.Fatal(err)
			}
		},
		"missing lock": func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, ".lock")); err != nil {
				t.Fatal(err)
			}
		},
		"WAL checksum": func(t *testing.T, dir string) {
			p := filepath.Join(dir, "commands.wal")
			b, _ := os.ReadFile(p)
			b[len(b)-1] ^= 1
			if err := os.WriteFile(p, b, 0600); err != nil {
				t.Fatal(err)
			}
		},
		"foreign WAL": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "commands.wal"), fixture(t, "commands.wal"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink WAL": func(t *testing.T, dir string) {
			p := filepath.Join(dir, "commands.wal")
			if err := os.Rename(p, p+".keep"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("commands.wal.keep", p); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "run")
			s := mustInit(t, dir, fakeOps())
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			mutate(t, dir)
			before := directoryBytes(t, dir)
			if got, err := openStore(dir, fakeOps()); err == nil {
				_ = got.Close()
				t.Fatal("corrupt storage opened")
			}
			if !reflect.DeepEqual(before, directoryBytes(t, dir)) {
				t.Fatal("Open modified corrupt files")
			}
		})
	}
}
func directoryBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[entry.Name()] = string(b)
	}
	return out
}

func TestExistingDirectoryAndGenesisCopies(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "run")
	s := mustInit(t, dir, fakeOps())
	before := directoryBytes(t, dir)
	if got, err := initStore(dir, testConfig(), fakeOps()); err == nil {
		_ = got.Close()
		t.Fatal("existing run reset")
	}
	if !reflect.DeepEqual(before, directoryBytes(t, dir)) {
		t.Fatal("existing run changed")
	}
	if _, err := initStore(parent, testConfig(), fakeOps()); err == nil {
		t.Fatal("existing parent accepted")
	}
	empty := filepath.Join(parent, "empty")
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := initStore(empty, testConfig(), fakeOps()); err == nil {
		t.Fatal("empty directory reset")
	}
	g := s.Genesis()
	g.Config.Accounts[0].BaseLots = 999
	if s.Genesis().Config.Accounts[0].BaseLots != 10 {
		t.Fatal("Genesis alias")
	}
	for _, name := range []string{".lock", "genesis.bin", "commands.wal"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("%s: %v %v", name, info, err)
		}
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("directory mode %v %v", info, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(nil); !errors.Is(err, ErrPoisoned) {
		t.Fatal(err)
	}
	if err := s.ConfirmRecovery(); !errors.Is(err, ErrPoisoned) {
		t.Fatal(err)
	}
	if err := s.Replay(nil); !errors.Is(err, ErrPoisoned) {
		t.Fatal(err)
	}
}

var injected = errors.New("injected file operation failure")

func tracedOps(events *[]string, fail int) fileOps {
	o := fakeOps()
	step := func(label string) error {
		*events = append(*events, label)
		if len(*events) == fail {
			return injected
		}
		return nil
	}
	o.checkFS = func(string) error { return step("filesystem") }
	o.mkdir = func(p string, m os.FileMode) error {
		if err := step("mkdir"); err != nil {
			return err
		}
		return os.Mkdir(p, m)
	}
	o.open = func(p string, f int, m os.FileMode) (*os.File, error) {
		if err := step("open:" + filepath.Base(p)); err != nil {
			return nil, err
		}
		return os.OpenFile(p, f, m)
	}
	o.lock = func(*os.File) error { return step("lock") }
	o.write = func(f *os.File, b []byte) (int, error) {
		if err := step("write:" + filepath.Base(f.Name())); err != nil {
			return 0, err
		}
		return f.Write(b)
	}
	o.sync = func(f *os.File) error { return step("sync:" + filepath.Base(f.Name())) }
	o.truncate = func(f *os.File, n int64) error {
		if err := step("truncate"); err != nil {
			return err
		}
		return f.Truncate(n)
	}
	return o
}

func TestInitEveryOperationFailureAndSyncOrder(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	want := []string{"filesystem", "mkdir", "open:.lock", "lock", "sync:.lock", "open:genesis.bin", "write:genesis.bin", "sync:genesis.bin", "open:commands.wal", "write:commands.wal", "sync:commands.wal", "open:run", "sync:run", "open:parent", "sync:parent"}
	var events []string
	s := mustInit(t, filepath.Join(parent, "run"), tracedOps(&events, 0))
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("%v", events)
	}
	_ = s.Close()
	for fail := 1; fail <= len(want); fail++ {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "parent")
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(p, "run")
			events = nil
			got, err := initStore(dir, testConfig(), tracedOps(&events, fail))
			if !errors.Is(err, injected) || got != nil || !reflect.DeepEqual(events, want[:fail]) {
				t.Fatalf("failure %d: %v %v", fail, events, err)
			}
			if fail > 2 {
				if _, err := os.Stat(dir); err != nil {
					t.Fatal("failed init directory was removed")
				}
				if _, err := initStore(dir, testConfig(), fakeOps()); err == nil {
					t.Fatal("incomplete init silently reset")
				}
			}
		})
	}
}

func TestOpenEveryOperationFailureClosesHandles(t *testing.T) {
	want := []string{"filesystem", "open:.lock", "lock", "open:genesis.bin", "open:commands.wal"}
	for fail := 1; fail <= len(want); fail++ {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "run")
			s := mustInit(t, dir, fakeOps())
			_ = s.Close()
			before := directoryBytes(t, dir)
			var events []string
			var handles []*os.File
			ops := tracedOps(&events, fail)
			open := ops.open
			ops.open = func(p string, flag int, mode os.FileMode) (*os.File, error) {
				f, err := open(p, flag, mode)
				if f != nil {
					handles = append(handles, f)
				}
				return f, err
			}
			got, err := openStore(dir, ops)
			if got != nil || !errors.Is(err, injected) || !reflect.DeepEqual(events, want[:fail]) {
				t.Fatalf("%v %v", events, err)
			}
			for _, f := range handles {
				if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("leaked handle %s: %v", f.Name(), err)
				}
			}
			if !reflect.DeepEqual(before, directoryBytes(t, dir)) {
				t.Fatal("failed Open changed files")
			}
		})
	}
}

func TestInvalidConfigDoesNotCreateDirectory(t *testing.T) {
	c := testConfig()
	c.Accounts[0].BaseLots = -1
	dir := filepath.Join(t.TempDir(), "run")
	var events []string
	if _, err := initStore(dir, c, tracedOps(&events, 0)); err == nil || len(events) != 0 {
		t.Fatalf("invalid config reached I/O: %v %v", events, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("invalid config created a run")
	}
}

func TestRecoveryEveryOperationFailureAndSyncOrder(t *testing.T) {
	want := []string{"truncate", "sync:commands.wal", "open:genesis.bin", "sync:genesis.bin", "sync:.lock", "open:run", "sync:run", "open:parent", "sync:parent"}
	for fail := 0; fail <= len(want); fail++ {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "parent")
			if err := os.Mkdir(p, 0700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(p, "run")
			s := mustInit(t, dir, fakeOps())
			g := s.Genesis()
			r := bind(place(1, "alice", "a"), g)
			if err := s.Append([]Record{r}); err != nil {
				t.Fatal(err)
			}
			_ = s.Close()
			appendFile(t, filepath.Join(dir, "commands.wal"), []byte("EXGR"))
			s = mustOpen(t, dir, fakeOps())
			var events []string
			s.ops = tracedOps(&events, fail)
			err := s.ConfirmRecovery()
			if fail == 0 {
				if err != nil || s.DurableSeq() != 1 || !reflect.DeepEqual(events, want) {
					t.Fatalf("%v %v", events, err)
				}
				return
			}
			if !errors.Is(err, injected) || s.DurableSeq() != 0 || !reflect.DeepEqual(events, want[:fail]) {
				t.Fatalf("failure %d: %v %v", fail, events, err)
			}
			if err := s.Append([]Record{bind(place(2, "bob", "b"), g)}); !errors.Is(err, ErrPoisoned) {
				t.Fatal(err)
			}
			if err := s.ConfirmRecovery(); !errors.Is(err, ErrPoisoned) {
				t.Fatal(err)
			}
			_ = s.Close()
			s = mustOpen(t, dir, fakeOps())
			if err := s.ConfirmRecovery(); err != nil || s.DurableSeq() != 1 {
				t.Fatalf("fresh recovery: %v", err)
			}
		})
	}
}

func TestStoreFailedSyncThenCompleteGroupRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s := mustInit(t, dir, fakeOps())
	g := s.Genesis()
	// Replace the sink before any append to inject the actual file-sync boundary.
	s.writer.sink = fileSink{s.file, s.ops.write, func(*os.File) error { return injected }}
	if err := s.Append([]Record{bind(place(1, "alice", "a"), g)}); !errors.Is(err, injected) || s.DurableSeq() != 0 {
		t.Fatalf("%v", err)
	}
	if err := s.Append([]Record{bind(place(2, "bob", "b"), g)}); !errors.Is(err, ErrPoisoned) {
		t.Fatal(err)
	}
	_ = s.Close()
	s = mustOpen(t, dir, fakeOps())
	if s.Recovery().LastSeq != 1 || s.DurableSeq() != 0 {
		t.Fatal("incorrect unacknowledged recovery")
	}
	if err := s.ConfirmRecovery(); err != nil || s.DurableSeq() != 1 {
		t.Fatal(err)
	}
}

func TestReplayCallbackFailurePoisonsStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s := mustInit(t, dir, fakeOps())
	g := s.Genesis()
	if err := s.Append([]Record{bind(place(1, "alice", "a"), g)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Replay(func(Record) error { return injected }); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if err := s.Append([]Record{bind(place(2, "bob", "b"), g)}); !errors.Is(err, ErrPoisoned) {
		t.Fatal(err)
	}
}

func TestInitPartialFileWritePreservesIncompleteRun(t *testing.T) {
	for _, name := range []string{"genesis.bin", "commands.wal"} {
		for _, cut := range []int{0, 1, 19, 20, 51} {
			t.Run(fmt.Sprintf("%s/%d", name, cut), func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "run")
				ops := fakeOps()
				ops.write = func(f *os.File, b []byte) (int, error) {
					if filepath.Base(f.Name()) == name {
						n, err := f.Write(b[:cut])
						return n, errors.Join(err, injected)
					}
					return f.Write(b)
				}
				if _, err := initStore(dir, testConfig(), ops); !errors.Is(err, injected) {
					t.Fatal(err)
				}
				info, err := os.Stat(filepath.Join(dir, name))
				if err != nil || info.Size() != int64(cut) {
					t.Fatalf("%v %v", info, err)
				}
				if s, err := openStore(dir, fakeOps()); err == nil {
					_ = s.Close()
					t.Fatal("incomplete initialization recovered")
				}
			})
		}
	}
}

func TestStoreAppendWriteErrorIsFailStop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	s := mustInit(t, dir, fakeOps())
	g := s.Genesis()
	s.writer.sink = fileSink{s.file, func(f *os.File, b []byte) (int, error) {
		n, err := f.Write(b[:17])
		return n, errors.Join(err, io.ErrShortWrite)
	}, s.ops.sync}
	if err := s.Append([]Record{bind(place(1, "alice", "a"), g)}); !errors.Is(err, io.ErrShortWrite) || s.DurableSeq() != 0 {
		t.Fatal(err)
	}
	_ = s.Close()
	s = mustOpen(t, dir, fakeOps())
	if !s.Recovery().TornTail || s.Recovery().LastSeq != 0 {
		t.Fatal("torn write not detected")
	}
	if err := s.ConfirmRecovery(); err != nil {
		t.Fatal(err)
	}
}
