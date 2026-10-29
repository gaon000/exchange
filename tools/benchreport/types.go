package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gaon000/exchange/internal/bench"
	"github.com/gaon000/exchange/internal/buildinfo"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
	"github.com/gaon000/exchange/internal/wal"
)

type settings struct {
	Workload  bench.Spec       `json:"workload"`
	Execution bench.RunOptions `json:"execution"`
}
type criteria struct {
	Version                   int           `json:"version"`
	ConfigFile                string        `json:"configFile"`
	WorkloadFile              string        `json:"workloadFile"`
	P99Limit                  time.Duration `json:"p99LimitNanos"`
	Window                    time.Duration `json:"windowNanos"`
	MinimumRepeats            int           `json:"minimumRepeats"`
	MaxOperationalFailureRate float64       `json:"maxOperationalFailureRate"`
	MaxGeneratorFailureRate   float64       `json:"maxGeneratorFailureRate"`
	MaxQueueGrowth            float64       `json:"maxLastWindowQueueMeanGrowth"`
	MinimumMetricsPerWindow   int           `json:"minimumMetricsPerWindow"`
	MinimumSnapshotOverlaps   int           `json:"minimumSnapshotOverlaps"`
	MetricsInterval           time.Duration `json:"metricsIntervalNanos"`
	SnapshotInterval          time.Duration `json:"snapshotIntervalNanos"`
	RateResolution            int           `json:"rateResolution"`
	hash, configHash          string
	inputHash                 map[string]string
	expected                  settings
}

func loadCriteria(path string) (criteria, error) {
	var c criteria
	b, err := readSmall(path)
	if err != nil {
		return c, err
	}
	if err := strictjson.Decode(b, &c); err != nil {
		return c, err
	}
	c.hash = digest(b)
	if c.Version != 1 || c.P99Limit <= 0 || c.Window <= 0 || c.MinimumRepeats < 3 || c.MinimumRepeats > 100 || c.MaxOperationalFailureRate != 0 || c.MaxGeneratorFailureRate != 0 || c.MaxQueueGrowth < 0 || c.MinimumMetricsPerWindow < 1 || c.MinimumSnapshotOverlaps < 1 || c.MetricsInterval <= 0 || c.SnapshotInterval <= 0 || c.RateResolution < 1 || !filepath.IsLocal(c.ConfigFile) || !filepath.IsLocal(c.WorkloadFile) {
		return c, fmt.Errorf("invalid v1 criteria; zero allowed failure rates required")
	}
	configBytes, err := readSmall(filepath.Join(filepath.Dir(path), c.ConfigFile))
	if err != nil {
		return c, err
	}
	config, err := wal.DecodeConfig(configBytes)
	if err != nil {
		return c, err
	}
	g, err := wal.NewGenesis("11111111-1111-4111-8111-111111111111", config)
	if err != nil {
		return c, err
	}
	c.configHash = g.ConfigHash
	work, err := readSmall(filepath.Join(filepath.Dir(path), c.WorkloadFile))
	if err != nil {
		return c, err
	}
	if err := strictjson.Decode(work, &c.expected); err != nil {
		return c, err
	}
	if err := c.expected.Workload.Validate(); err != nil {
		return c, err
	}
	if err := c.expected.Execution.Validate(); err != nil {
		return c, err
	}
	if c.expected.Workload.Duration%c.Window != 0 || c.expected.Workload.Duration/c.Window < 2 || c.expected.Workload.Duration/c.Window > 1000 {
		return c, fmt.Errorf("criteria require 2 to 1000 equal windows")
	}
	c.inputHash = map[string]string{c.ConfigFile: digest(configBytes), c.WorkloadFile: digest(work)}
	return c, nil
}

