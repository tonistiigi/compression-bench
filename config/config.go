// Package config defines the benchmark configuration file format and resolves it
// into concrete methods to run. See DESIGN.md for the schema.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/tonistiigi/compression-bench/method"
	"gopkg.in/yaml.v3"
)

// Config is the parsed benchmark configuration.
type Config struct {
	Images         []string     `yaml:"images"`
	Methods        []MethodSpec `yaml:"methods"`
	Levels         []string     `yaml:"levels"`
	Ops            []string     `yaml:"ops"`
	BufferSizes    []ByteSize   `yaml:"bufferSizes"`
	JobConcurrency []int        `yaml:"jobConcurrency"`
	Iterations     int          `yaml:"iterations"`
	Gap            Duration     `yaml:"gap"`
	Corpus         string       `yaml:"corpus"`  // corpus dir, default "corpus"
	Results        string       `yaml:"results"` // results dir, default "results"
}

// MethodSpec is one entry under `methods`. It is one of:
//   - a YAML scalar: a builtin method name (uses the global levels)
//   - a mapping `{name: <builtin>, levels: [...]}`: builtin with a per-method
//     subset of levels to run
//   - a mapping `{external: {...}}`: an external-command method
type MethodSpec struct {
	Builtin  string        // builtin method name (scalar, or `name:` in a mapping)
	Levels   []string      // per-method normalized levels for a builtin; empty = global
	External *ExternalSpec // set when the entry is `external: {...}`
}

// ExternalSpec configures an external-command method.
type ExternalSpec struct {
	Name      string            `yaml:"name"`
	Cmd       []string          `yaml:"cmd"`
	Decmd     []string          `yaml:"decmd"`
	Levels    []string          `yaml:"levels"`    // normalized levels to run; empty = global
	RawLevels map[string]string `yaml:"rawLevels"` // normalized name -> raw level; nil = gzip 1/6/9
}

// runLevelNames returns the per-method normalized level names ("" slice = use global).
func (m MethodSpec) runLevelNames() []string {
	if m.External != nil {
		return m.External.Levels
	}
	return m.Levels
}

func (m MethodSpec) displayName() string {
	if m.External != nil {
		return m.External.Name
	}
	return m.Builtin
}

// UnmarshalYAML accepts a scalar (builtin name), a `{name, levels}` builtin
// mapping, or an `{external: {...}}` mapping.
func (m *MethodSpec) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		m.Builtin = node.Value
		return nil
	}
	var wrap struct {
		External *ExternalSpec `yaml:"external"`
		Name     string        `yaml:"name"`
		Levels   []string      `yaml:"levels"`
	}
	if err := node.Decode(&wrap); err != nil {
		return errors.WithStack(err)
	}
	switch {
	case wrap.External != nil:
		m.External = wrap.External
	case wrap.Name != "":
		m.Builtin = wrap.Name
		m.Levels = wrap.Levels
	default:
		return errors.Errorf("method entry at line %d is neither a name nor an `external` block", node.Line)
	}
	return nil
}

// Skip records a configured method that could not be activated (e.g. cgo not
// compiled in, or an external binary missing). The run records these rather than
// failing.
type Skip struct {
	Name   string
	Reason string
}

// ResolvedMethod is a concrete method paired with the normalized levels it runs.
type ResolvedMethod struct {
	Method method.Method
	Levels []method.Level
}

// Resolve turns the configured method specs into concrete methods (each with its
// levels), returning any specs that had to be skipped.
func (c *Config) Resolve() ([]ResolvedMethod, []Skip, error) {
	globalLevels, err := c.ParsedLevels()
	if err != nil {
		return nil, nil, err
	}
	var methods []ResolvedMethod
	var skips []Skip
	for _, spec := range c.Methods {
		levels := globalLevels
		if names := spec.runLevelNames(); len(names) > 0 {
			levels, err = parseLevelNames(names)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "method %q", spec.displayName())
			}
		}

		switch {
		case spec.External != nil:
			raw, err := parseLevelMap(spec.External.RawLevels)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "external %q", spec.External.Name)
			}
			ext, err := method.NewExternal(spec.External.Name, spec.External.Cmd, spec.External.Decmd, raw)
			if err != nil {
				skips = append(skips, Skip{Name: spec.External.Name, Reason: err.Error()})
				continue
			}
			methods = append(methods, ResolvedMethod{Method: ext, Levels: levels})
		case spec.Builtin != "":
			m, ok := method.Get(spec.Builtin)
			if !ok {
				skips = append(skips, Skip{Name: spec.Builtin, Reason: "not registered (build without required tag?)"})
				continue
			}
			methods = append(methods, ResolvedMethod{Method: m, Levels: levels})
		default:
			return nil, nil, errors.New("empty method spec")
		}
	}
	return methods, skips, nil
}

