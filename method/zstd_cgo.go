//go:build cgo_zstd

package method

import (
	"io"

	ddzstd "github.com/DataDog/zstd"
)

// This file is compiled only with `-tags cgo_zstd` (and CGO_ENABLED=1). It is
// additive: the cgo binding is registered *alongside* the pure-Go kp-zstd so a
// single run compares both on the same corpus. See zstd_cgo_stub.go for the
// pure-Go build, which registers nothing.

func init() {
	Register(zstdCgo{})
}

func cgoZstdLevel(l Level) int {
	switch l {
	case Fast:
		return 1
	case Best:
		return 19
	default:
		return 3
	}
}

type zstdCgo struct{}

func (zstdCgo) Name() string            { return "zstd-cgo" }
func (zstdCgo) Version() string         { return modVersion("github.com/DataDog/zstd") }
func (zstdCgo) RawLevel(l Level) string { return zstdRawLevel(l) }
func (zstdCgo) GoMemory() bool          { return false } // libzstd memory is off the Go heap

func (zstdCgo) NewWriter(w io.Writer, level Level) (io.WriteCloser, error) {
	return ddzstd.NewWriterLevel(w, cgoZstdLevel(level)), nil
}

func (zstdCgo) NewReader(r io.Reader) (io.ReadCloser, error) {
	return ddzstd.NewReader(r), nil
}
