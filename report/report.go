// Package report renders aggregated benchmark results: throughput/ratio grouped
// by environment (throughput is only comparable within one machine) and the
// determinism matrix computed across all runs.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/pkg/errors"
	"github.com/tonistiigi/compression-bench/result"
)

// Render writes a text report for the given runs to w.
func Render(w io.Writer, runs []*result.Run) error {
	if len(runs) == 0 {
		return errors.New("no results to report")
	}
	fmt.Fprintf(w, "# compression-bench report\n%d run(s)\n\n", len(runs))

	if err := renderThroughput(w, runs); err != nil {
		return err
	}
	if err := renderDeterminism(w, runs); err != nil {
		return err
	}
	return renderCrossImpl(w, runs)
}

// envGroup collects rows sharing one environment.
type envGroup struct {
	env  result.Env
	rows []result.Row
}

func groupByEnv(runs []*result.Run) []envGroup {
	byKey := map[string]*envGroup{}
	var order []string
	for _, run := range runs {
		k := run.Env.Key()
		g, ok := byKey[k]
		if !ok {
			g = &envGroup{env: run.Env}
			byKey[k] = g
			order = append(order, k)
		}
		g.rows = append(g.rows, run.Rows...)
	}
	sort.Strings(order)
	out := make([]envGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

func renderThroughput(w io.Writer, runs []*result.Run) error {
	for _, g := range groupByEnv(runs) {
		fmt.Fprintf(w, "## env: %s  (%d cores, %s)\n", g.env.Key(), g.env.NumCPU, g.env.GoVersion)

		rows := append([]result.Row(nil), g.rows...)
		sort.Slice(rows, func(i, j int) bool { return rowLess(rows[i], rows[j]) })

		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "image\tlayers\tmethod\tlevel\top\tbuf\tjc\tMB/s\tratio\tstddev%\tmem/op\tallocs/op")
		for _, r := range rows {
			ratio := "-"
			if r.Op == "compress" {
				ratio = fmt.Sprintf("%.3f", r.Ratio)
			}
			mem, allocs := "-", "-"
			if r.BytesAllocated > 0 { // pure-Go methods only
				mem = humanBytes(int(r.BytesAllocated))
				allocs = fmt.Sprintf("%d", r.Allocs)
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%d\t%.1f\t%s\t%.1f\t%s\t%s\n",
				r.Image, r.NumLayers, r.Method, r.Level, r.Op, humanBytes(r.BufferSize), r.JobConcurrency,
				r.ThroughputMBps, ratio, stddevPct(r), mem, allocs)
		}
		if err := tw.Flush(); err != nil {
			return errors.WithStack(err)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func renderDeterminism(w io.Writer, runs []*result.Run) error {
	fmt.Fprintln(w, "## determinism (does a method reproduce identical bytes?)")
	fmt.Fprintln(w, "replay = re-run on the same machine; across-arch = different CPU. Not asserted across library versions.")
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "method\tlevel\treplay (same env)\tacross-arch\tsamples")
	for _, d := range result.Determinism(runs) {
		replay := boolMark(d.DeterministicReplay)
		if !d.ReplayComparable {
			replay = "n/a (1 run)"
		}
		arch := boolMark(d.DeterministicAcrossArch)
		if !d.ArchComparable {
			arch = "n/a (1 arch)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", d.Method, d.Level, replay, arch, d.Samples)
	}
	return errors.WithStack(tw.Flush())
}

// renderCrossImpl reports where different implementations produce byte-identical
// compressed output for the same image+level — e.g. zstd-cgo and the zstd CLI
// (both reference libzstd) often match, while pure-Go encoders usually differ.
func renderCrossImpl(w io.Writer, runs []*result.Run) error {
	fmt.Fprintln(w, "\n## identical compressed output across implementations")
	fmt.Fprintln(w, "methods whose whole-image output is byte-identical for the same image+level")

	// (env, image, level) -> hash -> set of methods
	type key struct{ env, image, level string }
	groups := map[key]map[string]map[string]struct{}{}
	var order []key
	for _, run := range runs {
		env := run.Env.Key()
		for i := range run.Rows {
			r := &run.Rows[i]
			if r.Op != "compress" || r.OutputSHA256 == "" {
				continue
			}
			k := key{env, r.Image, r.Level}
			if groups[k] == nil {
				groups[k] = map[string]map[string]struct{}{}
				order = append(order, k)
			}
			if groups[k][r.OutputSHA256] == nil {
				groups[k][r.OutputSHA256] = map[string]struct{}{}
			}
			groups[k][r.OutputSHA256][r.Method] = struct{}{}
		}
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.env != b.env {
			return a.env < b.env
		}
		if a.image != b.image {
			return a.image < b.image
		}
		return a.level < b.level
	})

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	any := false
	for _, k := range order {
		var matches []string
		for _, methods := range groups[k] {
			if len(methods) < 2 {
				continue
			}
			names := make([]string, 0, len(methods))
			for m := range methods {
				names = append(names, m)
			}
			sort.Strings(names)
			matches = append(matches, strings.Join(names, " = "))
		}
		if len(matches) == 0 {
			continue
		}
		any = true
		sort.Strings(matches)
		fmt.Fprintf(tw, "%s\t%s/%s\t%s\n", k.env, k.image, k.level, strings.Join(matches, ", "))
	}
	if !any {
		fmt.Fprintln(tw, "(none — every implementation produced distinct output)")
	}
	return errors.WithStack(tw.Flush())
}

func rowLess(a, b result.Row) bool {
	if a.Image != b.Image {
		return a.Image < b.Image
	}
	if a.Method != b.Method {
		return a.Method < b.Method
	}
	if a.Level != b.Level {
		return a.Level < b.Level
	}
	if a.Op != b.Op {
		return a.Op < b.Op
	}
	if a.BufferSize != b.BufferSize {
		return a.BufferSize < b.BufferSize
	}
	return a.JobConcurrency < b.JobConcurrency
}

func stddevPct(r result.Row) float64 {
	if r.DurMedianNS == 0 {
		return 0
	}
	return float64(r.DurStddevNS) / float64(r.DurMedianNS) * 100
}

func boolMark(b bool) string {
	if b {
		return "yes"
	}
	return "NO"
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		if n%(1<<20) == 0 {
			return fmt.Sprintf("%dMiB", n/(1<<20))
		}
		return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20))
	case n >= 1<<10:
		if n%(1<<10) == 0 {
			return fmt.Sprintf("%dKiB", n/(1<<10))
		}
		return fmt.Sprintf("%.1fKiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
