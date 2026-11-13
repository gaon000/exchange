//go:build !darwin || !cgo

package main

import "errors"

func peakRSS() (int64, error) { return 0, errors.New("state scaling requires native macOS/APFS/cgo") }

func residentRSS() (int64, error) { return 0, errors.New("resident RSS requires native macOS/cgo") }
