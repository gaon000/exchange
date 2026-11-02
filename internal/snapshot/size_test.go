package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

func checkValueSize(t testing.TB, v any) {
	t.Helper()
	wire, wantErr := json.Marshal(v)
	want := min(len(wire), maxValueBytes+1)
	got, err := valueSize(v)
	if min(got, maxValueBytes+1) != want || fmt.Sprint(err) != fmt.Sprint(wantErr) || !reflect.DeepEqual(err, wantErr) {
		t.Fatalf("size of %T: got %d/%v, want %d/%v", v, got, err, want, wantErr)
	}
}

func TestPreflightLeafSizesMatchJSON(t *testing.T) {
	for _, c := range leafCases(t) {
		if valueSizes[c.typ] == nil {
			t.Fatalf("current leaf unexpectedly uses Marshal fallback: %v", c.typ)
		}
		v := reflect.New(c.typ)
		if err := json.Unmarshal(c.wire, v.Interface()); err != nil {
			t.Fatal(err)
		}
		checkValueSize(t, v.Elem().Interface())
	}
	for _, n := range []int64{math.MinInt64, -1, 0, 1, 9, 10, 99, 100, math.MaxInt64} {
		checkValueSize(t, int(n))
		checkValueSize(t, d.Int(n))
	}
	for n := int64(1); n <= 100_000_000_000_000_000; {
		n *= 10
		for _, v := range []int64{n - 1, n, n + 1, -n} {
			checkValueSize(t, int(v))
			checkValueSize(t, d.Int(v))
		}
	}
	for i := range 256 {
		checkValueSize(t, string([]byte{byte(i)}))
	}
	for _, s := range []string{"", "한글😀", "\u2028\u2029", "\ufffd", "\xff\xfe\xe2\x80", "\"\\\b\f\n\r\t\x00<>&", strings.Repeat("x", maxValueBytes-2), strings.Repeat("x", maxValueBytes-1), strings.Repeat("<", maxValueBytes)} {
		checkValueSize(t, s)
		// Reason permits every nonempty string on write, even though restore
		// validates the enum. Do not tighten the existing writer contract.
		checkValueSize(t, d.CheckpointOrder{Order: d.Order{Reason: d.Reason(s)}})
	}
	for mask := range 8 {
		v := d.CheckpointResult{}
		if mask&1 != 0 {
			v.Command.Place = &d.Place{}
		}
		if mask&2 != 0 {
			v.Command.Cancel = &d.Cancel{}
		}
		if mask&4 != 0 {
			v.Response.Order = &d.Order{}
		}
		checkValueSize(t, v)
	}
}

// Populate the real DTO fields independently of size plans. Random strings
// include malformed UTF-8; numbers and nullable pointers cover every leaf.
func fillSizeValue(v reflect.Value, r *rand.Rand, valid bool) {
	switch v.Kind() {
	case reflect.String:
		b := make([]byte, r.Intn(80))
		_, _ = r.Read(b)
		if valid {
			alphabet := []rune("abcXYZ019\"\\\b\f\n\r\t\x00<>&한글😀\u2028\u2029\ufffd")
			runes := make([]rune, len(b))
			for i := range runes {
				runes[i] = alphabet[int(b[i])%len(alphabet)]
			}
			v.SetString(string(runes))
		} else {
			v.SetString(string(b))
		}
	case reflect.Int, reflect.Int64:
		n := r.Int63()
		if !valid && r.Intn(10) == 0 {
			n = -n
		}
		v.SetInt(n)
	case reflect.Pointer:
		if r.Intn(2) == 0 {
			v.SetZero()
		} else {
			v.Set(reflect.New(v.Type().Elem()))
			fillSizeValue(v.Elem(), r, valid)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			fillSizeValue(v.Field(i), r, valid)
		}
	default:
		panic("unexpected test DTO field")
	}
}

func TestPreflightDifferentialValues(t *testing.T) {
	r := rand.New(rand.NewSource(51385))
	for _, v := range []any{"", int(0), d.Int(0), d.CheckpointAccount{}, d.CheckpointOrder{}, d.OrderRef{}, d.Trade{}, d.CheckpointResult{}} {
		x := reflect.New(reflect.TypeOf(v)).Elem()
		for i := range 1000 {
			fillSizeValue(x, r, i%2 == 0)
			checkValueSize(t, x.Interface())
		}
	}
}

func oldMeasure(doc Document, limit int64) (int64, error) {
	if err := doc.shape(); err != nil {
		return 0, err
	}
	c := &counter{limit: limit}
	if err := payload(c, doc); err != nil {
		return 0, err
	}
	return c.n, nil
}

