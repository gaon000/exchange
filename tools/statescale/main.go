// statescale prepares bounded fixtures and measures one fresh-process recovery.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	"github.com/gaon000/exchange/internal/engine"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/telemetry"
	"github.com/gaon000/exchange/internal/wal"
)

type counts struct {
	Orders, Active, Filled, Cancelled, Trades, Results int
}

type fixture struct {
	Version  int               `json:"version"`
	Kind     string            `json:"kind"`
	Commands int               `json:"commands"`
	RunID    string            `json:"runId"`
	Counts   counts            `json:"counts"`
	Files    map[string]string `json:"files"`
}

type memory struct {
	HeapAlloc, HeapInuse, HeapSys, TotalAlloc, Mallocs, NumGC uint64
}

type phase struct {
	WallNanos              int64
	Before, After, AfterGC memory
	PeakRSSBytes           int64
}

type observation struct {
	Version                   int
	Mode                      string
	Commands                  int
	Open, Snapshot            phase
	Recovery                  rt.RecoveryReport
	SnapshotStages            telemetry.StageReport
	Publication               string
	Verified                  bool
	SnapshotSHA256, WALSHA256 string
	GOMAXPROCS                int
	GoVersion                 string
}

func main() {
	if err := command(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func command(args []string) error {
	if len(args) == 0 {
		return errors.New("expected prepare or measure")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := fs.String("dir", "", "new fixture or coordinator-owned copied run")
	output := fs.String("output", "", "new JSON result file")
	n := fs.Int("count", 0, "multiple of four, 4..10000")
	kind := fs.String("kind", "", "active or history")
	mode := fs.String("mode", "", "wal or snapshot")
	want := fs.String("snapshot-sha", "", "reference complete snapshot SHA256")
	walSHA := fs.String("wal-sha", "", "reference WAL SHA256")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" || *output == "" || *n < 4 || *n > 10000 || *n%4 != 0 {
		return errors.New("invalid probe arguments")
	}
	if err := durable.CheckFilesystem(filepath.Dir(*dir)); err != nil {
		return err
	}
	switch args[0] {
	case "prepare":
		if *kind != "active" && *kind != "history" {
			return errors.New("invalid state kind")
		}
		f, err := prepare(*dir, *kind, *n)
		if err != nil {
			return err
		}
		return save(*output, f)
	case "measure":
		if *mode != "wal" && *mode != "snapshot" {
			return errors.New("invalid recovery mode")
		}
		for _, sha := range []string{*want, *walSHA} {
			b, e := hex.DecodeString(sha)
			if e != nil || len(b) != 32 || hex.EncodeToString(b) != sha {
				return errors.New("invalid expected SHA256")
			}
		}
		if info, err := os.Lstat(filepath.Join(*dir, ".statescale-owned")); err != nil || !info.Mode().IsRegular() {
			return errors.New("measure requires a coordinator-owned copied run")
		}
		o, err := measure(*dir, *mode, *n, *want, *walSHA)
		if err != nil {
			return err
		}
		return save(*output, o)
	}
	return errors.New("unknown probe command")
}

func save(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, e := f.Write(append(b, '\n'))
	return errors.Join(e, f.Sync(), f.Close())
}

func digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}

func configuration(n int) d.Config {
	c := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: d.Int(n + 100), QuoteAtoms: d.Int(n)*20000 + 100000}, {AccountID: "bob", BaseLots: d.Int(n + 100), QuoteAtoms: d.Int(n)*20000 + 100000}})
	c.MaxCommands = d.Int(n)
	return c
}

func input(run, kind string, i int) d.Command {
	c := d.Command{RunID: run, AccountID: "alice", Kind: d.PlaceOrder}
	place := func(id string, side d.Side, price, qty d.Int) d.Place {
		return d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: price, QuantityLots: qty}
	}
	if kind == "active" {
		c.Place = place(fmt.Sprintf("a-%05d", i), d.Buy, 9900, 1)
		if i%2 == 1 {
			c.AccountID = "bob"
			c.Place.Side = d.Sell
			c.Place.PriceTicks = 10100
		}
		return c
	}
	cycle := i / 4
	maker, taker := fmt.Sprintf("maker-%05d", cycle), fmt.Sprintf("taker-%05d", cycle)
	switch i % 4 {
	case 0:
		c.AccountID = "bob"
		c.Place = place(maker, d.Sell, 10000, 2)
	case 1:
		c.Place = place(taker, d.Buy, 10000, 1)
	case 2:
		c.AccountID = "bob"
		c.Kind = d.CancelOrder
		c.Cancel = d.Cancel{CancelRequestID: fmt.Sprintf("cancel-maker-%05d", cycle), ClientOrderID: maker}
	case 3:
		c.Kind = d.CancelOrder
		c.Cancel = d.Cancel{CancelRequestID: fmt.Sprintf("cancel-filled-%05d", cycle), ClientOrderID: taker}
	}
	return c
}

func composition(cp d.Checkpoint) counts {
	c := counts{Orders: len(cp.Orders), Trades: len(cp.Trades), Results: len(cp.Results)}
	for _, o := range cp.Orders {
		if o.Order.Active() {
			c.Active++
		}
		if o.Order.Status == d.Filled {
			c.Filled++
		}
		if o.Order.Status == d.Cancelled {
			c.Cancelled++
		}
	}
	return c
}

