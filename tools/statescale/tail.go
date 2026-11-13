package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	"github.com/gaon000/exchange/internal/engine"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type tailWitness struct {
	Command d.Command
	First   d.Result
}
type tailFixture struct {
	Version   int
	Kind      string
	Commands  int
	RunID     string
	Counts    counts
	Cuts      []int
	Files     map[string]string
	Witnesses []tailWitness
}
type tailObservation struct {
	Version                                 int
	Kind                                    string
	Commands, Tail                          int
	Open                                    phase
	Recovery                                rt.RecoveryReport
	FirstResponsesVerified, RetriesVerified int
	OrdersBalancesTradeVerified             bool
	FinalSnapshotSHA256, WALSHA256          string
	Verified                                bool
	GoVersion                               string
	GOMAXPROCS                              int
	RSSAfterGCBytes                         int64 `json:",omitempty"`
}

func tailCuts(n int) []int {
	if n == 24 {
		return []int{12, 16, 24}
	}
	return []int{5000, 9000, 10000}
}

func tailInput(run, kind string, i, n int) d.Command {
	if i >= 2 && i < n-6 {
		return input(run, kind, i-2)
	}
	c := d.Command{RunID: run, AccountID: "alice", Kind: d.PlaceOrder}
	switch i {
	case 0:
		c.Place = d.Place{ClientOrderID: "carry-fill", Symbol: d.Symbol, Side: d.Buy, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 9990, QuantityLots: 2}
	case 1:
		c.Place = d.Place{ClientOrderID: "carry-cancel", Symbol: d.Symbol, Side: d.Buy, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 9980, QuantityLots: 1}
	case n - 6:
		c.AccountID = "bob"
		c.Place = d.Place{ClientOrderID: "carry-taker", Symbol: d.Symbol, Side: d.Sell, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 9990, QuantityLots: 1}
	default:
		c.Kind = d.CancelOrder
		switch i {
		case n - 5:
			c.Cancel = d.Cancel{CancelRequestID: "carry-fill-cancel", ClientOrderID: "carry-fill"}
		case n - 4:
			c.Cancel = d.Cancel{CancelRequestID: "carry-cancel-request", ClientOrderID: "carry-cancel"}
		case n - 3:
			c.AccountID = "bob"
			c.Cancel = d.Cancel{CancelRequestID: "carry-taker-cancel", ClientOrderID: "carry-taker"}
		case n - 2:
			c.Cancel = d.Cancel{CancelRequestID: "carry-again", ClientOrderID: "carry-fill"}
		case n - 1:
			c.AccountID = "bob"
			c.Cancel = d.Cancel{CancelRequestID: "carry-missing", ClientOrderID: "missing"}
		}
	}
	return c
}

func expectedTailCounts(kind string, n int) counts {
	if kind == "active" {
		return counts{Orders: n - 5, Active: n - 8, Filled: 1, Cancelled: 2, Trades: 1, Results: n}
	}
	cycles := (n - 8) / 4
	return counts{Orders: 2*cycles + 3, Filled: cycles + 1, Cancelled: cycles + 2, Trades: cycles + 1, Results: n}
}

func expectedTailBalance(kind, account string, n int) d.Balances {
	base, quote := d.Int(n+100), d.Int(n)*20000+100000
	if kind == "history" {
		cycles := d.Int((n - 8) / 4)
		if account == "alice" {
			return d.Balances{Base: d.Balance{Available: base + cycles + 1}, Quote: d.Balance{Available: quote - cycles*10000 - 9990}}
		}
		return d.Balances{Base: d.Balance{Available: base - cycles - 1}, Quote: d.Balance{Available: quote + cycles*10000 + 9990}}
	}
	resting := d.Int((n - 8) / 2)
	if account == "alice" {
		return d.Balances{Base: d.Balance{Available: base + 1}, Quote: d.Balance{Available: quote - resting*9900 - 9990, Reserved: resting * 9900}}
	}
	return d.Balances{Base: d.Balance{Available: base - resting - 1, Reserved: resting}, Quote: d.Balance{Available: quote + 9990}}
}

