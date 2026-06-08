// Package ui renders the benchmark results as a self-contained static HTML page.
// The data is inlined into the page (no external assets, no CDN, no fetch), so
// the same file works opened from disk and deployed to GitHub Pages.
package ui

import (
	_ "embed"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/tonistiigi/compression-bench/result"
)

//go:embed index.html
var indexHTML string

//go:embed chart.umd.js
var chartJS []byte

// dataToken is replaced in index.html with the JSON dataset.
const dataToken = "__DATA__"

type dataset struct {
	Generated string             `json:"generated"`
	Runs      int                `json:"runs"`
	Envs      map[string]envInfo `json:"envs"`
	Rows      []row              `json:"rows"`
}

type envInfo struct {
	Arch       string `json:"arch"`
	OS         string `json:"os"`
	CPUModel   string `json:"cpuModel"`
	NumCPU     int    `json:"numCPU"`
	GoVersion  string `json:"goVersion"`
	CGOEnabled bool   `json:"cgoEnabled"`
}

type row struct {
	Env            string  `json:"env"`
	Image          string  `json:"image"`
	NumLayers      int     `json:"numLayers"`
	Method         string  `json:"method"`
	Level          string  `json:"level"`
	BufferSize     int     `json:"bufferSize"`
	JobConcurrency int     `json:"jc"`
	Op             string  `json:"op"`
	MBps           float64 `json:"mbps"`
	Ratio          float64 `json:"ratio"`
	BytesAllocated int64   `json:"bytesAllocated"`
	Allocs         int64   `json:"allocs"`
	StddevPct      float64 `json:"stddevPct"`
}

// Build returns the self-contained HTML page with the dataset inlined.
func Build(runs []*result.Run, generated time.Time) ([]byte, error) {
	ds := dataset{
		Generated: generated.UTC().Format(time.RFC3339),
		Runs:      len(runs),
		Envs:      map[string]envInfo{},
	}
	for _, run := range runs {
		k := run.Env.Key()
		if _, ok := ds.Envs[k]; !ok {
			ds.Envs[k] = envInfo{
				Arch: run.Env.Arch, OS: run.Env.OS, CPUModel: run.Env.CPUModel,
				NumCPU: run.Env.NumCPU, GoVersion: run.Env.GoVersion, CGOEnabled: run.Env.CGOEnabled,
			}
		}
		for i := range run.Rows {
			r := &run.Rows[i]
			stddev := 0.0
			if r.DurMedianNS > 0 {
				stddev = float64(r.DurStddevNS) / float64(r.DurMedianNS) * 100
			}
			ds.Rows = append(ds.Rows, row{
				Env: k, Image: r.Image, NumLayers: r.NumLayers,
				Method: r.Method, Level: r.Level, BufferSize: r.BufferSize,
				JobConcurrency: r.JobConcurrency, Op: r.Op,
				MBps: r.ThroughputMBps, Ratio: r.Ratio,
				BytesAllocated: r.BytesAllocated, Allocs: r.Allocs, StddevPct: stddev,
			})
		}
	}
	data, err := json.Marshal(ds) // Go escapes <,>,& so it is safe inside <script>
	if err != nil {
		return nil, errors.WithStack(err)
	}
	if !strings.Contains(indexHTML, dataToken) {
		return nil, errors.New("ui template missing data token")
	}
	return []byte(strings.Replace(indexHTML, dataToken, string(data), 1)), nil
}

// WriteSite writes the report to dir: index.html (data inlined) plus the vendored
// chart.umd.js sibling. Both load from file:// and from GitHub Pages.
func WriteSite(dir string, runs []*result.Run, generated time.Time) error {
	html, err := Build(runs, generated)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errors.WithStack(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.WithStack(err)
	}
	defer root.Close()
	if err := root.WriteFile("chart.umd.js", chartJS, 0o644); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(root.WriteFile("index.html", html, 0o644))
}
