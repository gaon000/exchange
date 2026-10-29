// Package wal owns versioned command records, durable groups, and run files.
package wal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

// Storage errors distinguish invalid input, corrupt files, and unavailable writes.
var (
	ErrCorrupt  = errors.New("corrupt or incompatible storage")
	ErrInvalid  = errors.New("invalid WAL input")
	ErrPoisoned = errors.New("storage failed; close and recover")
	ErrNotReady = errors.New("recovery durability not confirmed")
)

// Genesis is the immutable identity and initial supply of a run.
type Genesis struct {
	RunID      string   `json:"runId"`
	ConfigHash string   `json:"configHash"`
	Config     d.Config `json:"config"`
}

// Metadata binds every following record to its Genesis and semantic versions.
type Metadata struct {
	RunID           string `json:"runId"`
	ConfigHash      string `json:"configHash"`
	RulesVersion    int    `json:"rulesVersion"`
	ResponseVersion int    `json:"responseVersion"`
}

// Record is an explicit sequence plus its normalized command.
type Record struct {
	Seq     d.Int
	Command d.Command
}

type commandWire struct {
	Seq       d.Int           `json:"commandSeq"`
	Kind      d.Kind          `json:"kind"`
	AccountID string          `json:"accountId"`
	Payload   json.RawMessage `json:"payload"`
}

// DecodeConfig reads strict user JSON, leaving files and identity generation to callers.
func DecodeConfig(data []byte) (d.Config, error) {
	var c d.Config
	if len(data) > MaxGenesisPayload {
		return c, ErrInvalid
	}
	if err := strictjson.Decode(data, &c); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// NewGenesis validates and copies configuration, sorting accounts canonically.
func NewGenesis(runID string, c d.Config) (Genesis, error) {
	if !d.ValidRunID(runID) {
		return Genesis{}, ErrInvalid
	}
	if err := c.Validate(); err != nil {
		return Genesis{}, err
	}
	c.Accounts = append([]d.InitialAccount(nil), c.Accounts...)
	sort.Slice(c.Accounts, func(i, j int) bool { return c.Accounts[i].AccountID < c.Accounts[j].AccountID })
	data, err := json.Marshal(c)
	if err != nil {
		return Genesis{}, err
	}
	hash := sha256.Sum256(data)
	return Genesis{runID, hex.EncodeToString(hash[:]), c}, nil
}

// Metadata derives the only permitted header for this Genesis.
func (g Genesis) Metadata() Metadata {
	return Metadata{g.RunID, g.ConfigHash, g.Config.RulesVersion, g.Config.ResponseVersion}
}

func encodeRecord(r Record) ([]byte, error) {
	if r.Seq < 1 {
		return nil, ErrInvalid
	}
	if err := r.Command.Validate(); err != nil {
		return nil, err
	}
	var payload []byte
	var err error
	if r.Command.Kind == d.PlaceOrder {
		payload, err = json.Marshal(r.Command.Place)
	} else {
		payload, err = json.Marshal(r.Command.Cancel)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(commandWire{r.Seq, r.Command.Kind, r.Command.AccountID, payload})
}

func decodeRecord(data []byte, runID string) (Record, error) {
	var w commandWire
	if err := strictjson.Decode(data, &w); err != nil {
		return Record{}, fmt.Errorf("%w: command: %v", ErrCorrupt, err)
	}
	c := d.Command{RunID: runID, AccountID: w.AccountID, Kind: w.Kind}
	var err error
	switch w.Kind {
	case d.PlaceOrder:
		err = strictjson.Decode(w.Payload, &c.Place)
	case d.CancelOrder:
		err = strictjson.Decode(w.Payload, &c.Cancel)
	default:
		err = ErrInvalid
	}
	r := Record{w.Seq, c}
	canonical, encodeErr := encodeRecord(r)
	if err != nil || encodeErr != nil || !bytes.Equal(data, canonical) {
		return Record{}, fmt.Errorf("%w: noncanonical command", ErrCorrupt)
	}
	return r, nil
}

func decodeGenesis(data []byte) (Genesis, error) {
	var g Genesis
	if err := strictjson.Decode(data, &g); err != nil {
		return g, fmt.Errorf("%w: genesis: %v", ErrCorrupt, err)
	}
	want, err := NewGenesis(g.RunID, g.Config)
	if err != nil {
		return g, fmt.Errorf("%w: genesis: %v", ErrCorrupt, err)
	}
	b, _ := json.Marshal(want)
	if !bytes.Equal(data, b) {
		return g, fmt.Errorf("%w: genesis identity/canonical bytes", ErrCorrupt)
	}
	return want, nil
}
