package main

import (
	"github.com/gaon000/exchange/internal/buildinfo"
	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"runtime"
)

type initializedReport struct {
	Event       string `json:"event"`
	RunID       string `json:"runId"`
	DataDir     string `json:"dataDir"`
	ConfigHash  string `json:"configHash"`
	Accounts    int    `json:"accounts"`
	MaxCommands d.Int  `json:"maxCommands"`
}
type readyReport struct {
	Event       string             `json:"event"`
	RunID       string             `json:"runId"`
	ConfigHash  string             `json:"configHash"`
	Build       buildinfo.Info     `json:"build"`
	DataDir     string             `json:"dataDir"`
	Listen      string             `json:"listen"`
	Runtime     runtimeSettings    `json:"runtime"`
	HTTP        httpSettings       `json:"http"`
	Recovery    recoverySettings   `json:"recovery"`
	Metrics     metricsSettings    `json:"metrics"`
	Experiments experimentSettings `json:"experiments"`
}
type experimentSettings struct {
	SnapshotInterval string `json:"snapshotInterval"`
	SnapshotLog      string `json:"snapshotLog"`
	SnapshotLimit    int    `json:"snapshotLimit"`
	ProfileDir       string `json:"profileDir"`
	MemProfileRate   int    `json:"memProfileRate"`
}
type metricsSettings struct {
	Enabled         bool   `json:"enabled"`
	Instrumentation bool   `json:"instrumentation"`
	File            string `json:"file"`
	Interval        string `json:"interval"`
}
type runtimeSettings struct {
	CommandQueue   int    `json:"commandQueue"`
	QueryQueue     int    `json:"queryQueue"`
	RequestLimit   int    `json:"requestLimit"`
	PendingWaiters int    `json:"pendingWaiters"`
	BatchCommands  int    `json:"batchCommands"`
	BatchBytes     int    `json:"batchBytes"`
	BatchDelay     string `json:"batchDelay"`
}
type httpSettings struct {
	RequestLimit      int    `json:"requestLimit"`
	ReadHeaderTimeout string `json:"readHeaderTimeout"`
	ReadBodyTimeout   string `json:"readBodyTimeout"`
	ResultTimeout     string `json:"resultTimeout"`
	WriteTimeout      string `json:"writeTimeout"`
	ShutdownTimeout   string `json:"shutdownTimeout"`
}
type recoverySettings struct {
	SnapshotFile     string            `json:"snapshotFile"`
	SnapshotSeq      d.Int             `json:"snapshotSeq"`
	ReplayedCommands d.Int             `json:"replayedCommands"`
	Skipped          []skippedSnapshot `json:"skippedSnapshots"`
}
type skippedSnapshot struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func readyReportFor(r *rt.Runtime, dir, address string, o serveOptions) readyReport {
	recovery := r.Recovery()
	skipped := make([]skippedSnapshot, 0, len(recovery.Skipped))
	for _, s := range recovery.Skipped {
		skipped = append(skipped, skippedSnapshot{s.Name, s.Reason})
	}
	return readyReport{Event: "ready", RunID: r.RunID(), ConfigHash: r.ConfigHash(), Build: buildinfo.Current(), DataDir: dir, Listen: address,
		Runtime:  runtimeSettings{o.runtime.CommandQueue, o.runtime.QueryQueue, o.runtime.RequestLimit, o.runtime.PendingWaiters, o.runtime.BatchCommands, o.runtime.BatchBytes, o.runtime.BatchDelay.String()},
		HTTP:     httpSettings{o.http.RequestLimit, o.http.ReadHeaderTimeout.String(), o.http.ReadBodyTimeout.String(), o.http.ResultTimeout.String(), o.http.WriteTimeout.String(), o.httpShutdownTimeout.String()},
		Recovery: recoverySettings{recovery.SnapshotFile, recovery.SnapshotSeq, recovery.ReplayedCommands, skipped}, Metrics: metricsSettings{o.metricsFile != "", true, o.metricsFile, o.metricsInterval.String()},
		Experiments: experimentSettings{o.snapshotInterval.String(), o.snapshotLog, 1000, o.profileDir, runtime.MemProfileRate}}
}
