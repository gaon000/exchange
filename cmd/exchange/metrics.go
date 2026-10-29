package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gaon000/exchange/internal/api"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/telemetry"
)

type metricsRecord struct {
	Version     int               `json:"version"`
	Stage       string            `json:"stage"`
	RunID       string            `json:"runId"`
	StartedUTC  time.Time         `json:"startedUTC"`
	FinishedUTC time.Time         `json:"finishedUTC"`
	Runtime     rt.Statistics     `json:"runtime"`
	HTTP        api.Statistics    `json:"http"`
	Process     telemetry.Process `json:"process"`
	WALBytes    int64             `json:"walBytes,string"`
}

func collectMetrics(stage string, runtime *rt.Runtime, server *api.Server, dir string) (metricsRecord, error) {
	r := metricsRecord{Version: 1, Stage: stage, RunID: runtime.RunID(), StartedUTC: time.Now().UTC(), Runtime: runtime.Statistics(), HTTP: server.Statistics(), Process: telemetry.ReadProcess()}
	info, err := os.Stat(filepath.Join(dir, "commands.wal"))
	if err != nil {
		return r, err
	}
	if !info.Mode().IsRegular() {
		return r, fmt.Errorf("WAL is not a regular file")
	}
	r.WALBytes = info.Size()
	r.FinishedUTC = time.Now().UTC()
	return r, nil
}

type metricsSampler struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
	err  error
}

// startMetrics writes the initial report before readiness, then samples on an
// independent worker. A recording failure requests ordinary server shutdown.
// Finish must run after HTTP/runtime drain to include the final applied work.
func startMetrics(w io.WriteCloser, interval time.Duration, collect func(string) (metricsRecord, error), cancel func()) (*metricsSampler, error) {
	if interval <= 0 {
		return nil, errors.Join(fmt.Errorf("positive metrics interval required"), w.Close())
	}
	write := func(stage string) error {
		value, err := collect(stage)
		if err != nil {
			return err
		}
		return json.NewEncoder(w).Encode(value)
	}
	if err := write("initial"); err != nil {
		return nil, errors.Join(err, w.Close())
	}
	s := &metricsSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer func() {
			if f, ok := w.(interface{ Sync() error }); ok {
				s.err = errors.Join(s.err, f.Sync())
			}
			s.err = errors.Join(s.err, w.Close())
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				s.err = write("final")
				return
			case <-ticker.C:
				if err := write("periodic"); err != nil {
					s.err = err
					cancel()
					return
				}
			}
		}
	}()
	return s, nil
}

func (s *metricsSampler) Finish() error { s.once.Do(func() { close(s.stop) }); <-s.done; return s.err }
