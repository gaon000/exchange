//go:build !darwin || !cgo

package main

import (
	"errors"
	"os"
)

func fullSync(_ *os.File) error  { return errors.New("storage probe requires macOS and CGO_ENABLED=1") }
func tryLock(_ *os.File) error   { return errors.New("storage probe requires macOS and CGO_ENABLED=1") }
func lockContended(_ error) bool { return false }
