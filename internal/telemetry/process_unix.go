//go:build darwin || linux

package telemetry

import (
	"runtime"
	"syscall"
)

func readOSProcess() Process {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return Process{OSError: err.Error()}
	}
	rss := uint64(usage.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	return Process{OSAvailable: true, UserCPUNanos: uint64(usage.Utime.Sec)*1_000_000_000 + uint64(usage.Utime.Usec)*1000, SystemCPUNanos: uint64(usage.Stime.Sec)*1_000_000_000 + uint64(usage.Stime.Usec)*1000, PeakRSSBytes: rss}
}
