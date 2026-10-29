package telemetry

import (
	"runtime/metrics"
	"strconv"
)

// PauseBucket contains Go's cumulative GC pause counts. UpperSeconds is a
// decimal string, including +Inf, rather than an invalid JSON floating value.
type PauseBucket struct {
	UpperSeconds string `json:"upperSeconds"`
	Count        uint64 `json:"count,string"`
}

// Process describes this process only; Go-managed memory is not resident memory.
// PeakRSSBytes is the OS lifetime high-water mark, not current RSS. Runtime
// metrics and OS accounting are sampled separately, without forcing a GC.
type Process struct {
	OSAvailable        bool          `json:"osAvailable"`
	OSError            string        `json:"osError,omitempty"`
	UserCPUNanos       uint64        `json:"userCpuNanos,string"`
	SystemCPUNanos     uint64        `json:"systemCpuNanos,string"`
	PeakRSSBytes       uint64        `json:"peakRssBytes,string"`
	GoHeapObjectsBytes uint64        `json:"goHeapObjectsBytes,string"`
	GoLiveHeapBytes    uint64        `json:"goLiveHeapBytes,string"`
	GoTotalMemoryBytes uint64        `json:"goTotalMemoryBytes,string"`
	GoGCCycles         uint64        `json:"goGcCycles,string"`
	GoGoroutines       uint64        `json:"goGoroutines,string"`
	GoGCCPUSeconds     float64       `json:"goGcCpuSeconds"`
	GCPauses           []PauseBucket `json:"gcPauses"`
	RuntimeUnavailable []string      `json:"runtimeUnavailable"`
}

// ReadProcess samples the Go runtime and OS without stopping the business owner
// or reading engine state. The pinned Go metric names are checked by type.
func ReadProcess() Process {
	p := readOSProcess()
	p.GCPauses = []PauseBucket{}
	p.RuntimeUnavailable = []string{}
	names := []string{"/memory/classes/heap/objects:bytes", "/gc/heap/live:bytes", "/memory/classes/total:bytes", "/gc/cycles/total:gc-cycles", "/sched/goroutines:goroutines", "/cpu/classes/gc/total:cpu-seconds", "/sched/pauses/total/gc:seconds"}
	samples := make([]metrics.Sample, len(names))
	for i, name := range names {
		samples[i].Name = name
	}
	metrics.Read(samples)
	values := []*uint64{&p.GoHeapObjectsBytes, &p.GoLiveHeapBytes, &p.GoTotalMemoryBytes, &p.GoGCCycles, &p.GoGoroutines}
	for i, dst := range values {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			*dst = samples[i].Value.Uint64()
		} else {
			p.RuntimeUnavailable = append(p.RuntimeUnavailable, names[i])
		}
	}
	if samples[5].Value.Kind() == metrics.KindFloat64 {
		p.GoGCCPUSeconds = samples[5].Value.Float64()
	} else {
		p.RuntimeUnavailable = append(p.RuntimeUnavailable, names[5])
	}
	if samples[6].Value.Kind() == metrics.KindFloat64Histogram {
		h := samples[6].Value.Float64Histogram()
		for i, count := range h.Counts {
			if count != 0 {
				p.GCPauses = append(p.GCPauses, PauseBucket{strconv.FormatFloat(h.Buckets[i+1], 'g', -1, 64), count})
			}
		}
	} else {
		p.RuntimeUnavailable = append(p.RuntimeUnavailable, names[6])
	}
	return p
}
