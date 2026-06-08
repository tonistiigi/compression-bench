package method

import "runtime/debug"

// modVersion looks up the version of a dependency module from the binary's build
// info, so the report records the exact library version actually linked in
// rather than what go.mod requests. Returns "unknown" if not found.
func modVersion(path string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, d := range info.Deps {
		if d.Path == path {
			if d.Replace != nil {
				return d.Replace.Version
			}
			return d.Version
		}
	}
	return "unknown"
}
