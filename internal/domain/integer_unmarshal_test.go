package domain_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

// Independent pre-optimization oracle, including error precedence and mutation.
func legacyUnmarshalInt(n *d.Int, data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return d.ErrInteger
	}
	v, err := d.ParseInt(s)
	if err != nil {
		return err
	}
	*n = v
	return nil
}

func checkUnmarshalInt(t *testing.T, data []byte) {
	t.Helper()
	original := bytes.Clone(data)
	for _, wrapped := range []bool{false, true} {
		got, want := d.Int(71), d.Int(71)
		var gotErr, wantErr error
		if wrapped {
			gotErr = json.Unmarshal(data, &got)
			wantErr = json.Unmarshal(data, &legacyIntReceiver{&want})
		} else {
			gotErr = got.UnmarshalJSON(data)
			wantErr = legacyUnmarshalInt(&want, data)
		}
		if got != want || fmt.Sprintf("%T:%v", gotErr, gotErr) != fmt.Sprintf("%T:%v", wantErr, wantErr) || errors.Is(gotErr, d.ErrInteger) != errors.Is(wantErr, d.ErrInteger) || errors.Is(gotErr, d.ErrOverflow) != errors.Is(wantErr, d.ErrOverflow) || !bytes.Equal(data, original) {
			t.Fatalf("wrapped=%v data=%q: got %d %v, want %d %v; input changed=%v", wrapped, data, got, gotErr, want, wantErr, !bytes.Equal(data, original))
		}
	}
}

type legacyIntReceiver struct{ value *d.Int }

func (v legacyIntReceiver) UnmarshalJSON(data []byte) error {
	return legacyUnmarshalInt(v.value, data)
}

func TestIntegerUnmarshalMatchesLegacyContract(t *testing.T) {
	inputs := []string{
		`"0"`, `"1"`, `"9223372036854775807"`, `"9223372036854775808"`,
		`"9999999999999999999"`, `"9223372036854775808x"`, `"922337203685477580x8"`,
		`"0000000000000000000"`, `"00000000000000000000"`, `"0001"`,
		`"\u0031\u0032"`, `"0\u0031"`, `"\u0039\u0032\u0032\u0033\u0033\u0037\u0032\u0030\u0033\u0036\u0038\u0035\u0034\u0037\u0037\u0035\u0038\u0030\u0038"`,
		`"\uD800"`, `"\uD800\uDC00"`, `"\u0000"`, `"\t1"`, `"\x31"`,
		`""`, `"-1"`, `"+1"`, `"1.0"`, `"1e2"`, `" 1"`, `"１"`,
		`null`, `1`, `true`, `{}`, `[]`, `"1" "2"`, `"1"null`,
		`"9223372036854775808\q"`, `"123`, "", "\x00", "\"\xff\"",
	}
	for _, input := range inputs {
		for _, padding := range [][2]string{{"", ""}, {" \t\r\n", "\n\r\t "}} {
			checkUnmarshalInt(t, []byte(padding[0]+input+padding[1]))
		}
	}
	rng := rand.New(rand.NewSource(20261113))
	for range 2000 {
		digits := strconv.FormatInt(rng.Int63(), 10)
		checkUnmarshalInt(t, []byte(strconv.Quote(digits)))
		checkUnmarshalInt(t, []byte(strconv.Quote(strings.Repeat("0", rng.Intn(22))+digits)))
		var escaped strings.Builder
		for _, digit := range digits {
			fmt.Fprintf(&escaped, `\u%04x`, digit)
		}
		checkUnmarshalInt(t, []byte(`"`+escaped.String()+`"`))
		noise := make([]byte, rng.Intn(96))
		_, _ = rng.Read(noise)
		checkUnmarshalInt(t, noise)
	}
}

func FuzzIntegerUnmarshalCompatibility(f *testing.F) {
	for _, input := range []string{`"0"`, `"0001"`, `"9223372036854775807"`, `"9223372036854775808"`, `"9223372036854775808x"`, `"9223372036854775808\q"`, `"\u0031"`, " \"1\"\n", `""`, `null`, `"1"null`, "\"\xff\""} {
		f.Add([]byte(input))
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkUnmarshalInt(t, data) })
}

var integerUnmarshalSink d.Int

func BenchmarkIntegerUnmarshalJSON(b *testing.B) {
	for _, variant := range []struct {
		name string
		fn   func(*d.Int, []byte) error
	}{{"legacy", legacyUnmarshalInt}, {"current", (*d.Int).UnmarshalJSON}} {
		for _, input := range []struct{ name, json string }{{"small", `"1234"`}, {"max", `"9223372036854775807"`}, {"escaped", `"\u0031"`}} {
			b.Run(variant.name+"/"+input.name, func(b *testing.B) {
				data := []byte(input.json)
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if err := variant.fn(&integerUnmarshalSink, data); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
