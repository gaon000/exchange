// storageprobe checks storage calls in a fresh temporary directory.
// It does not simulate a power failure or validate the exchange WAL.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type report struct {
	GoVersion string   `json:"goVersion"`
	Platform  string   `json:"platform"`
	Checks    []string `json:"checks"`
	PowerLoss string   `json:"powerLoss"`
}

func main() {
	dir := flag.String("dir", ".", "existing parent on the target filesystem")
	flag.Parse()
	result, err := probe(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probe(parent string) (result report, err error) {
	result = report{GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, PowerLoss: "not tested"}
	if err := checkFilesystem(parent); err != nil {
		return result, fmt.Errorf("APFS preflight: %w", err)
	}
	dir, err := os.MkdirTemp(parent, ".exchange-probe-")
	if err != nil {
		return result, err
	}
	defer func() {
		if cleanupErr := os.RemoveAll(dir); err == nil {
			err = cleanupErr
		}
	}()
	file, err := os.OpenFile(filepath.Join(dir, "record.tmp"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return result, err
	}
	defer file.Close()
	const payload = "exchange P0 storage probe\n"
	if _, err = file.WriteString(payload); err != nil {
		return result, err
	}
	if err = fullSync(file); err != nil {
		return result, fmt.Errorf("file F_FULLFSYNC: %w", err)
	}
	result.Checks = append(result.Checks, "file F_FULLFSYNC")
	if err = os.Rename(file.Name(), filepath.Join(dir, "record")); err != nil {
		return result, err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return result, err
	}
	defer directory.Close()
	if err = fullSync(directory); err != nil {
		return result, fmt.Errorf("directory F_FULLFSYNC: %w", err)
	}
	result.Checks = append(result.Checks, "rename + directory F_FULLFSYNC")
	data, err := os.ReadFile(filepath.Join(dir, "record"))
	if err != nil || string(data) != payload {
		return result, fmt.Errorf("read-back mismatch (read error: %v)", err)
	}
	if err = tryLock(file); err != nil {
		return result, fmt.Errorf("exclusive lock: %w", err)
	}
	second, err := os.OpenFile(filepath.Join(dir, "record"), os.O_RDWR, 0)
	if err != nil {
		return result, err
	}
	defer second.Close()
	if err = tryLock(second); !lockContended(err) {
		return result, fmt.Errorf("second lock must be contended, got: %v", err)
	}
	result.Checks = append(result.Checks, "independent descriptor lock contention")
	if err = file.Truncate(8); err != nil {
		return result, err
	}
	if err = fullSync(file); err != nil {
		return result, err
	}
	result.Checks = append(result.Checks, "truncate + F_FULLFSYNC", "read-back")
	return result, nil
}