func prepare(dir, kind string, n int) (_ fixture, err error) {
	s, err := wal.Init(dir, configuration(n))
	if err != nil {
		return fixture{}, err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	g := s.Genesis()
	e, err := engine.New(g.RunID, g.Config)
	if err != nil {
		return fixture{}, err
	}
	for start := 0; start < n; start += 256 {
		var group []wal.Record
		for i := start; i < min(start+256, n); i++ {
			group = append(group, wal.Record{Seq: d.Int(i + 1), Command: input(g.RunID, kind, i)})
		}
		if err = s.Append(group); err != nil {
			return fixture{}, err
		}
		for _, record := range group {
			if _, err = e.Apply(record.Seq, record.Command); err != nil {
				return fixture{}, err
			}
		}
	}
	if err = e.CheckInvariants(); err != nil {
		return fixture{}, err
	}
	cp := e.Capture()
	got := composition(cp)
	expected := counts{Orders: n, Active: n, Results: n}
	if kind == "history" {
		expected = counts{Orders: n / 2, Filled: n / 4, Cancelled: n / 4, Trades: n / 4, Results: n}
	}
	if got != expected {
		return fixture{}, fmt.Errorf("fixture composition mismatch: %+v want %+v", got, expected)
	}
	doc := snapshot.Document{Identity: snapshot.Identity{RunID: g.RunID, ConfigHash: g.ConfigHash, RulesVersion: g.Config.RulesVersion, ResponseVersion: g.Config.ResponseVersion}, SnapshotSeq: d.Int(n), State: cp}
	if err = snapshot.Publish(dir, doc); err != nil {
		return fixture{}, err
	}
	name, _ := snapshot.Filename(d.Int(n))
	f := fixture{Version: 1, Kind: kind, Commands: n, RunID: g.RunID, Counts: got, Files: map[string]string{}}
	for _, name := range []string{".lock", "genesis.bin", "commands.wal", name} {
		sha, e := digest(filepath.Join(dir, name))
		if e != nil {
			return fixture{}, e
		}
		f.Files[name] = sha
	}
	return f, nil
}

func mem() memory {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return memory{m.HeapAlloc, m.HeapInuse, m.HeapSys, m.TotalAlloc, m.Mallocs, uint64(m.NumGC)}
}

func measure(dir, mode string, n int, want, walSHA string) (_ observation, err error) {
	files, err := snapshot.List(dir)
	if err != nil {
		return observation{}, err
	}
	if mode == "wal" && len(files) != 0 || mode == "snapshot" && len(files) != 1 {
		return observation{}, errors.New("wrong snapshot presence for selected mode")
	}
	o := observation{Version: 1, Mode: mode, Commands: n, GOMAXPROCS: runtime.GOMAXPROCS(0), GoVersion: runtime.Version(), Publication: "new"}
	if mode == "snapshot" {
		o.Publication = "reconfirm"
	}
	runtime.GC()
	o.Open.Before = mem()
	start := time.Now()
	r, err := rt.Open(dir, rt.DefaultOptions())
	o.Open.WallNanos = time.Since(start).Nanoseconds()
	if err != nil {
		return o, err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(err, r.Shutdown(c))
	}()
	o.Open.After = mem()
	o.Open.PeakRSSBytes, err = peakRSS()
	if err != nil {
		return o, err
	}
	runtime.GC()
	o.Open.AfterGC = mem()
	runtime.KeepAlive(r)
	o.Recovery = r.Recovery()
	selected := d.Int(0)
	if mode == "snapshot" {
		selected = d.Int(n)
	}
	if o.Recovery.SnapshotSeq != selected || o.Recovery.ReplayedCommands != d.Int(n)-selected || len(o.Recovery.Skipped) != 0 || (o.Recovery.SnapshotFile != "") != (mode == "snapshot") {
		return o, fmt.Errorf("unexpected recovery: %+v", o.Recovery)
	}
	runtime.GC()
	o.Snapshot.Before = mem()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	start = time.Now()
	saved, e := r.Snapshot(ctx)
	o.Snapshot.WallNanos = time.Since(start).Nanoseconds()
	cancel()
	if e != nil || saved.SnapshotSeq != d.Int(n) {
		return o, fmt.Errorf("snapshot publication: %+v %v", saved, e)
	}
	o.Snapshot.After = mem()
	o.Snapshot.PeakRSSBytes, err = peakRSS()
	if err != nil {
		return o, err
	}
	runtime.GC()
	o.Snapshot.AfterGC = mem()
	runtime.KeepAlive(r)
	o.SnapshotStages = r.Statistics().SnapshotStages
	// Validation is deliberately after both measured phases and RSS readings.
	name, _ := snapshot.Filename(d.Int(n))
	o.SnapshotSHA256, err = digest(filepath.Join(dir, name))
	if err != nil {
		return o, err
	}
	o.WALSHA256, err = digest(filepath.Join(dir, "commands.wal"))
	if err != nil {
		return o, err
	}
	if o.SnapshotSHA256 != want || o.WALSHA256 != walSHA {
		return o, errors.New("complete canonical state or WAL bytes changed")
	}
	if o.Snapshot.PeakRSSBytes > 2<<30 {
		return o, errors.New("worker RSS high-water exceeded 2GiB")
	}
	o.Verified = true
	return o, nil
}
