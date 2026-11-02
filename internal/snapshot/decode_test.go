package snapshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

type leafCase struct {
	name      string
	wire      []byte
	typ       reflect.Type
	decode    func([]byte) (any, error)
	reference func([]byte) (any, error)
}

func leaf[T snapshotValue](value T) leafCase {
	wire, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return leafCase{
		name: reflect.TypeOf(value).String(), wire: wire, typ: reflect.TypeOf(value),
		decode: func(b []byte) (any, error) {
			p := &decoder{r: bufio.NewReader(io.MultiReader(bytes.NewReader(b), strings.NewReader(",")))}
			var got T
			parseValue(p, &got)
			if p.err != nil {
				return got, p.err
			}
			// Model the enclosing parser: a value may not silently discard a suffix.
			if p.next() != ',' {
				return got, ErrCorrupt
			}
			if _, err := p.r.ReadByte(); err != io.EOF {
				return got, ErrCorrupt
			}
			return got, nil
		},
		reference: func(b []byte) (any, error) {
			// The old decoder's strict validation + canonical comparison, kept
			// independent of the optimized implementation as a differential oracle.
			var got T
			if len(b) > maxValueBytes {
				return got, ErrCorrupt
			}
			if err := strictjson.Decode(b, &got); err != nil {
				return got, err
			}
			canonical, err := json.Marshal(got)
			if err != nil || !bytes.Equal(b, canonical) {
				return got, ErrCorrupt
			}
			return got, nil
		},
	}
}

func leafCases(t testing.TB) []leafCase {
	doc, _, _ := fixture(t)
	cases := []leafCase{leaf(""), leaf("한글😀\"\\\n<>&\u2028"), leaf(1), leaf(-1), leaf(d.Int(0)), leaf(d.Int(math.MaxInt64)), leaf(d.CheckpointResult{})}
	for _, a := range doc.State.Accounts {
		cases = append(cases, leaf(a))
	}
	for _, o := range doc.State.Orders {
		cases = append(cases, leaf(o))
	}
	for _, x := range doc.State.Trades {
		cases = append(cases, leaf(x), leaf(x.Maker), leaf(x.Taker))
	}
	for _, r := range doc.State.Results {
		cases = append(cases, leaf(r))
	}
	// The syntax layer must continue accepting nullable Order and both wire
	// payload shapes; engine.Restore remains responsible for semantic relations.
	cases = append(cases, leaf(d.CheckpointResult{Command: d.CheckpointCommand{AccountID: "a", Kind: d.CancelOrder, Cancel: &d.Cancel{CancelRequestID: "cancel", ClientOrderID: "missing"}}, Response: d.Result{SchemaVersion: 1, RunID: testRun, AccountID: "a", Kind: d.CancelOrder, RequestID: "cancel", CommandSeq: 1, Code: d.OrderNotFound}}))
	return cases
}

func TestCanonicalLeafSchema(t *testing.T) {
	var check func(reflect.Type)
	check = func(typ reflect.Type) {
		if typ.Kind() == reflect.Pointer {
			check(typ.Elem())
			return
		}
		if typ == reflect.TypeOf(d.Int(0)) || typ == reflect.TypeOf(d.Reason("")) {
			return // existing custom decoders enforce their integer/null contracts
		}
		for _, codec := range []reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[json.Unmarshaler]()} {
			if typ.Implements(codec) || reflect.PointerTo(typ).Implements(codec) {
				t.Fatalf("new custom codec needs a canonical validation proof: %v", typ)
			}
		}
		switch typ.Kind() {
		case reflect.String, reflect.Int:
		case reflect.Struct:
			names := map[string]bool{}
			for i := range typ.NumField() {
				field := typ.Field(i)
				tag := strings.Split(field.Tag.Get("json"), ",")
				if field.Anonymous || !field.IsExported() || tag[0] == "" || tag[0] == "-" || names[tag[0]] || len(tag) > 2 || len(tag) == 2 && tag[1] != "omitempty" {
					t.Fatalf("unsupported snapshot schema: %v.%s", typ, field.Name)
				}
				names[tag[0]] = true
				check(field.Type)
			}
		default:
			t.Fatalf("canonical leaf must not gain maps/slices/interfaces: %v", typ)
		}
	}
	for _, c := range leafCases(t) {
		check(c.typ)
	}
}

type member struct{ key, value string }

