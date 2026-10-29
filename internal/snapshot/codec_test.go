package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	"github.com/gaon000/exchange/internal/strictjson"
	"github.com/gaon000/exchange/internal/wal"
)

const testRun = "11111111-1111-4111-8111-111111111111"

func fixture(t testing.TB) (Document, d.Config, []byte) {
	t.Helper()
	configBytes, err := os.ReadFile("testdata/config.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := wal.DecodeConfig(configBytes)
	if err != nil {
		t.Fatal(err)
	}
	g, err := wal.NewGenesis(testRun, c)
	if err != nil {
		t.Fatal(err)
	}
	doc := Document{Identity: Identity{g.RunID, g.ConfigHash, c.RulesVersion, c.ResponseVersion}, SnapshotSeq: 3}
	state, err := os.ReadFile("../engine/testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := strictjson.Decode(state, &doc.State); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile("testdata/snapshot.bin")
	if err != nil {
		t.Fatal(err)
	}
	return doc, c, wire
}

func TestIndependentGoldenEnvelope(t *testing.T) {
	doc, c, want := fixture(t)
	var got bytes.Buffer
	if err := Write(&got, doc); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatal("snapshot differs from independent Python envelope")
	}
	// The streaming JSON must also equal the ordinary DTO encoding; neither
	// comparison relies on the snapshot decoder to produce its expected bytes.
	ordinary, err := json.Marshal(doc)
	if err != nil || !bytes.Equal(ordinary, want[headerBytes:len(want)-sha256.Size]) {
		t.Fatalf("payload differs from DTO: %v", err)
	}
	r, err := Read(bytes.NewReader(want), int64(len(want)), doc.Identity, 3)
	if err != nil || !reflect.DeepEqual(r, doc) {
		t.Fatalf("read differs: %v", err)
	}
	e, err := engine.Restore(testRun, c, r.SnapshotSeq, r.State)
	if err != nil || !reflect.DeepEqual(e.Capture(), doc.State) {
		t.Fatalf("cannot restore golden state: %v", err)
	}
	// Empty Genesis state must use [] for every collection and seq zero.
	e, _ = engine.New(testRun, c)
	doc.SnapshotSeq, doc.State = 0, e.Capture()
	got.Reset()
	if err := Write(&got, doc); err != nil {
		t.Fatal(err)
	}
	r, err = Read(bytes.NewReader(got.Bytes()), int64(got.Len()), doc.Identity, 0)
	if err != nil || !reflect.DeepEqual(r, doc) {
		t.Fatalf("zero checkpoint: %v", err)
	}
}

func TestRejectEveryTruncationAndCorruptedByte(t *testing.T) {
	doc, _, wire := fixture(t)
	for cut := 0; cut < len(wire); cut++ {
		if _, err := Read(bytes.NewReader(wire[:cut]), int64(cut), doc.Identity, 3); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("cut %d: %v", cut, err)
		}
	}
	mutated := bytes.Clone(wire)
	for i := range wire {
		for bit := byte(1); bit != 0; bit <<= 1 {
			mutated[i] ^= bit
			if _, err := Read(bytes.NewReader(mutated), int64(len(mutated)), doc.Identity, 3); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("corruption byte %d bit %d: %v", i, bit, err)
			}
			mutated[i] ^= bit
		}
	}
	if _, err := Read(bytes.NewReader(append(wire, 0)), int64(len(wire)+1), doc.Identity, 3); !errors.Is(err, ErrCorrupt) {
		t.Fatal("trailing bytes accepted")
	}
}

// Independent local envelope builder lets malformed JSON reach the decoder
// with a valid digest, instead of only testing checksum rejection.
func wrapPayload(payload []byte, version uint32) []byte {
	h := []byte("EXSNP001")
	h = binary.LittleEndian.AppendUint32(h, version)
	h = binary.LittleEndian.AppendUint64(h, uint64(len(payload)))
	h = append(h, payload...)
	digest := sha256.Sum256(h)
	return append(h, digest[:]...)
}

