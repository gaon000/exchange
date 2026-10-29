package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func readSmall(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("expected regular JSON at most 4 MiB: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	err = errors.Join(err, f.Close())
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("oversized JSON")
	}
	return b, err
}
func readJSON(path string, v any) error {
	b, err := readSmall(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func hashFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("non-regular file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	err = errors.Join(err, f.Close())
	return hex.EncodeToString(h.Sum(nil)), err
}
func lines(path string, visit func([]byte) error) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("non-regular JSONL: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1<<20)
	for s.Scan() {
		if err := visit(s.Bytes()); err != nil {
			return err
		}
	}
	return s.Err()
}
func canonical(v any) string { b, _ := json.Marshal(v); return string(b) }

type seal struct {
	Version int               `json:"version"`
	RunID   string            `json:"runId"`
	Files   map[string]string `json:"sha256"`
}

func verifyFiles(dir string) (seal, error) {
	var s seal
	if err := readJSON(filepath.Join(dir, "completed.json"), &s); err != nil {
		return s, err
	}
	if s.Version != 1 || s.RunID == "" || len(s.Files) < 1 || len(s.Files) > 1200 {
		return s, fmt.Errorf("invalid completion marker")
	}
	for name, hash := range s.Files {
		b, err := hex.DecodeString(hash)
		if !fs.ValidPath(name) || name == "completed.json" || err != nil || len(b) != 32 {
			return s, fmt.Errorf("invalid hash entry")
		}
	}
	count := 0
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular evidence file: %s", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "completed.json" {
			return nil
		}
		expected, ok := s.Files[rel]
		if !ok {
			return fmt.Errorf("unlisted evidence file: %s", rel)
		}
		actual, err := hashFile(path)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("hash mismatch: %s", rel)
		}
		count++
		return nil
	})
	if err != nil {
		return s, err
	}
	if count != len(s.Files) {
		return s, fmt.Errorf("missing evidence file")
	}
	for _, name := range []string{"manifest.json", "environment.json", "server-ready.json", "server-metrics.jsonl", "verification.json", "config.json", "workload.json", "bin/exchange", "bin/loadgen", "loadgen/manifest.json", "loadgen/plan.jsonl", "loadgen/preparation.json", "loadgen/samples.jsonl", "loadgen/run.json", "loadgen/summary.json", "loadgen/completed.json"} {
		if _, ok := s.Files[name]; !ok {
			return s, fmt.Errorf("missing required artifact %s", name)
		}
	}
	return s, nil
}
