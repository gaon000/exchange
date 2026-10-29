package wal

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
)

// Store holds the lifetime run lock and the append-only WAL. A single owner must
// call its methods; it does not synchronize concurrent calls. After any storage
// failure, close it and recover into a fresh engine before publishing results.
type Store struct {
	dir     string
	genesis Genesis
	file    *os.File
	lock    *os.File
	info    Recovery
	writer  *writer
	valid   *validator
	ops     fileOps
	failed  bool
	closed  bool
}

// fileOps provides per-store fault injection without a mutable global hook.
type fileOps struct {
	checkFS  func(string) error
	mkdir    func(string, os.FileMode) error
	open     func(string, int, os.FileMode) (*os.File, error)
	lock     func(*os.File) error
	write    func(*os.File, []byte) (int, error)
	sync     func(*os.File) error
	truncate func(*os.File, int64) error
}

func nativeOps() fileOps {
	return fileOps{durable.CheckFilesystem, os.Mkdir, os.OpenFile, durable.Lock,
		func(f *os.File, b []byte) (int, error) { return f.Write(b) },
		durable.Sync, func(f *os.File, n int64) error { return f.Truncate(n) }}
}

type fileSink struct {
	*os.File
	write func(*os.File, []byte) (int, error)
	sync  func(*os.File) error
}

func (s fileSink) Write(b []byte) (int, error) { return s.write(s.File, b) }
func (s fileSink) Sync() error                 { return s.sync(s.File) }

func (s *Store) sink(f *os.File) fileSink { return fileSink{f, s.ops.write, s.ops.sync} }

// Init creates a new run below an existing parent, durably recording its
// Genesis and WAL header. An existing directory, even empty, is never reused.
// Failed initialization preserves its files for inspection; it never resets them.
func Init(dir string, config d.Config) (*Store, error) {
	return initStore(dir, config, nativeOps())
}

