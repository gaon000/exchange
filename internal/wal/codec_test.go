package wal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

const testRun = "00000000-0000-4000-8000-000000000001"

func testConfig() d.Config {
	return d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 10, QuoteAtoms: 10000}, {AccountID: "bob", BaseLots: 20, QuoteAtoms: 20000}})
}
func testGenesis(t testing.TB) Genesis {
	t.Helper()
	g, err := NewGenesis(testRun, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func place(seq d.Int, account, id string) Record {
	return Record{Seq: seq, Command: d.Command{RunID: testRun, AccountID: account, Kind: d.PlaceOrder, Place: d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: d.Buy, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: 100, QuantityLots: 2}}}
}
func group(t testing.TB, records ...Record) []byte {
	t.Helper()
	b, err := encodeGroup(records)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func header(g Genesis) []byte { b, _ := json.Marshal(g.Metadata()); return envelope("EXWAL001", b) }
func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func scanBytes(b []byte, g Genesis) (Recovery, []Record, error) {
	var got []Record
	info, _, err := scan(bytes.NewReader(b), g, func(r Record) error { got = append(got, r); return nil })
	return info, got, err
}

func TestIndependentGoldenBytes(t *testing.T) {
	g := testGenesis(t)
	b, _ := json.Marshal(g.Config)
	if !bytes.Equal(b, fixture(t, "config.json")) {
		t.Fatal("config canonical bytes changed")
	}
	genJSON, _ := json.Marshal(g)
	if !bytes.Equal(envelope("EXGEN001", genJSON), fixture(t, "genesis.bin")) {
		t.Fatal("Genesis bytes changed")
	}
	r := place(1, "alice", "buy-1")
	record, _ := encodeRecord(r)
	if !bytes.Equal(record, fixture(t, "record.json")) {
		t.Fatal("command bytes changed")
	}
	cancel := Record{Seq: 2, Command: d.Command{RunID: testRun, AccountID: "alice", Kind: d.CancelOrder, Cancel: d.Cancel{CancelRequestID: "cancel-1", ClientOrderID: "buy-1"}}}
	cancelJSON, err := encodeRecord(cancel)
	if err != nil || !bytes.Equal(cancelJSON, fixture(t, "cancel.json")) {
		t.Fatalf("cancel bytes changed: %s %v", cancelJSON, err)
	}
	decodedCancel, err := decodeRecord(fixture(t, "cancel.json"), testRun)
	if err != nil || decodedCancel != cancel {
		t.Fatalf("cancel decode: %+v %v", decodedCancel, err)
	}
	want := fixture(t, "commands.wal")
	if got := append(header(g), group(t, r)...); !bytes.Equal(got, want) {
		t.Fatal("WAL metadata/group bytes changed")
	}
	info, records, err := scanBytes(want, g)
	if err != nil || info.LastSeq != 1 || info.EndOffset != int64(len(want)) || info.TornTail || !reflect.DeepEqual(records, []Record{r}) {
		t.Fatalf("%+v %+v %v", info, records, err)
	}
	payload, _, err := readEnvelope(bytes.NewReader(fixture(t, "genesis.bin")), "EXGEN001", MaxGenesisPayload)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeGenesis(payload)
	if err != nil || !reflect.DeepEqual(g, got) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestMaximumGroupAndSequenceBounds(t *testing.T) {
	g := testGenesis(t)
	records := make([]Record, MaxGroupCommands)
	for i := range records {
		records[i] = place(d.Int(i+1), "alice", fmt.Sprintf("order-%d", i))
	}
	info, got, err := scanBytes(append(header(g), group(t, records...)...), g)
	if err != nil || info.LastSeq != MaxGroupCommands || !reflect.DeepEqual(records, got) {
		t.Fatalf("maximum group: %+v %v", info, err)
	}
	for _, bad := range [][]Record{nil, append(records, place(257, "alice", "extra")), {place(-1, "alice", "a")}, {place(math.MaxInt64, "alice", "a"), place(1, "bob", "b")}} {
		if _, err := encodeGroup(bad); err == nil {
			t.Fatal("invalid group encoded")
		}
	}
	if err := newValidator(g).check([]Record{place(1, "alice", "a")}, math.MaxInt64); err == nil {
		t.Fatal("sequence overflow accepted")
	}
}

func TestConfigNormalizationAndRejections(t *testing.T) {
	g := testGenesis(t)
	c := testConfig()
	c.Accounts[0], c.Accounts[1] = c.Accounts[1], c.Accounts[0]
	data, _ := json.MarshalIndent(c, "", "  ")
	data = bytes.Replace(data, []byte(`"10000"`), []byte(`"00010000"`), 1)
	normal, err := DecodeConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewGenesis(testRun, normal)
	if err != nil || !reflect.DeepEqual(g, got) {
		t.Fatalf("%+v %v", got, err)
	}
	if c.Accounts[0].AccountID != "bob" {
		t.Fatal("caller config sorted in place")
	}
	got.Config.Accounts[0].BaseLots = 0
	if c.Accounts[1].BaseLots != 10 {
		t.Fatal("Genesis aliased input")
	}
	fixtureConfig := fixture(t, "config.json")
	for _, b := range [][]byte{nil, []byte(`null`), bytes.Repeat([]byte(" "), MaxGenesisPayload+1), bytes.Replace(fixtureConfig, []byte(`"schemaVersion":1`), []byte(`"schemaVersion":2`), 1), bytes.Replace(fixtureConfig, []byte(`"bob"`), []byte(`"alice"`), 1), bytes.Replace(fixtureConfig, []byte(`"baseLots":"10"`), []byte(`"baseLots":null`), 1)} {
		if _, err := DecodeConfig(b); err == nil {
			t.Fatal("bad config accepted")
		}
	}
	if _, err := NewGenesis("wrong", c); err == nil {
		t.Fatal("bad run ID")
	}
}

func TestEveryEnvelopeTruncationAndCorruption(t *testing.T) {
	for _, tc := range []struct {
		magic string
		data  []byte
		limit uint64
	}{{"EXGEN001", fixture(t, "genesis.bin"), MaxGenesisPayload}, {"EXWAL001", header(testGenesis(t)), MaxMetadataPayload}} {
		for cut := 0; cut < len(tc.data); cut++ {
			if _, _, err := readEnvelope(bytes.NewReader(tc.data[:cut]), tc.magic, tc.limit); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s cut %d: %v", tc.magic, cut, err)
			}
		}
		for i := range tc.data {
			bad := bytes.Clone(tc.data)
			bad[i] ^= 1
			if _, _, err := readEnvelope(bytes.NewReader(bad), tc.magic, tc.limit); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s flip %d: %v", tc.magic, i, err)
			}
		}
		for _, n := range []uint64{0, tc.limit + 1, math.MaxUint64} {
			bad := bytes.Clone(tc.data)
			little.PutUint64(bad[12:20], n)
			if _, _, err := readEnvelope(bytes.NewReader(bad), tc.magic, tc.limit); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("bad size %d: %v", n, err)
			}
		}
	}
}

