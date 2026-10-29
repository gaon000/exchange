package engine_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

// The oracle uses a total-ownership ledger, not reservation transfers. Available
// balances are recomputed from all open orders; matching sorts a flat candidate
// list rather than using price levels/FIFO nodes. Values are intentionally small.
type reference struct {
	totals   map[string][2]d.Int
	orders   map[d.OrderRef]d.Order
	journal  []d.Trade
	outcomes map[d.Key]engine.Outcome
	seq, max d.Int
}

func newReference(c d.Config) *reference {
	m := &reference{totals: make(map[string][2]d.Int), orders: make(map[d.OrderRef]d.Order), outcomes: make(map[d.Key]engine.Outcome), journal: []d.Trade{}, max: c.MaxCommands}
	for _, a := range c.Accounts {
		m.totals[a.AccountID] = [2]d.Int{a.BaseLots, a.QuoteAtoms}
	}
	return m
}

func (m *reference) balance(id string) d.Balances {
	total := m.totals[id]
	var base, quote d.Int
	for ref, o := range m.orders {
		if ref.AccountID != id || o.OpenQty == 0 {
			continue
		}
		if o.Side == d.Buy {
			quote += o.PriceTicks * o.OpenQty
		} else {
			base += o.OpenQty
		}
	}
	return d.Balances{Base: d.Balance{Available: total[0] - base, Reserved: base}, Quote: d.Balance{Available: total[1] - quote, Reserved: quote}}
}

func (m *reference) submit(c d.Command) (d.Result, string) {
	if old, ok := m.outcomes[c.Key()]; ok {
		if old.Command != c {
			return d.Result{}, "IDEMPOTENCY_CONFLICT"
		}
		return old.Result.Clone(), ""
	}
	if m.seq >= m.max {
		return d.Result{}, "RUN_LIMIT_REACHED"
	}
	m.seq++
	r := d.Result{SchemaVersion: 1, RunID: runID, AccountID: c.AccountID, Kind: c.Kind, RequestID: c.Key().RequestID, CommandSeq: m.seq}
	if c.Kind == d.CancelOrder {
		ref := d.OrderRef{AccountID: c.AccountID, ClientOrderID: c.Cancel.ClientOrderID}
		o, ok := m.orders[ref]
		switch {
		case !ok || o.Status == d.Rejected:
			r.Code = d.OrderNotFound
		case o.Status == d.Cancelled:
			r.Code = d.AlreadyCancelled
			r.Order = &o
		case o.Status == d.Filled:
			r.Code = d.AlreadyFilled
			r.Order = &o
		default:
			o.CancelledQty = o.OpenQty
			o.OpenQty = 0
			o.Status = d.Cancelled
			o.Reason = d.UserCancel
			o.UpdatedSeq = m.seq
			m.orders[ref] = o
			r.Order = &o
			r.Code = d.CancelSucceeded
		}
	} else {
		p := c.Place
		ref := d.OrderRef{AccountID: c.AccountID, ClientOrderID: p.ClientOrderID}
		o := d.Order{ClientOrderID: p.ClientOrderID, Symbol: d.Symbol, Side: p.Side, PriceTicks: p.PriceTicks, OriginalQty: p.QuantityLots, CreatedSeq: m.seq, UpdatedSeq: m.seq}
		available := m.balance(c.AccountID)
		if p.Side == d.Buy && available.Quote.Available < p.PriceTicks*p.QuantityLots || p.Side == d.Sell && available.Base.Available < p.QuantityLots {
			o.Status = d.Rejected
			o.Reason = d.InsufficientFunds
			r.Code = d.InsufficientBalance
		} else {
			o.OpenQty = o.OriginalQty
			o.Status = d.Open
			r.Code = d.Accepted
			var candidates []d.OrderRef
			for key, other := range m.orders {
				if other.OpenQty <= 0 || other.Side == p.Side {
					continue
				}
				if p.Side == d.Buy && other.PriceTicks <= p.PriceTicks || p.Side == d.Sell && other.PriceTicks >= p.PriceTicks {
					candidates = append(candidates, key)
				}
			}
			sort.Slice(candidates, func(i, j int) bool {
				a, b := m.orders[candidates[i]], m.orders[candidates[j]]
				if a.PriceTicks == b.PriceTicks {
					return a.CreatedSeq < b.CreatedSeq
				}
				if p.Side == d.Buy {
					return a.PriceTicks < b.PriceTicks
				}
				return a.PriceTicks > b.PriceTicks
			})
			for _, key := range candidates {
				if o.OpenQty == 0 {
					break
				}
				if key.AccountID == c.AccountID {
					o.CancelledQty = o.OpenQty
					o.OpenQty = 0
					o.Status = d.Cancelled
					o.Reason = d.SelfTradePrevention
					break
				}
				other := m.orders[key]
				qty := o.OpenQty
				if other.OpenQty < qty {
					qty = other.OpenQty
				}
				cash := qty * other.PriceTicks
				buyer, seller := c.AccountID, key.AccountID
				if p.Side == d.Sell {
					buyer, seller = key.AccountID, c.AccountID
				}
				bt, st := m.totals[buyer], m.totals[seller]
				bt[0] += qty
				bt[1] -= cash
				st[0] -= qty
				st[1] += cash
				m.totals[buyer], m.totals[seller] = bt, st
				o.ExecutedQty += qty
				o.ExecutedQuoteAtoms += cash
				o.OpenQty -= qty
				other.ExecutedQty += qty
				other.ExecutedQuoteAtoms += cash
				other.OpenQty -= qty
				other.UpdatedSeq = m.seq
				other.Status = d.PartiallyFilled
				if other.OpenQty == 0 {
					other.Status = d.Filled
				}
				m.orders[key] = other
				r.TradeCount++
				m.journal = append(m.journal, d.Trade{TradeID: fmt.Sprintf("%s:%s:%s", runID, m.seq, r.TradeCount), CommandSeq: m.seq, MatchIndex: r.TradeCount, Maker: key, Taker: ref, TakerSide: p.Side, PriceTicks: other.PriceTicks, QuantityLots: qty})
			}
			if o.Status != d.Cancelled {
				if o.OpenQty == 0 {
					o.Status = d.Filled
				} else if o.ExecutedQty > 0 {
					o.Status = d.PartiallyFilled
				}
			}
		}
		m.orders[ref] = o
		r.Order = &o
	}
	m.outcomes[c.Key()] = engine.Outcome{Command: c, Result: r.Clone()}
	return r, ""
}

