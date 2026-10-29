package wal

import (
	"errors"
	"testing"
)

func TestAppendMetricsSeparateWriteSyncAndDurability(t *testing.T) {
	for _, mode := range []string{"ok", "write", "sync"} {
		t.Run(mode, func(t *testing.T) {
			g := testGenesis(t)
			s := &memorySink{}
			fault := errors.New("disk")
			if mode == "write" {
				s.writeErr = fault
				s.failAfter = 3
			}
			if mode == "sync" {
				s.syncErr = fault
			}
			store := &Store{}
			w := writer{sink: s, validator: newValidator(g), metrics: &store.metrics}
			err := w.append([]Record{place(1, "alice", "one")})
			if (err == nil) != (mode == "ok") {
				t.Fatal(err)
			}
			stats := store.Statistics()
			if stats.Encode.Count != 1 || stats.Write.Count != 1 || stats.EncodedBytes == 0 {
				t.Fatalf("missing stages: %+v", stats)
			}
			if mode == "write" && stats.Sync.Count != 0 || mode != "write" && stats.Sync.Count != 1 {
				t.Fatal("wrong sync observation", stats)
			}
			if mode == "ok" {
				if stats.Groups != 1 || stats.Commands != 1 || stats.IOErrors != 0 || w.last != 1 {
					t.Fatal(stats)
				}
			} else {
				if stats.Groups != 0 || stats.Commands != 0 || stats.IOErrors != 1 || w.last != 0 {
					t.Fatal("failed I/O claimed durability", stats)
				}
				_ = w.append([]Record{place(1, "alice", "one")})
				if store.Statistics().Write.Count != 1 {
					t.Fatal("poisoned writer reused")
				}
			}
		})
	}
}
