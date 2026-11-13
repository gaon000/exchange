package snapshot

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func compareCanonicalReference(t testing.TB, p *decoder, value any, raw []byte) {
	t.Helper()
	canonical, err := json.Marshal(value)
	want := err == nil && len(raw) <= maxValueBytes && bytes.Equal(canonical, raw)
	if got := p.matchesCanonical(value, raw); got != want {
		t.Fatalf("canonical acceptance changed: value=%T raw=%q got=%v want=%v marshal=%v", value, raw, got, want, err)
	}
	if p.comparison.want != nil {
		t.Fatal("borrowed input retained after comparison")
	}
}

func TestCanonicalBufferReuseMatchesMarshal(t *testing.T) {
	p := &decoder{}
	for repeat := 0; repeat < 3; repeat++ {
		for _, c := range leafCases(t) {
			value, err := c.decode(c.wire)
			if err != nil {
				t.Fatal(err)
			}
			compareCanonicalReference(t, p, value, c.wire)
			for _, raw := range [][]byte{nil, []byte("null"), append(bytes.Clone(c.wire), '\n'), append([]byte(" "), c.wire...), append(bytes.Clone(c.wire), 'x')} {
				compareCanonicalReference(t, p, value, raw)
			}
			if strings.HasPrefix(string(c.wire), "{") {
				objectMutations(string(c.wire), func(raw string) { compareCanonicalReference(t, p, value, []byte(raw)) })
			}
		}
	}
	// Both marshal errors and byte mismatches must leave the next comparison usable.
	for _, value := range []any{math.NaN(), math.Inf(1), func() {}, make(chan int), "<>&\u2028\u2029\n\\\"한글😀"} {
		raw, _ := json.Marshal(value)
		compareCanonicalReference(t, p, value, raw)
		compareCanonicalReference(t, p, "ok", []byte(`"ok"`))
	}
	for _, n := range []int{maxValueBytes - 3, maxValueBytes - 2, maxValueBytes - 1, maxValueBytes, maxValueBytes + 1} {
		value := strings.Repeat("x", n)
		raw, _ := json.Marshal(value)
		compareCanonicalReference(t, p, value, raw)
	}
	// Raw unescaped HTML is legal JSON but not the existing canonical representation;
	// canonical expansion beyond the raw 16KiB bound still has to be rejected.
	value := strings.Repeat("<", maxValueBytes-2)
	compareCanonicalReference(t, p, value, []byte(`"`+value+`"`))
}

func TestCanonicalComparisonStreamingFraming(t *testing.T) {
	for _, raw := range []string{`""`, `"a\\n"`, `null`, `{"a":"b"}`} {
		for _, stream := range []string{raw + "\n", raw, raw + "\n\n", raw + "x\n", "\n" + raw + "\n"} {
			for step := 1; step <= len(stream)+1; step++ {
				c := canonicalComparison{want: []byte(raw)}
				for offset := 0; offset < len(stream); offset += step {
					chunk := []byte(stream[offset:min(offset+step, len(stream))])
					n, err := c.Write(chunk)
					if n != len(chunk) || err != nil {
						t.Fatal("comparison writer changed framing/error contract")
					}
				}
				got := !c.mismatch && c.offset == len(raw)+1
				if got != (stream == raw+"\n") {
					t.Fatalf("framing acceptance: %q step=%d got=%v", stream, step, got)
				}
				if c.offset > len(raw)+1 {
					t.Fatal("comparison offset exceeded bounded expected value")
				}
			}
		}
	}
}

func FuzzCanonicalBufferReuse(f *testing.F) {
	for _, s := range []string{"", "ok", "<>&", "한글😀", "\x00\n\\\"", "\xff", "\u2028\u2029"} {
		f.Add(s, uint8(0))
	}
	f.Fuzz(func(t *testing.T, s string, mutation uint8) {
		if len(s) > maxValueBytes*2 {
			t.Skip()
		}
		p := &decoder{}
		raw, _ := json.Marshal(s)
		compareCanonicalReference(t, p, s, raw)
		switch mutation % 4 {
		case 0:
			raw = append(raw, '\n')
		case 1:
			raw = append([]byte(" "), raw...)
		case 2:
			if len(raw) > 0 {
				raw = raw[:len(raw)-1]
			}
		case 3:
			if len(raw) > 1 {
				raw[len(raw)/2] ^= 1
			}
		}
		compareCanonicalReference(t, p, s, raw)
		compareCanonicalReference(t, p, "next", []byte(`"next"`))
	})
}
