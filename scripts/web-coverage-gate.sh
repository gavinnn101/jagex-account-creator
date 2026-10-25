#!/usr/bin/env bash
# Fails when HEAD has more uncovered statements in web/ than the base commit. A base with no web/package.json has 0.
# It also fails when the head tests change the tree outside their report and cache dirs.
# Usage: web-coverage-gate.sh [base commit]. The default base is the merge base with origin/main.
set -euo pipefail

# shellcheck source=scripts/file-state.sh
. "$(dirname "${BASH_SOURCE[0]}")/file-state.sh"

# node is a native Windows program there, so it needs a drive-letter path.
native_path() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# Each tree pins its own Playwright, so each one installs its own Chromium. test:unit writes web/coverage/coverage-final.json.
install_browser() { npx --prefix web playwright install chromium; }
run_tests() { npm --prefix web run test:unit; }

# Prints one `file:line` for each uncovered statement of the istanbul report $1, with the file relative to $2.
# A statement with a hit count of 0 is uncovered.
uncovered() {
  node -e '
    const fs = require("node:fs");
    const path = require("node:path");
    const [file, root] = process.argv.slice(1);
    const files = Object.values(JSON.parse(fs.readFileSync(file, "utf8")));
    // A report with no file measured nothing, so it must not pass as a count of 0.
    if (files.length === 0) {
      console.error("web coverage gate: the report " + file + " holds no file");
      process.exit(1);
    }
    for (const f of files) {
      const rel = path.relative(root, f.path).split(path.sep).join("/");
      for (const [id, hits] of Object.entries(f.s)) {
        if (hits === 0) console.log(rel + ":" + f.statementMap[id].start.line);
      }
    }
  ' "$(native_path "$1")" "$(native_path "$2")"
}

count() { wc -l <"$1" | tr -d '[:space:]'; }

# The test run itself writes in these dirs, so both checks skip them: its report, the sync of the SvelteKit plugin,
# the Vite cache, and the temp dir where Vite writes the bundled vite.config.ts and deletes it again.
# Limitation: a test that writes in one of those dirs passes.
skips=(web/coverage web/.svelte-kit web/node_modules/.vite web/node_modules/.vite-temp)
tree_state() {
  git status --porcelain --ignored --untracked-files=all -- . "${skips[@]/#/:!}"
}
run_files() { file_state "${skips[@]/#/./}"; }

root="$(native_path "$(git rev-parse --show-toplevel)")"
cd "$root"
base="$(git rev-parse --verify --short "${1:-$(git merge-base origin/main HEAD)}^{commit}")"

work="$(native_path "$(mktemp -d)")"
trap 'git worktree remove --force "$work/base" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

# Limitation: a stale web/node_modules on a dev host drives the head run. This fails toward a local run on stale dependencies, never a false CI pass, because the ci job runs npm ci first.
[ -d web/node_modules ] || npm --prefix web ci
install_browser
# A report from an earlier run must not stand in for this run.
rm -rf web/coverage

tree_before="$(tree_state)"
files_before="$(run_files)"
run_tests

files_after="$(run_files)"
tree_after="$(tree_state)"
if [ "$files_before" != "$files_after" ] || [ "$tree_before" != "$tree_after" ]; then
  if [ "$files_before" != "$files_after" ]; then
    echo 'the tests added, changed or deleted these files in the tree (path, mtime, size):' >&2
    diff <(printf '%s\n' "$files_before") <(printf '%s\n' "$files_after") >&2 || true
  fi
  if [ "$tree_before" != "$tree_after" ]; then
    echo 'the tests changed the tree state:' >&2
    diff <(printf '%s\n' "$tree_before") <(printf '%s\n' "$tree_after") >&2 || true
  fi
  echo "web coverage gate: FAIL, a test wrote in the tree" >&2
  exit 1
fi
uncovered web/coverage/coverage-final.json "$root" >"$work/head.uncovered"

git worktree add --quiet --detach "$work/base" "$base"
if [ ! -f "$work/base/web/package.json" ]; then
  : >"$work/base.uncovered"
elif ! (
  cd "$work/base" &&
    npm --prefix web ci &&
    install_browser &&
    run_tests &&
    uncovered web/coverage/coverage-final.json "$work/base" >"$work/base.uncovered"
) >"$work/base.log" 2>&1; then
  cat "$work/base.log"
  echo "web coverage gate: the run on base $base failed" >&2
  exit 1
fi

base_uncovered="$(count "$work/base.uncovered")"
head_uncovered="$(count "$work/head.uncovered")"
echo "uncovered web statements: base $base $base_uncovered, head $head_uncovered"

# Limitation: a change that removes a test and removes an equal number of uncovered statements in another place passes.
if [ "$head_uncovered" -gt "$base_uncovered" ]; then
  echo "the uncovered statements of head, as file:line:" >&2
  sort -t: -k1,1 -k2,2n -u "$work/head.uncovered" >&2
  echo "web coverage gate: FAIL, the count of uncovered statements is above the base" >&2
  exit 1
fi
echo "web coverage gate: PASS"
