// Package snapshot encodes detached state without buffering the full payload.
// File publication and runtime ownership are separate from the wire codec.
package snapshot

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	d "github.com/gaon000/exchange/internal/domain"
)

// Wire limits bound the whole payload and each fixed-size JSON value. Large
// arrays, including the FIFO within a single price level, are streamed.
const (
	MaxPayloadBytes int64 = 2 << 30
	maxValueBytes         = 16 << 10
	headerBytes           = 20
	bufferBytes           = 32 << 10
)

// Errors distinguish a candidate that may be skipped from a foreign run/version
// or impossible sequence that must stop recovery. Underlying I/O errors retain
// their original identity and must not be treated as ordinary corruption.
var (
	ErrCorrupt  = errors.New("corrupt snapshot")
	ErrIdentity = errors.New("snapshot identity/version/sequence mismatch")
	ErrInvalid  = errors.New("invalid snapshot input")
	ErrTooLarge = errors.New("snapshot exceeds size limit")
)

// Identity binds the snapshot to the immutable Genesis configuration.
type Identity struct {
	RunID           string `json:"runId"`
	ConfigHash      string `json:"configHash"`
	RulesVersion    int    `json:"rulesVersion"`
	ResponseVersion int    `json:"responseVersion"`
}

func (id Identity) valid() bool {
	hash, err := hex.DecodeString(id.ConfigHash)
	return d.ValidRunID(id.RunID) && err == nil && len(hash) == sha256.Size && hex.EncodeToString(hash) == id.ConfigHash && id.RulesVersion == d.Version && id.ResponseVersion == d.Version
}

// Document is a snapshot at one applied command boundary. State must be an
// immutable, validated checkpoint for the entire duration of Write.
type Document struct {
	Identity
	SnapshotSeq d.Int        `json:"snapshotSeq"`
	State       d.Checkpoint `json:"state"`
}

func (doc Document) shape() error {
	c := doc.State
	if !doc.Identity.valid() || doc.SnapshotSeq < 0 || d.Int(len(c.Results)) != doc.SnapshotSeq || len(c.Accounts) < 1 || len(c.Accounts) > 10_000 || c.Orders == nil || c.Trades == nil || c.Results == nil || c.Book.Bids == nil || c.Book.Asks == nil || d.Int(len(c.Orders)) > doc.SnapshotSeq || d.Int(len(c.Trades)) > doc.SnapshotSeq {
		return ErrInvalid
	}
	var refs d.Int
	for _, levels := range [][]d.CheckpointLevel{c.Book.Bids, c.Book.Asks} {
		for _, level := range levels {
			if len(level.Orders) == 0 || d.Int(len(level.Orders)) > doc.SnapshotSeq-refs {
				return ErrInvalid
			}
			refs += d.Int(len(level.Orders))
		}
	}
	return nil
}

// Write emits one canonical envelope. It counts the payload before touching w,
// then streams it through SHA-256 using bounded buffers. It does not sync or
// publish a file, and never changes the supplied checkpoint.
func Write(w io.Writer, doc Document) error { return write(w, doc, MaxPayloadBytes) }

func write(w io.Writer, doc Document, limit int64) error {
	n, err := measure(doc, limit)
	if err != nil {
		return err
	}
	return writeEnvelope(w, doc, n)
}

func measure(doc Document, limit int64) (int64, error) {
	if err := doc.shape(); err != nil {
		return 0, err
	}
	count := &counter{limit: limit}
	if err := payload(count, doc); err != nil {
		return 0, err
	}
	return count.n, nil
}

func writeEnvelope(w io.Writer, doc Document, payloadBytes int64) error {
	var header [headerBytes]byte
	copy(header[:], "EXSNP001")
	binary.LittleEndian.PutUint32(header[8:12], 1)
	binary.LittleEndian.PutUint64(header[12:], uint64(payloadBytes))
	hash := sha256.New()
	sink := io.MultiWriter(fullWriter{w}, hash)
	if _, err := sink.Write(header[:]); err != nil {
		return err
	}
	buffer := bufio.NewWriterSize(sink, bufferBytes)
	if err := payload(buffer, doc); err != nil {
		return err
	}
	if err := buffer.Flush(); err != nil {
		return err
	}
	_, err := (fullWriter{w}).Write(hash.Sum(nil))
	return err
}

type counter struct{ n, limit int64 }

func (c *counter) Write(b []byte) (int, error) {
	if int64(len(b)) > c.limit-c.n {
		return 0, ErrTooLarge
	}
	c.n += int64(len(b))
	return len(b), nil
}

type fullWriter struct{ io.Writer }