func parseLevelMap(m map[string]string) (map[method.Level]string, error) {
	if m == nil {
		return nil, nil
	}
	out := make(map[method.Level]string, len(m))
	for k, v := range m {
		l, err := method.ParseLevel(k)
		if err != nil {
			return nil, err
		}
		out[l] = v
	}
	return out, nil
}

// ParsedLevels returns the global configured levels as method.Level values.
func (c *Config) ParsedLevels() ([]method.Level, error) {
	return parseLevelNames(c.Levels)
}

func parseLevelNames(names []string) ([]method.Level, error) {
	out := make([]method.Level, 0, len(names))
	for _, s := range names {
		l, err := method.ParseLevel(s)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// Load reads and validates a config file, applying defaults.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "read config %s", path)
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, errors.Wrapf(err, "parse config %s", path)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if len(c.Levels) == 0 {
		// "best" is omitted by default: it's slow for marginal gain on gzip;
		// enable it per-method (e.g. for zstd) via a method's `levels:`.
		c.Levels = []string{"fast", "default"}
	}
	if len(c.Ops) == 0 {
		c.Ops = []string{"compress", "decompress"}
	}
	if len(c.BufferSizes) == 0 {
		c.BufferSizes = []ByteSize{64 * 1024, 1024 * 1024}
	}
	if len(c.JobConcurrency) == 0 {
		c.JobConcurrency = []int{1, 2}
	}
	if c.Iterations == 0 {
		c.Iterations = 3
	}
	if c.Gap == 0 {
		c.Gap = Duration(200 * time.Millisecond)
	}
	if c.Corpus == "" {
		c.Corpus = ".corpus" // hidden cache dir; avoids colliding with the corpus/ package
	}
	if c.Results == "" {
		c.Results = "results"
	}
}

func (c *Config) validate() error {
	if len(c.Images) == 0 {
		return errors.New("config: at least one image is required")
	}
	if len(c.Methods) == 0 {
		return errors.New("config: at least one method is required")
	}
	if _, err := c.ParsedLevels(); err != nil {
		return errors.Wrap(err, "config")
	}
	for _, op := range c.Ops {
		if op != "compress" && op != "decompress" {
			return errors.Errorf("config: unknown op %q", op)
		}
	}
	for _, jc := range c.JobConcurrency {
		if jc < 1 {
			return errors.Errorf("config: jobConcurrency must be >= 1, got %d", jc)
		}
	}
	return nil
}

// ByteSize is a byte count parsed from forms like "64KiB", "1MiB", "1MB", "512".
type ByteSize int

func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	n, err := parseByteSize(node.Value)
	if err != nil {
		return errors.Wrapf(err, "bufferSize %q (line %d)", node.Value, node.Line)
	}
	*b = ByteSize(n)
	return nil
}

func parseByteSize(s string) (int, error) {
	s = strings.TrimSpace(s)
	mult := 1
	for _, suf := range []struct {
		s string
		m int
	}{
		{"KiB", 1024}, {"MiB", 1024 * 1024}, {"GiB", 1024 * 1024 * 1024},
		{"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000},
		{"B", 1},
	} {
		if strings.HasSuffix(s, suf.s) {
			mult = suf.m
			s = strings.TrimSpace(strings.TrimSuffix(s, suf.s))
			break
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	return n * mult, nil
}

// Duration is a time.Duration parsed from a string like "200ms".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return errors.Wrapf(err, "gap %q (line %d)", node.Value, node.Line)
	}
	*d = Duration(v)
	return nil
}

// Std returns the standard library duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }
