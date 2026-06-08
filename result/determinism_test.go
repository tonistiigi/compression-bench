package result

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func compRow(method, level, hash string) Row {
	return Row{
		Image:  "img",
		Method: method, Level: level, JobConcurrency: 1, BufferSize: 65536,
		Op: "compress", OutputSHA256: hash,
	}
}

func run(arch, runID string, rows ...Row) *Run {
	return &Run{SchemaVersion: SchemaVersion, RunID: runID, Env: Env{OS: "linux", Arch: arch}, Rows: rows}
}

func find(t *testing.T, res []DetermResult, method, level string) DetermResult {
	t.Helper()
	for _, r := range res {
		if r.Method == method && r.Level == level {
			return r
		}
	}
	t.Fatalf("no determinism result for %s/%s", method, level)
	return DetermResult{}
}

func TestReplaySameEnv(t *testing.T) {
	// Two runs on the same env: identical hashes => replay-deterministic;
	// differing hashes => not.
	runs := []*Run{
		run("arm64", "r1", compRow("stable", "best", "H1"), compRow("flaky", "best", "A")),
		run("arm64", "r2", compRow("stable", "best", "H1"), compRow("flaky", "best", "B")),
	}
	res := Determinism(runs)

	s := find(t, res, "stable", "best")
	require.True(t, s.ReplayComparable)
	require.True(t, s.DeterministicReplay)

	f := find(t, res, "flaky", "best")
	require.True(t, f.ReplayComparable)
	require.False(t, f.DeterministicReplay)
}

func TestReplayNotComparableSingleRun(t *testing.T) {
	runs := []*Run{run("arm64", "r1", compRow("m", "fast", "H"))}
	r := find(t, Determinism(runs), "m", "fast")
	require.False(t, r.ReplayComparable)
	require.False(t, r.DeterministicReplay, "cannot assert replay from one run")
}

func TestDeterminismAcrossArch(t *testing.T) {
	runs := []*Run{
		run("amd64", "a", compRow("m", "fast", "SAME"), compRow("p", "fast", "X64")),
		run("arm64", "b", compRow("m", "fast", "SAME"), compRow("p", "fast", "XARM")),
	}
	res := Determinism(runs)

	m := find(t, res, "m", "fast")
	require.True(t, m.ArchComparable)
	require.True(t, m.DeterministicAcrossArch)

	p := find(t, res, "p", "fast")
	require.True(t, p.ArchComparable)
	require.False(t, p.DeterministicAcrossArch, "parallel codec differs across arch")
}

func TestArchNotComparableSingleArch(t *testing.T) {
	runs := []*Run{run("arm64", "r1", compRow("m", "fast", "H"))}
	r := find(t, Determinism(runs), "m", "fast")
	require.False(t, r.ArchComparable)
	require.False(t, r.DeterministicAcrossArch)
}

func TestDeterminismIgnoresDecompressRows(t *testing.T) {
	runs := []*Run{run("arm64", "r1",
		Row{Method: "m", Level: "fast", Op: "decompress"}, // no hash, ignored
		compRow("m", "fast", "H"),
	)}
	require.Equal(t, 1, find(t, Determinism(runs), "m", "fast").Samples)
}

func TestStats(t *testing.T) {
	durs := []time.Duration{30, 10, 20, 40, 50}
	min, median, stddev := Stats(durs)
	require.Equal(t, time.Duration(10), min)
	require.Equal(t, time.Duration(30), median)
	require.InDelta(t, 14.14, float64(stddev), 0.5)
}

func TestThroughput(t *testing.T) {
	require.InDelta(t, 100.0, ThroughputMBps(100_000_000, time.Second), 0.001)
	require.Equal(t, 0.0, ThroughputMBps(100, 0))
}
