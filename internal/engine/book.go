package engine

import (
	"container/list"
	"sort"

	d "github.com/gaon000/exchange/internal/domain"
)

type entry struct {
	ref     d.OrderRef
	order   d.Order
	element *list.Element
}

// Prices are ascending on both sides; best bid is the last price.
// Each level has FIFO nodes, permitting O(1) cancellation within a level.
type book struct {
	side   d.Side
	prices []d.Int
	levels map[d.Int]*list.List
}

func newBook(side d.Side) book { return book{side: side, levels: make(map[d.Int]*list.List)} }

func (b *book) add(e *entry) {
	p := e.order.PriceTicks
	level := b.levels[p]
	if level == nil {
		level = list.New()
		b.levels[p] = level
		i := sort.Search(len(b.prices), func(i int) bool { return b.prices[i] >= p })
		b.prices = append(b.prices, 0)
		copy(b.prices[i+1:], b.prices[i:])
		b.prices[i] = p
	}
	e.element = level.PushBack(e)
}

func (b *book) remove(e *entry) {
	p := e.order.PriceTicks
	level := b.levels[p]
	level.Remove(e.element)
	e.element = nil
	if level.Len() == 0 {
		delete(b.levels, p)
		i := sort.Search(len(b.prices), func(i int) bool { return b.prices[i] >= p })
		b.prices = append(b.prices[:i], b.prices[i+1:]...)
	}
}

func (b *book) best() *entry {
	if len(b.prices) == 0 {
		return nil
	}
	i := 0
	if b.side == d.Buy {
		i = len(b.prices) - 1
	}
	return b.levels[b.prices[i]].Front().Value.(*entry)
}

// Level is a value-only aggregate price level, in lots.
type Level struct {
	PriceTicks   d.Int
	QuantityLots d.Int
}

func (b *book) view() []Level {
	out := make([]Level, 0, len(b.prices))
	for i := range len(b.prices) {
		j := i
		if b.side == d.Buy {
			j = len(b.prices) - 1 - i
		}
		p := b.prices[j]
		var qty d.Int
		for n := b.levels[p].Front(); n != nil; n = n.Next() {
			qty = add(qty, n.Value.(*entry).order.OpenQty)
		}
		out = append(out, Level{p, qty})
	}
	return out
}
