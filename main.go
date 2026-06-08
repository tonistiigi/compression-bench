// Command compbench benchmarks compression methods on container image layers.
// See DESIGN.md for the model. Subcommands: prep, run, report, pull.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tonistiigi/compression-bench/bench"
	"github.com/tonistiigi/compression-bench/config"
	"github.com/tonistiigi/compression-bench/corpus"
	"github.com/tonistiigi/compression-bench/keychain"
	"github.com/tonistiigi/compression-bench/pull"
	"github.com/tonistiigi/compression-bench/report"
	"github.com/tonistiigi/compression-bench/result"
	"github.com/tonistiigi/compression-bench/ui"
)

// tokenService/tokenAccount key the GitHub token in the OS keychain.
const (
	tokenService = "compbench"
	tokenAccount = "github.com"
)

// resolveToken returns the GitHub token, preferring the named env var (set by CI
// and as an explicit override) and falling back to the OS keychain. The token is
// never accepted as a command-line argument, so it can't leak into the process
// list or shell history.
func resolveToken(envVar string) string {
	if envVar != "" {
		if t := os.Getenv(envVar); t != "" {
			return t
		}
	}
	if keychain.Supported() {
		if t, err := keychain.Load(tokenService, tokenAccount); err == nil {
			return t
		}
	}
	return ""
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "prep":
		err = cmdPrep(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "pull":
		err = cmdPull(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %+v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `compbench - compression benchmark for container image layers

usage:
  compbench prep   [--config c.yaml] [--force]   pull + decompress corpus (optional warm-up)
  compbench run    [--config c.yaml]             auto-prep missing images, then benchmark
  compbench report [--results dir] [--ui dir]    render aggregated report (text, or a static HTML site)
  compbench pull   [--repo o/r] [--n N] [--results dir]   fetch CI results via GitHub API
`)
}

func cmdPrep(args []string) error {
	fs := flag.NewFlagSet("prep", flag.ExitOnError)
	cfgPath := fs.String("config", "compbench.yaml", "config file")
	force := fs.Bool("force", false, "re-pull even if corpus exists")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	for _, ref := range cfg.Images {
		img, err := corpus.Ensure(cfg.Corpus, ref, *force)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d layers (%s)\n", ref, len(img.Layers), img.Platform)
	}
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", "compbench.yaml", "config file")
	force := fs.Bool("force", false, "re-pull corpus before running")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	methods, skips, err := cfg.Resolve()
	if err != nil {
		return err
	}
	for _, s := range skips {
		fmt.Fprintf(os.Stderr, "skip %s: %s\n", s.Name, s.Reason)
	}

	runID := newRunID()
	tty := isTerminal(os.Stderr)
	r := &bench.Runner{
		Config:  cfg,
		Methods: methods,
		Skips:   skips,
		Force:   *force,
		Progress: func(f string, a ...any) {
			if tty {
				fmt.Fprint(os.Stderr, "\r\033[K") // clear any transient status line first
			}
			fmt.Fprintf(os.Stderr, f+"\n", a...)
		},
		Status: statusFunc(tty),
	}
	run, err := r.Execute(runID)
	if err != nil {
		return err
	}
	out := fmt.Sprintf("%s/%s.json", cfg.Results, runID)
	if err := run.Write(out); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d rows)\n", out, len(run.Rows))
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	resultsDir := fs.String("results", "results", "results directory")
	uiDir := fs.String("ui", "", "write a self-contained static HTML report to this dir instead of text")
	fs.Parse(args)

	runs, err := result.LoadDir(*resultsDir)
	if err != nil {
		return err
	}
	if *uiDir != "" {
		if err := ui.WriteSite(*uiDir, runs, time.Now()); err != nil {
			return err
		}
		fmt.Printf("wrote %s/index.html (%d run(s))\n", *uiDir, len(runs))
		return nil
	}
	return report.Render(os.Stdout, runs)
}

func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ExitOnError)
	repo := fs.String("repo", os.Getenv("GITHUB_REPOSITORY"), "owner/repo (or $GITHUB_REPOSITORY)")
	tokenEnv := fs.String("token-env", "GITHUB_TOKEN", "env var holding the GitHub token (falls back to the OS keychain; see `compbench token`)")
	n := fs.Int("n", 10, "max number of recent CI artifacts to fetch (0 = all)")
	resultsDir := fs.String("results", "results", "results directory to write into")
	fs.Parse(args)

	if *repo == "" {
		return fmt.Errorf("pull: --repo (owner/repo) is required, or set $GITHUB_REPOSITORY")
	}
	token := resolveToken(*tokenEnv)
	if token == "" {
		printTokenHelp(*tokenEnv)
		return errors.New("pull: no GitHub token available")
	}
	c := &pull.Client{Repo: *repo, Token: token}
	written, err := pull.Run(context.Background(), c, *resultsDir, *n)
	if err != nil {
		return err
	}
	fmt.Printf("pulled %d result file(s) into %s\n", written, *resultsDir)
	return nil
}

// isTerminal reports whether f is a character device (interactive terminal),
// so we only emit in-place \r status updates there and keep CI logs clean.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// statusFunc returns a transient in-place status printer for a TTY, or a no-op.
func statusFunc(tty bool) func(string) {
	if !tty {
		return func(string) {}
	}
	return func(msg string) {
		if len(msg) > 110 { // avoid line wrap breaking the in-place update
			msg = msg[:110]
		}
		fmt.Fprintf(os.Stderr, "\r\033[K%s", msg)
	}
}

// printTokenHelp explains how to obtain and store a GitHub token, including the
// scopes it needs to read Actions artifacts.
func printTokenHelp(envVar string) {
	var b strings.Builder
	fmt.Fprintf(&b, "No GitHub token found (checked $%s", envVar)
	if keychain.Supported() {
		b.WriteString(" and the OS keychain")
	}
	b.WriteString(").\n\n")
	b.WriteString(`Create a token with read access to the repo's Actions artifacts:

  fine-grained PAT  https://github.com/settings/personal-access-tokens/new
    - Repository access: the repo you pull results from
    - Permissions -> Actions: Read-only

  classic PAT       https://github.com/settings/tokens/new
    - scope "repo" (private repos), or "public_repo" (public repos)

`)
	if keychain.Supported() {
		// `-w` with no value makes `security` prompt for the token without
		// echoing it, so it never lands in argv or shell history. The service /
		// account match what resolveToken reads back.
		fmt.Fprintf(&b, "Store it in the keychain (you'll be prompted for the value):\n"+
			"  security add-generic-password -U -s %s -a %s -T '' -w\n\n"+
			"compbench then reads it automatically. Or for a single run:\n"+
			"  %s=<token> compbench pull --repo owner/repo\n", tokenService, tokenAccount, envVar)
	} else {
		fmt.Fprintf(&b, "Provide it via the environment:\n"+
			"  %s=<token> compbench pull --repo owner/repo\n", envVar)
	}
	fmt.Fprint(os.Stderr, b.String())
}

func newRunID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "host"
	}
	return fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102-150405"), host)
}