func (m *reference) state() engine.State {
	s := engine.State{RunID: runID, AppliedSeq: m.seq, Accounts: []engine.AccountState{}, Orders: []engine.OwnedOrder{}, Bids: []engine.Level{}, Asks: []engine.Level{}, Trades: append([]d.Trade{}, m.journal...), Results: []engine.Outcome{}}
	for id := range m.totals {
		s.Accounts = append(s.Accounts, engine.AccountState{AccountID: id, Balances: m.balance(id)})
	}
	bids, asks := make(map[d.Int]d.Int), make(map[d.Int]d.Int)
	for ref, o := range m.orders {
		s.Orders = append(s.Orders, engine.OwnedOrder{Ref: ref, Order: o})
		if o.OpenQty > 0 {
			if o.Side == d.Buy {
				bids[o.PriceTicks] += o.OpenQty
			} else {
				asks[o.PriceTicks] += o.OpenQty
			}
		}
	}
	for p, q := range bids {
		s.Bids = append(s.Bids, engine.Level{PriceTicks: p, QuantityLots: q})
	}
	for p, q := range asks {
		s.Asks = append(s.Asks, engine.Level{PriceTicks: p, QuantityLots: q})
	}
	for _, o := range m.outcomes {
		s.Results = append(s.Results, o)
	}
	sort.Slice(s.Accounts, func(i, j int) bool { return s.Accounts[i].AccountID < s.Accounts[j].AccountID })
	sort.Slice(s.Orders, func(i, j int) bool {
		a, b := s.Orders[i].Ref, s.Orders[j].Ref
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.ClientOrderID < b.ClientOrderID
	})
	sort.Slice(s.Results, func(i, j int) bool { return s.Results[i].Result.CommandSeq < s.Results[j].Result.CommandSeq })
	sort.Slice(s.Bids, func(i, j int) bool { return s.Bids[i].PriceTicks > s.Bids[j].PriceTicks })
	sort.Slice(s.Asks, func(i, j int) bool { return s.Asks[i].PriceTicks < s.Asks[j].PriceTicks })
	return s
}

