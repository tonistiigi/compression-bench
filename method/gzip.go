package method

import (
	"io"

	stdgzip "compress/gzip"

	kpgzip "github.com/klauspost/compress/gzip"
	"github.com/klauspost/pgzip"
	"github.com/pkg/errors"
)

// gzipLevel maps the normalized level onto the gzip 1..9 scale, shared by every
// gzip-family method (stdlib, klauspost, pgzip).
func gzipRawLevel(l Level) string {
	switch l {
	case Fast:
		return "1"
	case Best:
		return "9"
	default:
		return "6"
	}
}

func gzipNativeLevel(l Level) int {
	switch l {
	case Fast:
		return 1
	case Best:
		return 9
	default:
		return 6
	}
}

func init() {
	Register(stdlibGzip{})
	Register(kpGzip{})
	Register(kpPgzip{})
}

// stdlibGzip is compress/gzip, the single-threaded baseline.
type stdlibGzip struct{}

func (stdlibGzip) Name() string            { return "stdlib-gzip" }
func (stdlibGzip) Version() string         { return "stdlib" }
func (stdlibGzip) RawLevel(l Level) string { return gzipRawLevel(l) }
func (stdlibGzip) GoMemory() bool          { return true }

func (stdlibGzip) NewWriter(w io.Writer, level Level) (io.WriteCloser, error) {
	gw, err := stdgzip.NewWriterLevel(w, gzipNativeLevel(level))
	return gw, errors.WithStack(err)
}

func (stdlibGzip) NewReader(r io.Reader) (io.ReadCloser, error) {
	gr, err := stdgzip.NewReader(r)
	return gr, errors.WithStack(err)
}

// kpGzip is klauspost/compress/gzip, single-threaded.
type kpGzip struct{}

func (kpGzip) Name() string            { return "kp-gzip" }
func (kpGzip) Version() string         { return modVersion("github.com/klauspost/compress") }
func (kpGzip) RawLevel(l Level) string { return gzipRawLevel(l) }
func (kpGzip) GoMemory() bool          { return true }

func (kpGzip) NewWriter(w io.Writer, level Level) (io.WriteCloser, error) {
	gw, err := kpgzip.NewWriterLevel(w, gzipNativeLevel(level))
	return gw, errors.WithStack(err)
}

func (kpGzip) NewReader(r io.Reader) (io.ReadCloser, error) {
	gr, err := kpgzip.NewReader(r)
	return gr, errors.WithStack(err)
}

// kpPgzip is klauspost/pgzip, in-process goroutine-parallel gzip. Internal
// concurrency is left at the library default on purpose.
type kpPgzip struct{}

func (kpPgzip) Name() string            { return "kp-pgzip" }
func (kpPgzip) Version() string         { return modVersion("github.com/klauspost/pgzip") }
func (kpPgzip) RawLevel(l Level) string { return gzipRawLevel(l) }
func (kpPgzip) GoMemory() bool          { return true }

func (kpPgzip) NewWriter(w io.Writer, level Level) (io.WriteCloser, error) {
	gw, err := pgzip.NewWriterLevel(w, gzipNativeLevel(level))
	return gw, errors.WithStack(err)
}

func (kpPgzip) NewReader(r io.Reader) (io.ReadCloser, error) {
	gr, err := pgzip.NewReader(r)
	return gr, errors.WithStack(err)
}
