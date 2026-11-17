package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

const scenarioRunID = "00000000-0000-4000-8000-000000000001"

type concurrentAttempt struct {
	Client         int                 `json:"client"`
	Command        d.CheckpointCommand `json:"command"`
	Fault          string              `json:"fault,omitempty"`
	ExpectConflict bool                `json:"expectConflict,omitempty"`
}

type concurrentSchedule struct {
	Order         []d.CheckpointCommand `json:"order"`
	SnapshotAfter d.Int                 `json:"snapshotAfter"`
}

type concurrentGroup struct {
	Initial        []concurrentAttempt `json:"initial"`
	Retries        []concurrentAttempt `json:"retries"`
	SnapshotClient int                 `json:"snapshotClient"`
	Recorded       *concurrentSchedule `json:"recorded,omitempty"`
}

func concurrentPlan(seed int64) (plan, error) {
	if seed < 0 || seed > 1<<31-1 {
		return plan{}, errors.New("concurrent seed must be 0..2147483647")
	}
	rng := rand.New(rand.NewSource(seed))
	p := plan{Version: 2, Seed: seed}
	for wave := 0; wave < 2; wave++ {
		id := fmt.Sprintf("parallel-%d", wave)
		price := d.Int(100 + wave*20)
		maker := place("alice", id+"-maker", d.Sell, price, 6)
		fill := place("bob", id+"-fill", d.Buy, price, 1)
		a := place("bob", id+"-buy", d.Buy, price, d.Int(1+rng.Intn(3)))
		cancel := cancelOrder("alice", id+"-cancel", id+"-maker")
		held := place("carol", id+"-held", d.Buy, 90, d.Int(1+rng.Intn(3)))
		tail := place([]string{"alice", "bob", "carol"}[rng.Intn(3)], id+"-tail", d.Sell, d.Int(90+rng.Intn(6)), d.Int(1+rng.Intn(3)))
		g := &concurrentGroup{SnapshotClient: 0,
			Initial: []concurrentAttempt{{Client: 0, Command: *a.Command, Fault: "drop-headers"}, {Client: 1, Command: *a.Command, Fault: "truncate-body"}, {Client: 2, Command: *cancel.Command}, {Client: 3, Command: *held.Command, Fault: "held-original"}},
			Retries: []concurrentAttempt{{Client: 0, Command: *a.Command}, {Client: 1, Command: *cancel.Command}, {Client: 2, Command: *held.Command}, {Client: 3, Command: *tail.Command}},
		}
		p.Actions = append(p.Actions, maker, fill, action{Kind: "concurrent", Concurrent: g})
	}
	p.Actions = append(p.Actions, action{Kind: "restart"})
	return p, validatePlan(p)
}

func validateConcurrent(g concurrentGroup, known map[d.Key]d.Command) error {
	return validateConcurrentVersion(g, known, 2)
}

