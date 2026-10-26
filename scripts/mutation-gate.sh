#!/usr/bin/env bash
# Fails when a covered mutant survives the tests and its source line has no waiver for the mutant type.
# It also fails when a waiver names a type that has no surviving mutant on its line, and on a Stryker disable or restore comment under web/src.
# Usage: mutation-gate.sh [base commit|full]. The default base is the merge base with origin/main. It needs Linux, jq and the Node of web/.nvmrc.
# gremlins mutates the Go code, and Stryker mutates web/src. With a base, each half mutates only the files that scripts/mutation-scope.sh selects. With full, each half mutates every file.
# A waiver is `// mutation-ignore: <TYPE>[, <TYPE>]: <reason>` on the line of the mutant. In markup, the opener is `<!--`.
# The verdict ignores the thresholds of both tools. A crash or a failed baseline test run exits nonzero, and `set -e` stops the script there.
set -euo pipefail

# shellcheck source=scripts/mutation-scope.sh
. "$(dirname "${BASH_SOURCE[0]}")/mutation-scope.sh"

# The run adds no go.mod entry, so this line is the only pin.
tool="github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0"

# The reason can contain a colon, so the match anchors on the type list.
# gremlins types are upper case with underscores, and Stryker types are CamelCase.
waiver='(//|<!--) mutation-ignore: ([A-Za-z_]+(, [A-Za-z_]+)*): [^[:space:]]'

# gremlins marks each mutant NOT COVERED on Windows, so a run there measures nothing.
[ "$(uname -s)" = Linux ] || { echo "mutation gate: the gate needs Linux, and this host is $(uname -s)" >&2; exit 1; }

# Without this check, a missing jq fails only after the full run.
command -v jq >/dev/null 2>&1 || { echo "mutation gate: jq is not installed" >&2; exit 1; }

cd "$(git rev-parse --show-toplevel)"
if [ "${1:-}" = full ]; then
  base=full
else
  base="$(git rev-parse --verify --short "${1:-$(git merge-base origin/main HEAD)}^{commit}")"
fi
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
report="$work/report.json"
stryker_report=web/reports/mutation/mutation.json

mutation_scope "$base" "$work/scope"
go_mode="$(cat "$work/scope/go.mode")"
web_mode="$(cat "$work/scope/web.mode")"
mapfile -d '' go_files <"$work/scope/go.files"
mapfile -d '' go_outside <"$work/scope/go.outside"
mapfile -d '' web_files <"$work/scope/web.files"
echo "mutation gate: base $base, Go $go_mode with ${#go_files[@]} files, web $web_mode with ${#web_files[@]} files"

# Stryker reports a disabled mutant as Ignored and never runs it, so no stale check can see the disable.
# Limitation: the match also refuses a comment that only names the directive.
git grep -n --untracked -E 'Stryker (disable|restore)' -- web/src >"$work/disabled.txt" || [ $? -eq 1 ]
if [ -s "$work/disabled.txt" ]; then
  cat "$work/disabled.txt"
  echo "mutation gate: FAIL, a Stryker disable or restore comment hides mutants. Use a mutation-ignore waiver" >&2
  exit 1
fi

: >"$work/survivors.raw"
# The pathspecs of the files that the run mutated. The stale-waiver check reads only these files.
waiver_paths=()

if [ "$go_mode" = scoped ] && [ "${#go_files[@]}" -eq 0 ]; then
  go_result="gremlins skipped"
  echo "mutation gate: gremlins skipped, the change touches no Go source file and no Go test"
else
  # The code that oapi-codegen generates gets no mutants. The path is relative to the module root. A second generated file adds its own flag.
  excludes=(--exclude-files '^internal/api/api\.gen\.go$')
  if [ "${#go_outside[@]}" -gt 0 ]; then
    # gremlins excludes a file that any of its regexes matches, so one alternation holds every file outside the scope.
    excludes+=(--exclude-files "^($(printf '%s\n' "${go_outside[@]}" | sed 's/[][\.^$|?*+(){}]/\\&/g' | paste -sd '|'))\$")
  fi
  # gremlins copies its module root with no filter, so it runs on a copy of the working tree without the ignored paths, such as web/node_modules.
  # A file that the working tree deleted is not in the copy, so tar skips it.
  mkdir "$work/tree"
  tar --null --ignore-failed-read -T "$work/scope/tree.z" -c | tar -x -C "$work/tree"
  (
    cd "$work/tree"
    # The test cache makes the baseline time too short, and then most mutants time out.
    export GOFLAGS=-count=1
    # This compiles each test binary and runs no test, so the measured baseline includes no cold compile.
    go test -cover -run '^$' ./... >/dev/null
    # Limitation: the default mutators change only conditions and arithmetic.
    # Each mutant's timeout must cover the CLAUDE.md guard cap plus a test build under full worker load. The baseline holds neither.
    # Limitation: the timeout still scales with host speed. A baseline under about 1s fails toward a false TIMED OUT.
    go run "$tool" unleash --timeout-coefficient 15 "${excludes[@]}" -o "$report" . 2>&1 | tee "$work/gremlins.log"
  )
  # gremlins writes no report when it finds no mutant, for example in a file behind a non-Linux build tag. It then logs this line.
  if [ ! -f "$report" ]; then
    grep -qxF 'No results to report.' "$work/gremlins.log" || { echo "mutation gate: FAIL, gremlins wrote no report" >&2; exit 1; }
    echo '{"mutants_killed": 0, "files": []}' >"$report"
  fi
  killed="$(jq '.mutants_killed' "$report")"
  go_result="gremlins killed $killed"
  if [ "$go_mode" = full ]; then
    # The whole module always has a mutant that its tests kill. A full run that kills none had a coverage step that measured nothing.
    # A scoped run can kill none, because each mutant of a file that needs a live Chrome is NOT COVERED.
    [ "$killed" -gt 0 ] || { echo "mutation gate: FAIL, the full gremlins run killed no mutant, so it measured nothing" >&2; exit 1; }
    waiver_paths+=('*.go')
  else
    for path in "${go_files[@]}"; do waiver_paths+=(":(literal)$path"); done
  fi
  # A pipeline with pipefail fails the script when jq fails. A process substitution does not.
  jq -r '.files[] | .file_name as $f | .mutations[]
    | select(.status == "LIVED" or .status == "TIMED OUT")
    | [$f, .line, .type, .status] | @tsv' "$report" >>"$work/survivors.raw"
