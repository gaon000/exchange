package main

import (
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
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("expected regular file at most 4 MiB: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	err = errors.Join(err, f.Close())
	if len(b) > 4<<20 {
		err = errors.Join(err, fmt.Errorf("file grew beyond 4 MiB"))
	}
	return b, err
}
func readJSON(path string, target any) error {
	b, err := readSmall(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
func writeJSON(path string, value any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(value)
	return errors.Join(err, f.Sync(), f.Close())
}
func copyFile(source, target string, executable bool) error {
	before, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("input is not a regular file: %s", source)
	}
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	limit := int64(4 << 20)
	mode := os.FileMode(0600)
	if executable {
		limit = 128 << 20
		mode = 0700
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return fmt.Errorf("invalid or oversized input %s", source)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(f, limit+1))
	if n > limit {
		err = errors.Join(err, fmt.Errorf("input grew beyond limit"))
	}
	return errors.Join(err, out.Sync(), out.Close())
}
func hashFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("non-regular artifact: %s", path)
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
func bundleHashes(dir string) (map[string]string, error) {
	hashes := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if len(hashes) >= 1200 {
			return fmt.Errorf("artifact file count exceeds 1200")
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		hashes[filepath.ToSlash(rel)] = hash
		return nil
	})
	return hashes, err
}
