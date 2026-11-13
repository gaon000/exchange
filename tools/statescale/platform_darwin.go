//go:build darwin && cgo

package main

import "syscall"

// Darwin's current getrusage(2) reports ru_maxrss in bytes, unlike Linux.
func peakRSS() (int64, error) {
	var r syscall.Rusage
	err := syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return r.Maxrss, err
}
