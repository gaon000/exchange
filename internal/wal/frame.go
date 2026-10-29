package wal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math"

	d "github.com/gaon000/exchange/internal/domain"
)

// Wire limits bound allocation before reading untrusted lengths.
const (
	MaxGenesisPayload  = 4 << 20
	MaxMetadataPayload = 4 << 10
	MaxRecordBytes     = 4096
	MaxGroupPayload    = 1 << 20
	MaxGroupCommands   = 256
	headerBytes        = 36
	footerBytes        = 16
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)
var little = binary.LittleEndian

func envelope(magic string, payload []byte) []byte {
	b := make([]byte, 20+len(payload)+sha256.Size)
	copy(b, magic)
	little.PutUint32(b[8:12], 1)
	little.PutUint64(b[12:20], uint64(len(payload)))
	copy(b[20:], payload)
	digest := sha256.Sum256(b[:20+len(payload)])
	copy(b[20+len(payload):], digest[:])
	return b
}

func readEnvelope(r io.Reader, magic string, limit uint64) ([]byte, int64, error) {
	h := make([]byte, 20)
	if _, err := io.ReadFull(r, h); err != nil {
		return nil, 0, fmt.Errorf("%w: envelope header: %v", ErrCorrupt, err)
	}
	n := little.Uint64(h[12:20])
	if string(h[:8]) != magic || little.Uint32(h[8:12]) != 1 || n == 0 || n > limit {
		return nil, 0, fmt.Errorf("%w: envelope bounds/version", ErrCorrupt)
	}
	b := make([]byte, int(n)+sha256.Size)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, 0, fmt.Errorf("%w: envelope payload: %v", ErrCorrupt, err)
	}
	hash := sha256.New()
	_, _ = hash.Write(h)
	_, _ = hash.Write(b[:n])
	if !bytes.Equal(hash.Sum(nil), b[n:]) {
		return nil, 0, fmt.Errorf("%w: envelope checksum", ErrCorrupt)
	}
	return b[:n], int64(20 + n + sha256.Size), nil
}

func encodeGroup(records []Record) ([]byte, error) {
	if len(records) == 0 || len(records) > MaxGroupCommands {
		return nil, ErrInvalid
	}
	var payload []byte
	for i, r := range records {
		if r.Seq < 1 || records[0].Seq > math.MaxInt64-d.Int(i) || r.Seq != records[0].Seq+d.Int(i) {
			return nil, ErrInvalid
		}
		b, err := encodeRecord(r)
		if err != nil {
			return nil, err
		}
		if len(b) == 0 || len(b) > MaxRecordBytes || len(payload)+4+len(b) > MaxGroupPayload {
			return nil, ErrInvalid
		}
		payload = little.AppendUint32(payload, uint32(len(b)))
		payload = append(payload, b...)
	}
	out := make([]byte, headerBytes+len(payload)+footerBytes)
	copy(out, "EXGRP001")
	little.PutUint32(out[8:12], headerBytes)
	little.PutUint32(out[12:16], uint32(len(payload)))
	little.PutUint64(out[16:24], uint64(records[0].Seq))
	little.PutUint32(out[24:28], uint32(len(records)))
	little.PutUint32(out[32:36], crc32.Checksum(out[:32], castagnoli))
	copy(out[headerBytes:], payload)
	footer := out[headerBytes+len(payload):]
	copy(footer, "EXEND001")
	little.PutUint32(footer[8:12], uint32(len(out)))
	little.PutUint32(footer[12:16], crc32.Checksum(out[:len(out)-4], castagnoli))
	return out, nil
}

// Recovery describes validated bytes, not a previous process's sync completion.
type Recovery struct {
	LastSeq   d.Int
	EndOffset int64
	TornTail  bool
}

type validator struct {
	genesis  Genesis
	accounts map[string]bool
	seen     map[d.Key]bool
}

func newValidator(g Genesis) *validator {
	v := &validator{genesis: g, accounts: make(map[string]bool), seen: make(map[d.Key]bool)}
	for _, a := range g.Config.Accounts {
		v.accounts[a.AccountID] = true
	}
	return v
}

