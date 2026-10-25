#!/usr/bin/env bash
# Fails when HEAD has more uncovered statements than the base commit.
# It also fails when the head tests leave a path in their user cache dir or change the tree.
# It also fails when the head tests run every statement of a function that carries coverage-ignore.
# Usage: coverage-gate.sh [base commit]. The default base is the merge base with origin/main.
set -euo pipefail

# shellcheck source=scripts/file-state.sh
. "$(dirname "${BASH_SOURCE[0]}")/file-state.sh"

# The pin moves with the `go` line of go.mod, because a newer release of the tool needs a newer Go and the CI runner does not download a toolchain.
tool="github.com/vladopajic/go-test-coverage/v2@v2.20.0"

# go is a native Windows program there, so it needs a drive-letter path.
native_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# The build cache defaults to a dir under the user cache dir, so it must not move with the tests' cache dir.
GOCACHE="$(go env GOCACHE)"
export GOCACHE

# Both commits use this one command, because the covered set depends on the flags.
# $2 is an empty dir that the tests get as the user cache dir. HOME stays, so GOPATH and the go config stay.
# -count=1 runs each test, because a cached pass replays no write.
run_tests() {
  XDG_CACHE_HOME="$(native_path "$2")" LOCALAPPDATA="$(native_path "$2")" go test -count=1 -race -coverprofile="$1" ./...
}

# Prints the count of uncovered statements in a breakdown file. Each line is `name;total;covered`.
uncovered() {
  # The tool passes without a comparison when a breakdown is empty.
  [ -s "$1" ] || { echo "coverage gate: the breakdown file $1 is empty" >&2; exit 1; }
  awk -F';' '{ sum += $2 - $3 } END { print sum + 0 }' "$1"
}

# Lists every path in the tree, ignored files one by one, so a change in an ignored dir shows.
tree_state() {
  git status --porcelain --ignored --untracked-files=all
}

root="$(native_path "$(git rev-parse --show-toplevel)")"
cd "$root"
base="$(git rev-parse --verify --short "${1:-$(git merge-base origin/main HEAD)}^{commit}")"
config="$root/.testcoverage.yml"

work="$(native_path "$(mktemp -d)")"
trap 'git worktree remove --force "$work/base" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
mkdir "$work/head-cache" "$work/base-cache"

tree_before="$(tree_state)"
files_before="$(file_state)"
run_tests "$work/head.out" "$work/head-cache"

# Limitation: the check cannot see a write under os.TempDir, where t.TempDir also lives, or a write in the user cache dir that the test removes again.
leftovers="$(cd "$work/head-cache" && find . -mindepth 1 | sort)"
files_after="$(file_state)"
tree_after="$(tree_state)"
if [ -n "$leftovers" ] || [ "$files_before" != "$files_after" ] || [ "$tree_before" != "$tree_after" ]; then
  [ -z "$leftovers" ] || printf 'the tests left these paths in their user cache dir:\n%s\n' "$leftovers" >&2
  if [ "$files_before" != "$files_after" ]; then
    echo 'the tests added, changed or deleted these files in the tree (path, mtime, size):' >&2
    diff <(printf '%s\n' "$files_before") <(printf '%s\n' "$files_after") >&2 || true
  fi
  if [ "$tree_before" != "$tree_after" ]; then
    echo 'the tests changed the tree state:' >&2
    diff <(printf '%s\n' "$tree_before") <(printf '%s\n' "$tree_after") >&2 || true
  fi
  echo "coverage gate: FAIL, a test wrote outside its temp dir" >&2
  exit 1
fi

# A function that the head tests run in full has a test, so its annotation hides nothing.
# The tool honors any comment that contains the word, so the grep has no colon.
# Limitation: a job sees only the files that its GOOS builds, so each OS job checks its own files.
module="$(go list -m)"
go tool cover -func="$work/head.out" >"$work/head.func"
git grep -n --untracked 'coverage-ignore' -- '*.go' >"$work/annotations.txt" || [ $? -eq 1 ]
stale=0
while IFS=: read -r file line _; do
  # The function of an annotation is the last one that starts at or above its line.
  pct="$(awk -F'\t+' -v f="$module/$file" -v l="$line" '{ split($1, a, ":") }
    a[1] == f && a[2] + 0 <= l + 0 && a[2] + 0 > best { best = a[2] + 0; pct = $NF } END { print pct }' "$work/head.func")"
  if [ "$pct" = "100.0%" ]; then
    echo "$file:$line stale coverage-ignore" >&2
    stale=$((stale + 1))
  fi
done <"$work/annotations.txt"
if [ "$stale" -gt 0 ]; then
  echo "coverage gate: FAIL, the head tests run every statement of a function that carries coverage-ignore" >&2
  exit 1
fi

git worktree add --quiet --detach "$work/base" "$base"
if ! (
  cd "$work/base" &&
    run_tests "$work/base.out" "$work/base-cache" &&
    go run "$tool" --config "$config" --profile "$work/base.out" --breakdown-file-name "$work/base.breakdown"
) >"$work/base.log" 2>&1; then
  cat "$work/base.log"
  echo "coverage gate: the run on base $base failed" >&2
  exit 1
fi

# The exit of this run carries only the check that each coverage-ignore comment has a reason.
go run "$tool" --config "$config" --profile "$work/head.out" --breakdown-file-name "$work/head.breakdown"

base_uncovered="$(uncovered "$work/base.breakdown")"
head_uncovered="$(uncovered "$work/head.breakdown")"
echo "uncovered statements: base $base $base_uncovered, head $head_uncovered"

# Limitation: a change that removes a test and removes an equal number of uncovered statements in another place passes.
# Limitation: review holds an annotated function that the tests run in part.
# Limitation: the run sets no -coverpkg, so review holds an annotated function that only the tests of another package run.
if [ "$head_uncovered" -gt "$base_uncovered" ]; then
  echo "coverage gate: FAIL, the count of uncovered statements is above the base" >&2
  exit 1
fi
echo "coverage gate: PASS"
