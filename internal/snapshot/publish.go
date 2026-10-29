package snapshot

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
)

// ErrExistsMismatch prevents replacement of a different or damaged snapshot at
// the same sequence. No existing snapshot is ever deleted by this package.
var ErrExistsMismatch = errors.New("snapshot destination has different bytes")

// Filename returns the canonical basename for a nonnegative snapshot sequence.
func Filename(seq d.Int) (string, error) {
	if seq < 0 {
		return "", ErrInvalid
	}
	return fmt.Sprintf("snapshot-%020d.bin", int64(seq)), nil
}

// Publish writes, strictly syncs, closes, exclusively renames, and directory-syncs
// one immutable checkpoint. The caller must hold the run's WAL ownership lock
// and serialize publishers. A repeated identical document re-confirms both file
// and directory durability. An error never advances the caller's snapshotSeq;
// a fully named file can still exist after an error or process termination.
func Publish(dir string, doc Document) error {
	return publish(dir, doc, nativeFileOps(), MaxPayloadBytes)
}

type file interface {
	io.Reader
	io.Writer
	Name() string
	Stat() (os.FileInfo, error)
	Close() error
}

// Per-call injection avoids global mutable hooks when runtimes run concurrently.
type fileOps struct {
	checkFS func(string) error
	open    func(string) (file, error)
	temp    func(string) (file, error)
	sync    func(file) error
	rename  func(string, string) error
	remove  func(string) error
}

func nativeFileOps() fileOps {
	return fileOps{
		checkFS: durable.CheckFilesystem,
		open:    func(path string) (file, error) { return os.Open(path) },
		temp:    func(dir string) (file, error) { return os.CreateTemp(dir, ".snapshot-*.tmp") },
		sync:    func(f file) error { return durable.Sync(f.(*os.File)) },
		rename:  durable.RenameExclusive,
		remove:  os.Remove,
	}
}

func publish(dir string, doc Document, ops fileOps, limit int64) (err error) {
	size, err := measure(doc, limit)
	if err != nil {
		return err // rejected size/input must not create even a temporary file
	}
	if err := ops.checkFS(dir); err != nil {
		return err
	}
	directory, err := ops.open(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrInvalid
	}
	name, _ := Filename(doc.SnapshotSeq)
	destination := filepath.Join(dir, name)
	if exists, err := confirmIdentical(destination, doc, size, ops); exists || err != nil {
		if err != nil {
			return err
		}
		return ops.sync(directory)
	}
	f, err := ops.temp(dir)
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() {
		if f != nil {
			err = errors.Join(err, f.Close())
		}
		if temporary != "" {
			if removeErr := ops.remove(temporary); !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	if err := writeEnvelope(f, doc, size); err != nil {
		return err
	}
	if err := ops.sync(f); err != nil {
		return err
	}
	err = f.Close()
	f = nil // Close failure is terminal too; do not reuse or close the FD twice
	if err != nil {
		return err
	}
	if err := ops.rename(temporary, destination); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		// The destination appeared after our first lookup. RENAME_EXCL left it
		// untouched; only exact equality permits idempotent success.
		if exists, err := confirmIdentical(destination, doc, size, ops); err != nil {
			return err
		} else if !exists {
			return ErrExistsMismatch
		}
		if err := ops.remove(temporary); err != nil {
			return err
		}
	}
	temporary = "" // renamed successfully, or our own redundant temp was removed
	return ops.sync(directory)
}

func confirmIdentical(path string, doc Document, size int64, ops fileOps) (exists bool, err error) {
	f, err := ops.open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return true, err
	}
	if !info.Mode().IsRegular() || info.Size() != headerBytes+size+sha256.Size {
		return true, ErrExistsMismatch
	}
	// Compare streaming canonical bytes, including the digest. Equality proves
	// the existing file is this document, without allocating a second checkpoint.
	compare := &comparingWriter{r: f}
	if err := writeEnvelope(compare, doc, size); err != nil {
		return true, err
	}
	var tail [1]byte
	if n, err := f.Read(tail[:]); n != 0 || err != io.EOF {
		if err != nil && err != io.EOF {
			return true, err
		}
		return true, ErrExistsMismatch
	}
	return true, ops.sync(f)
}

type comparingWriter struct {
	r      io.Reader
	buffer [bufferBytes]byte
}

func (w *comparingWriter) Write(b []byte) (int, error) {
	written := 0
	for len(b) > 0 {
		n := min(len(b), len(w.buffer))
		part := w.buffer[:n]
		if _, err := io.ReadFull(w.r, part); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return written, ErrExistsMismatch
			}
			return written, err
		}
		if !bytes.Equal(part, b[:n]) {
			return written, ErrExistsMismatch
		}
		written += n
		b = b[n:]
	}
	return written, nil
}