func checkTailOrder(id string, o d.Order) bool {
	switch id {
	case "carry-fill":
		return o.Status == d.Cancelled && o.OriginalQty == 2 && o.ExecutedQty == 1 && o.CancelledQty == 1 && o.OpenQty == 0 && o.ExecutedQuoteAtoms == 9990 && o.Reason == d.UserCancel
	case "carry-cancel":
		return o.Status == d.Cancelled && o.OriginalQty == 1 && o.ExecutedQty == 0 && o.CancelledQty == 1 && o.OpenQty == 0 && o.Reason == d.UserCancel
	case "carry-taker":
		return o.Status == d.Filled && o.OriginalQty == 1 && o.ExecutedQty == 1 && o.OpenQty == 0 && o.ExecutedQuoteAtoms == 9990
	}
	return false
}

func prepareTail(dir, kind string, n int) (_ tailFixture, err error) {
	s, err := wal.Init(dir, configuration(n))
	if err != nil {
		return tailFixture{}, err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	g := s.Genesis()
	e, err := engine.New(g.RunID, g.Config)
	if err != nil {
		return tailFixture{}, err
	}
	f := tailFixture{Version: 1, Kind: kind, Commands: n, RunID: g.RunID, Cuts: tailCuts(n), Files: map[string]string{}}
	start := 0
	for _, cut := range f.Cuts {
		for start < cut {
			var group []wal.Record
			end := min(start+256, cut)
			for i := start; i < end; i++ {
				group = append(group, wal.Record{Seq: d.Int(i + 1), Command: tailInput(g.RunID, kind, i, n)})
			}
			if err = s.Append(group); err != nil {
				return f, err
			}
			for _, record := range group {
				result, e2 := e.Apply(record.Seq, record.Command)
				if e2 != nil {
					return f, e2
				}
				if record.Seq <= 2 || record.Seq > d.Int(n-6) {
					f.Witnesses = append(f.Witnesses, tailWitness{record.Command, result.Clone()})
				}
			}
			start = end
		}
		if err = e.CheckInvariants(); err != nil {
			return f, err
		}
		cp := e.Capture()
		if cut < n {
			for _, id := range []string{"carry-fill", "carry-cancel"} {
				o, ok := e.Order("alice", id)
				if !ok || o.Status != d.Open {
					return f, errors.New("carry order changed before snapshot cut")
				}
			}
		}
		doc := snapshot.Document{Identity: snapshot.Identity{RunID: g.RunID, ConfigHash: g.ConfigHash, RulesVersion: g.Config.RulesVersion, ResponseVersion: g.Config.ResponseVersion}, SnapshotSeq: d.Int(cut), State: cp}
		if err = snapshot.Publish(dir, doc); err != nil {
			return f, err
		}
		name, _ := snapshot.Filename(d.Int(cut))
		f.Files[name] = ""
	}
	f.Counts = composition(e.Capture())
	if f.Counts != expectedTailCounts(kind, n) || len(f.Witnesses) != 8 {
		return f, fmt.Errorf("tail fixture composition: %+v", f.Counts)
	}
	for _, account := range []string{"alice", "bob"} {
		b, _ := e.Balances(account)
		if b != expectedTailBalance(kind, account, n) {
			return f, fmt.Errorf("independent balance mismatch %s: %+v", account, b)
		}
	}
	for i := 0; i < 2; i++ {
		if f.Witnesses[i].First.Order == nil || f.Witnesses[i].First.Order.Status != d.Open {
			return f, errors.New("initial response mutated")
		}
	}
	for _, name := range []string{".lock", "genesis.bin", "commands.wal"} {
		f.Files[name] = ""
	}
	for name := range f.Files {
		sha, e2 := digest(filepath.Join(dir, name))
		if e2 != nil {
			return f, e2
		}
		f.Files[name] = sha
	}
	return f, nil
}

func tailCommand(args []string) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := fs.String("dir", "", "new fixture or coordinator-owned copied run")
	output := fs.String("output", "", "new JSON file")
	kind := fs.String("kind", "", "active or history")
	n := fs.Int("count", 0, "24 (smoke) or 10000")
	tail := fs.Int("tail", -1, "commands after selected snapshot")
	receipt := fs.String("receipt", "", "fixture receipt")
	ioOutput := fs.String("io-output", "", "buffer experiment diagnostic JSON, outside Open timing")
	resident := fs.Bool("resident", false, "current RSS after GC, outside Open timing (macOS/cgo)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dir == "" || *output == "" || (*n != 24 && *n != 10000) || (*kind != "active" && *kind != "history") {
		return errors.New("invalid tail arguments")
	}
	if args[0] == "buffer-measure" && *ioOutput == "" {
		return errors.New("missing buffer IO output")
	}
	if *resident && args[0] != "tail-measure" {
		return errors.New("resident RSS requires tail-measure")
	}
	if err := durable.CheckFilesystem(filepath.Dir(*dir)); err != nil {
		return err
	}
	if args[0] == "tail-prepare" {
		f, err := prepareTail(*dir, *kind, *n)
		if err != nil {
			return err
		}
		return save(*output, f)
	}
	if *receipt == "" {
		return errors.New("missing tail fixture receipt")
	}
	data, err := os.ReadFile(*receipt)
	if err != nil {
		return err
	}
	var f tailFixture
	if err = json.Unmarshal(data, &f); err != nil {
		return err
	}
	valid := false
	for _, cut := range tailCuts(*n) {
		if *n-cut == *tail {
			valid = true
		}
	}
	if !valid || f.Version != 1 || f.Kind != *kind || f.Commands != *n || len(f.Witnesses) != 8 || f.Counts != expectedTailCounts(*kind, *n) {
		return errors.New("invalid tail fixture/cut")
	}
	if info, e2 := os.Lstat(filepath.Join(*dir, ".tail-owned")); e2 != nil || !info.Mode().IsRegular() {
		return errors.New("tail requires coordinator-owned copy")
	}
	o, err := measureTailResident(*dir, f, *tail, *resident)
	if err != nil {
		return err
	}
	if args[0] == "buffer-measure" {
		stats, e := inspectReadIO(*dir, f, *tail)
		if e != nil {
			return e
		}
		if e = save(*ioOutput, stats); e != nil {
			return e
		}
	}
	return save(*output, o)
}

