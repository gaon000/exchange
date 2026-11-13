package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

// Only experiment binaries set this label. Observed request sizes must agree.
var readBufferLabel = "32768"

type readCounts struct {
	Calls, RequestedBytes, ReadBytes int64
	MaxRequest                       int
	Sizes                            map[int]int64
}

func (s *readCounts) add(request, read int) {
	s.Calls++
	s.RequestedBytes += int64(request)
	s.ReadBytes += int64(read)
	s.MaxRequest = max(s.MaxRequest, request)
	if s.Sizes == nil {
		s.Sizes = map[int]int64{}
	}
	s.Sizes[request]++
}

type observedReader struct {
	source            io.ReaderAt
	footer            int64
	started, decoding bool
	phases            map[string]*readCounts
}

func (r *observedReader) ReadAt(p []byte, off int64) (int, error) {
	phase := "checksum"
	if !r.started {
		phase = "header"
		r.started = true
	} else if !r.decoding && off == r.footer && len(p) == 32 {
		phase = "digest"
		r.decoding = true
	} else if r.decoding {
		phase = "decode"
	}
	n, err := r.source.ReadAt(p, off)
	if r.phases[phase] == nil {
		r.phases[phase] = &readCounts{}
	}
	r.phases[phase].add(len(p), n)
	return n, err
}

type preadReader struct {
	call                    func([]byte, int64) (int, error)
	Counts                  readCounts
	Interrupted, ShortReads int64
}

func (r *preadReader) ReadAt(p []byte, off int64) (int, error) {
	total := 0
	for len(p) > 0 {
		n, err := r.call(p, off)
		if n < 0 {
			n = 0
		}
		r.Counts.add(len(p), n)
		if isInterrupted(err) {
			r.Interrupted++
			continue
		}
		if n > len(p) {
			return total, errors.New("invalid pread count")
		}
		total += n
		p = p[n:]
		off += int64(n)
		if err != nil {
			return total, err
		}
		if len(p) > 0 {
			r.ShortReads++
		}
		if n == 0 {
			return total, io.EOF
		}
	}
	return total, nil
}

type readIO struct {
	Version, BufferBytes         int
	SnapshotSeq                  d.Int
	SnapshotFile, SnapshotSHA256 string
	PayloadBytes                 int64
	FileReadAt, DirectPread      map[string]*readCounts
	PreadCalls                   readCounts
	Interrupted, ShortReads      int64
	Verified                     bool
}

func verifyReadCounts(phases map[string]*readCounts, payload int64, buffer int) error {
	sizes := map[string]int64{"header": 20, "checksum": payload + 20, "digest": 32, "decode": payload}
	if len(phases) != len(sizes) {
		return errors.New("IO phases missing")
	}
	for name, n := range sizes {
		quantum := int64(buffer)
		if name == "checksum" {
			quantum = 32768
		}
		if name == "header" || name == "digest" {
			quantum = n
		}
		s := phases[name]
		if s == nil || s.Calls != (n+quantum-1)/quantum || s.RequestedBytes != n || s.ReadBytes != n || s.MaxRequest != int(min(n, quantum)) {
			return fmt.Errorf("IO phase %s mismatch: %+v", name, s)
		}
		want := map[int]int64{}
		want[int(quantum)] = n / quantum
		if n%quantum > 0 {
			want[int(n%quantum)]++
		}
		if want[int(quantum)] == 0 {
			delete(want, int(quantum))
		}
		if !reflect.DeepEqual(want, s.Sizes) {
			return errors.New("IO request histogram mismatch")
		}
	}
	return nil
}
func inspectReadIO(dir string, f tailFixture, tail int) (_ readIO, err error) {
	buffer, err := strconv.Atoi(readBufferLabel)
	if err != nil || (buffer != 32768 && buffer != 65536 && buffer != 131072 && buffer != 262144) {
		return readIO{}, errors.New("invalid buffer build label")
	}
	seq := d.Int(f.Commands - tail)
	name, _ := snapshot.Filename(seq)
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return readIO{}, err
	}
	if !info.Mode().IsRegular() {
		return readIO{}, errors.New("IO requires regular snapshot")
	}
	file, err := os.Open(path)
	if err != nil {
		return readIO{}, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	var header [20]byte
	if _, err = file.ReadAt(header[:], 0); err != nil {
		return readIO{}, err
	}
	payload := int64(binary.LittleEndian.Uint64(header[12:]))
	if payload <= 0 || payload > snapshot.MaxPayloadBytes || info.Size() != payload+52 {
		return readIO{}, errors.New("invalid IO envelope")
	}
	g, err := wal.NewGenesis(f.RunID, configuration(f.Commands))
	if err != nil {
		return readIO{}, err
	}
	id := snapshot.Identity{RunID: g.RunID, ConfigHash: g.ConfigHash, RulesVersion: g.Config.RulesVersion, ResponseVersion: g.Config.ResponseVersion}
	observe := func(source io.ReaderAt) (snapshot.Document, map[string]*readCounts, error) {
		reader := &observedReader{source: source, footer: payload + 20, phases: map[string]*readCounts{}}
		doc, e := snapshot.Read(reader, info.Size(), id, d.Int(f.Commands))
		if e == nil {
			e = verifyReadCounts(reader.phases, payload, buffer)
		}
		return doc, reader.phases, e
	}
	a, osCounts, err := observe(file)
	if err != nil {
		return readIO{}, err
	}
	direct := &preadReader{call: nativePread(file)}
	b, counts, err := observe(direct)
	runtime.KeepAlive(file)
	if err != nil {
		return readIO{}, err
	}
	if a.SnapshotSeq != seq || !reflect.DeepEqual(a, b) || !reflect.DeepEqual(osCounts, counts) {
		return readIO{}, errors.New("ReadAt/pread document or request mismatch")
	}
	sha, err := digest(path)
	if err != nil {
		return readIO{}, err
	}
	if sha != f.Files[name] {
		return readIO{}, errors.New("IO changed selected snapshot")
	}
	return readIO{Version: 1, BufferBytes: buffer, SnapshotSeq: seq, SnapshotFile: name, SnapshotSHA256: sha, PayloadBytes: payload, FileReadAt: osCounts, DirectPread: counts,
		PreadCalls: direct.Counts, Interrupted: direct.Interrupted, ShortReads: direct.ShortReads, Verified: true}, nil
}
