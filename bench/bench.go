// Package bench executes the benchmark loop. The unit of measurement is one
// IMAGE: for every (image, method, level, bufferSize, jobConcurrency, op) it
// processes ALL of the image's layers (each compressed/decompressed
// independently), with up to jobConcurrency layers in flight at once, and times
// the whole image. It records aggregate ratio and throughput, per-image
// determinism (a hash of the per-layer hashes), per-image memory churn, and a
// hard round-trip correctness check.
package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/tonistiigi/compression-bench/config"
	"github.com/tonistiigi/compression-bench/corpus"
	"github.com/tonistiigi/compression-bench/method"
	"github.com/tonistiigi/compression-bench/result"
)

// Runner executes a benchmark run.
type Runner struct {
	Config   *config.Config
	Methods  []config.ResolvedMethod
	Skips    []config.Skip
	Force    bool                          // force re-prep of corpus
	Progress func(format string, a ...any) // optional permanent progress lines
	Status   func(msg string)              // optional transient (in-place) status, e.g. iteration counter
	Now      func() time.Time              // injectable clock; defaults to time.Now
}

func (r *Runner) progress(format string, a ...any) {
	if r.Progress != nil {
		r.Progress(format, a...)
	}
}

func (r *Runner) status(format string, a ...any) {
	if r.Status != nil {
		r.Status(fmt.Sprintf(format, a...))
	}
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Execute auto-preps any missing corpus images, runs the full matrix, and
// returns a self-contained result. runID names the output file.
func (r *Runner) Execute(runID string) (*result.Run, error) {
	tmp, err := os.MkdirTemp("", "compbench-"+runID+"-")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer os.RemoveAll(tmp)
	// All artifact I/O is confined to the temp dir via this root.
	artRoot, err := os.OpenRoot(tmp)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer artRoot.Close()

	run := &result.Run{
		SchemaVersion: result.SchemaVersion,
		RunID:         runID,
		Timestamp:     r.now(),
		Env:           result.CaptureEnv(),
	}
	for _, s := range r.Skips {
		run.Skips = append(run.Skips, result.Skip{Name: s.Name, Reason: s.Reason})
	}

	for _, ref := range r.Config.Images {
		r.progress("prep %s", ref)
		img, err := corpus.Ensure(r.Config.Corpus, ref, r.Force)
		if err != nil {
			return nil, errors.Wrapf(err, "corpus for %s", ref)
		}
		rows, err := r.benchImage(artRoot, img)
		img.Close()
		if err != nil {
			return nil, err
		}
		run.Rows = append(run.Rows, rows...)
	}
	return run, nil
}

// benchImage benchmarks one image across all methods/levels. For each
// (method, level) it compresses every layer once into a temp artifact (untimed)
// to obtain compressed sizes, the determinism hash, memory churn and round-trip
// correctness, then times the requested ops over the whole image at each buffer
// size and job concurrency.
func (r *Runner) benchImage(artRoot *os.Root, img *corpus.Image) ([]result.Row, error) {
	n := len(img.Layers)
	if n == 0 {
		return nil, nil
	}
	rawTotal := imageRawSize(img)
	var rows []result.Row

	for _, rm := range r.Methods {
		m := rm.Method
		for _, level := range rm.Levels {
			start := time.Now()
			r.progress("  %-13s %-9s %-7s  %2d layers %9s ...", img.Ref, m.Name(), level, n, mib(rawTotal))

			p, err := r.prepImage(artRoot, m, level, img)
			if err != nil {
				return nil, err
			}

			base := result.Row{
				Image: img.Ref, NumLayers: n,
				Method: m.Name(), LibVersion: m.Version(),
				Level: level.String(), LevelRaw: m.RawLevel(level),
				RoundtripOK: true,
			}

			var group []result.Row
			for _, bufSize := range r.Config.BufferSizes {
				for _, jc := range r.Config.JobConcurrency {
					if jc > n {
						continue // more workers than layers behaves identically to jc=n
					}
					for _, op := range r.Config.Ops {
						row, err := r.measureOp(artRoot, base, m, level, op, img, p, int(bufSize), jc)
						if err != nil {
							return nil, err
						}
						group = append(group, row)
					}
				}
			}
			rows = append(rows, group...)

			cMBps, cRatio := compressSummary(group)
			r.progress("  %-13s %-9s %-7s  %2d layers %9s  done %7s  compress %6.0f MB/s  ratio %.3f",
				img.Ref, m.Name(), level, n, mib(p.totalComp),
				time.Since(start).Round(time.Millisecond), cMBps, cRatio)
		}
	}
	return rows, nil
}

// artifact is the compressed form of one layer for one (method, level). name is
// relative to the artifact root (the run's temp dir).
type artifact struct {
	name   string
	sha256 string
	size   int64
}

// imagePrep holds everything derived from compressing an image's layers once
// (untimed) for a given (method, level): the per-layer artifacts plus aggregate
// sizes, the combined determinism hash, and memory churn for the compress and
// decompress passes (zero for non-GoMemory methods).
type imagePrep struct {
	arts      []artifact
	totalRaw  int64
	totalComp int64
	combHash  string

	compBytes, compAllocs     int64
	decompBytes, decompAllocs int64
}

func (r *Runner) prepImage(artRoot *os.Root, m method.Method, level method.Level, img *corpus.Image) (*imagePrep, error) {
	p := &imagePrep{arts: make([]artifact, len(img.Layers))}

	// Compress pass: build artifacts + aggregate + combined hash. Memory churn of
	// this whole-image pass is the compress memory metric (pure-Go methods only).
	compress := func() error {
		h := sha256.New()
		p.totalRaw, p.totalComp = 0, 0
		for i, layer := range img.Layers {
			a, err := r.compressArtifact(artRoot, img, m, level, layer, i)
			if err != nil {
				return err
			}
			p.arts[i] = a
			p.totalRaw += layer.RawSize
			p.totalComp += a.size
			h.Write([]byte(a.sha256))
		}
		p.combHash = hex.EncodeToString(h.Sum(nil))
		return nil
	}
	if b, a, err := r.measured(m, compress); err != nil {
		return nil, err
	} else {
		p.compBytes, p.compAllocs = b, a
	}

	// Decompress + round-trip pass: every layer must reproduce its diffID. Memory
	// churn of this pass is the decompress metric.
	verify := func() error {
		for i, layer := range img.Layers {
			ok, err := roundtripArtifact(artRoot, m, p.arts[i].name, layer.Digest)
			if err != nil {
				return errors.Wrapf(err, "%s/%s on %s", m.Name(), level, short(layer.Digest))
			}
			if !ok {
				return errors.Errorf("round-trip FAILED: method=%s level=%s layer=%s image=%s",
					m.Name(), level, layer.Digest, img.Ref)
			}
		}
		return nil
	}
	if b, a, err := r.measured(m, verify); err != nil {
		return nil, err
	} else {
		p.decompBytes, p.decompAllocs = b, a
	}

	return p, nil
}

// measured runs fn, returning Go-heap bytes/allocs churned if m is GoMemory
// (clean GC first; fn runs single-threaded here), else (0,0) without the GC.
func (r *Runner) measured(m method.Method, fn func() error) (bytes, allocs int64, err error) {
	if !m.GoMemory() {
		return 0, 0, fn()
	}
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	if err := fn(); err != nil {
		return 0, 0, err
	}
	runtime.ReadMemStats(&m1)
	return int64(m1.TotalAlloc - m0.TotalAlloc), int64(m1.Mallocs - m0.Mallocs), nil
}

func (r *Runner) compressArtifact(artRoot *os.Root, img *corpus.Image, m method.Method, level method.Level, layer corpus.Layer, idx int) (artifact, error) {
	src, err := img.OpenLayer(layer)
	if err != nil {
		return artifact{}, err
	}
	defer src.Close()

	name := fmt.Sprintf("art-%d", idx)
	out, err := artRoot.Create(name)
	if err != nil {
		return artifact{}, errors.WithStack(err)
	}
	defer out.Close()

	h := sha256.New()
	if err := method.Compress(m, io.MultiWriter(out, h), src, level, 1<<20); err != nil {
		return artifact{}, err
	}
	if err := out.Close(); err != nil {
		return artifact{}, errors.WithStack(err)
	}
	fi, err := artRoot.Stat(name)
	if err != nil {
		return artifact{}, errors.WithStack(err)
	}
	return artifact{name: name, sha256: hex.EncodeToString(h.Sum(nil)), size: fi.Size()}, nil
}

// measureOp times processing the whole image for one (op, bufSize, jc): all
// layers streamed through the codec with up to jc in flight, repeated for the
// configured iterations.
func (r *Runner) measureOp(artRoot *os.Root, base result.Row, m method.Method, level method.Level, op string, img *corpus.Image, p *imagePrep, bufSize, jc int) (result.Row, error) {
	var doLayer func(i int) error
	switch op {
	case "compress":
		doLayer = func(i int) error {
			src, err := img.OpenLayer(img.Layers[i])
			if err != nil {
				return err
			}
			defer src.Close()
			return method.Compress(m, io.Discard, src, level, bufSize)
		}
	case "decompress":
		doLayer = func(i int) error {
			src, err := artRoot.Open(p.arts[i].name)
			if err != nil {
				return errors.WithStack(err)
			}
			defer src.Close()
			return method.Decompress(m, io.Discard, src, bufSize)
		}
	default:
		return result.Row{}, errors.Errorf("unknown op %q", op)
	}

	label := fmt.Sprintf("  %-13s %-9s %-7s %-10s buf=%-6s jc=%d (%d layers)",
		img.Ref, m.Name(), level, op, humanBuf(bufSize), jc, len(img.Layers))
	durs, err := r.timed(jc, len(img.Layers), label, doLayer)
	if err != nil {
		return result.Row{}, errors.Wrapf(err, "%s %s op", m.Name(), op)
	}
	min, median, stddev := result.Stats(durs)

	row := base
	row.BufferSize = bufSize
	row.JobConcurrency = jc
	row.Op = op
	row.Iterations = r.Config.Iterations
	row.DurMinNS = min.Nanoseconds()
	row.DurMedianNS = median.Nanoseconds()
	row.DurStddevNS = stddev.Nanoseconds()
	row.ThroughputMBps = result.ThroughputMBps(p.totalRaw, median)

	switch op {
	case "compress":
		row.BytesIn = p.totalRaw
		row.BytesOut = p.totalComp
		if p.totalComp > 0 {
			row.Ratio = float64(p.totalRaw) / float64(p.totalComp)
		}
		row.OutputSHA256 = p.combHash
		row.BytesAllocated = p.compBytes
		row.Allocs = p.compAllocs
	case "decompress":
		row.BytesIn = p.totalComp
		row.BytesOut = p.totalRaw
		row.BytesAllocated = p.decompBytes
		row.Allocs = p.decompAllocs
	}
	return row, nil
}

// timed runs the whole-image pass (n layers, up to jc concurrent) for the
// configured iterations, sleeping the gap between them, and returns one total
// duration per iteration.
func (r *Runner) timed(jc, n int, label string, doLayer func(i int) error) ([]time.Duration, error) {
	durs := make([]time.Duration, 0, r.Config.Iterations)
	for it := 0; it < r.Config.Iterations; it++ {
		r.status("%s  iter %d/%d", label, it+1, r.Config.Iterations)
		if it > 0 {
			time.Sleep(r.Config.Gap.Std())
		}
		start := time.Now()
		if err := runPool(n, jc, doLayer); err != nil {
			return nil, err
		}
		durs = append(durs, time.Since(start))
	}
	return durs, nil
}

// runPool runs fn(0..n-1) with at most jc running concurrently, returning the
// first error.
func runPool(n, jc int, fn func(i int) error) error {
	if jc < 1 {
		jc = 1
	}
	sem := make(chan struct{}, jc)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = fn(i)
		}(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// roundtripArtifact decompresses a layer's artifact and checks the result hashes
// to the layer's diffID (digest of the uncompressed tar), proving lossless round
// trip without re-reading the original.
func roundtripArtifact(artRoot *os.Root, m method.Method, artName, diffID string) (bool, error) {
	src, err := artRoot.Open(artName)
	if err != nil {
		return false, errors.WithStack(err)
	}
	defer src.Close()
	h := sha256.New()
	if err := method.Decompress(m, h, src, 1<<20); err != nil {
		return false, err
	}
	want := strings.TrimPrefix(diffID, "sha256:")
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}

// compressSummary returns the single-job (lowest jobConcurrency) compress
// throughput and ratio from a (method, level) group, for the progress line.
func compressSummary(group []result.Row) (mbps, ratio float64) {
	best := -1
	for i := range group {
		if group[i].Op != "compress" {
			continue
		}
		if best == -1 || group[i].JobConcurrency < group[best].JobConcurrency {
			best = i
		}
	}
	if best == -1 {
		return 0, 0
	}
	return group[best].ThroughputMBps, group[best].Ratio
}

func imageRawSize(img *corpus.Image) int64 {
	var n int64
	for _, l := range img.Layers {
		n += l.RawSize
	}
	return n
}

func mib(n int64) string { return fmt.Sprintf("%.1fMiB", float64(n)/(1<<20)) }

func humanBuf(n int) string {
	switch {
	case n%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", n>>20)
	case n%(1<<10) == 0:
		return fmt.Sprintf("%dKiB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func short(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		return d[:12]
	}
	return d
}
