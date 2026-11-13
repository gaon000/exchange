package snapshot

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

// The previous append-based parser is a differential oracle for values, exact
// errors and input consumption, independent of the candidate capacity policy.
func legacyArray[T any](p *decoder, limit d.Int, item func() T) []T {
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

func compareArrays[T snapshotValue](t testing.TB, input string, limit d.Int) []T {
	t.Helper()
	a := &decoder{r: bufio.NewReaderSize(strings.NewReader(input), bufferBytes)}
	b := &decoder{r: bufio.NewReaderSize(strings.NewReader(input), bufferBytes)}
	got := parseValues[T](a, limit)
	want := legacyArray(b, limit, func() (v T) { parseValue(b, &v); return })
	restA, readA := io.ReadAll(a.r)
	restB, readB := io.ReadAll(b.r)
	if !reflect.DeepEqual(got, want) || fmt.Sprint(a.err) != fmt.Sprint(b.err) || !bytes.Equal(restA, restB) || fmt.Sprint(readA) != fmt.Sprint(readB) {
		t.Fatalf("array contract differs: limit=%d got=%v want=%v errors=%v/%v remainder=%q/%q", limit, got, want, a.err, b.err, restA, restB)
	}
	// Limits do not cause early reservation. Include a tiny actual array with an
	// enormous declared bound, and malformed items just after growth boundaries.
	if len(got) == 0 {
		if got == nil || cap(got) != 0 {
			t.Fatal("empty array reserved memory or became nil")
		}
	} else if cap(got) > 2*len(got) || d.Int(cap(got)) > limit {
		t.Fatalf("capacity not bounded by accepted items/count limit: %d/%d/%d", len(got), cap(got), limit)
	}
	return got
}

func TestArrayGrowthPreservesContractAndActualCountBound(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 7, 8, 9, 127, 128, 129, 255, 256, 257, 511, 512, 513, 4095, 4096, 4097, 9999, 10000} {
		raw := "[" + strings.TrimSuffix(strings.Repeat(`"1",`, n), ",") + "]sentinel"
		for _, limit := range []d.Int{0, d.Int(n / 2), d.Int(n), d.Int(n + 1), math.MaxInt64} {
			compareArrays[d.Int](t, raw, limit)
			compareArrays[d.Int](t, strings.Replace(raw, "]sentinel", `,invalid]sentinel`, 1), limit)
		}
	}
	for _, raw := range []string{"", "[", "[]tail", "[ ]", `["1",]tail`, `["1" "2"]`, `["01"]`, `[null]`, `["1",{"x":[[[0]]]}]`, "[\xff]", `["1",` + strings.Repeat("[", 65) + `0]`} {
		for _, limit := range []d.Int{-1, 0, 1, 2, math.MaxInt64} {
			compareArrays[d.Int](t, raw, limit)
		}
	}
}

func TestArrayGrowthPreservesDTOValuesAndPointerIndependence(t *testing.T) {
	doc, _, _ := fixture(t)
	orders := make([]d.CheckpointOrder, 0, 513)
	results := make([]d.CheckpointResult, 0, 513)
	for i := 0; i < 513; i++ {
		orders = append(orders, doc.State.Orders[i%len(doc.State.Orders)])
		results = append(results, doc.State.Results[i%len(doc.State.Results)])
	}
	a, _ := json.Marshal(orders)
	b, _ := json.Marshal(results)
	compareArrays[d.CheckpointOrder](t, string(a), math.MaxInt64)
	got := compareArrays[d.CheckpointResult](t, string(b), math.MaxInt64)
	if !reflect.DeepEqual(got, results) {
		t.Fatal("saved results changed across growth")
	}
	// A late canonical failure must not publish a partially decoded item.
	compareArrays[d.CheckpointResult](t, strings.TrimSuffix(string(b), "]")+`,{}]`, math.MaxInt64)
}

func FuzzArrayGrowthContract(f *testing.F) {
	for _, s := range []string{`[]`, `["1","2"]`, `["1",null]`, `["1",]`, `["01"]`, `[null]`} {
		f.Add(s, int64(3))
	}
	f.Fuzz(func(t *testing.T, s string, limit int64) {
		if len(s) > 128<<10 {
			t.Skip()
		}
		compareArrays[d.Int](t, s, d.Int(limit))
	})
}
