//go:build darwin && cgo

// Package durable provides the verified local-APFS persistence boundary.
package durable

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
	"fmt"
	"os"
	"syscall"
)

// Sync requires F_FULLFSYNC and never falls back to a weaker operation.
func Sync(file *os.File) error {
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

// Lock takes an exclusive nonblocking advisory lock until the file is closed.
func Lock(file *os.File) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var lockErr error
	err = raw.Control(func(fd uintptr) {
		for {
			lockErr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
			if !errors.Is(lockErr, syscall.EINTR) {
				break
			}
		}
	})
	return errors.Join(err, lockErr)
}

// Contended identifies a lock already held by another open file description.
func Contended(err error) bool { return errors.Is(err, syscall.EWOULDBLOCK) }

// CheckFilesystem rejects filesystems outside the verified v1 APFS scope.
func CheckFilesystem(path string) error {
	var info syscall.Statfs_t
	if err := syscall.Statfs(path, &info); err != nil {
		return err
	}
	var name []byte
	for _, c := range info.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	if string(name) != "apfs" {
		return fmt.Errorf("unsupported filesystem: %s", name)
	}
	return nil
}
