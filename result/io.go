package result

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pkg/errors"
)

// writeJSON marshals v to path, confining the write to the destination directory
// via an os.Root.
func writeJSON(path string, v any) error {
	dir, name := splitDir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errors.WithStack(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.WithStack(err)
	}
	defer root.Close()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(root.WriteFile(name, data, 0o644))
}

// LoadRun reads a single run file (confined to its directory).
func LoadRun(path string) (*Run, error) {
	dir, name := splitDir(path)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer root.Close()
	return loadRun(root, name)
}

// LoadDir loads every *.json run file in dir, sorted by filename, reading them
// through an os.Root anchored at dir.
func LoadDir(dir string) ([]*Run, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	runs := make([]*Run, 0, len(names))
	for _, name := range names {
		r, err := loadRun(root, name)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func loadRun(root *os.Root, name string) (*Run, error) {
	data, err := root.ReadFile(name)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, errors.Wrapf(err, "parse %s", name)
	}
	if r.SchemaVersion != SchemaVersion {
		return nil, errors.Errorf("%s: schema version %d, want %d", name, r.SchemaVersion, SchemaVersion)
	}
	return &r, nil
}

// splitDir splits a path into a directory (never empty) and a base name.
func splitDir(path string) (dir, name string) {
	dir, name = filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	return dir, name
}
