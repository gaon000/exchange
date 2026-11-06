package domain_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

// Keep the pre-optimization encoder as an independent compatibility oracle.
func legacyMarshalInt(n d.Int) ([]byte, error) {
	if n < 0 {
		return nil, d.ErrInteger
	}
	return json.Marshal(n.String())
}

func TestIntegerMarshalMatchesLegacyContract(t *testing.T) {
	values := []d.Int{-1, math.MinInt64, 0, 1, 9, 10, 99, 100, math.MaxInt64}
	rng := rand.New(rand.NewSource(17))
	for range 1000 {
		values = append(values, d.Int(rng.Int63()))
	}
	for _, n := range values {
		want, wantErr := legacyMarshalInt(n)
		got, gotErr := n.MarshalJSON()
		if !bytes.Equal(got, want) || !errors.Is(gotErr, wantErr) {
			t.Fatalf("%d: got %q %v, want %q %v", n, got, gotErr, want, wantErr)
		}
	}
}

var integerMarshalSink []byte

func BenchmarkIntegerMarshalJSON(b *testing.B) {
	for _, variant := range []struct {
		name string
		fn   func(d.Int) ([]byte, error)
	}{{"legacy", legacyMarshalInt}, {"append", d.Int.MarshalJSON}} {
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
