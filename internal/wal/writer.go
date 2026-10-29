package wal

import (
	"errors"
	"io"
	"syscall"

	d "github.com/gaon000/exchange/internal/domain"
)

type sink interface {
	Write([]byte) (int, error)
	Sync() error
}
type writer struct {
	sink      sink
	validator *validator
	last      d.Int
	failed    bool
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	return nil
}

func (w *writer) append(records []Record) error {
	if w.failed {
		return ErrPoisoned
	}
	if err := w.validator.check(records, w.last); err != nil {
		return err
	}
	group, err := encodeGroup(records)
	if err != nil {
		return err
	}
	if err = writeAll(w.sink, group); err != nil {
		w.failed = true
		return err
	}
	if err = w.sink.Sync(); err != nil {
		w.failed = true
		return err
	}
	w.last = records[len(records)-1].Seq
	w.validator.accept(records)
	return nil
}
