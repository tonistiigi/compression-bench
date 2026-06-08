// Package method defines the compression methods under test and the streaming
// primitives used to benchmark them. Every method is exercised through the same
// bounded-buffer streaming path so the benchmark reflects realistic layer
// compression rather than whole-file-in-memory codec calls.
package method

import (
	"io"
	"sort"

	"github.com/pkg/errors"
)

// Level is the normalized compression level. Each Method maps it onto its own
// native level (see the table in DESIGN.md). The raw value is reported
// alongside the normalized one.
type Level int

const (
	Fast Level = iota
	Default
	Best
)

// Levels is the set of normalized levels, in reporting order.
var Levels = []Level{Fast, Default, Best}

func (l Level) String() string {
	switch l {
	case Fast:
		return "fast"
	case Default:
		return "default"
	case Best:
		return "best"
	default:
		return "unknown"
	}
}

// ParseLevel maps a normalized level name to a Level.
func ParseLevel(s string) (Level, error) {
	for _, l := range Levels {
		if l.String() == s {
			return l, nil
		}
	}
	return 0, errors.Errorf("unknown level %q", s)
}

// Method is a compression implementation under test. Implementations stream:
// NewWriter wraps a destination and compresses what is written to it, NewReader
// wraps a source and decompresses what is read from it. RawLevel exposes the
// native level the normalized Level maps to, for the report.
type Method interface {
	Name() string
	// Version reports the implementation version (module version, or the
	// external tool's reported version).
	Version() string
	// RawLevel returns the native level string for a normalized Level, as it
	// appears in the report (e.g. "9" for gzip best, "19" for zstd best).
	RawLevel(Level) string
	// NewWriter returns a WriteCloser that compresses into w at level. The
	// caller must Close it to flush the trailer.
	NewWriter(w io.Writer, level Level) (io.WriteCloser, error)
	// NewReader returns a ReadCloser that decompresses from r.
	NewReader(r io.Reader) (io.ReadCloser, error)
	// GoMemory reports whether Go-heap allocation stats represent this method's
	// memory use. False for cgo/external methods, whose working memory lives off
	// the Go heap (in C, or in a child process) and is invisible to MemStats.
	GoMemory() bool
}

var registry = map[string]Method{}

// Register adds a method to the global registry. It panics on a duplicate name,
// which can only happen via a programming error at init time.
func Register(m Method) {
	if _, ok := registry[m.Name()]; ok {
		panic("method already registered: " + m.Name())
	}
	registry[m.Name()] = m
}

// Get returns a registered method by name.
func Get(name string) (Method, bool) {
	m, ok := registry[name]
	return m, ok
}

// Names returns the registered method names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Compress streams src through m at level into dst, using a copy buffer of
// bufSize bytes. It closes the compressor so the trailer is written. This is the
// single compression path the benchmark uses.
func Compress(m Method, dst io.Writer, src io.Reader, level Level, bufSize int) error {
	w, err := m.NewWriter(dst, level)
	if err != nil {
		return errors.Wrapf(err, "new writer for %s", m.Name())
	}
	buf := make([]byte, bufSize)
	if _, err := io.CopyBuffer(w, src, buf); err != nil {
		w.Close()
		return errors.Wrapf(err, "compress with %s", m.Name())
	}
	if err := w.Close(); err != nil {
		return errors.Wrapf(err, "close %s writer", m.Name())
	}
	return nil
}

// Decompress streams src through m into dst, using a copy buffer of bufSize
// bytes. This is the single decompression path the benchmark uses.
func Decompress(m Method, dst io.Writer, src io.Reader, bufSize int) error {
	r, err := m.NewReader(src)
	if err != nil {
		return errors.Wrapf(err, "new reader for %s", m.Name())
	}
	defer r.Close()
	buf := make([]byte, bufSize)
	if _, err := io.CopyBuffer(dst, r, buf); err != nil {
		return errors.Wrapf(err, "decompress with %s", m.Name())
	}
	return r.Close()
}
