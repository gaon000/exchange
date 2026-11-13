package snapshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/telemetry"
)

// Fixed literals enforce field order and delimiters. Variable-size collections
// are read item by item; even a malformed single value cannot grow our buffer
// beyond maxValueBytes. json.Decoder.Decode on the whole document/price level
// would silently buffer the entire array before enforcing any limit.
type decoder struct {
	r          *bufio.Reader
	err        error
	valueBuf   [maxValueBytes]byte
	json       *json.Encoder
	comparison canonicalComparison
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

// snapshotValue is deliberately closed: its leaf DTOs contain no maps, slices,
// interfaces or RawMessage. Only pointers and domain.Reason can encode null.
// TestCanonicalLeafSchema guards these assumptions when the DTOs evolve.
type snapshotValue interface {
	string | int | d.Int | d.CheckpointAccount | d.CheckpointOrder | d.OrderRef | d.Trade | d.CheckpointResult
}

func parseValue[T snapshotValue](p *decoder, dst *T) {
	if p.err != nil {
		return
	}
	b := p.valueBuf[:0]
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
	// Typed decoding validates JSON syntax, scalar types and custom integers /
	// reasons. Re-encoding must match every input byte: this also rejects missing,
	// duplicate, unknown, case-folded or reordered fields, noncanonical escapes,
	// whitespace and forbidden nulls for this closed set of leaf DTOs. Unlike the
	// general HTTP/WAL decoder, a snapshot never accepts noncanonical JSON.
	if !utf8.Valid(b) {
		p.err = corrupt("invalid UTF-8")
		return
	}
	if err := json.Unmarshal(b, dst); err != nil {
		p.err = corrupt("invalid JSON value: " + err.Error())
		return
	}
	if !p.matchesCanonical(dst, b) {
		p.err = corrupt("noncanonical JSON value")
	}
}

// canonicalComparison consumes the Encoder's borrowed bytes during Write. It
// compares exactly one compact value plus the Encoder's framing newline, without
// copying the encoded value or retaining the input buffer after the comparison.
// A mismatch is recorded rather than returned as an I/O error: Encoder latches
// writer errors, which must not leak into a later comparison on the same decoder.
type canonicalComparison struct {
	want     []byte
	offset   int
	mismatch bool
}

func (c *canonicalComparison) Write(b []byte) (int, error) {
	n := len(b)
	if c.mismatch {
		return n, nil
	}
	if n > len(c.want)+1-c.offset {
		c.mismatch = true
		return n, nil
	}
	if c.offset < len(c.want) {
		k := min(n, len(c.want)-c.offset)
		if !bytes.Equal(b[:k], c.want[c.offset:c.offset+k]) {
			c.mismatch = true
		}
		c.offset += k
		b = b[k:]
	}
	if len(b) > 0 {
		if len(b) != 1 || c.offset != len(c.want) || b[0] != '\n' {
			c.mismatch = true
		}
		c.offset += len(b)
	}
	return n, nil
}

func (p *decoder) matchesCanonical(value any, raw []byte) bool {
	if len(raw) > maxValueBytes {
		return false
	}
	if p.json == nil {
		p.json = json.NewEncoder(&p.comparison)
	}
	p.comparison = canonicalComparison{want: raw}
	err := p.json.Encode(value)
	matched := err == nil && !p.comparison.mismatch && p.comparison.offset == len(raw)+1
	p.comparison.want = nil
	return matched
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
			if len(values) == cap(values) {
				// Grow only after another item has passed validation. Never reserve
				// from the untrusted declared limit alone. Doubling avoids repeated
				// large DTO copies while keeping capacity below twice the number of
				// accepted items; clamp the final growth to the existing count limit.
				extra := max(1, cap(values))
				if remaining := limit - d.Int(len(values)); d.Int(extra) > remaining {
					extra = int(remaining)
				}
				grown := make([]T, len(values), len(values)+extra)
				copy(grown, values)
				values = grown
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
func parseValues[T snapshotValue](p *decoder, limit d.Int) []T {
	return parseArray(p, limit, func() (v T) { parseValue(p, &v); return })
}
func (p *decoder) book(seq d.Int, refs *d.Int) []d.CheckpointLevel {
	return parseArray(p, seq, func() (level d.CheckpointLevel) {
		p.expect(`{"priceTicks":`)
		parseValue(p, &level.PriceTicks)
		p.expect(`,"orders":`)
		level.Orders = parseValues[d.OrderRef](p, seq-*refs)
		*refs += d.Int(len(level.Orders))
		p.expect("}")
		return
	})
}
func decodePayload(r io.Reader, want Identity, maxSeq d.Int) (Document, error) {
	return decodePayloadWithTimings(r, want, maxSeq, nil)
}

func decodePayloadWithTimings(r io.Reader, want Identity, maxSeq d.Int, t *telemetry.Stages) (Document, error) {
	span := t.Begin("decode.identity")
	defer span.End()
	p := &decoder{r: bufio.NewReaderSize(r, bufferBytes)}
	var doc Document
	p.expect(`{"runId":`)
	parseValue(p, &doc.RunID)
	p.expect(`,"configHash":`)
	parseValue(p, &doc.ConfigHash)
	p.expect(`,"rulesVersion":`)
	parseValue(p, &doc.RulesVersion)
	p.expect(`,"responseVersion":`)
	parseValue(p, &doc.ResponseVersion)
	p.expect(`,"snapshotSeq":`)
	parseValue(p, &doc.SnapshotSeq)
	if p.err != nil {
		return Document{}, p.err
	}
	if doc.Identity != want || doc.SnapshotSeq > maxSeq {
		return Document{}, ErrIdentity
	}
	p.expect(`,"state":{"accounts":`)
	span.Next("decode.accounts")
	doc.State.Accounts = parseValues[d.CheckpointAccount](p, 10_000)
	p.expect(`,"orders":`)
	span.Next("decode.orders")
	doc.State.Orders = parseValues[d.CheckpointOrder](p, doc.SnapshotSeq)
	p.expect(`,"book":{"bids":`)
	span.Next("decode.book")
	var refs d.Int
	doc.State.Book.Bids = p.book(doc.SnapshotSeq, &refs)
	p.expect(`,"asks":`)
	doc.State.Book.Asks = p.book(doc.SnapshotSeq, &refs)
	p.expect(`},"trades":`)
	span.Next("decode.trades")
	doc.State.Trades = parseValues[d.Trade](p, doc.SnapshotSeq)
	p.expect(`,"results":`)
	span.Next("decode.results")
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
	span.Next("decode.shape")
	if doc.shape() != nil {
		return Document{}, corrupt("state shape")
	}
	return doc, nil
}