func (v *validator) check(records []Record, last d.Int) error {
	batch := make(map[d.Key]bool, len(records))
	if len(records) == 0 || len(records) > MaxGroupCommands {
		return ErrInvalid
	}
	for i, r := range records {
		if last > math.MaxInt64-d.Int(i)-1 || r.Seq != last+d.Int(i)+1 || r.Seq > v.genesis.Config.MaxCommands {
			return fmt.Errorf("%w: sequence/run limit", ErrCorrupt)
		}
		if r.Command.RunID != v.genesis.RunID || !v.accounts[r.Command.AccountID] || r.Command.Validate() != nil {
			return fmt.Errorf("%w: command identity", ErrCorrupt)
		}
		key := r.Command.Key()
		if v.seen[key] || batch[key] {
			return fmt.Errorf("%w: repeated request key", ErrCorrupt)
		}
		batch[key] = true
	}
	return nil
}
func (v *validator) accept(records []Record) {
	for _, r := range records {
		v.seen[r.Command.Key()] = true
	}
}

func scan(r io.Reader, g Genesis, visit func(Record) error) (Recovery, *validator, error) {
	var info Recovery
	data, n, err := readEnvelope(r, "EXWAL001", MaxMetadataPayload)
	if err != nil {
		return info, nil, err
	}
	want, _ := json.Marshal(g.Metadata())
	if !bytes.Equal(data, want) {
		return info, nil, fmt.Errorf("%w: WAL identity", ErrCorrupt)
	}
	info.EndOffset = n
	v := newValidator(g)
	for {
		h := make([]byte, headerBytes)
		n, err := io.ReadFull(r, h)
		if err == io.EOF && n == 0 {
			return info, v, nil
		}
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			info.TornTail = true
			return info, v, nil
		}
		if err != nil {
			return info, nil, err
		}
		payloadLen, count, first := little.Uint32(h[12:16]), little.Uint32(h[24:28]), little.Uint64(h[16:24])
		if string(h[:8]) != "EXGRP001" || little.Uint32(h[8:12]) != headerBytes || payloadLen == 0 || payloadLen > MaxGroupPayload || count == 0 || count > MaxGroupCommands || little.Uint32(h[28:32]) != 0 || little.Uint32(h[32:36]) != crc32.Checksum(h[:32], castagnoli) || first == 0 || first > math.MaxInt64 || first+uint64(count)-1 > math.MaxInt64 || info.LastSeq == math.MaxInt64 || d.Int(first) != info.LastSeq+1 {
			return info, nil, fmt.Errorf("%w: group header", ErrCorrupt)
		}
		if payloadLen < count*5 || payloadLen > count*(4+MaxRecordBytes) || first+uint64(count)-1 > uint64(g.Config.MaxCommands) {
			return info, nil, fmt.Errorf("%w: impossible group size/run limit", ErrCorrupt)
		}
		tail := make([]byte, int(payloadLen)+footerBytes)
		if _, err := io.ReadFull(r, tail); err == io.EOF || err == io.ErrUnexpectedEOF {
			info.TornTail = true
			return info, v, nil
		} else if err != nil {
			return info, nil, err
		}
		footer := tail[payloadLen:]
		crc := crc32.Update(0, castagnoli, h)
		crc = crc32.Update(crc, castagnoli, tail[:len(tail)-4])
		if string(footer[:8]) != "EXEND001" || little.Uint32(footer[8:12]) != uint32(headerBytes+len(tail)) || little.Uint32(footer[12:16]) != crc {
			return info, nil, fmt.Errorf("%w: group footer/checksum", ErrCorrupt)
		}
		payload := tail[:payloadLen]
		records := make([]Record, 0, count)
		for j := uint32(0); j < count; j++ {
			if len(payload) < 4 {
				return info, nil, fmt.Errorf("%w: record prefix", ErrCorrupt)
			}
			size := little.Uint32(payload[:4])
			payload = payload[4:]
			if size == 0 || size > MaxRecordBytes || uint64(size) > uint64(len(payload)) {
				return info, nil, fmt.Errorf("%w: record size", ErrCorrupt)
			}
			record, err := decodeRecord(payload[:size], g.RunID)
			if err != nil {
				return info, nil, err
			}
			records = append(records, record)
			payload = payload[size:]
		}
		if len(payload) != 0 {
			return info, nil, fmt.Errorf("%w: surplus payload", ErrCorrupt)
		}
		if err := v.check(records, info.LastSeq); err != nil {
			return info, nil, err
		}
		v.accept(records)
		if visit != nil {
			for _, record := range records {
				if err := visit(record); err != nil {
					return info, nil, err
				}
			}
		}
		info.LastSeq = records[len(records)-1].Seq
		info.EndOffset += int64(headerBytes + len(tail))
	}
}
