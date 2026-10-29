//go:build darwin && cgo

package main

/*
#include <errno.h>
#include <fcntl.h>

static int exchange_full_sync(int fd) {
    int result;
    do { result = fcntl(fd, F_FULLFSYNC); } while (result == -1 && errno == EINTR);
    return result == -1 ? errno : 0;
}
*/
import "C"

import (
	"errors"
	"os"
	"syscall"
)

// fullSync intentionally has no fallback to a weaker synchronization call.
func fullSync(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var syncErr error
	err = raw.Control(func(fd uintptr) {
		if code := C.exchange_full_sync(C.int(fd)); code != 0 {
			syncErr = syscall.Errno(code)
		}
	})
	return errors.Join(err, syncErr)
}

func tryLock(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func lockContended(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK)
}
