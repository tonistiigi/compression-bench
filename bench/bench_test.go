package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/compression-bench/config"
	"github.com/tonistiigi/compression-bench/method"
)

// fakeCorpus writes a corpus dir with one image whose layers have the given byte
// sizes; each layer's tar content hashes to the diffID recorded in the manifest,
// so Ensure loads it without any network/registry access.
func fakeCorpus(t *testing.T, ref string, sizes ...int) (corpusDir string) {
	t.Helper()
	corpusDir = t.TempDir()
	imgDir := filepath.Join(corpusDir, ref) // ref chosen with no special chars
	require.NoError(t, os.MkdirAll(filepath.Join(imgDir, "layers"), 0o755))

	var layers []map[string]any
	for li, size := range sizes {
		content := make([]byte, size)
		for i := range content {
			content[i] = byte((i*7 + li) % 251) // compressible, distinct per layer
		}
		sum := sha256.Sum256(content)
		hexsum := hex.EncodeToString(sum[:])
		rel := filepath.Join("layers", "sha256_"+hexsum+".tar")
		require.NoError(t, os.WriteFile(filepath.Join(imgDir, rel), content, 0o644))
		layers = append(layers, map[string]any{"digest": "sha256:" + hexsum, "file": rel, "rawSize": size})
	}

	manifest := map[string]any{"ref": ref, "platform": "linux/amd64", "layers": layers}
	data, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(imgDir, "manifest.json"), data, 0o644))
	return corpusDir
}

func TestExecuteSingleLayerSkipsHighConcurrency(t *testing.T) {
	ref := "testimg"
	cfg := &config.Config{
		Images:         []string{ref},
		Levels:         []string{"fast"},
		Ops:            []string{"compress", "decompress"},
		BufferSizes:    []config.ByteSize{64 * 1024},
		JobConcurrency: []int{1, 2}, // jc=2 must be skipped: only 1 layer
		Iterations:     2,
		Gap:            0,
		Corpus:         fakeCorpus(t, ref, 256*1024),
		Results:        t.TempDir(),
	}
	m, ok := method.Get("kp-zstd")
	require.True(t, ok)

	r := &Runner{
		Config:  cfg,
		Methods: []config.ResolvedMethod{{Method: m, Levels: []method.Level{method.Fast}}},
		Now:     func() time.Time { return time.Unix(0, 0).UTC() },
	}
	run, err := r.Execute("testrun")
	require.NoError(t, err)

	// 1 image × 1 level × 1 buffer × (jc=1 only, jc=2 skipped) × 2 ops = 2 rows
	require.Len(t, run.Rows, 2)
	for _, row := range run.Rows {
		require.Equal(t, 1, row.JobConcurrency, "jc=2 skipped for a 1-layer image")
		require.Equal(t, 1, row.NumLayers)
		require.True(t, row.RoundtripOK)
		require.Greater(t, row.ThroughputMBps, 0.0)
		require.Greater(t, row.BytesAllocated, int64(0), "pure-Go method records allocations")
		if row.Op == "compress" {
			require.NotEmpty(t, row.OutputSHA256)
			require.Greater(t, row.Ratio, 1.0)
			require.Equal(t, int64(256*1024), row.BytesIn)
		} else {
			require.Empty(t, row.OutputSHA256)
		}
	}

	// Write + reload round-trips through the schema.
	out := filepath.Join(cfg.Results, "testrun.json")
	require.NoError(t, run.Write(out))
	require.FileExists(t, out)
}

func TestExecuteMultiLayerAggregates(t *testing.T) {
	ref := "multi"
	cfg := &config.Config{
		Images:         []string{ref},
		Levels:         []string{"fast"},
		Ops:            []string{"compress"},
		BufferSizes:    []config.ByteSize{64 * 1024},
		JobConcurrency: []int{1, 2}, // both run: 3 layers
		Iterations:     1,
		Gap:            0,
		Corpus:         fakeCorpus(t, ref, 128*1024, 64*1024, 32*1024),
		Results:        t.TempDir(),
	}
	m, _ := method.Get("kp-gzip")
	r := &Runner{
		Config:  cfg,
		Methods: []config.ResolvedMethod{{Method: m, Levels: []method.Level{method.Fast}}},
	}
	run, err := r.Execute("multi")
	require.NoError(t, err)

	// 1 image × 1 level × 1 buffer × 2 jc × 1 op = 2 rows (jc=1 and jc=2)
	require.Len(t, run.Rows, 2)
	for _, row := range run.Rows {
		require.Equal(t, 3, row.NumLayers)
		require.Equal(t, int64(128*1024+64*1024+32*1024), row.BytesIn, "aggregate raw size over all layers")
		require.Greater(t, row.Ratio, 1.0)
	}
	// jc=1 and jc=2 share the same compressed output (job concurrency can't change bytes).
	require.Equal(t, run.Rows[0].OutputSHA256, run.Rows[1].OutputSHA256)
}

func TestExecuteRoundtripGate(t *testing.T) {
	// Corrupt the artifact path indirectly: use a method whose decompress can't
	// read the other's output. We simulate a round-trip failure by writing a
	// layer whose recorded digest is wrong.
	ref := "badimg"
	corpusDir := t.TempDir()
	imgDir := filepath.Join(corpusDir, ref, "layers")
	require.NoError(t, os.MkdirAll(imgDir, 0o755))
	content := []byte("hello world, this is a layer")
	rel := filepath.Join("layers", "sha256_deadbeef.tar")
	require.NoError(t, os.WriteFile(filepath.Join(corpusDir, ref, rel), content, 0o644))
	manifest := map[string]any{
		"ref": ref, "platform": "linux/amd64",
		"layers": []map[string]any{{
			"digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			"file":   rel, "rawSize": len(content),
		}},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(corpusDir, ref, "manifest.json"), data, 0o644))

	cfg := &config.Config{
		Images: []string{ref}, Levels: []string{"fast"},
		Ops: []string{"compress"}, BufferSizes: []config.ByteSize{1024},
		JobConcurrency: []int{1}, Iterations: 1, Corpus: corpusDir, Results: t.TempDir(),
	}
	m, _ := method.Get("kp-zstd")
	r := &Runner{Config: cfg, Methods: []config.ResolvedMethod{{Method: m, Levels: []method.Level{method.Fast}}}}
	_, err := r.Execute("bad")
	require.Error(t, err)
	require.Contains(t, err.Error(), "round-trip FAILED")
}