func TestEveryGroupTruncation(t *testing.T) {
	g := testGenesis(t)
	prefix := append(header(g), group(t, place(1, "alice", "a"))...)
	last := group(t, place(2, "bob", "b"), place(3, "alice", "c"))
	for cut := 0; cut <= len(last); cut++ {
		data := append(bytes.Clone(prefix), last[:cut]...)
		info, records, err := scanBytes(data, g)
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		if cut < len(last) {
			if info.LastSeq != 1 || info.EndOffset != int64(len(prefix)) || info.TornTail != (cut > 0) || len(records) != 1 {
				t.Fatalf("cut %d: %+v records %d", cut, info, len(records))
			}
		} else if info.LastSeq != 3 || info.TornTail || len(records) != 3 || info.EndOffset != int64(len(data)) {
			t.Fatalf("complete: %+v %v", info, records)
		}
	}
}

func TestEveryCompleteGroupBitFlipIsFatal(t *testing.T) {
	g := testGenesis(t)
	valid := group(t, place(1, "alice", "a"), place(2, "bob", "b"))
	for i := range valid {
		for bit := uint(0); bit < 8; bit++ {
			bad := bytes.Clone(valid)
			bad[i] ^= 1 << bit
			_, records, err := scanBytes(append(header(g), bad...), g)
			if !errors.Is(err, ErrCorrupt) || len(records) != 0 {
				t.Fatalf("byte %d bit %d: records %d err %v", i, bit, len(records), err)
			}
		}
	}
}

