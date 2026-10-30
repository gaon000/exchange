package main

import (
	"os"

	"github.com/gaon000/exchange/internal/durable"
)

func checkFilesystem(path string) error { return durable.CheckFilesystem(path) }
func fullSync(f *os.File) error         { return durable.Sync(f) }
func tryLock(f *os.File) error          { return durable.Lock(f) }
func lockContended(err error) bool      { return durable.Contended(err) }
