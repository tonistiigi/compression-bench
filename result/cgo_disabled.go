//go:build !cgo

package result

// CGOEnabled reflects whether the binary was built with cgo (CGO_ENABLED=1).
const CGOEnabled = false