type serverRecord struct {
	Event      string          `json:"event"`
	RunID      string          `json:"runId"`
	ConfigHash string          `json:"configHash"`
	Build      buildinfo.Info  `json:"build"`
	Runtime    json.RawMessage `json:"runtime"`
	HTTP       json.RawMessage `json:"http"`
	Metrics    struct {
		Enabled         bool   `json:"enabled"`
		Instrumentation bool   `json:"instrumentation"`
		Interval        string `json:"interval"`
	} `json:"metrics"`
	Experiments struct {
		SnapshotInterval string `json:"snapshotInterval"`
		SnapshotLog      string `json:"snapshotLog"`
		ProfileDir       string `json:"profileDir"`
		MemProfileRate   int    `json:"memProfileRate"`
	} `json:"experiments"`
}
type manifest struct {
	Version       int            `json:"version"`
	PlanDigest    string         `json:"planDigest"`
	Settings      settings       `json:"settings"`
	InitialConfig d.Config       `json:"initialConfig"`
	Server        serverRecord   `json:"server"`
	Generator     buildinfo.Info `json:"generator"`
}
type environmentRecord struct {
	LogicalCPUs        int               `json:"logicalCPUs"`
	GOMAXPROCS         int               `json:"runnerGOMAXPROCS"`
	RuntimeEnvironment map[string]string `json:"runtimeEnvironment"`
	Hardware           map[string]struct {
		Value string `json:"value"`
		Error string `json:"error,omitempty"`
	} `json:"hardware"`
	Storage struct {
		Type       string `json:"type"`
		BlockBytes uint64 `json:"blockBytes,string"`
		Local      bool   `json:"local"`
	} `json:"storage"`
}
type windowReport struct {
	bench.WindowSummary
	MetricSamples int     `json:"metricSamples"`
	QueueMean     float64 `json:"queueMean"`
	QueueMax      int     `json:"queueMax"`
}
type caseReport struct {
	Bundle               string         `json:"bundle"`
	RunID                string         `json:"runId"`
	Scenario             string         `json:"scenario"`
	Rate                 int            `json:"requestsPerSecond"`
	Series               string         `json:"series"`
	ServerBuild          buildinfo.Info `json:"serverBuild"`
	ServerSHA            string         `json:"serverSHA256"`
	GeneratorSHA         string         `json:"generatorSHA256"`
	Sealed               bool           `json:"sealed"`
	Recomputed           bool           `json:"summaryRecomputed"`
	Pass                 bool           `json:"pass"`
	Reasons              []string       `json:"reasons"`
	Summary              bench.Summary  `json:"summary"`
	Windows              []windowReport `json:"windows"`
	SnapshotOverlaps     int            `json:"snapshotOverlaps"`
	SnapshotCaptureNanos uint64         `json:"snapshotCaptureTotalNanos,string"`
	SnapshotPublishNanos uint64         `json:"snapshotPublishTotalNanos,string"`
	WALBytes             int64          `json:"walBytes,string"`
	PeakRSSBytes         uint64         `json:"peakRssBytes,string"`
	ServerCPUSeconds     float64        `json:"serverCpuSeconds"`
}
type groupReport struct {
	Series                string        `json:"series"`
	Scenario              string        `json:"scenario"`
	Rate                  int           `json:"requestsPerSecond"`
	Runs                  int           `json:"runs"`
	Passed                int           `json:"passed"`
	Qualified             bool          `json:"qualified"`
	MinNewOrdersPerSecond float64       `json:"minNewOrdersPerSecond"`
	MaxNewOrdersPerSecond float64       `json:"maxNewOrdersPerSecond"`
	WorstRequestP99       time.Duration `json:"worstRequestP99Nanos"`
	WorstScheduledP99     time.Duration `json:"worstScheduledP99Nanos"`
}
type report struct {
	Version     int               `json:"version"`
	CreatedUTC  time.Time         `json:"createdUTC"`
	Build       buildinfo.Info    `json:"build"`
	CriteriaSHA string            `json:"criteriaSHA256"`
	InputSHA    map[string]string `json:"inputSHA256"`
	Cases       []caseReport      `json:"cases"`
	Groups      []groupReport     `json:"groups"`
}