func initStore(dir string, config d.Config, ops fileOps) (_ *Store, err error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	id[6], id[8] = (id[6]&0x0f)|0x40, (id[8]&0x3f)|0x80
	runID := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
	g, err := NewGenesis(runID, config)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := ops.checkFS(filepath.Dir(dir)); err != nil {
		return nil, err
	}
	if err := ops.mkdir(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, genesis: g, ops: ops}
	defer func() {
		if err != nil {
			err = errors.Join(fmt.Errorf("initialization incomplete; preserved %s: %w", dir, err), s.Close())
		}
	}()
	s.lock, err = ops.open(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = ops.lock(s.lock); err != nil {
		return nil, err
	}
	if err = ops.sync(s.lock); err != nil {
		return nil, err
	}
	genesisJSON, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	if len(genesisJSON) > MaxGenesisPayload {
		return nil, ErrInvalid
	}
	if err = s.createGenesis(envelope("EXGEN001", genesisJSON)); err != nil {
		return nil, err
	}
	s.file, err = ops.open(filepath.Join(dir, "commands.wal"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	metadata, _ := json.Marshal(g.Metadata())
	data := envelope("EXWAL001", metadata)
	if err = writeAll(s.sink(s.file), data); err != nil {
		return nil, err
	}
	if err = ops.sync(s.file); err != nil {
		return nil, err
	}
	if err = s.syncDirectories(); err != nil {
		return nil, err
	}
	s.info = Recovery{EndOffset: int64(len(data))}
	s.valid = newValidator(g)
	s.writer = &writer{sink: s.sink(s.file), validator: s.valid}
	return s, nil
}

func (s *Store) createGenesis(data []byte) error {
	f, err := s.ops.open(filepath.Join(s.dir, "genesis.bin"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = writeAll(s.sink(f), data)
	if err == nil {
		err = s.ops.sync(f)
	}
	return errors.Join(err, f.Close())
}

// Open locks and validates an existing run without truncating or reinitializing
// it. Replay into a fresh engine, then ConfirmRecovery before serving requests.
func Open(dir string) (*Store, error) { return openStore(dir, nativeOps()) }

func openStore(dir string, ops fileOps) (_ *Store, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := ops.checkFS(dir); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, ops: ops}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	s.lock, err = s.openRegular(".lock")
	if err != nil {
		return nil, err
	}
	if err = ops.lock(s.lock); err != nil {
		return nil, err
	}
	f, err := s.openRegular("genesis.bin")
	if err != nil {
		return nil, err
	}
	data, _, readErr := readEnvelope(f, "EXGEN001", MaxGenesisPayload)
	if readErr == nil {
		var extra [1]byte
		if n, e := f.Read(extra[:]); n != 0 || e != io.EOF {
			readErr = fmt.Errorf("%w: surplus Genesis bytes or read failure: %v", ErrCorrupt, e)
		}
	}
	if err = errors.Join(readErr, f.Close()); err != nil {
		return nil, err
	}
	s.genesis, err = decodeGenesis(data)
	if err != nil {
		return nil, err
	}
	s.file, err = s.openRegular("commands.wal")
	if err != nil {
		return nil, err
	}
	s.info, s.valid, err = scan(s.file, s.genesis, nil)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) openRegular(name string) (*os.File, error) {
	path := filepath.Join(s.dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrCorrupt, name)
	}
	// Files are owned by this run; external replacement while open is unsupported.
	return s.ops.open(path, os.O_RDWR, 0)
}

// Genesis returns a detached copy of the immutable run configuration.
func (s *Store) Genesis() Genesis {
	g := s.genesis
	g.Config.Accounts = append([]d.InitialAccount(nil), g.Config.Accounts...)
	return g
}

// Recovery reports the validated log boundary from Open or the last append.
func (s *Store) Recovery() Recovery { return s.info }

// Replay delivers only complete, validated groups. On error the caller must
// discard all partially reconstructed state. Calling it after confirmation is
// allowed, but the visitor must not call Store methods or mutate the run files.
func (s *Store) Replay(visit func(Record) error) error {
	if s.failed || s.closed {
		return ErrPoisoned
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		s.failed = true
		return err
	}
	info, _, err := scan(s.file, s.genesis, visit)
	if err == nil && info != s.info {
		err = fmt.Errorf("%w: WAL changed during recovery", ErrCorrupt)
	}
	if err != nil {
		s.failed = true
	}
	return err
}

// ConfirmRecovery durably retains complete groups and removes only an allowed
// incomplete EOF tail. Complete surviving groups may never have been acknowledged
// by the previous process. No new append is allowed until all syncs succeed.
func (s *Store) ConfirmRecovery() error {
	if s.failed || s.closed {
		return ErrPoisoned
	}
	if s.writer != nil {
		return nil
	}
	err := s.confirm()
	if err != nil {
		s.failed = true
		return err
	}
	s.info.TornTail = false
	s.writer = &writer{sink: s.sink(s.file), validator: s.valid, last: s.info.LastSeq}
	return nil
}

func (s *Store) confirm() error {
	if s.info.TornTail {
		if err := s.ops.truncate(s.file, s.info.EndOffset); err != nil {
			return err
		}
	}
	if _, err := s.file.Seek(s.info.EndOffset, io.SeekStart); err != nil {
		return err
	}
	if err := s.ops.sync(s.file); err != nil {
		return err
	}
	// A prior init might have left complete but unconfirmed files. Reconfirm the
	// complete Genesis, lock inode, and both directory entries as well.
	f, err := s.openRegular("genesis.bin")
	if err != nil {
		return err
	}
	if err = errors.Join(s.ops.sync(f), f.Close()); err != nil {
		return err
	}
	if err = s.ops.sync(s.lock); err != nil {
		return err
	}
	return s.syncDirectories()
}

func (s *Store) syncDirectories() error {
	for _, path := range []string{s.dir, filepath.Dir(s.dir)} {
		f, err := s.ops.open(path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		if err := errors.Join(s.ops.sync(f), f.Close()); err != nil {
			return err
		}
	}
	return nil
}

// Append writes and strictly synchronizes one group. Call Lookup and coalesce
// pending duplicates before logging; WAL request keys must be unique. No engine
// application or result publication may precede a successful return.
func (s *Store) Append(records []Record) error {
	if s.failed || s.closed {
		return ErrPoisoned
	}
	if s.writer == nil {
		return ErrNotReady
	}
	err := s.writer.append(records)
	if s.writer.failed {
		s.failed = true
	}
	if err == nil {
		s.info.LastSeq = s.writer.last
		// File writes are sequential; Seek also catches an unexpectedly closed FD.
		s.info.EndOffset, err = s.file.Seek(0, io.SeekCurrent)
		if err != nil {
			s.failed = true
		}
	}
	return err
}

// DurableSeq is the largest sequence confirmed in this process. Failed writes
// or syncs never advance it, even if their complete bytes survive a restart.
func (s *Store) DurableSeq() d.Int {
	if s.writer == nil {
		return 0
	}
	return s.writer.last
}

// Close releases the WAL handle and lifetime lock without deleting either file.
func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var errs []error
	if s.file != nil {
		errs = append(errs, s.file.Close())
	}
	if s.lock != nil {
		errs = append(errs, s.lock.Close())
	}
	return errors.Join(errs...)
}
