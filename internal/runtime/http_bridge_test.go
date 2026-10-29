package runtime

import (
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

// HTTPTestHooks exposes private boundaries only to the external test package.
// This file is absent from normal builds and does not add a production option.
type HTTPTestHooks struct {
	Append        func(*wal.Store, []wal.Record) error
	BeforeApply   func(wal.Record)
	AfterApply    func(wal.Record)
	Publish       func(string, snapshot.Document) error
	BeforeCapture func()
	AfterCapture  func(snapshot.Document)
}

type httpTestJournal struct {
	*wal.Store
	appendGroup func(*wal.Store, []wal.Record) error
}

func (s httpTestJournal) Append(records []wal.Record) error {
	if s.appendGroup != nil {
		return s.appendGroup(s.Store, records)
	}
	return s.Store.Append(records)
}

type httpTestCheckpoints struct {
	diskCheckpoints
	publish func(string, snapshot.Document) error
}

func (s httpTestCheckpoints) Publish(doc snapshot.Document) error {
	if s.publish != nil {
		return s.publish(s.dir, doc)
	}
	return s.diskCheckpoints.Publish(doc)
}

// OpenHTTPTestRuntime uses the real store, recovery and workers with test gates.
func OpenHTTPTestRuntime(dir string, opts Options, h HTTPTestHooks) (*Runtime, error) {
	s, err := wal.Open(dir)
	if err != nil {
		return nil, err
	}
	return startWithSnapshots(httpTestJournal{s, h.Append}, httpTestCheckpoints{diskCheckpoints{dir}, h.Publish}, opts,
		hooks{beforeApply: h.BeforeApply, afterApply: h.AfterApply, beforeCapture: h.BeforeCapture, afterCapture: h.AfterCapture})
}

// HTTPTestQueues observes bounded channels without accessing the owner's state.
func HTTPTestQueues(r *Runtime) (commands, queries int) { return len(r.commands), len(r.queries) }