func compareStream(t testing.TB, data []byte) {
	t.Helper()
	if len(data) > 1536 {
		data = data[:1536]
	}
	c := d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 30, QuoteAtoms: 1000}, {AccountID: "b", BaseLots: 30, QuoteAtoms: 1000}, {AccountID: "c", BaseLots: 30, QuoteAtoms: 1000}})
	c.MaxCommands = 100
	e, replay := newEngine(t, c), newEngine(t, c)
	m := newReference(c)
	var history []d.Command
	var places []d.Command
	for i := 0; i+5 < len(data); i += 6 {
		b := data[i : i+6]
		account := []string{"a", "b", "c"}[int(b[1])%3]
		side := d.Buy
		if b[5]%2 == 1 {
			side = d.Sell
		}
		cmd := place(account, fmt.Sprintf("o%d", i), side, 98+d.Int(b[3]%5), 1+d.Int(b[4]%7))
		switch b[0] % 6 {
		case 0:
			target := "absent"
			if len(places) > 0 {
				target = places[int(b[2])%len(places)].Place.ClientOrderID
			}
			cmd = cancel(account, fmt.Sprintf("c%d", i), target)
		case 1:
			if len(history) > 0 {
				cmd = history[int(b[2])%len(history)]
			}
		case 2:
			if len(places) > 0 {
				cmd = places[int(b[2])%len(places)]
				cmd.Place.PriceTicks++
			}
		case 3:
			cmd.Place.QuantityLots = 1000
		}
		history = append(history, cmd)
		if cmd.Kind == d.PlaceOrder {
			places = append(places, cmd)
		}
		want, wantErr := m.submit(cmd)
		got, err := e.Apply(e.AppliedSeq()+1, cmd)
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		if errText != wantErr || !reflect.DeepEqual(got, want) {
			t.Fatalf("step %d command %+v\nresult %+v (%s)\nmodel %+v (%s)", i/6, cmd, got, errText, want, wantErr)
		}
		if err := e.CheckInvariants(); err != nil {
			t.Fatalf("step %d: %v", i/6, err)
		}
		if !reflect.DeepEqual(e.State(), m.state()) {
			t.Fatalf("step %d state mismatch\nengine %+v\nmodel %+v", i/6, e.State(), m.state())
		}
		again, againErr := replay.Apply(replay.AppliedSeq()+1, cmd)
		if !reflect.DeepEqual(got, again) || fmt.Sprint(err) != fmt.Sprint(againErr) || !reflect.DeepEqual(e.State(), replay.State()) {
			t.Fatalf("nondeterministic replay at step %d", i/6)
		}
		if (i/6)%13 == 0 {
			// Resume the next commands from a directly restored checkpoint while
			// the independent model and full-replay engine retain their histories.
			e = restore(t, e, c)
		}
	}
}

func TestReferenceModelRandomStreams(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			data := make([]byte, 128*6)
			_, _ = rand.New(rand.NewSource(seed)).Read(data)
			compareStream(t, data)
		})
	}
}

func FuzzEngineInvariants(f *testing.F) {
	f.Add([]byte{4, 0, 0, 2, 4, 0, 4, 1, 0, 1, 3, 1, 0, 0, 0, 0, 0, 0})
	data := make([]byte, 128*6)
	_, _ = rand.New(rand.NewSource(42)).Read(data)
	f.Add(data)
	f.Fuzz(func(t *testing.T, data []byte) { compareStream(t, data) })
}
