package wal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

// Reference is the unchanged decoder from master ec420e0, kept only in tests.
func decodeRecordReference(data []byte, runID string) (Record, error) {
	var w commandWire
	if err := strictjson.Decode(data, &w); err != nil {
		return Record{}, fmt.Errorf("%w: command: %v", ErrCorrupt, err)
	}
	c := d.Command{RunID: runID, AccountID: w.AccountID, Kind: w.Kind}
	var err error
	switch w.Kind {
	case d.PlaceOrder:
		err = strictjson.Decode(w.Payload, &c.Place)
	case d.CancelOrder:
		err = strictjson.Decode(w.Payload, &c.Cancel)
	default:
		err = ErrInvalid
	}
	r := Record{w.Seq, c}
	canonical, encodeErr := encodeRecord(r)
	if err != nil || encodeErr != nil || !bytes.Equal(data, canonical) {
		return Record{}, fmt.Errorf("%w: noncanonical command", ErrCorrupt)
	}
	return r, nil
}

func compareRecordDecoders(t testing.TB, data []byte, runID string) {
	t.Helper()
	want, a := decodeRecordReference(data, runID)
	got, b := decodeRecord(data, runID)
	if (a == nil) != (b == nil) || (a == nil && got != want) || errors.Is(a, ErrCorrupt) != errors.Is(b, ErrCorrupt) {
		t.Fatalf("decoder mismatch input=%q run=%q\nold=%+v %v\nnew=%+v %v", data, runID, want, a, got, b)
	}
}

func TestWALRecordCanonicalSchema(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(commandWire{}), reflect.TypeOf(d.Place{}), reflect.TypeOf(d.Cancel{})} {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			tag := field.Tag.Get("json")
			if !field.IsExported() || field.Anonymous || tag == "" || tag == "-" || strings.Contains(tag, ",") {
				t.Fatalf("canonical required field changed: %s.%s", typ, field.Name)
			}
			if typ == reflect.TypeOf(commandWire{}) && field.Name == "Payload" && field.Type == reflect.TypeOf(json.RawMessage{}) {
				continue
			}
			if field.Type.Kind() != reflect.String && field.Type != reflect.TypeOf(d.Int(0)) {
				t.Fatalf("non-scalar canonical field: %s.%s %s", typ, field.Name, field.Type)
			}
		}
	}
}

func validRecordCorpus(t testing.TB) [][]byte {
	t.Helper()
	records := []Record{place(1, "alice", "a"), place(math.MaxInt64, strings.Repeat("A", 64), strings.Repeat("z", 64)),
		{Seq: 2, Command: d.Command{RunID: testRun, AccountID: "bob", Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: "cancel-1", ClientOrderID: "a"}}}}
	records[1].Command.Place.Side = d.Sell
	records[1].Command.Place.PriceTicks = d.MaxPriceTicks
	records[1].Command.Place.QuantityLots = d.MaxQuantityLots
	values := [][]byte{fixture(t, "record.json"), fixture(t, "cancel.json")}
	for _, r := range records {
		b, e := encodeRecord(r)
		if e != nil {
			t.Fatal(e)
		}
		values = append(values, b)
	}
	return values
}

func recordMutations(data []byte) [][]byte {
	values := [][]byte{nil, []byte("null"), []byte("{}"), []byte("[]"), append(bytes.Clone(data), ' '), append([]byte(" "), data...), append(bytes.Clone(data), data...)}
	// Both nested and outer objects: duplicate (same or conflicting), missing,
	// case-folded/escaped key, null/type mismatch, unknown, and reordered fields.
	object := func(b []byte) [][]byte {
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) != nil {
			return nil
		}
		var result [][]byte
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := fields[key]
			needle := append([]byte(`"`+key+`":`), value...)
			for _, replacement := range [][]byte{
				append(append(bytes.Clone(needle), ','), needle...),
				append(append(bytes.Clone(needle), ','), []byte(`"`+key+`":null`)...),
				[]byte(`"` + strings.ToUpper(key) + `":` + string(value)),
				[]byte(`"\u` + fmt.Sprintf("%04x", key[0]) + key[1:] + `":` + string(value)),
				[]byte(`"` + key + `":null`), []byte(`"` + key + `":0`), []byte(`"` + key + `":true`),
			} {
				result = append(result, bytes.Replace(b, needle, replacement, 1))
			}
			removed := bytes.Replace(b, append(bytes.Clone(needle), ','), nil, 1)
			if bytes.Equal(removed, b) {
				removed = bytes.Replace(b, append([]byte{','}, needle...), nil, 1)
			}
			result = append(result, removed)
		}
		result = append(result, bytes.Replace(b, []byte("{"), []byte(`{"extra":1,`), 1))
		// Marshaling the map changes field order relative to the DTO schema.
		reordered, _ := json.Marshal(fields)
		result = append(result, reordered)
		return result
	}
	values = append(values, object(data)...)
	var w commandWire
	if json.Unmarshal(data, &w) == nil {
		for _, payload := range object(w.Payload) {
			values = append(values, bytes.Replace(data, w.Payload, payload, 1))
		}
	}
	for _, token := range []string{`"1"`, `"2"`, `"100"`, `"100000000"`, `"9223372036854775807"`} {
		for _, bad := range []string{`null`, `0`, `"0"`, `"01"`, `"-1"`, `"+1"`, `"1.0"`, `"1e2"`, `"9223372036854775808"`, `"\u0031"`, `""`} {
			values = append(values, bytes.Replace(data, []byte(token), []byte(bad), 1))
		}
	}
	values = append(values, bytes.Replace(data, []byte("alice"), []byte("a\\u006cice"), 1),
		bytes.Replace(data, []byte("alice"), []byte{0xff}, 1),
		bytes.Replace(data, []byte(`"payload":`), []byte(`"payload":`+strings.Repeat("[", 65)), 1))
	return values
}

func TestRecordDecoderDifferentialCorpus(t *testing.T) {
	valid := validRecordCorpus(t)
	for _, data := range valid {
		if _, e := decodeRecord(data, testRun); e != nil {
			t.Fatalf("valid rejected %s: %v", data, e)
		}
		for _, run := range []string{testRun, "bad", "00000000-0000-4000-8000-000000000002"} {
			compareRecordDecoders(t, data, run)
			for _, bad := range recordMutations(data) {
				compareRecordDecoders(t, bad, run)
			}
		}
		for cut := 0; cut < len(data); cut++ {
			compareRecordDecoders(t, data[:cut], testRun)
		}
		for i := range data {
			bad := bytes.Clone(data)
			bad[i] ^= 1
			compareRecordDecoders(t, bad, testRun)
		}
	}
}

func FuzzRecordDecoderCompatibility(f *testing.F) {
	for _, b := range validRecordCorpus(f) {
		f.Add(b, testRun)
		for _, bad := range recordMutations(b) {
			f.Add(bad, testRun)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte, run string) {
		if len(b) > MaxRecordBytes || len(run) > 128 {
			return
		}
		compareRecordDecoders(t, b, run)
	})
}

func BenchmarkRecordDecoder(b *testing.B) {
	values := map[string][]byte{"place": fixture(b, "record.json"), "cancel": fixture(b, "cancel.json")}
	for name, data := range values {
		for side, decode := range map[string]func([]byte, string) (Record, error){"baseline": decodeRecordReference, "candidate": decodeRecord} {
			b.Run(name+"/"+side, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					if _, err := decode(data, testRun); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
