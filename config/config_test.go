package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/compression-bench/method"
)

const sample = `
images: [alpine:3.19]
methods:
  - kp-zstd
  - stdlib-gzip
  - external: { name: pigz, cmd: [pigz, "-{level}", -c], decmd: [pigz, -d, -c], rawLevels: { fast: "1", default: "6", best: "9" } }
bufferSizes: [64KiB, 1MiB]
jobConcurrency: [1, 2]
iterations: 3
gap: 200ms
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestLoadAndDefaults(t *testing.T) {
	c, err := Load(writeTemp(t, sample))
	require.NoError(t, err)

	require.Equal(t, []string{"alpine:3.19"}, c.Images)
	require.Equal(t, []ByteSize{64 * 1024, 1024 * 1024}, c.BufferSizes)
	require.Equal(t, []int{1, 2}, c.JobConcurrency)
	require.Equal(t, 3, c.Iterations)
	require.Equal(t, 200*time.Millisecond, c.Gap.Std())

	// defaults applied ("best" omitted by default)
	require.Equal(t, []string{"fast", "default"}, c.Levels)
	require.Equal(t, []string{"compress", "decompress"}, c.Ops)
	require.Equal(t, ".corpus", c.Corpus)
	require.Equal(t, "results", c.Results)
}

func TestMethodSpecParsing(t *testing.T) {
	c, err := Load(writeTemp(t, sample))
	require.NoError(t, err)
	require.Len(t, c.Methods, 3)
	require.Equal(t, "kp-zstd", c.Methods[0].Builtin)
	require.Nil(t, c.Methods[0].External)
	require.NotNil(t, c.Methods[2].External)
	require.Equal(t, "pigz", c.Methods[2].External.Name)
	require.Equal(t, []string{"pigz", "-{level}", "-c"}, c.Methods[2].External.Cmd)
}

func TestResolveSkipsMissing(t *testing.T) {
	// An unregistered builtin and a missing external binary are both skipped
	// (build-mode independent: use a name that is never registered, unlike
	// zstd-cgo which exists under -tags cgo_zstd). Builtins that exist resolve.
	body := `
images: [x]
methods:
  - kp-zstd
  - no-such-method
  - external: { name: ghost, cmd: [definitely-not-real-xyz, "-c"], decmd: [definitely-not-real-xyz, -d] }
`
	c, err := Load(writeTemp(t, body))
	require.NoError(t, err)
	methods, skips, err := c.Resolve()
	require.NoError(t, err)

	names := make([]string, len(methods))
	for i, rm := range methods {
		names[i] = rm.Method.Name()
	}
	require.Contains(t, names, "kp-zstd")
	require.NotContains(t, names, "no-such-method")

	skipNames := make([]string, len(skips))
	for i, s := range skips {
		skipNames[i] = s.Name
	}
	require.Contains(t, skipNames, "no-such-method")
	require.Contains(t, skipNames, "ghost")
}

func TestPerMethodLevels(t *testing.T) {
	body := `
images: [x]
levels: [fast, default]
methods:
  - kp-gzip
  - { name: kp-zstd, levels: [fast] }
  - external: { name: zstd-cli, cmd: [gzip, "-{level}", -c], decmd: [gzip, -d, -c], levels: [default] }
`
	c, err := Load(writeTemp(t, body))
	require.NoError(t, err)
	methods, _, err := c.Resolve()
	require.NoError(t, err)

	byName := map[string][]method.Level{}
	for _, rm := range methods {
		byName[rm.Method.Name()] = rm.Levels
	}
	require.Equal(t, []method.Level{method.Fast, method.Default}, byName["kp-gzip"], "inherits global")
	require.Equal(t, []method.Level{method.Fast}, byName["kp-zstd"], "per-method override")
	require.Equal(t, []method.Level{method.Default}, byName["zstd-cli"], "external per-method override")
}

func TestParsedLevels(t *testing.T) {
	c, err := Load(writeTemp(t, sample))
	require.NoError(t, err)
	lv, err := c.ParsedLevels()
	require.NoError(t, err)
	require.Equal(t, []method.Level{method.Fast, method.Default}, lv)
}

func TestByteSizeParsing(t *testing.T) {
	cases := map[string]int{"64KiB": 65536, "1MiB": 1048576, "1MB": 1000000, "512": 512, "2GiB": 2 << 30}
	for in, want := range cases {
		got, err := parseByteSize(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
}

func TestRejectsUnknownField(t *testing.T) {
	_, err := Load(writeTemp(t, "images: [x]\nmethods: [kp-zstd]\nbogusField: 1\n"))
	require.Error(t, err)
}
