package wal

import (
	"errors"
	"io"
	"syscall"
	"time"

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
	metrics   *measurements
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
	started := time.Now()
	group, err := encodeGroup(records)
	if w.metrics != nil {
		w.metrics.encode.Observe(time.Since(started))
	}
	if err != nil {
		return err
	}
	if w.metrics != nil {
		w.metrics.bytes.Add(uint64(len(group)))
	}
	started = time.Now()
	err = writeAll(w.sink, group)
	if w.metrics != nil {
		w.metrics.write.Observe(time.Since(started))
	}
	if err != nil {
		if w.metrics != nil {
			w.metrics.errors.Add(1)
		}
		w.failed = true
		return err
	}
	started = time.Now()
	err = w.sink.Sync()
	if w.metrics != nil {
		w.metrics.sync.Observe(time.Since(started))
	}
	if err != nil {
		if w.metrics != nil {
			w.metrics.errors.Add(1)
		}
		w.failed = true
		return err
	}
	w.last = records[len(records)-1].Seq
	w.validator.accept(records)
	if w.metrics != nil {
		w.metrics.groups.Add(1)
		w.metrics.commands.Add(uint64(len(records)))
	}
	return nil
}
