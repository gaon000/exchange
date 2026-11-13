//go:build !darwin && !linux

package main

import (
	"errors"
	"os"
)

func nativePread(f *os.File) func([]byte, int64) (int, error) {
	return func([]byte, int64) (int, error) { return 0, errors.New("pread diagnostic unsupported") }
}
func isInterrupted(err error) bool { return false }