func measureTail(dir string, f tailFixture, tail int) (_ tailObservation, err error) {
	return measureTailResident(dir, f, tail, false)
}

func measureTailResident(dir string, f tailFixture, tail int, collectRSS bool) (_ tailObservation, err error) {
	cut := f.Commands - tail
	name, _ := snapshot.Filename(d.Int(cut))
	files, err := snapshot.List(dir)
	if err != nil {
		return tailObservation{}, err
	}
	if len(files) != 1 || files[0].Name != name {
		return tailObservation{}, errors.New("tail snapshot presence mismatch")
	}
	for _, file := range []string{".lock", "genesis.bin", "commands.wal", name} {
		sha, e2 := digest(filepath.Join(dir, file))
		if e2 != nil {
			return tailObservation{}, e2
		}
		if sha != f.Files[file] {
			return tailObservation{}, errors.New("tail input hash mismatch")
		}
	}
	o := tailObservation{Version: 1, Kind: f.Kind, Commands: f.Commands, Tail: tail, GoVersion: runtime.Version(), GOMAXPROCS: runtime.GOMAXPROCS(0)}
	runtime.GC()
	o.Open.Before = mem()
	start := time.Now()
	r, err := rt.Open(dir, rt.DefaultOptions())
	o.Open.WallNanos = time.Since(start).Nanoseconds()
	if err != nil {
		return o, err
	}
	defer func() { err = errors.Join(err, shutdownProfile(r)) }()
	o.Open.After = mem()
	o.Open.PeakRSSBytes, err = peakRSS()
	if err != nil {
		return o, err
	}
	runtime.GC()
	o.Open.AfterGC = mem()
	if collectRSS {
		o.RSSAfterGCBytes, err = residentRSS()
		if err != nil {
			return o, err
		}
		if o.RSSAfterGCBytes > 2<<30 {
			return o, errors.New("post-GC resident RSS exceeded 2GiB budget")
		}
	}
	runtime.KeepAlive(r)
	o.Recovery = r.Recovery()
	if o.Recovery.SnapshotSeq != d.Int(cut) || o.Recovery.ReplayedCommands != d.Int(tail) || o.Recovery.SnapshotFile != name || len(o.Recovery.Skipped) != 0 || o.Open.PeakRSSBytes > 2<<30 {
		return o, errors.New("wrong tail recovery path or RSS budget")
	}
	// All queries, retries and validation publication are outside Open timing/RSS.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	query := func(q rt.Query) (rt.View, error) {
		q.RunID = f.RunID
		v, e2 := r.Read(ctx, q)
		if e2 == nil && v.AsOfSeq != d.Int(f.Commands) {
			e2 = errors.New("tail query sequence changed")
		}
		return v, e2
	}
	for _, w := range f.Witnesses {
		key := w.Command.Key()
		v, e2 := query(rt.Query{Kind: rt.ResultQuery, AccountID: key.AccountID, RequestKind: key.Kind, ID: key.RequestID})
		if e2 != nil || v.ResultState != "COMPLETED" || v.Result == nil || !reflect.DeepEqual(*v.Result, w.First) {
			return o, fmt.Errorf("first response mismatch %s: %v", key.RequestID, e2)
		}
		o.FirstResponsesVerified++
	}
	for _, id := range []string{"carry-fill", "carry-cancel", "carry-taker"} {
		account := "alice"
		if id == "carry-taker" {
			account = "bob"
		}
		v, e2 := query(rt.Query{Kind: rt.OrderQuery, AccountID: account, ID: id})
		if e2 != nil || v.Order == nil || !checkTailOrder(id, *v.Order) {
			return o, fmt.Errorf("post-snapshot order mismatch %s: %v", id, e2)
		}
	}
	for _, account := range []string{"alice", "bob"} {
		v, e2 := query(rt.Query{Kind: rt.BalancesQuery, AccountID: account})
		if e2 != nil || v.Balances != expectedTailBalance(f.Kind, account, f.Commands) {
			return o, fmt.Errorf("post-snapshot reservation/refund mismatch %s: %v", account, e2)
		}
	}
	through := d.Int(f.Commands)
	// TradePage requires an actual previous trade cursor, not an arbitrary seq.
	after, index := d.Int(0), d.Int(0)
	if f.Kind == "history" {
		after, index = d.Int(f.Commands-8), 1
	}
	v, e2 := query(rt.Query{Kind: rt.TradesQuery, AccountID: "alice", AfterSeq: after, AfterIndex: index, ThroughSeq: &through, Limit: 1})
	if e2 != nil || len(v.Trades) != 1 {
		return o, fmt.Errorf("post-snapshot trade missing: %v", e2)
	}
	trade := v.Trades[0]
	if trade.CommandSeq != d.Int(f.Commands-5) || trade.PriceTicks != 9990 || trade.QuantityLots != 1 || trade.Maker != (d.OrderRef{AccountID: "alice", ClientOrderID: "carry-fill"}) || trade.Taker != (d.OrderRef{AccountID: "bob", ClientOrderID: "carry-taker"}) {
		return o, errors.New("post-snapshot maker/price/quantity mismatch")
	}
	o.OrdersBalancesTradeVerified = true
	for _, w := range f.Witnesses[:2] {
		got, e2 := r.Submit(ctx, w.Command)
		if e2 != nil || !reflect.DeepEqual(got, w.First) {
			return o, fmt.Errorf("retry response mismatch: %v", e2)
		}
		o.RetriesVerified++
	}
	saved, e2 := r.Snapshot(ctx)
	if e2 != nil || saved.SnapshotSeq != d.Int(f.Commands) {
		return o, fmt.Errorf("tail validation publication: %v", e2)
	}
	finalName, _ := snapshot.Filename(d.Int(f.Commands))
	o.FinalSnapshotSHA256, err = digest(filepath.Join(dir, finalName))
	if err != nil {
		return o, err
	}
	if o.FinalSnapshotSHA256 != f.Files[finalName] {
		return o, errors.New("tail full canonical state mismatch")
	}
	for _, file := range []string{".lock", "genesis.bin", "commands.wal", name} {
		sha, e2 := digest(filepath.Join(dir, file))
		if e2 != nil {
			return o, e2
		}
		if sha != f.Files[file] {
			return o, errors.New("tail immutable input changed")
		}
	}
	o.WALSHA256 = f.Files["commands.wal"]
	o.Verified = true
	return o, nil
}