fi

if [ "$web_mode" = scoped ] && [ "${#web_files[@]}" -eq 0 ]; then
  web_result="Stryker skipped"
  echo "mutation gate: Stryker skipped, the change touches no web/src source file and no web/src test"
else
  # Stryker runs the browser-mode tests, so it needs the dependencies of web/package.json and the Chromium of Playwright.
  # Limitation: a stale web/node_modules on a dev host drives Stryker. This fails toward a local run on stale dependencies, never a false CI pass, because in CI the dir comes from npm ci of the same commit.
  [ -d web/node_modules ] || npm --prefix web ci
  npx --prefix web playwright install chromium
  # A report from an earlier run must not stand in for this run.
  rm -f "$stryker_report"
  if [ "$web_mode" = full ]; then
    npm --prefix web run mutate
  else
    # Stryker splits the list at each comma, so a path with a comma cannot be in it.
    for path in "${web_files[@]}"; do
      [[ "$path" != *,* ]] || { echo "mutation gate: Stryker cannot take the path $path, because it holds a comma" >&2; exit 1; }
    done
    # A --mutate list replaces the list of web/stryker.config.json. Each item is a glob relative to web/, so a bracket class escapes each glob character, such as the [ of a route dir.
    mutate_list="$(printf '%s\n' "${web_files[@]#web/}" | sed 's/[][*?{}()]/[&]/g' | paste -sd ',')"
    npm --prefix web run mutate -- --mutate "$mutate_list"
  fi
  stryker_killed="$(jq '[.files[].mutants[] | select(.status == "Killed")] | length' "$stryker_report")"
  web_result="Stryker killed $stryker_killed"
  if [ "$web_mode" = full ]; then
    # All of web/src always has a mutant that its tests kill. A full run that kills none measured nothing.
    # A scoped run can kill none, because each mutant of its files can be NoCoverage.
    [ "$stryker_killed" -gt 0 ] || { echo "mutation gate: FAIL, the full Stryker run killed no mutant, so it measured nothing" >&2; exit 1; }
    # A git pathspec `*` also matches a `/`, so web/src/*.ts reaches each subdir.
    waiver_paths+=('web/src/*.ts' 'web/src/*.svelte')
  else
    for path in "${web_files[@]}"; do waiver_paths+=(":(literal)$path"); done
  fi
  # Stryker names each file relative to web/. A NoCoverage mutant goes to the web coverage gate, as NOT COVERED goes to the Go one.
  jq -r '.files | to_entries[] | .key as $f | .value.mutants[]
    | select(.status == "Survived" or .status == "Timeout")
    | ["web/" + $f, .location.start.line, .mutatorName, .status] | @tsv' "$stryker_report" >>"$work/survivors.raw"
fi

# One survivors file holds the rows of both tools, so one loop checks each waiver. Each file is relative to the repo root.
sort -u -t $'\t' -k1,1 -k2,2n -k3 "$work/survivors.raw" >"$work/survivors.tsv"

# Limitation: a waived type covers each site of that type on the line.
# Limitation: only review checks the reason of a waiver.
findings=0
while IFS=$'\t' read -r file line type status; do
  src="$(sed -n "${line}p" "$file")"
  needle=", $type, "
  if [[ "$src" =~ $waiver ]] && [[ ", ${BASH_REMATCH[2]}, " == *"$needle"* ]]; then
    continue
  fi
  echo "$file:$line $type $status"
  findings=$((findings + 1))
done <"$work/survivors.tsv"

# A waived type with no surviving mutant on its line waives nothing. Only a file that the run mutated can show that.
# Limitation: gremlins reads only the files that Linux builds, so a waiver in a _windows.go file counts as stale.
: >"$work/waivers.txt"
if [ "${#waiver_paths[@]}" -gt 0 ]; then
  git grep -n --untracked -E "$waiver" -- "${waiver_paths[@]}" >"$work/waivers.txt" || [ $? -eq 1 ]
fi
while IFS=: read -r file line src; do
  [[ "$src" =~ $waiver ]] || { echo "mutation gate: bash does not match the waiver that git grep found at $file:$line" >&2; exit 1; }
  for type in ${BASH_REMATCH[2]//,/}; do
    if ! awk -F'\t' -v f="$file" -v l="$line" -v t="$type" '$1 == f && $2 == l && $3 == t { found = 1 } END { exit !found }' "$work/survivors.tsv"; then
      echo "$file:$line $type stale waiver"
      findings=$((findings + 1))
    fi
  done
done <"$work/waivers.txt"

echo "mutation gate: Go $go_mode, ${#go_files[@]} files, $go_result. Web $web_mode, ${#web_files[@]} files, $web_result. Findings: $findings"
if [ "$findings" -gt 0 ]; then
  echo "mutation gate: FAIL, a mutant survived with no waiver on its line, or a waiver names no surviving mutant" >&2
  exit 1
fi
echo "mutation gate: PASS"
