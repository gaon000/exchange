package snapshot

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"

	d "github.com/gaon000/exchange/internal/domain"
)

// Preflight counts canonical JSON without allocating its bytes. Plans are built
// once for the closed set of streamed leaves, then read concurrently. The actual
// writer still uses encoding/json. Unsupported schemas/codecs fall back to it.
var valueSizes = makeValueSizes()

type sizeKind uint8

const (
	sizeString sizeKind = iota
	sizeInteger
	sizeDomainInt
	sizeReason
	sizePointer
	sizeStruct
)

type sizePlan struct {
	kind   sizeKind
	elem   *sizePlan
	fields []sizeField
}

type sizeField struct {
	index int
	key   int // encoded field name plus colon
	omit  bool
	plan  *sizePlan
}

func makeValueSizes() map[reflect.Type]*sizePlan {
	plans := make(map[reflect.Type]*sizePlan)
	for _, v := range []any{"", int(0), d.Int(0), d.CheckpointAccount{}, d.CheckpointOrder{}, d.OrderRef{}, d.Trade{}, d.CheckpointResult{}} {
		t := reflect.TypeOf(v)
		plans[t] = makeSizePlan(t, 0)
	}
	return plans
}

func makeSizePlan(t reflect.Type, depth int) *sizePlan {
	if depth > 64 {
		return nil // unsupported recursive schema
	}
	switch t {
	case reflect.TypeFor[d.Int]():
		return &sizePlan{kind: sizeDomainInt}
	case reflect.TypeFor[d.Reason]():
		return &sizePlan{kind: sizeReason}
	}
	for _, codec := range []reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[encoding.TextMarshaler]()} {
		if t.Implements(codec) || reflect.PointerTo(t).Implements(codec) {
			return nil
		}
	}
	switch t.Kind() {
	case reflect.String:
		return &sizePlan{kind: sizeString}
	case reflect.Int:
		return &sizePlan{kind: sizeInteger}
	case reflect.Pointer:
		if elem := makeSizePlan(t.Elem(), depth+1); elem != nil {
			return &sizePlan{kind: sizePointer, elem: elem}
		}
	case reflect.Struct:
		p := &sizePlan{kind: sizeStruct}
		names := make(map[string]bool)
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, option, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous || f.PkgPath != "" || name == "" || name == "-" || names[name] || (option != "" && (option != "omitempty" || f.Type.Kind() != reflect.Pointer)) {
				return nil
			}
			// Only explicit simple field names; unusual JSON tags use Marshal's
			// own name selection rules via fallback instead of approximating them.
			for _, c := range name {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
					return nil
				}
			}
			child := makeSizePlan(f.Type, depth+1)
			if child == nil {
				return nil
			}
			names[name] = true
			p.fields = append(p.fields, sizeField{i, len(name) + 3, option == "omitempty", child})
		}
		return p
	}
	return nil
}

// valueSize is exact up to the per-leaf limit and saturates above it. If a
// negative domain integer occurs anywhere, Marshal retains the original error
// type/wrapping and its precedence over a too-long string in the same leaf.
func valueSize(v any) (int, error) {
	if p := valueSizes[reflect.TypeOf(v)]; p != nil {
		if n, ok := p.size(reflect.ValueOf(v)); ok {
			return n, nil
		}
	}
	b, err := json.Marshal(v)
	return len(b), err
}

func (p *sizePlan) size(v reflect.Value) (int, bool) {
	switch p.kind {
	case sizeString:
		n := stringSize(v.String())
		return n, n >= 0
	case sizeReason:
		if v.Len() == 0 {
			return 4, true // null
		}
		n := stringSize(v.String())
		return n, n >= 0
	case sizeDomainInt:
		if v.Int() < 0 {
			return 0, false
		}
		return decimalSize(v.Int()) + 2, true // decimal string
	case sizeInteger:
		return decimalSize(v.Int()), true
	case sizePointer:
		if v.IsNil() {
			return 4, true
		}
		return p.elem.size(v.Elem())
	case sizeStruct:
		n, comma := 2, 0
		for _, f := range p.fields {
			value := v.Field(f.index)
			if f.omit && value.IsNil() {
				continue
			}
			size, ok := f.plan.size(value)
			if !ok {
				return 0, false
			}
			n = min(maxValueBytes+1, n+comma+f.key+size)
			comma = 1
		}
		return n, true
	default:
		return 0, false
	}
}

func decimalSize(v int64) int {
	n, u := 1, uint64(v)
	if v < 0 {
		n++
		u = uint64(-(v + 1)) + 1 // includes MinInt64 without overflow
	}
	for u >= 10 {
		u /= 10
		n++
	}
	return n
}

// stringSize follows encoding/json's default HTML escaping and U+2028/U+2029.
// Malformed UTF-8 returns -1 so Marshal itself handles replacement bytes. No
// encoded string is allocated on the valid UTF-8 path.
func stringSize(s string) int {
	n := 2 // quotes
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"', '\\', '\b', '\f', '\n', '\r', '\t':
				n += 2
			case '<', '>', '&':
				n += 6
			default:
				if c < 0x20 {
					n += 6
				} else {
					n++
				}
			}
			i++
		} else {
			r, width := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && width == 1 {
				return -1
			}
			if r == '\u2028' || r == '\u2029' {
				n += 6
			} else {
				n += width
			}
			i += width
		}
		if n > maxValueBytes {
			return maxValueBytes + 1
		}
	}
	return n
}
