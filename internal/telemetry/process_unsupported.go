//go:build !darwin && !linux

package telemetry

func readOSProcess() Process {
	return Process{OSError: "OS process accounting unsupported on this platform"}
}
