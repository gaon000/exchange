package domain_test

import (
	"bytes"
	"errors"
	"math"
	"math/rand"
	"strconv"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

// This prototype stays in tests until a qualified full-path comparison exists.
func appendIntCandidate(n d.Int) ([]byte, error) {
	if n < 0 {
		return nil, d.ErrInteger
	}
	out := make([]byte, 1, 21)
	out[0] = '"'
	out = strconv.AppendInt(out, int64(n), 10)
	return append(out, '"'), nil
}

func TestIntegerMarshalCandidateMatchesCurrentContract(t *testing.T) {
	values := []d.Int{-1, math.MinInt64, 0, 1, 9, 10, 99, 100, math.MaxInt64}
	rng := rand.New(rand.NewSource(17))
	for range 1000 {
		values = append(values, d.Int(rng.Int63()))
	}
	for _, n := range values {
		want, wantErr := n.MarshalJSON()
		got, gotErr := appendIntCandidate(n)
		if !bytes.Equal(got, want) || !errors.Is(gotErr, wantErr) {
			t.Fatalf("%d: got %q %v, want %q %v", n, got, gotErr, want, wantErr)
		}
	}
}

var integerMarshalSink []byte

func BenchmarkIntegerMarshalCandidate(b *testing.B) {
	for _, variant := range []struct {
		name string
		fn   func(d.Int) ([]byte, error)
	}{{"current", d.Int.MarshalJSON}, {"append-prototype", appendIntCandidate}} {
		b.Run(variant.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var err error
				integerMarshalSink, err = variant.fn(1234567890123456789)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