func TestCanonicalJSONAndIdentityFailures(t *testing.T) {
	doc, _, wire := fixture(t)
	s := string(wire[headerBytes : len(wire)-sha256.Size])
	cases := map[string]struct {
		body string
		want error
	}{
		"wrong run":             {strings.Replace(s, testRun, "22222222-2222-4222-8222-222222222222", 1), ErrIdentity},
		"wrong config":          {strings.Replace(s, doc.ConfigHash, strings.Repeat("0", 64), 1), ErrIdentity},
		"wrong rules":           {strings.Replace(s, `"rulesVersion":1`, `"rulesVersion":2`, 1), ErrIdentity},
		"wrong response":        {strings.Replace(s, `"responseVersion":1`, `"responseVersion":2`, 1), ErrIdentity},
		"future sequence":       {strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":"4"`, 1), ErrIdentity},
		"negative sequence":     {strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":"-1"`, 1), ErrCorrupt},
		"overflow sequence":     {strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":"9223372036854775808"`, 1), ErrCorrupt},
		"leading zero":          {strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":"03"`, 1), ErrCorrupt},
		"number sequence":       {strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":3`, 1), ErrCorrupt},
		"null sequence":         {strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":null`, 1), ErrCorrupt},
		"whitespace":            {" " + s, ErrCorrupt},
		"newline":               {s + "\n", ErrCorrupt},
		"unknown":               {strings.Replace(s, `"state":{`, `"other":0,"state":{`, 1), ErrCorrupt},
		"duplicate":             {strings.Replace(s, `"rulesVersion":1`, `"rulesVersion":1,"rulesVersion":1`, 1), ErrCorrupt},
		"missing":               {strings.Replace(s, `"rulesVersion":1,`, "", 1), ErrCorrupt},
		"case folding":          {strings.Replace(s, `"rulesVersion"`, `"RulesVersion"`, 1), ErrCorrupt},
		"null array":            {strings.Replace(s, `"bids":[]`, `"bids":null`, 1), ErrCorrupt},
		"null amount":           {strings.Replace(s, `"availableBaseLots":"1"`, `"availableBaseLots":null`, 1), ErrCorrupt},
		"missing amount":        {strings.Replace(s, `"availableBaseLots":"1",`, "", 1), ErrCorrupt},
		"unknown leaf field":    {strings.Replace(s, `"availableBaseLots":"1"`, `"extra":1,"availableBaseLots":"1"`, 1), ErrCorrupt},
		"duplicate leaf field":  {strings.Replace(s, `"availableBaseLots":"1"`, `"availableBaseLots":"1","availableBaseLots":"1"`, 1), ErrCorrupt},
		"escaped ID":            {strings.Replace(s, `"accountId":"a"`, `"accountId":"\u0061"`, 1), ErrCorrupt},
		"empty reason":          {strings.Replace(s, `"reason":null`, `"reason":""`, 1), ErrCorrupt},
		"null order field":      {strings.Replace(s, `"symbol":"BTC-USDT"`, `"symbol":null`, 1), ErrCorrupt},
		"unknown reason":        {strings.Replace(s, `"reason":null`, `"reason":"OTHER"`, 1), ErrCorrupt},
		"counts exceed seq":     {strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":"1"`, 1), ErrCorrupt},
		"extra final delimiter": {s + "}", ErrCorrupt},
		"huge value":            {strings.Replace(s, `"accountId":"a"`, `"accountId":"`+strings.Repeat("a", maxValueBytes)+`"`, 1), ErrCorrupt},
		"deep value":            {strings.Replace(s, `"accountId":"a"`, `"accountId":`+strings.Repeat("[", 70)+`"a"`+strings.Repeat("]", 70), 1), ErrCorrupt},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.body == s {
				t.Fatal("mutation missed fixture")
			}
			b := wrapPayload([]byte(tc.body), 1)
			if got, err := Read(bytes.NewReader(b), int64(len(b)), doc.Identity, 3); !errors.Is(err, tc.want) || !reflect.DeepEqual(got, Document{}) {
				t.Fatalf("bad document admitted: %v", err)
			}
		})
	}
	b := wrapPayload([]byte(s), 2)
	if _, err := Read(bytes.NewReader(b), int64(len(b)), doc.Identity, 3); !errors.Is(err, ErrIdentity) {
		t.Fatalf("format version not fatal: %v", err)
	}
	// Huge legal sequence still does not allocate an array from the sequence.
	b = wrapPayload([]byte(strings.Replace(s, `"snapshotSeq":"3"`, `"snapshotSeq":"9223372036854775807"`, 1)), 1)
	if _, err := Read(bytes.NewReader(b), int64(len(b)), doc.Identity, math.MaxInt64); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("count mismatch: %v", err)
	}
}

type boundedSink struct {
	bytes.Buffer
	max, short int
}

func (w *boundedSink) Write(b []byte) (int, error) {
	w.max = max(w.max, len(b))
	if len(b) > bufferBytes {
		return 0, fmt.Errorf("unbounded write: %d", len(b))
	}
	if w.short > 0 {
		b = b[:min(w.short, len(b))]
	}
	return w.Buffer.Write(b)
}

type boundedSource struct {
	*bytes.Reader
	max int
}

func (r *boundedSource) ReadAt(b []byte, off int64) (int, error) {
	r.max = max(r.max, len(b))
	if len(b) > bufferBytes {
		return 0, fmt.Errorf("unbounded read: %d", len(b))
	}
	return r.Reader.ReadAt(b, off)
}

func TestStreamingLargeSinglePriceFIFO(t *testing.T) {
	c := d.DefaultConfig([]d.InitialAccount{{AccountID: "a", QuoteAtoms: 1_000_000}})
	g, _ := wal.NewGenesis(testRun, c)
	e, _ := engine.New(testRun, c)
	for i := 1; i <= 1000; i++ {
		cmd := d.Command{RunID: testRun, AccountID: "a", Kind: d.PlaceOrder, Place: d.Place{ClientOrderID: fmt.Sprintf("order-%04d", i), Symbol: d.Symbol, Side: d.Buy, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 100, QuantityLots: 1}}
		if _, err := e.Apply(d.Int(i), cmd); err != nil {
			t.Fatal(err)
		}
	}
	doc := Document{Identity: Identity{testRun, g.ConfigHash, 1, 1}, SnapshotSeq: e.AppliedSeq(), State: e.Capture()}
	level, _ := json.Marshal(doc.State.Book.Bids[0])
	if len(level) <= maxValueBytes {
		t.Fatal("test must exceed the bounded value size")
	}
	sink := &boundedSink{short: 41}
	if err := Write(sink, doc); err != nil {
		t.Fatal(err)
	}
	source := &boundedSource{Reader: bytes.NewReader(sink.Bytes())}
	got, err := Read(source, int64(sink.Len()), doc.Identity, doc.SnapshotSeq)
	if err != nil || !reflect.DeepEqual(got, doc) {
		t.Fatalf("large FIFO read: %v", err)
	}
	if _, err := engine.Restore(testRun, c, got.SnapshotSeq, got.State); err != nil {
		t.Fatal(err)
	}
	t.Logf("payload=%d, FIFO=%d, largest write=%d read=%d", sink.Len()-52, len(level), sink.max, source.max)
}

var errDisk = errors.New("injected disk error")

type failWriter struct{ remaining int }

func (w *failWriter) Write(b []byte) (int, error) {
	n := min(len(b), w.remaining)
	w.remaining -= n
	if w.remaining == 0 {
		return n, errDisk // even a full write with an error must fail
	}
	return n, nil
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type failSource struct {
	*bytes.Reader
	calls, failAt int
}

func (r *failSource) ReadAt(b []byte, off int64) (int, error) {
	r.calls++
	if r.calls == r.failAt {
		return 0, errDisk
	}
	return r.Reader.ReadAt(b, off)
}

func TestIOFailuresAndLimits(t *testing.T) {
	doc, _, wire := fixture(t)
	for _, offset := range []int{0, 1, headerBytes, headerBytes + 1, len(wire) - sha256.Size, len(wire) - 1, len(wire)} {
		if err := Write(&failWriter{remaining: offset}, doc); !errors.Is(err, errDisk) {
			t.Fatalf("write %d: %v", offset, err)
		}
	}
	if err := Write(zeroWriter{}, doc); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero writer: %v", err)
	}
	count := &failSource{Reader: bytes.NewReader(wire)}
	if _, err := Read(count, int64(len(wire)), doc.Identity, 3); err != nil {
		t.Fatal(err)
	}
	for call := 1; call <= count.calls; call++ {
		r := &failSource{Reader: bytes.NewReader(wire), failAt: call}
		if _, err := Read(r, int64(len(wire)), doc.Identity, 3); !errors.Is(err, errDisk) || errors.Is(err, ErrCorrupt) {
			t.Fatalf("read %d must retain fatal I/O error: %v", call, err)
		}
	}
	for _, length := range []uint64{0, uint64(MaxPayloadBytes) + 1, math.MaxUint64} {
		b := bytes.Clone(wire)
		binary.LittleEndian.PutUint64(b[12:20], length)
		if _, err := Read(bytes.NewReader(b), int64(len(b)), doc.Identity, 3); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("length %d: %v", length, err)
		}
	}
	var sink bytes.Buffer
	if err := write(&sink, doc, int64(len(wire)-53)); !errors.Is(err, ErrTooLarge) || sink.Len() != 0 {
		t.Fatalf("size failure wrote output: %v", err)
	}
	if err := write(&sink, doc, int64(len(wire)-52)); err != nil || !bytes.Equal(sink.Bytes(), wire) {
		t.Fatalf("exact size limit: %v", err)
	}
	invalid := doc
	invalid.State.Results = nil
	sink.Reset()
	if err := Write(&sink, invalid); !errors.Is(err, ErrInvalid) || sink.Len() != 0 {
		t.Fatalf("invalid checkpoint wrote output: %v", err)
	}
}

type changingSource struct {
	original, changed []byte
	decode            bool
}

func (r *changingSource) ReadAt(b []byte, off int64) (int, error) {
	if off == headerBytes {
		r.decode = true
	}
	data := r.original
	if r.decode {
		data = r.changed
	}
	return bytes.NewReader(data).ReadAt(b, off)
}

func TestRejectPayloadChangedAfterChecksumPass(t *testing.T) {
	doc, _, wire := fixture(t)
	changed := bytes.Replace(wire, []byte(`"availableBaseLots":"1"`), []byte(`"availableBaseLots":"2"`), 1)
	r := &changingSource{original: wire, changed: changed}
	if _, err := Read(r, int64(len(wire)), doc.Identity, 3); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("changed reader: %v", err)
	}
}

func FuzzSnapshotReader(f *testing.F) {
	doc, _, wire := fixture(f)
	f.Add(wire)
	f.Add(wire[headerBytes : len(wire)-sha256.Size])
	f.Add([]byte("EXSNP001"))
	f.Add([]byte(`{"runId":null}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 128<<10 {
			t.Skip()
		}
		for _, candidate := range [][]byte{b, wrapPayload(b, 1)} {
			got, err := Read(bytes.NewReader(candidate), int64(len(candidate)), doc.Identity, 1000)
			if err == nil {
				var encoded bytes.Buffer
				if err := Write(&encoded, got); err != nil || !bytes.Equal(encoded.Bytes(), candidate) {
					t.Fatalf("noncanonical success: %v", err)
				}
			}
		}
	})
}
