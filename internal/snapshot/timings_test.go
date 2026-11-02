package snapshot

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gaon000/exchange/internal/telemetry"
)

func TestStageTimingsPreserveBytesAndFailureBoundaries(t *testing.T) {
	doc, _, wire := fixture(t)
	dir := t.TempDir()
	var timings telemetry.Stages
	trace := &fileTrace{}
	if err := publishWithTimings(dir, doc, trace.ops(dir), MaxPayloadBytes, &timings); err != nil {
		t.Fatal(err)
	}
	name, _ := Filename(doc.SnapshotSeq)
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || !bytes.Equal(got, wire) {
		t.Fatalf("measured publication changed bytes: %v", err)
	}
	loaded, err := LoadWithTimings(dir, Candidate{name, doc.SnapshotSeq}, doc.Identity, doc.SnapshotSeq, &timings)
	if err != nil || !reflect.DeepEqual(doc, loaded) {
		t.Fatalf("measured decode changed state: %v", err)
	}
	if err := confirmWithTimings(dir, doc, trace.ops(dir), &timings); err != nil {
		t.Fatal(err)
	}
	if err := publishWithTimings(dir, doc, trace.ops(dir), MaxPayloadBytes, &timings); err != nil {
		t.Fatal(err)
	}
	r := timings.Snapshot()
	for _, name := range []string{"publish.encodeWrite", "publish.fileSync", "load.checksum", "load.decode", "decode.results", "confirm.compareBytes", "confirm.existingFileSync", "publish.compareBytes", "publish.existingFileSync"} {
		if r[name].Count != 1 {
			t.Fatalf("wrong stage attempts %s: %+v", name, r[name])
		}
	}
	if r["publish.measure"].Count != 2 || r["publish.directorySync"].Count != 2 || trace.handles != 0 {
		t.Fatal(r)
	}
	// A failed file sync is observed, but rename/directory sync never happened.
	failDir := t.TempDir()
	failed := &telemetry.Stages{}
	ops := trace.ops(failDir)
	ops.sync = func(file) error { return errDisk }
	if err := publishWithTimings(failDir, doc, ops, MaxPayloadBytes, failed); !errors.Is(err, errDisk) {
		t.Fatal(err)
	}
	f := failed.Snapshot()
	if f["publish.fileSync"].Count != 1 || f["publish.rename"].Count != 0 || f["publish.directorySync"].Count != 0 || trace.handles != 0 {
		t.Fatal(f)
	}
	// Invalid envelopes stop before JSON decoding, while still timing the attempt.
	broken := append([]byte{}, wire...)
	broken[len(broken)-1] ^= 1
	bad := &telemetry.Stages{}
	if _, err := readWithTimings(bytes.NewReader(broken), int64(len(broken)), doc.Identity, doc.SnapshotSeq, bad); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	if r := bad.Snapshot(); r["load.checksum"].Count != 1 || r["load.decode"].Count != 0 {
		t.Fatal(r)
	}
}
