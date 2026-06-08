package result

import (
	"strconv"
	"strings"
)

// sep is an ASCII unit separator, which never appears in image refs, digests,
// method/level names or numbers, so composite keys split unambiguously.
const sep = "\x1f"

func joinKey(parts ...string) string { return strings.Join(parts, sep) }
func splitKey(s string) []string     { return strings.Split(s, sep) }
func itoa(n int) string              { return strconv.Itoa(n) }
