package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type artifacts struct {
	dir    string
	hashes map[string]string
}

func (a *artifacts) json(name string, value any) error {
	return a.write(name, func(w io.Writer) error { return json.NewEncoder(w).Encode(value) })
}
func (a *artifacts) write(name string, write func(io.Writer) error) error {
	f, err := os.OpenFile(filepath.Join(a.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	writeErr := write(io.MultiWriter(f, h))
	syncErr := f.Sync()
	if err := errors.Join(writeErr, syncErr, f.Close()); err != nil {
		return err
	}
	a.hashes[name] = hex.EncodeToString(h.Sum(nil))
	return nil
}
