package method

import (
	"bytes"
	"io"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// testData returns deterministic, semi-compressible bytes (no randomness, so the
// streaming==one-shot byte-equality check is stable).
func testData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*7 + i/13) % 251)
	}
	return b
}

const fixtureSize = 300 * 1024 // larger than pgzip's default block, exercises parallelism

func TestRoundTrip(t *testing.T) {
	data := testData(fixtureSize)
	for _, name := range Names() {
		m, _ := Get(name)
		for _, level := range Levels {
			t.Run(name+"/"+level.String(), func(t *testing.T) {
				var comp bytes.Buffer
				require.NoError(t, Compress(m, &comp, bytes.NewReader(data), level, 64*1024))
				require.Less(t, comp.Len(), len(data), "should actually compress this fixture")

				var out bytes.Buffer
				require.NoError(t, Decompress(m, &out, bytes.NewReader(comp.Bytes()), 64*1024))
				require.True(t, bytes.Equal(data, out.Bytes()), "roundtrip mismatch")
			})
		}
	}
}

// TestStreamingMatchesOneShot guards the streaming wrapper: feeding the codec via
// a tiny copy buffer (many small writes) must produce byte-identical output to a
// single Write+Close. Catches missing flush/close and trailer-truncation bugs.
func TestStreamingMatchesOneShot(t *testing.T) {
	data := testData(fixtureSize)
	for _, name := range Names() {
		m, _ := Get(name)
		for _, level := range Levels {
			t.Run(name+"/"+level.String(), func(t *testing.T) {
				var oneShot bytes.Buffer
				w, err := m.NewWriter(&oneShot, level)
				require.NoError(t, err)
				_, err = w.Write(data)
				require.NoError(t, err)
				require.NoError(t, w.Close())

				var streamed bytes.Buffer
				require.NoError(t, Compress(m, &streamed, bytes.NewReader(data), level, 1024))

				require.True(t, bytes.Equal(oneShot.Bytes(), streamed.Bytes()),
					"streamed output differs from one-shot")
			})
		}
	}
}

func TestLevelParsing(t *testing.T) {
	for _, l := range Levels {
		got, err := ParseLevel(l.String())
		require.NoError(t, err)
		require.Equal(t, l, got)
	}
	_, err := ParseLevel("nope")
	require.Error(t, err)
}

// TestExternal verifies the subprocess plumbing and level substitution against a
// real gzip CLI when available; skipped otherwise so the suite stays hermetic.
func TestExternal(t *testing.T) {
	if _, err := exec.LookPath("gzip"); err != nil {
		t.Skip("gzip not on PATH")
	}
	m, err := NewExternal("gzip-cli",
		[]string{"gzip", "-{level}", "-c"},
		[]string{"gzip", "-d", "-c"}, nil)
	require.NoError(t, err)
	require.Equal(t, "6", m.RawLevel(Default))

	data := testData(fixtureSize)
	var comp bytes.Buffer
	require.NoError(t, Compress(m, &comp, bytes.NewReader(data), Best, 64*1024))
	require.Less(t, comp.Len(), len(data))

	var out bytes.Buffer
	require.NoError(t, Decompress(m, &out, bytes.NewReader(comp.Bytes()), 64*1024))
	require.True(t, bytes.Equal(data, out.Bytes()))
}

func TestExternalMissingBinary(t *testing.T) {
	_, err := NewExternal("nope", []string{"definitely-not-a-real-binary-xyz"}, []string{"x"}, nil)
	require.Error(t, err)
}

// sanity: a ReadCloser returned by NewReader is usable as io.Reader.
var _ io.Reader = io.ReadCloser(nil)
