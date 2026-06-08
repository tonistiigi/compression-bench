package method

import (
	"bytes"
	"io"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
)

// levelToken is replaced in external command args with the method's raw level.
const levelToken = "{level}"

// External runs an external compressor as a subprocess, piping layer data
// through stdin/stdout. It is constructed from config (not registered at init),
// so users can point it at pigz, the zstd CLI, etc. Internal concurrency is
// whatever the tool defaults to.
type External struct {
	name    string
	cmd     []string         // compress argv; "{level}" is substituted
	decmd   []string         // decompress argv
	levels  map[Level]string // normalized -> raw level string
	version string
}

// gzipExternalLevels is the default level map for external tools (gzip-style),
// used when a config entry omits an explicit map.
var gzipExternalLevels = map[Level]string{Fast: "1", Default: "6", Best: "9"}

// NewExternal builds an external method. It returns an error (so the caller can
// record a skip) if the binary is not on PATH. levels may be nil, in which case
// gzip-style 1/6/9 is used.
func NewExternal(name string, cmd, decmd []string, levels map[Level]string) (*External, error) {
	if len(cmd) == 0 || len(decmd) == 0 {
		return nil, errors.Errorf("external %q: cmd and decmd are required", name)
	}
	bin := cmd[0]
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, errors.Wrapf(err, "external %q: %s not found on PATH", name, bin)
	}
	if levels == nil {
		levels = gzipExternalLevels
	}
	e := &External{name: name, cmd: cmd, decmd: decmd, levels: levels}
	e.version = probeVersion(path)
	return e, nil
}

func probeVersion(path string) string {
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return "unknown"
	}
	if i := bytes.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	return strings.TrimSpace(string(out))
}

func (e *External) Name() string    { return e.name }
func (e *External) Version() string { return e.version }
func (e *External) GoMemory() bool  { return false } // memory lives in the child process

func (e *External) RawLevel(l Level) string {
	if v, ok := e.levels[l]; ok {
		return v
	}
	return gzipExternalLevels[l]
}

func substituteLevel(args []string, raw string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(a, levelToken, raw)
	}
	return out
}

func (e *External) NewWriter(w io.Writer, level Level) (io.WriteCloser, error) {
	argv := substituteLevel(e.cmd, e.RawLevel(level))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.Wrapf(err, "external %q: stdin pipe", e.name)
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrapf(err, "external %q: start %s", e.name, argv[0])
	}
	return &externalWriter{cmd: cmd, stdin: stdin, stderr: &stderr, name: e.name}, nil
}

func (e *External) NewReader(r io.Reader) (io.ReadCloser, error) {
	argv := substituteLevel(e.decmd, "")
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = r
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Wrapf(err, "external %q: stdout pipe", e.name)
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Wrapf(err, "external %q: start %s", e.name, argv[0])
	}
	return &externalReader{cmd: cmd, stdout: stdout, stderr: &stderr, name: e.name}, nil
}

type externalWriter struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *bytes.Buffer
	name   string
}

func (w *externalWriter) Write(p []byte) (int, error) {
	n, err := w.stdin.Write(p)
	return n, errors.WithStack(err)
}

func (w *externalWriter) Close() error {
	if err := w.stdin.Close(); err != nil {
		w.cmd.Wait()
		return errors.Wrapf(err, "external %q: close stdin", w.name)
	}
	if err := w.cmd.Wait(); err != nil {
		return errors.Wrapf(err, "external %q: %s", w.name, strings.TrimSpace(w.stderr.String()))
	}
	return nil
}

type externalReader struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr *bytes.Buffer
	name   string
}

func (r *externalReader) Read(p []byte) (int, error) {
	n, err := r.stdout.Read(p)
	if err == io.EOF {
		return n, io.EOF
	}
	return n, errors.WithStack(err)
}

func (r *externalReader) Close() error {
	if err := r.cmd.Wait(); err != nil {
		return errors.Wrapf(err, "external %q: %s", r.name, strings.TrimSpace(r.stderr.String()))
	}
	return nil
}
