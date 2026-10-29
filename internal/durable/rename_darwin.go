//go:build darwin && cgo

package durable

/*
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/stdio.h>
static int exchange_rename_exclusive(const char *from, const char *to) {
    return renamex_np(from, to, RENAME_EXCL) == -1 ? errno : 0;
}
*/
import "C"

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// RenameExclusive publishes a name atomically without replacing a destination.
// Unsupported filesystems fail; there is no check-then-rename fallback.
func RenameExclusive(from, to string) error {
	if strings.IndexByte(from, 0) >= 0 || strings.IndexByte(to, 0) >= 0 {
		return &os.LinkError{Op: "rename_exclusive", Old: from, New: to, Err: syscall.EINVAL}
	}
	a, b := C.CString(from), C.CString(to)
	defer C.free(unsafe.Pointer(a))
	defer C.free(unsafe.Pointer(b))
	if code := C.exchange_rename_exclusive(a, b); code != 0 {
		return &os.LinkError{Op: "rename_exclusive", Old: from, New: to, Err: syscall.Errno(code)}
	}
	return nil
}
