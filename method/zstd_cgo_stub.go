//go:build !cgo_zstd

package method

// Pure-Go build: the cgo zstd binding is not compiled in, so nothing is
// registered here. A config that references "zstd-cgo" will find no method and
// the run records it as skipped. Build with `-tags cgo_zstd` (CGO_ENABLED=1) to
// include it. See zstd_cgo.go.