func TestPreflightMatchesSerializationAndLimits(t *testing.T) {
	doc, _, _ := fixture(t)
	r := rand.New(rand.NewSource(1))
	for trial := range 100 {
		if trial != 0 {
			for _, values := range []any{doc.State.Accounts, doc.State.Orders, doc.State.Trades, doc.State.Results} {
				v := reflect.ValueOf(values)
				for i := range v.Len() {
					fillSizeValue(v.Index(i), r, trial%2 == 0)
				}
			}
		}
		n, _ := oldMeasure(doc, MaxPayloadBytes)
		for _, limit := range []int64{math.MinInt64, -1, 0, n - 1, n, MaxPayloadBytes} {
			want, wantErr := oldMeasure(doc, limit)
			got, err := measure(doc, limit)
			if want != got || !reflect.DeepEqual(err, wantErr) {
				t.Fatalf("trial=%d limit=%d: %d/%v != %d/%v", trial, limit, got, err, want, wantErr)
			}
		}
	}
}

func TestPreflightOversizedAndInvalidLeavesTouchNoFiles(t *testing.T) {
	for _, negative := range []bool{false, true} {
		doc, _, _ := fixture(t)
		doc.State.Accounts[0].AccountID = strings.Repeat("x", maxValueBytes)
		wantErr := ErrInvalid
		if negative {
			doc.State.Accounts[0].ReservedQuoteAtoms = -1
			wantErr = d.ErrInteger // Marshal error wins over leaf size error
		}
		_, oldErr := oldMeasure(doc, MaxPayloadBytes)
		s := &fileTrace{}
		err := publish(t.TempDir(), doc, s.ops("unused"), MaxPayloadBytes)
		if !errors.Is(err, wantErr) || !reflect.DeepEqual(err, oldErr) || len(s.events) != 0 {
			t.Fatalf("publication preflight: %v / %v", err, s.events)
		}
		var sink bytes.Buffer
		if err := Write(&sink, doc); !errors.Is(err, wantErr) || sink.Len() != 0 {
			t.Fatalf("write preflight: %v / %d", err, sink.Len())
		}
	}
	// The leaf, not just an individual string, must fit exactly in 16 KiB.
	doc, _, _ := fixture(t)
	doc.State.Accounts[0].AccountID = ""
	wire, _ := json.Marshal(doc.State.Accounts[0])
	doc.State.Accounts[0].AccountID = strings.Repeat("x", maxValueBytes-len(wire))
	if _, err := measure(doc, MaxPayloadBytes); err != nil {
		t.Fatalf("exact leaf limit rejected: %v", err)
	}
	doc.State.Accounts[0].AccountID += "x"
	if _, err := measure(doc, MaxPayloadBytes); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized composite leaf accepted: %v", err)
	}
}

type sizeCustomJSON string

func (sizeCustomJSON) MarshalJSON() ([]byte, error) { return []byte(`"custom"`), nil }

type sizeCustomText string

func (sizeCustomText) MarshalText() ([]byte, error) { return []byte("custom"), nil }

func TestPreflightUnsupportedSchemaFallback(t *testing.T) {
	for _, v := range []any{
		sizeCustomJSON(""), sizeCustomText(""), []string{"x"}, map[string]int{"x": 1},
		struct{ Value string }{"x"},
		struct {
			Value string `json:"value,omitempty"`
		}{},
		struct {
			Value int `json:"value,string"`
		}{1},
		struct {
			Value string `json:"value,,"`
		}{},
		struct {
			Value string `json:"odd!name"`
		}{},
		struct {
			Value string `json:"-"`
		}{},
		struct {
			Value any `json:"value"`
		}{true},
	} {
		if makeSizePlan(reflect.TypeOf(v), 0) != nil {
			t.Fatalf("unsupported schema optimized: %T", v)
		}
		checkValueSize(t, v)
	}
	checkValueSize(t, nil)
}

func FuzzPreflightMatchesJSON(f *testing.F) {
	for _, s := range []string{"", "plain", "한글😀\u2028\u2029", "\x00\xff\"\\<>&", strings.Repeat("x", maxValueBytes)} {
		f.Add(s, int64(0), uint8(0))
		f.Add(s, int64(-1), uint8(7))
	}
	f.Fuzz(func(t *testing.T, s string, n int64, mask uint8) {
		checkValueSize(t, s)
		checkValueSize(t, d.Int(n))
		v := d.CheckpointResult{Command: d.CheckpointCommand{AccountID: s}, Response: d.Result{RequestID: s, SchemaVersion: int(n)}}
		if mask&1 != 0 {
			v.Command.Place = &d.Place{ClientOrderID: s, PriceTicks: d.Int(n)}
		}
		if mask&2 != 0 {
			v.Command.Cancel = &d.Cancel{CancelRequestID: s, ClientOrderID: s}
		}
		if mask&4 != 0 {
			v.Response.Order = &d.Order{Reason: d.Reason(s), UpdatedSeq: d.Int(n)}
		}
		checkValueSize(t, v)
	})
}
