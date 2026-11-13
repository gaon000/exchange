//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"syscall"
)

func nativePread(f *os.File) func([]byte, int64) (int, error) {
	fd := int(f.Fd())
	return func(p []byte, off int64) (int, error) { return syscall.Pread(fd, p, off) }
}
func isInterrupted(err error) bool { return errors.Is(err, syscall.EINTR) }
