//go:build !darwin || !cgo

package main

import "errors"

func vmMemory() (rssVM, error) { return rssVM{}, errors.New("RSS VM diagnostics require macOS/cgo") }
