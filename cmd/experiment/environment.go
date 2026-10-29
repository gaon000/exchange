package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/gaon000/exchange/internal/buildinfo"
)

type observation struct {
	Value string `json:"value"`
	Error string `json:"error,omitempty"`
}
type environment struct {
	CapturedUTC time.Time              `json:"capturedUTC"`
	Build       buildinfo.Info         `json:"runnerBuild"`
	LogicalCPUs int                    `json:"logicalCPUs"`
	GOMAXPROCS  int                    `json:"runnerGOMAXPROCS"`
	RuntimeEnv  map[string]string      `json:"runtimeEnvironment"`
	Hardware    map[string]observation `json:"hardware"`
	Storage     storageInfo            `json:"storage"`
}

func captureEnvironment(ctx context.Context, dir string) (environment, error) {
	e := environment{CapturedUTC: time.Now().UTC(), Build: buildinfo.Current(), LogicalCPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), RuntimeEnv: map[string]string{}, Hardware: map[string]observation{}}
	for _, key := range []string{"GOMAXPROCS", "GOGC", "GOMEMLIMIT", "GODEBUG"} {
		if value, ok := os.LookupEnv(key); ok {
			e.RuntimeEnv[key] = value
		}
	}
	var err error
	e.Storage, err = readStorage(dir)
	if err != nil {
		return e, err
	}
	if runtime.GOOS != "darwin" {
		return e, fmt.Errorf("experiment baseline currently requires macOS/APFS")
	}
	for _, item := range []struct {
		key, cmd string
		args     []string
	}{
		{"cpu", "/usr/sbin/sysctl", []string{"-n", "machdep.cpu.brand_string"}},
		{"ramBytes", "/usr/sbin/sysctl", []string{"-n", "hw.memsize"}},
		{"model", "/usr/sbin/sysctl", []string{"-n", "hw.model"}},
		{"os", "/usr/bin/sw_vers", nil},
	} {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		b, err := exec.CommandContext(probe, item.cmd, item.args...).Output()
		cancel()
		o := observation{Value: strings.TrimSpace(string(b))}
		if err != nil {
			o.Error = err.Error()
		}
		e.Hardware[item.key] = o
	}
	return e, ctx.Err()
}
