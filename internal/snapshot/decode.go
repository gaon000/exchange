package snapshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

// Fixed literals enforce field order and delimiters. Variable-size collections
// are read item by item; even a malformed single value cannot grow our buffer
// beyond maxValueBytes. json.Decoder.Decode on the whole document/price level
// would silently buffer the entire array before enforcing any limit.
type decoder struct {
	r   *bufio.Reader
	err error
}

func (p *decoder) expect(s string) {
	for i := 0; i < len(s) && p.err == nil; i++ {
		if p.next() != s[i] && p.err == nil {
			p.err = corrupt("noncanonical field/delimiter")
		}
	}
}
func (p *decoder) next() byte {
	if p.err != nil {
		return 0
	}
	b, err := p.r.ReadByte()
	if err != nil {
		p.err = readError(err)
	}
	return b
}
func (p *decoder) peek() byte {
	if p.err != nil {
		return 0
	}
	b, err := p.r.Peek(1)
	if err != nil {
		p.err = readError(err)
		return 0
	}
	return b[0]
}
func (p *decoder) value(dst any) {
	if p.err != nil {
		return
	}
	var b []byte
	depth := 0
	quoted, escaped := false, false
	for p.err == nil {
		x := p.peek()
		if p.err != nil {
			return
		}
		if len(b) > 0 && depth == 0 && !quoted && (x == ',' || x == '}' || x == ']') {
			break
		}
		if len(b) == maxValueBytes {
			p.err = corrupt("JSON value exceeds limit")
			return
		}
		x = p.next()
		b = append(b, x)
		if quoted {
			if escaped {
				escaped = false
			} else if x == '\\' {
				escaped = true
			} else if x == '"' {
				quoted = false
			}
		} else {
			switch x {
			case '"':
				quoted = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		if depth > 64 || depth < 0 {
			p.err = corrupt("JSON nesting")
		}
	}
	if p.err != nil {
		return
	}
	if err := strictjson.Decode(b, dst); err != nil {
		p.err = corrupt("invalid JSON value: " + err.Error())
		return
	}
	canonical, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(b, canonical) {
		p.err = corrupt("noncanonical JSON value")
	}
}

func parseArray[T any](p *decoder, limit d.Int, item func() T) []T {
	values := make([]T, 0)
	p.expect("[")
	if p.peek() != ']' {
		for p.err == nil {
			if d.Int(len(values)) >= limit {
				p.err = corrupt("array count exceeds limit")
				break
			}
			v := item()
			if p.err != nil {
				break
			}
			values = append(values, v)
			if p.peek() == ']' {
				break
			}
			p.expect(",")
		}
	}
	p.expect("]")
	return values
}
func parseValues[T any](p *decoder, limit d.Int) []T {
	return parseArray(p, limit, func() (v T) { p.value(&v); return })
}
func (p *decoder) book(seq d.Int, refs *d.Int) []d.CheckpointLevel {
	return parseArray(p, seq, func() (level d.CheckpointLevel) {
		p.expect(`{"priceTicks":`)
		p.value(&level.PriceTicks)
		p.expect(`,"orders":`)
		level.Orders = parseValues[d.OrderRef](p, seq-*refs)
		*refs += d.Int(len(level.Orders))
		p.expect("}")
		return
	})
}
func decodePayload(r io.Reader, want Identity, maxSeq d.Int) (Document, error) {
	p := &decoder{r: bufio.NewReaderSize(r, bufferBytes)}
	var doc Document
	p.expect(`{"runId":`)
	p.value(&doc.RunID)
	p.expect(`,"configHash":`)
	p.value(&doc.ConfigHash)
	p.expect(`,"rulesVersion":`)
	p.value(&doc.RulesVersion)
	p.expect(`,"responseVersion":`)
	p.value(&doc.ResponseVersion)
	p.expect(`,"snapshotSeq":`)
	p.value(&doc.SnapshotSeq)
	if p.err != nil {
		return Document{}, p.err
	}
	if doc.Identity != want || doc.SnapshotSeq > maxSeq {
		return Document{}, ErrIdentity
	}
	p.expect(`,"state":{"accounts":`)
	doc.State.Accounts = parseValues[d.CheckpointAccount](p, 10_000)
	p.expect(`,"orders":`)
	doc.State.Orders = parseValues[d.CheckpointOrder](p, doc.SnapshotSeq)
	p.expect(`,"book":{"bids":`)
	var refs d.Int
	doc.State.Book.Bids = p.book(doc.SnapshotSeq, &refs)
	p.expect(`,"asks":`)
	doc.State.Book.Asks = p.book(doc.SnapshotSeq, &refs)
	p.expect(`},"trades":`)
	doc.State.Trades = parseValues[d.Trade](p, doc.SnapshotSeq)
	p.expect(`,"results":`)
	doc.State.Results = parseValues[d.CheckpointResult](p, doc.SnapshotSeq)
	p.expect("}}")
	if p.err != nil {
		return Document{}, p.err
	}
	if _, err := p.r.ReadByte(); err != io.EOF {
		if err != nil {
			return Document{}, readError(err)
		}
		return Document{}, corrupt("trailing payload bytes")
	}
	if doc.shape() != nil {
		return Document{}, corrupt("state shape")
	}
	return doc, nil
}
