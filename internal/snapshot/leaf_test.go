package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

// Independent previous leaf path: Marshal first (including its error priority),
// then apply the leaf bound, then write. Do not use the new encoder as oracle.
func marshalLeafReference(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err == nil && len(b) > maxValueBytes {
		return nil, ErrInvalid
	}
	return b, err
}

func checkEncodedLeaf(t testing.TB, v any) {
	t.Helper()
	want, wantErr := marshalLeafReference(v)
	var sink bytes.Buffer
	e := encoder{w: &sink}
	e.value(v)
	// UnsupportedValueError can carry NaN, which is never DeepEqual to itself.
	// Compare the error type/message and the wrapped domain sentinel instead.
	if reflect.TypeOf(e.err) != reflect.TypeOf(wantErr) || fmt.Sprint(e.err) != fmt.Sprint(wantErr) || errors.Is(e.err, d.ErrInteger) != errors.Is(wantErr, d.ErrInteger) {
		t.Fatalf("%T error mismatch %v != %v", v, e.err, wantErr)
	}
	if wantErr != nil {
		if sink.Len() != 0 {
			t.Fatalf("failed value leaked bytes: %T", v)
		}
	} else if !bytes.Equal(sink.Bytes(), want) {
		t.Fatalf("%T bytes mismatch: %q != %q", v, sink.Bytes(), want)
	}
}

func TestLeafEncoderMatchesMarshalValuesErrorsAndLimits(t *testing.T) {
	for _, c := range leafCases(t) {
		v := reflect.New(c.typ)
		if err := json.Unmarshal(c.wire, v.Interface()); err != nil {
			t.Fatal(err)
		}
		checkEncodedLeaf(t, v.Elem().Interface())
	}
	for _, n := range []int64{math.MinInt64, -1, 0, 1, 9, 10, 99, 100, math.MaxInt64} {
		checkEncodedLeaf(t, int(n))
		checkEncodedLeaf(t, d.Int(n))
	}
	for i := range 256 {
		checkEncodedLeaf(t, string([]byte{byte(i)}))
	}
	for _, s := range []string{"", "\"\\\b\f\n\r\t\x00<>&", "한글😀\u2028\u2029\ufffd", "\xff\xfe\xe2\x80", strings.Repeat("x", maxValueBytes-2), strings.Repeat("x", maxValueBytes-1), strings.Repeat("<", maxValueBytes)} {
		checkEncodedLeaf(t, s)
		checkEncodedLeaf(t, d.CheckpointOrder{Order: d.Order{Reason: d.Reason(s)}})
		// A later invalid integer must win over an earlier oversized string.
		checkEncodedLeaf(t, d.CheckpointAccount{AccountID: s, ReservedQuoteAtoms: -1})
	}
	for _, v := range []any{nil, sizeCustomJSON(""), sizeCustomText(""), []string{"\n"}, map[string]int{"b": 2, "a": 1}, math.NaN(), make(chan int), json.RawMessage(`{`)} {
		checkEncodedLeaf(t, v)
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
		checkEncodedLeaf(t, v)
	}
}

func TestLeafEncoderReuseAndWriterFailures(t *testing.T) {
	var sink, want bytes.Buffer
	e := encoder{w: &sink}
	for _, v := range []any{"first\n", d.Int(99), d.CheckpointOrder{}, "<>&\u2028", "last"} {
		b, err := marshalLeafReference(v)
		if err != nil {
			t.Fatal(err)
		}
		want.Write(b)
		e.value(v)
	}
	if e.err != nil || !bytes.Equal(sink.Bytes(), want.Bytes()) {
		t.Fatalf("reused encoder changed bytes: %v", e.err)
	}
	for _, p := range [][]byte{nil, []byte("missing newline"), []byte(strings.Repeat("x", maxValueBytes+1) + "\n")} {
		var target bytes.Buffer
		if _, err := (leafWriter{&target}).Write(p); !errors.Is(err, ErrInvalid) || target.Len() != 0 {
			t.Fatal("bad framing/limit wrote bytes")
		}
	}
	stop := errors.New("leaf write failed")
	e = encoder{w: leafFailure{err: stop}}
	e.value("value")
	if !errors.Is(e.err, stop) {
		t.Fatal("write error lost", e.err)
	}
	e.value("second")
	if !errors.Is(e.err, stop) {
		t.Fatal("sticky error lost", e.err)
	}
	e = encoder{w: leafFailure{}}
	e.value("value")
	if !errors.Is(e.err, io.ErrShortWrite) {
		t.Fatal("short write accepted", e.err)
	}
}

type leafFailure struct{ err error }

func (w leafFailure) Write(p []byte) (int, error) { return 0, w.err }

func TestLeafEncoderRandomDTOs(t *testing.T) {
	r := rand.New(rand.NewSource(20261113))
	for _, v := range []any{"", int(0), d.Int(0), d.CheckpointAccount{}, d.CheckpointOrder{}, d.OrderRef{}, d.Trade{}, d.CheckpointResult{}} {
		x := reflect.New(reflect.TypeOf(v)).Elem()
		for i := range 500 {
			fillSizeValue(x, r, i%2 == 0)
			checkEncodedLeaf(t, x.Interface())
		}
	}
}

func FuzzLeafEncoderMatchesMarshal(f *testing.F) {
	for _, s := range []string{"", "plain", "한글\u2028\u2029", "\xff\x00\"\\<>&", strings.Repeat("x", maxValueBytes)} {
		f.Add(s, int64(0), uint8(0))
		f.Add(s, int64(-1), uint8(7))
	}
	f.Fuzz(func(t *testing.T, s string, n int64, mask uint8) {
		if len(s) > maxValueBytes+1 {
			return
		}
		checkEncodedLeaf(t, s)
		checkEncodedLeaf(t, d.Int(n))
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
		checkEncodedLeaf(t, v)
	})
}