// Walk each nested object and mutate one field at a time, retaining all other
// bytes. This exercises the actual DTO shape rather than only a few hand-picked
// root fields; nullable/optional fields use the old decoder as their oracle.
func objectMutations(raw string, emit func(string)) {
	if !strings.HasPrefix(raw, "{") {
		return
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	_, _ = dec.Token()
	var fields []member
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			panic(err)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			panic(err)
		}
		name, _ := json.Marshal(key)
		fields = append(fields, member{string(name), string(value)})
	}
	encode := func(fields []member) string {
		parts := make([]string, len(fields))
		for i, f := range fields {
			parts[i] = f.key + ":" + f.value
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	emit(encode(append([]member{{`"unknown"`, "0"}}, fields...)))
	for i, f := range fields {
		copy := append([]member{}, fields...)
		emit(encode(append(append([]member{}, fields[:i]...), fields[i+1:]...)))
		emit(encode(append(append(append([]member{}, fields[:i]...), f), fields[i:]...)))
		copy[i].key = strings.ToUpper(f.key)
		emit(encode(copy))
		copy[i].key = fmt.Sprintf(`"\u%04x%s`, f.key[1], f.key[2:])
		emit(encode(copy))
		copy[i] = f
		for _, value := range []string{"null", "true", "[]", "{}", `""`, "0", "1.0", `"00"`, `"-1"`, `"9223372036854775808"`} {
			copy[i].value = value
			emit(encode(copy))
		}
		copy[i] = f
		objectMutations(f.value, func(value string) {
			copy[i].value = value
			emit(encode(copy))
		})
	}
	if len(fields) > 1 {
		fields[0], fields[1] = fields[1], fields[0]
		emit(encode(fields))
	}
}

func compareLeaf(t testing.TB, c leafCase, input []byte) {
	t.Helper()
	got, err := c.decode(input)
	want, oldErr := c.reference(input)
	if (err == nil) != (oldErr == nil) || err == nil && !reflect.DeepEqual(got, want) {
		t.Fatalf("%s contract changed for %q: new=%+v err=%v; old=%+v err=%v", c.name, input, got, err, want, oldErr)
	}
	if err != nil && !errors.Is(err, ErrCorrupt) {
		t.Fatalf("%s corruption classification changed: %v", c.name, err)
	}
}

func TestCanonicalLeafMatchesStrictValidation(t *testing.T) {
	checks := 0
	for _, c := range leafCases(t) {
		check := func(input []byte) { compareLeaf(t, c, input); checks++ }
		if _, err := c.reference(c.wire); err != nil {
			t.Fatal("invalid reference seed", c.name, err)
		}
		check(c.wire)
		objectMutations(string(c.wire), func(value string) { check([]byte(value)) })
		for i := range len(c.wire) {
			check(c.wire[:i])
		}
		for _, raw := range []string{"null", "{}", "[]", "true", "0", `"\ud800"`, `"\u0061"`, `"` + string([]byte{0xff}) + `"`, strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65), " " + string(c.wire), string(c.wire) + "\n", string(c.wire) + ",0"} {
			check([]byte(raw))
		}
	}
	t.Logf("%d differential inputs", checks)
}

func TestCanonicalValueBoundAndBufferOwnership(t *testing.T) {
	for _, n := range []int{maxValueBytes - 2, maxValueBytes - 1} {
		c := leaf(strings.Repeat("x", n))
		compareLeaf(t, c, c.wire)
		_, err := c.decode(c.wire)
		if (err == nil) != (n == maxValueBytes-2) {
			t.Fatalf("value limit changed: %d: %v", n, err)
		}
	}
	doc, _, _ := fixture(t)
	wire, _ := json.Marshal(doc.State.Results[0])
	p := &decoder{r: bufio.NewReader(io.MultiReader(bytes.NewReader(wire), strings.NewReader(`,"overwrite buffer"`), strings.NewReader(",")))}
	var first d.CheckpointResult
	parseValue(p, &first)
	p.expect(",")
	var second string
	parseValue(p, &second)
	for i := range p.valueBuf {
		p.valueBuf[i] = 0xff
	}
	if p.err != nil || second != "overwrite buffer" || !reflect.DeepEqual(first, doc.State.Results[0]) {
		t.Fatal("returned state aliases reused buffer", p.err, first, second)
	}
}

func FuzzCanonicalLeafMatchesStrict(f *testing.F) {
	cases := leafCases(f)
	for i, c := range cases {
		f.Add(uint8(i), c.wire)
		f.Add(uint8(i), []byte("null"))
		f.Add(uint8(i), []byte(strings.Replace(string(c.wire), `"accountId"`, `"AccountId"`, 1)))
	}
	f.Fuzz(func(t *testing.T, kind uint8, input []byte) {
		if len(input) > maxValueBytes+1 {
			return
		}
		compareLeaf(t, cases[int(kind)%len(cases)], input)
	})
}
