//go:build !darwin || !cgo

package durable

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("durable storage requires macOS APFS and CGO_ENABLED=1")

// Sync rejects an unverified durability platform.
func Sync(_ *os.File) error { return errUnsupported }

// Lock rejects an unsupported lock implementation.
func Lock(_ *os.File) error { return errUnsupported }

// Contended is false on platforms without a supported lock implementation.
func Contended(_ error) bool { return false }

// CheckFilesystem rejects unsupported platforms before creating any files.
func CheckFilesystem(_ string) error { return errUnsupported }
