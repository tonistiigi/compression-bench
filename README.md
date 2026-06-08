# compression-bench (`compbench`)

Benchmark compression methods on **real container image layers** — compression
and decompression throughput, ratio, parallel-job contention, and determinism.

It pulls images into a local corpus (decompressed layer tars), then streams every
layer through each codec the way a builder/registry would (bounded buffer, never
the whole layer in memory). See [DESIGN.md](DESIGN.md) for the full model.

It only writes under its own working dirs (`.corpus/`, `results/`, the report
output, a temp dir), and all file access is confined there with `os.Root`, so a
crafted manifest/artifact can't traverse outside. Nothing runs as root.

## What it measures

- **Throughput** (MB/s) and **ratio**, per image × method × level × buffer size.
  The unit is the **whole image**: all its layers are compressed/decompressed
  (each independently), and throughput/ratio are aggregated over them.
- **Job concurrency**: `jobConcurrency` is how many of the image's **layers** are
  processed in parallel (`jc=1` sequential, `jc=2` two at a time; `jc>layerCount`
  is skipped). This shows whether inherently-parallel codecs (pgzip, zstd, pigz),
  which already use many cores *per layer*, still gain from parallelizing across
  layers or just oversubscribe the CPU.
- **Memory**: Go-heap allocation churn to compress/decompress the whole image
  (`mem/op`, `allocs/op`) — *churn*, not peak RSS — for pure-Go methods.
  cgo/external methods show `-` because their memory lives off the Go heap.
- **Determinism** of the compressed bytes, on two axes: **replay** (same machine,
  different runs) and **across CPU arch**. Not across library versions. The text
  report also flags **cross-implementation matches** — implementations whose
  output is byte-identical for the same image+level.
- **Round-trip correctness** is enforced on every run — a failure aborts loudly.

## Methods

| id            | implementation                    |
|---------------|-----------------------------------|
| `stdlib-gzip` | `compress/gzip`                   |
| `kp-gzip`     | `klauspost/compress/gzip`         |
| `kp-pgzip`    | `klauspost/pgzip` (parallel)      |
| `kp-zstd`     | `klauspost/compress/zstd` (pure Go) |
| `zstd-cgo`    | cgo libzstd binding — only with `-tags cgo_zstd` |
| `external`    | any CLI via stdin/stdout (e.g. `pigz`, `zstd`) |

## Build

```sh
go build -o compbench .                              # pure Go (no zstd-cgo)
CGO_ENABLED=1 go build -tags cgo_zstd -o compbench . # include the cgo zstd binding
```

External methods (`pigz`, `zstd`, …) require those binaries on `PATH`; a missing
one is recorded as a skip, not a failure.

## Usage

```sh
compbench prep   [--config compbench.yaml] [--force]   # pull + decompress corpus (optional; run does this lazily)
compbench run    [--config compbench.yaml]             # auto-preps missing images, then benchmarks
compbench report [--results results/]                  # tables + determinism + cross-impl matches
compbench report --ui site/                             # self-contained static HTML report (charts)
compbench pull   --repo owner/repo [--n N] [--results results/]
```

Typical loop:

```sh
compbench run                       # writes results/<timestamp>-<host>.json
compbench pull --repo you/repo      # fetch CI artifacts into results/
compbench report                    # merged local + CI report
```