func fixGroupCRC(b []byte) {
	little.PutUint32(b[32:36], crc32.Checksum(b[:32], castagnoli))
	little.PutUint32(b[len(b)-4:], crc32.Checksum(b[:len(b)-4], castagnoli))
}

func TestInvalidCompleteHeadersNeverBecomeTornTail(t *testing.T) {
	g := testGenesis(t)
	for name, mutate := range map[string]func([]byte){
		"magic":                      func(b []byte) { b[0] = 'X' },
		"header bytes":               func(b []byte) { little.PutUint32(b[8:12], 35) },
		"payload zero":               func(b []byte) { little.PutUint32(b[12:16], 0) },
		"payload huge":               func(b []byte) { little.PutUint32(b[12:16], MaxGroupPayload+1) },
		"payload impossible":         func(b []byte) { little.PutUint32(b[12:16], 4) },
		"payload too long for count": func(b []byte) { little.PutUint32(b[12:16], MaxRecordBytes+5) },
		"count zero":                 func(b []byte) { little.PutUint32(b[24:28], 0) },
		"count huge":                 func(b []byte) { little.PutUint32(b[24:28], 257) },
		"seq zero":                   func(b []byte) { little.PutUint64(b[16:24], 0) },
		"seq gap":                    func(b []byte) { little.PutUint64(b[16:24], 2) },
		"seq overflow":               func(b []byte) { little.PutUint64(b[16:24], math.MaxUint64) },
		"flags":                      func(b []byte) { little.PutUint32(b[28:32], 1) },
	} {
		t.Run(name, func(t *testing.T) {
			b := group(t, place(1, "alice", "a"))
			mutate(b)
			fixGroupCRC(b)
			_, _, err := scanBytes(append(header(g), b[:headerBytes]...), g)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%v", err)
			}
		})
	}
	g.Config.MaxCommands = 1
	_, _, err := scanBytes(append(header(g), group(t, place(1, "alice", "a"), place(2, "bob", "b"))[:headerBytes]...), g)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatal("run limit treated as torn")
	}
}

// Frame arbitrary canonical or malformed bytes with valid outer checksums, so
// these tests reach semantic validation instead of stopping at a CRC failure.
func rawGroup(payload []byte, count uint32) []byte {
	b := make([]byte, headerBytes+len(payload)+footerBytes)
	copy(b, "EXGRP001")
	little.PutUint32(b[8:12], headerBytes)
	little.PutUint32(b[12:16], uint32(len(payload)))
	little.PutUint64(b[16:24], 1)
	little.PutUint32(b[24:28], count)
	copy(b[headerBytes:], payload)
	copy(b[len(b)-16:], "EXEND001")
	little.PutUint32(b[len(b)-8:len(b)-4], uint32(len(b)))
	fixGroupCRC(b)
	return b
}
func rawPayload(records ...[]byte) []byte {
	var b []byte
	for _, r := range records {
		b = little.AppendUint32(b, uint32(len(r)))
		b = append(b, r...)
	}
	return b
}