func (w fullWriter) Write(b []byte) (int, error) {
	written := 0
	for len(b) != 0 {
		n, err := w.Writer.Write(b)
		if n < 0 || n > len(b) {
			return written, io.ErrShortWrite
		}
		written += n
		b = b[n:]
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

type encoder struct {
	w   io.Writer
	err error
}

func (e *encoder) text(s string) {
	if e.err == nil {
		_, e.err = io.WriteString(e.w, s)
	}
}
func (e *encoder) value(v any) {
	if e.err != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		e.err = err
	} else if len(b) > maxValueBytes {
		e.err = ErrInvalid
	} else {
		_, e.err = e.w.Write(b)
	}
}
func emitArray[T any](e *encoder, values []T) {
	e.text("[")
	for i, v := range values {
		if e.err != nil {
			return
		}
		if i > 0 {
			e.text(",")
		}
		e.value(v)
	}
	e.text("]")
}
func (e *encoder) book(levels []d.CheckpointLevel) {
	e.text("[")
	for i, level := range levels {
		if e.err != nil {
			return
		}
		if i > 0 {
			e.text(",")
		}
		e.text(`{"priceTicks":`)
		e.value(level.PriceTicks)
		e.text(`,"orders":`)
		emitArray(e, level.Orders)
		e.text("}")
	}
	e.text("]")
}
func payload(w io.Writer, doc Document) error {
	e := &encoder{w: w}
	e.text(`{"runId":`)
	e.value(doc.RunID)
	e.text(`,"configHash":`)
	e.value(doc.ConfigHash)
	e.text(`,"rulesVersion":`)
	e.value(doc.RulesVersion)
	e.text(`,"responseVersion":`)
	e.value(doc.ResponseVersion)
	e.text(`,"snapshotSeq":`)
	e.value(doc.SnapshotSeq)
	e.text(`,"state":{"accounts":`)
	emitArray(e, doc.State.Accounts)
	e.text(`,"orders":`)
	emitArray(e, doc.State.Orders)
	e.text(`,"book":{"bids":`)
	e.book(doc.State.Book.Bids)
	e.text(`,"asks":`)
	e.book(doc.State.Book.Asks)
	e.text(`},"trades":`)
	emitArray(e, doc.State.Trades)
	e.text(`,"results":`)
	emitArray(e, doc.State.Results)
	e.text("}}")
	return e.err
}

// Read checks the exact envelope and its digest before decoding state. Only
// bounded values are buffered; arrays grow from actual parsed items, never from
// declared byte lengths. maxSeq is the verified end of the retained WAL. The
// caller must hold run ownership, call engine.Restore to validate semantics,
// compare saved inputs with the WAL, and confirm recovery durability.
func Read(r io.ReaderAt, size int64, want Identity, maxSeq d.Int) (Document, error) {
	if !want.valid() || maxSeq < 0 {
		return Document{}, ErrInvalid
	}
	if size < headerBytes+sha256.Size {
		return Document{}, corrupt("incomplete envelope")
	}
	var header [headerBytes]byte
	if _, err := io.ReadFull(io.NewSectionReader(r, 0, headerBytes), header[:]); err != nil {
		return Document{}, readError(err)
	}
	n := binary.LittleEndian.Uint64(header[12:])
	if string(header[:8]) != "EXSNP001" || n == 0 || n > uint64(MaxPayloadBytes) || size != int64(n)+headerBytes+sha256.Size {
		return Document{}, corrupt("envelope bounds/magic")
	}
	hash := sha256.New()
	copied, err := io.CopyBuffer(hash, io.NewSectionReader(r, 0, headerBytes+int64(n)), make([]byte, bufferBytes))
	if err != nil {
		return Document{}, readError(err)
	}
	if copied != headerBytes+int64(n) {
		return Document{}, corrupt("incomplete payload")
	}
	var digest [sha256.Size]byte
	if _, err := io.ReadFull(io.NewSectionReader(r, headerBytes+int64(n), sha256.Size), digest[:]); err != nil {
		return Document{}, readError(err)
	}
	if !bytes.Equal(hash.Sum(nil), digest[:]) {
		return Document{}, corrupt("checksum")
	}
	if binary.LittleEndian.Uint32(header[8:12]) != 1 {
		return Document{}, ErrIdentity
	}
	// Re-hash the bytes actually decoded as well: a changed backing reader must
	// not pass based on the digest of an earlier read.
	hash.Reset()
	_, _ = hash.Write(header[:])
	reader := io.TeeReader(io.NewSectionReader(r, headerBytes, int64(n)), hash)
	doc, err := decodePayload(reader, want, maxSeq)
	if err != nil {
		return Document{}, err
	}
	if !bytes.Equal(hash.Sum(nil), digest[:]) {
		return Document{}, corrupt("payload changed during read")
	}
	return doc, nil
}

func corrupt(reason string) error { return fmt.Errorf("%w: %s", ErrCorrupt, reason) }
func readError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return corrupt("unexpected EOF")
	}
	return err
}