`pull` uses the GitHub REST API directly (no `gh` CLI), and `--repo` defaults to
`$GITHUB_REPOSITORY`. The **token is never a CLI argument** (it would leak into
the process list and shell history). It's resolved from `$GITHUB_TOKEN` (used by
CI and as an explicit override; rename via `--token-env`), falling back to the OS
keychain. On macOS, store it there once (you'll be prompted for the value):

```sh
security add-generic-password -U -s compbench -a github.com -T '' -w
```

`pull` then reads it automatically. Elsewhere (Linux/CI), use `$GITHUB_TOKEN`.
Running `pull` without a token prints these instructions plus the token scopes it
needs (fine-grained: Actions read-only; classic: `repo`/`public_repo`).

## Configuration

```yaml
images: [alpine:3.23, golang:1.26]
methods:
  - kp-gzip                                   # uses global levels
  - { name: kp-zstd, levels: [fast, default] }  # per-method level subset
  - zstd-cgo
  - external: { name: pigz,     cmd: [pigz, "-{level}", -c], decmd: [pigz, -d, -c] }
  - external: { name: zstd-cli, cmd: [zstd, "-{level}", -c], decmd: [zstd, -d, -c],
                rawLevels: { fast: "1", default: "3" } }   # normalized -> raw level for the CLI
levels: [fast, default]         # global default; "best" omitted (slow, marginal gain)
ops: [compress, decompress]
bufferSizes: [64KiB, 1MiB]      # streaming copy-buffer variants
jobConcurrency: [1, 2]
iterations: 3
gap: 200ms                      # settle between iterations
corpus: .corpus                 # decompressed-layer cache (reused across runs)
results: results                # one self-contained JSON per run
```

- A method may override which normalized levels it runs via its own `levels:`
  (builtin: `{name: kp-zstd, levels: [fast]}`; external: inside the `external:`
  block). Methods without an override use the global `levels`.
- `levels` are normalized to `fast/default/best` and mapped per method (gzip 1/6/9,
  zstd 1/3/19). For an `external` method, `rawLevels` overrides that mapping, and
  `{level}` in its command is replaced with the raw value.

## Web report

`compbench report --ui site/` writes `site/` (an `index.html` with the data
inlined, plus a vendored `chart.umd.js` — no network, no CDN). Selectors:
**environment, image, level**. Everything else is its own chart, so you compare
at a glance: compress and decompress throughput (bars per method, grouped by job
concurrency, **whiskers = ±stddev**), compression ratio, memory churn per op
(compress vs decompress; cgo/external blank — off-heap), and the buffer-size
effect. Both files load from disk and from GitHub Pages. Determinism lives in the
text report (`compbench report`), not the UI.

### Deploy to GitHub Pages (manual)

Publish to the `gh-pages` branch from the CLI:

```sh
compbench pull --repo owner/repo   # optional: merge CI results across arches first
hack/publish.sh                    # render the report and push it to gh-pages
```

`hack/publish.sh` renders the site and force-syncs it onto `gh-pages` via a
throwaway git worktree (your working tree is untouched), creating the branch on
first run. Then set Pages → Source: "Deploy from a branch" → branch `gh-pages`,
folder `/ (root)`. No Pages workflow involved.

Equivalent by hand, if you'd rather not use the script:

```sh
compbench report --results results --ui /tmp/site
git worktree add /tmp/pages -B gh-pages origin/gh-pages   # first run: git worktree add --detach /tmp/pages && git -C /tmp/pages checkout --orphan gh-pages
git -C /tmp/pages rm -rfq .
cp -r /tmp/site/. /tmp/pages/
git -C /tmp/pages add -A && git -C /tmp/pages commit -m "update report"
git -C /tmp/pages push origin gh-pages
git worktree remove /tmp/pages
```

## CI

`.github/workflows/ci.yml` runs on every push/PR: gofmt, `go vet`, build and unit
tests in both the pure-Go and `-tags cgo_zstd` modes.

`.github/workflows/bench.yml` runs the (heavy) benchmark on amd64 and arm64
runners on demand / on a schedule and uploads each run's `results/*.json` as an
artifact. Because the corpus is always `linux/amd64` layers, running on both
architectures populates the cross-arch determinism matrix. Fetch the artifacts
locally with `compbench pull`. Set `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` secrets
to avoid anonymous Docker Hub pull limits.

## Testing

```sh
go test ./...                          # hermetic: no Docker, no network
CGO_ENABLED=1 go test -tags cgo_zstd ./...
```