func TestSemanticCorruptionWithValidChecksums(t *testing.T) {
	g := testGenesis(t)
	r1 := fixture(t, "record.json")
	for _, bad := range [][]byte{
		bytes.Replace(r1, []byte(`"1"`), []byte(`"01"`), 1),
		bytes.Replace(r1, []byte(`"1"`), []byte(`"2"`), 1),
		bytes.Replace(r1, []byte(`"alice"`), []byte(`"unknown"`), 1),
		bytes.Replace(r1, []byte(`"BUY"`), []byte(`"BAD"`), 1),
		bytes.Replace(r1, []byte(`"priceTicks":"100"`), []byte(`"priceTicks":100`), 1),
		bytes.Replace(r1, []byte(`"priceTicks":"100"`), []byte(`"priceTicks":"9223372036854775808"`), 1),
		bytes.Replace(r1, []byte(`"accountId":"alice"`), []byte(`"accountId":"alice","accountId":"bob"`), 1),
		bytes.Replace(r1, []byte(`"commandSeq"`), []byte(`"CommandSeq"`), 1),
		bytes.Replace(r1, []byte(`"quantityLots":"2"`), []byte(`"quantityLots":"0"`), 1),
		append([]byte(" "), r1...), []byte(`null`), []byte(`{}`),
	} {
		_, records, err := scanBytes(append(header(g), rawGroup(rawPayload(bad), 1)...), g)
		if !errors.Is(err, ErrCorrupt) || len(records) != 0 {
			t.Fatalf("accepted %s: %v", bad, err)
		}
	}
	second := bytes.Replace(r1, []byte(`"commandSeq":"1"`), []byte(`"commandSeq":"2"`), 1)
	for _, payload := range [][]byte{rawPayload(r1, second), rawPayload(r1, []byte(`{}`)), append(rawPayload(r1), 0), rawPayload(bytes.Repeat([]byte("x"), MaxRecordBytes+1)), append(little.AppendUint32(nil, 4096), r1...), append(little.AppendUint32(nil, 0), r1...)} {
		count := uint32(1)
		if bytes.Equal(payload, rawPayload(r1, second)) || bytes.Equal(payload, rawPayload(r1, []byte(`{}`))) {
			count = 2
		}
		_, records, err := scanBytes(append(header(g), rawGroup(payload, count)...), g)
		if !errors.Is(err, ErrCorrupt) || len(records) != 0 {
			t.Fatalf("malformed payload: %v records %d", err, len(records))
		}
	}
	// Duplicate keys across separate valid groups also fail; sequence alone is insufficient.
	data := append(header(g), group(t, place(1, "alice", "a"))...)
	data = append(data, group(t, place(2, "alice", "a"))...)
	info, records, err := scanBytes(data, g)
	if !errors.Is(err, ErrCorrupt) || info.LastSeq != 1 || len(records) != 1 {
		t.Fatalf("duplicate: %+v %v", info, err)
	}
}

func TestIdentityVersionsAndVisitorFailure(t *testing.T) {
	g := testGenesis(t)
	b := header(g)
	for _, needle := range []string{g.RunID, g.ConfigHash, `"rulesVersion":1`, `"responseVersion":1`} {
		p, _ := json.Marshal(g.Metadata())
		p = bytes.Replace(p, []byte(needle), []byte("bad"), 1)
		if _, _, err := scanBytes(envelope("EXWAL001", p), g); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("%s: %v", needle, err)
		}
	}
	p, _ := json.Marshal(g)
	for _, bad := range [][]byte{bytes.Replace(p, []byte(g.ConfigHash), []byte(strings.Repeat("0", 64)), 1), bytes.Replace(p, []byte(`"rulesVersion":1`), []byte(`"rulesVersion":2`), 1), append(p, ' ')} {
		if _, err := decodeGenesis(bad); !errors.Is(err, ErrCorrupt) {
			t.Fatal(err)
		}
	}
	stop := errors.New("visitor stopped")
	if _, _, err := scan(bytes.NewReader(append(b, group(t, place(1, "alice", "a"))...)), g, func(Record) error { return stop }); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	for _, offset := range []int{0, len(b), len(b) + headerBytes} {
		data := append(bytes.Clone(b), group(t, place(1, "alice", "a"))...)
		_, _, err := scan(io.MultiReader(bytes.NewReader(data[:offset]), errorReader{stop}), g, nil)
		if err == nil {
			t.Fatalf("read error at %d swallowed", offset)
		}
	}
}

type errorReader struct{ err error }

func (e errorReader) Read([]byte) (int, error) { return 0, e.err }

func FuzzWALReader(f *testing.F) {
	g := testGenesis(f)
	f.Add(fixture(f, "commands.wal"))
	f.Add(header(g))
	f.Add([]byte("bad"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 2*MaxGroupPayload {
			t.Skip()
		}
		info, records, err := scanBytes(b, g)
		if err == nil {
			if info.LastSeq != d.Int(len(records)) || info.EndOffset > int64(len(b)) {
				t.Fatalf("invalid recovery: %+v", info)
			}
			for i, r := range records {
				if r.Seq != d.Int(i+1) || r.Command.Validate() != nil {
					t.Fatal(fmt.Sprint(r))
				}
			}
		}
	})
}
