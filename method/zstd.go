package method

import (
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/pkg/errors"
)

// zstdRawLevel maps the normalized level onto the numeric zstd scale used by the
// zstd CLI and the report (so kp-zstd, zstd-cgo and external zstd line up).
func zstdRawLevel(l Level) string {
	switch l {
	case Fast:
		return "1"
	case Best:
		return "19"
	default:
		return "3"
	}
}

func kpZstdLevel(l Level) zstd.EncoderLevel {
	switch l {
	case Fast:
		return zstd.SpeedFastest
	case Best:
		return zstd.SpeedBestCompression
	default:
		return zstd.SpeedDefault
	}
}

func init() {
	Register(kpZstd{})
}

// kpZstd is klauspost/compress/zstd, pure Go. Internal concurrency is left at
// the library default.
type kpZstd struct{}

func (kpZstd) Name() string            { return "kp-zstd" }
func (kpZstd) Version() string         { return modVersion("github.com/klauspost/compress") }
func (kpZstd) RawLevel(l Level) string { return zstdRawLevel(l) }
func (kpZstd) GoMemory() bool          { return true }

func (kpZstd) NewWriter(w io.Writer, level Level) (io.WriteCloser, error) {
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(kpZstdLevel(level)))
	return enc, errors.WithStack(err)
}

func (kpZstd) NewReader(r io.Reader) (io.ReadCloser, error) {
	dec, err := zstd.NewReader(r)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return dec.IOReadCloser(), nil
}
