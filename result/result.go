// Package result defines the on-disk benchmark result schema and environment
// capture. One run produces one self-contained Run (env + rows), written as
// results/<runID>.json, so local and CI results can be merged by dropping files
// into one directory.
package result

import (
	"math"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
)

// SchemaVersion is bumped on incompatible changes to Run/Row so the report can
// reject or migrate old files when merging local + CI history.
// v2: rows are per-image (numLayers; aggregated sizes), replacing per-layer rows.
const SchemaVersion = 2

// Run is a single self-contained benchmark run.
type Run struct {
	SchemaVersion int       `json:"schemaVersion"`
	RunID         string    `json:"runID"`
	Timestamp     time.Time `json:"timestamp"`
	Env           Env       `json:"env"`
	Skips         []Skip    `json:"skips,omitempty"`
	Rows          []Row     `json:"rows"`
}

// Skip records a configured method that was not activated this run.
type Skip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Env describes where a run executed. Throughput is only comparable within the
// same Env; the report groups by it.
type Env struct {
	Arch       string `json:"arch"`
	OS         string `json:"os"`
	CPUModel   string `json:"cpuModel"`
	NumCPU     int    `json:"numCPU"`
	GoVersion  string `json:"goVersion"`
	CGOEnabled bool   `json:"cgoEnabled"`
}

// Key returns a stable identity for grouping rows by environment.
func (e Env) Key() string {
	cgo := "nocgo"
	if e.CGOEnabled {
		cgo = "cgo"
	}
	return strings.Join([]string{e.OS, e.Arch, e.CPUModel, cgo}, "/")
}

// Row is one measured (image, method, level, buffer, jobConcurrency, op). The
// unit is the whole image: all NumLayers layers processed with up to
// jobConcurrency in flight; sizes and ratio are aggregated over the layers.
type Row struct {
	Image     string `json:"image"`
	NumLayers int    `json:"numLayers"`

	Method         string `json:"method"`
	LibVersion     string `json:"libVersion"`
	Level          string `json:"level"`    // normalized
	LevelRaw       string `json:"levelRaw"` // native
	BufferSize     int    `json:"bufferSize"`
	JobConcurrency int    `json:"jobConcurrency"`
	Op             string `json:"op"` // compress | decompress

	Iterations     int     `json:"iterations"`
	DurMinNS       int64   `json:"durMinNs"`
	DurMedianNS    int64   `json:"durMedianNs"`
	DurStddevNS    int64   `json:"durStddevNs"`
	ThroughputMBps float64 `json:"throughputMBps"`

	BytesIn  int64   `json:"bytesIn"`
	BytesOut int64   `json:"bytesOut"`
	Ratio    float64 `json:"ratio,omitempty"` // bytesIn/bytesOut, compress only

	// Go-heap allocation for one op, measured in a clean single-threaded pass.
	// Only meaningful for pure-Go methods (GoMemory); zero/omitted for cgo and
	// external methods whose memory is off the Go heap.
	BytesAllocated int64 `json:"bytesAllocated,omitempty"`
	Allocs         int64 `json:"allocs,omitempty"`

	RoundtripOK  bool   `json:"roundtripOK"`
	OutputSHA256 string `json:"outputSha256,omitempty"` // compress only; determinism key
}

// CaptureEnv records the current environment.
func CaptureEnv() Env {
	return Env{
		Arch:       runtime.GOARCH,
		OS:         runtime.GOOS,
		CPUModel:   cpuModel(),
		NumCPU:     runtime.NumCPU(),
		GoVersion:  runtime.Version(),
		CGOEnabled: CGOEnabled,
	}
}

// cpuModel best-effort reads a human CPU name from /proc/cpuinfo; falls back to
// the arch. Never errors.
func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return runtime.GOARCH
	}
	for _, key := range []string{"model name", "Model", "Hardware"} {
		for _, line := range strings.Split(string(data), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if ok && strings.TrimSpace(k) == key {
				if s := strings.TrimSpace(v); s != "" {
					return s
				}
			}
		}
	}
	return runtime.GOARCH
}

// Stats summarizes a set of timed iterations: min, median and population stddev.
func Stats(durs []time.Duration) (min, median time.Duration, stddev time.Duration) {
	if len(durs) == 0 {
		return 0, 0, 0
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	min = sorted[0]
	n := len(sorted)
	if n%2 == 1 {
		median = sorted[n/2]
	} else {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	var mean float64
	for _, d := range sorted {
		mean += float64(d)
	}
	mean /= float64(n)
	var variance float64
	for _, d := range sorted {
		diff := float64(d) - mean
		variance += diff * diff
	}
	variance /= float64(n)
	stddev = time.Duration(math.Sqrt(variance))
	return min, median, stddev
}

// ThroughputMBps computes MB/s (decimal MB) for rawSize bytes processed in dur.
func ThroughputMBps(rawSize int64, dur time.Duration) float64 {
	if dur <= 0 {
		return 0
	}
	return (float64(rawSize) / 1e6) / dur.Seconds()
}

// Write marshals a run to path as indented JSON.
func (r *Run) Write(path string) error {
	return errors.WithStack(writeJSON(path, r))
}