func validateConcurrentVersion(g concurrentGroup, known map[d.Key]d.Command, version int) error {
	n := len(g.Initial)
	if n < 4 || n > 12 || len(g.Retries) != n || version == 2 && n != 4 || g.SnapshotClient < 0 || g.SnapshotClient >= n {
		return errors.New("concurrent phase requires four clients 0..3")
	}
	before := make(map[d.Key]d.Command, len(known))
	for k, c := range known {
		before[k] = c
	}
	fresh := make(map[d.Key]d.Command)
	heldCount := 0
	var held d.Command
	markerNew := false
	for phase, list := range [][]concurrentAttempt{g.Initial, g.Retries} {
		clients := make(map[int]bool)
		for _, a := range list {
			if a.Client < 0 || a.Client >= n || clients[a.Client] {
				return errors.New("invalid/duplicate concurrent client")
			}
			clients[a.Client] = true
			c, err := a.Command.Command(scenarioRunID)
			if err != nil || c.AccountID != "alice" && c.AccountID != "bob" && c.AccountID != "carol" {
				return errors.New("invalid concurrent command/account")
			}
			if phase == 1 && a.Fault != "" {
				return errors.New("retry phase cannot lose responses")
			}
			switch a.Fault {
			case "", "drop-headers", "truncate-body":
			case "held-original":
				heldCount++
				held = c
			default:
				return errors.New("unknown concurrent fault")
			}
			if a.ExpectConflict {
				old, exists := before[c.Key()]
				if version != 3 || !exists || old == c || a.Fault != "" || phase == 0 && a.Client == g.SnapshotClient {
					return errors.New("conflict requires a different payload for a previously completed key")
				}
				continue
			}
			if old, ok := known[c.Key()]; ok && old != c {
				return errors.New("concurrent key reuses different payload")
			}
			known[c.Key()] = c
			if _, ok := before[c.Key()]; !ok {
				fresh[c.Key()] = c
			}
			if phase == 0 && a.Client == g.SnapshotClient {
				_, existed := before[c.Key()]
				markerNew = !existed && a.Fault != "held-original"
			}
		}
	}
	if heldCount != 1 || !markerNew {
		return errors.New("requires one held original and a new snapshot witness")
	}
	if _, exists := before[held.Key()]; exists {
		return errors.New("held original must use a new key")
	}
	copies := 0
	retried := false
	for _, a := range g.Initial {
		c, _ := a.Command.Command(scenarioRunID)
		if c.Key() == held.Key() {
			copies++
		}
	}
	for _, a := range g.Retries {
		c, _ := a.Command.Command(scenarioRunID)
		retried = retried || c == held
	}
	if copies != 1 || !retried {
		return errors.New("held original must be isolated then retried identically")
	}
	if g.Recorded != nil {
		if len(g.Recorded.Order) != len(fresh) || g.Recorded.SnapshotAfter < 1 || g.Recorded.SnapshotAfter > d.Int(len(fresh)) {
			return errors.New("invalid recorded schedule size/boundary")
		}
		seen := make(map[d.Key]bool)
		initial := make(map[d.Key]bool)
		var marker d.Key
		for _, a := range g.Initial {
			c, _ := a.Command.Command(scenarioRunID)
			if a.Fault != "held-original" && !a.ExpectConflict {
				initial[c.Key()] = true
			}
			if a.Client == g.SnapshotClient {
				marker = c.Key()
			}
		}
		markerPos := d.Int(0)
		leftInitial := false
		for i, input := range g.Recorded.Order {
			c, err := input.Command(scenarioRunID)
			if err != nil || seen[c.Key()] || fresh[c.Key()] != c {
				return errors.New("invalid recorded command order")
			}
			seen[c.Key()] = true
			if c.Key() == marker {
				markerPos = d.Int(i + 1)
			}
			if !initial[c.Key()] {
				leftInitial = true
			}
			if initial[c.Key()] && leftInitial {
				return errors.New("recorded order crosses phase gates")
			}
			if d.Int(i+1) <= g.Recorded.SnapshotAfter && !initial[c.Key()] {
				return errors.New("snapshot includes retry-phase command")
			}
		}
		if markerPos == 0 || markerPos > g.Recorded.SnapshotAfter {
			return errors.New("snapshot precedes its witness")
		}
	}
	return nil
}

// admissionOrder constrains new WAL keys on recorded replay. Same-key copies
// remain concurrent; the next new key waits for a complete upstream result.
type admissionOrder struct {
	mu            sync.Mutex
	order         []d.Key
	next          int
	snapshotAfter int
	snapshotDone  bool
	changed       chan struct{}
}

func newAdmissionOrder(g concurrentGroup, run string) *admissionOrder {
	if g.Recorded == nil {
		return nil
	}
	o := &admissionOrder{snapshotAfter: int(g.Recorded.SnapshotAfter), changed: make(chan struct{})}
	for _, input := range g.Recorded.Order {
		c, _ := input.Command(run)
		o.order = append(o.order, c.Key())
	}
	return o
}
func (o *admissionOrder) signal() { close(o.changed); o.changed = make(chan struct{}) }
func (o *admissionOrder) enter(ctx context.Context, k d.Key) error {
	if o == nil {
		return nil
	}
	for {
		o.mu.Lock()
		position := -1
		for i, key := range o.order {
			if key == k {
				position = i
				break
			}
		}
		allowed := position < o.next || position < 0 || (position == o.next && (o.next != o.snapshotAfter || o.snapshotDone))
		wait := o.changed
		o.mu.Unlock()
		if allowed {
			return nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (o *admissionOrder) complete(k d.Key) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.next < len(o.order) && o.order[o.next] == k {
		o.next++
		o.signal()
	}
}
func (o *admissionOrder) beforeSnapshot(ctx context.Context) error {
	if o == nil {
		return nil
	}
	for {
		o.mu.Lock()
		ready := o.next >= o.snapshotAfter
		wait := o.changed
		o.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (o *admissionOrder) afterSnapshot() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.snapshotDone = true
	o.signal()
}

func reproduction(output string) string {
	path := filepath.Join(output, "scenario.json")
	entries, _ := os.ReadDir(output)
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, "replay-") && strings.HasSuffix(name, ".json") {
			candidate := filepath.Join(output, name)
			b, err := readRegular(candidate, 1<<20)
			var p plan
			if err == nil && strictjson.Decode(b, &p) == nil && validatePlan(p) == nil {
				path = candidate
			}
		}
	}
	return "sh scripts/random-recovery.sh --replay " + shellQuote(path)
}
