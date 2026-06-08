#!/usr/bin/env bash
# Publish the compbench report UI to the gh-pages branch.
#
#   hack/publish.sh [results-dir]      # results-dir defaults to "results"
#
# Renders the static report from the results, then force-syncs it onto the
# gh-pages branch and pushes it. Uses a throwaway git worktree so your working
# tree is untouched. Set REMOTE to override the git remote (default: origin).
#
# Tip: run `compbench pull --repo owner/repo` first to include CI results
# (and the cross-arch determinism rows) before publishing.
set -euo pipefail

results="${1:-results}"
remote="${REMOTE:-origin}"
site="$(mktemp -d)"
wt="$(mktemp -d)"
trap 'git worktree remove --force "$wt" 2>/dev/null || true; rm -rf "$site"' EXIT

# render the static site from the results
go run . report --results "$results" --ui "$site"

# check out gh-pages into a throwaway worktree (create it if it does not exist)
if git ls-remote --exit-code --heads "$remote" gh-pages >/dev/null 2>&1; then
  git fetch "$remote" gh-pages
  git worktree add "$wt" -B gh-pages "$remote/gh-pages"
else
  git worktree add --detach "$wt"
  git -C "$wt" checkout --orphan gh-pages
fi

# replace the branch contents with the freshly rendered site
git -C "$wt" rm -rfq . 2>/dev/null || true
cp -r "$site"/. "$wt"/
touch "$wt/.nojekyll" # serve files as-is, no Jekyll processing

git -C "$wt" add -A
if git -C "$wt" diff --cached --quiet; then
  echo "no changes to publish"
else
  git -C "$wt" commit -q -m "report $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  git -C "$wt" push "$remote" gh-pages
  echo "published to $remote/gh-pages"
fi
