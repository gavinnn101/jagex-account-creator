#!/usr/bin/env bash
# Fails when a covered mutant survives the tests and its source line has no waiver for the mutant type.
# It also fails when a waiver names a type that has no surviving mutant on its line, and on a Stryker disable or restore comment under web/src.
# Usage: mutation-gate.sh. It needs Linux, jq and the Node of web/.nvmrc. gremlins mutates the Go code, and Stryker mutates web/src.
# A waiver is `// mutation-ignore: <TYPE>[, <TYPE>]: <reason>` on the line of the mutant. In markup, the opener is `<!--`.
# The verdict ignores the thresholds of both tools. A crash or a failed baseline test run exits nonzero, and `set -e` stops the script there.
set -euo pipefail

# The run adds no go.mod entry, so this line is the only pin.
tool="github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0"

# The reason can contain a colon, so the match anchors on the type list.
# gremlins types are upper case with underscores, and Stryker types are CamelCase.
waiver='(//|<!--) mutation-ignore: ([A-Za-z_]+(, [A-Za-z_]+)*): [^[:space:]]'

# Without this check, a missing jq fails only after the full run.
command -v jq >/dev/null 2>&1 || { echo "mutation gate: jq is not installed" >&2; exit 1; }

cd "$(git rev-parse --show-toplevel)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
report="$work/report.json"
stryker_report=web/reports/mutation/mutation.json

# Stryker reports a disabled mutant as Ignored and never runs it, so no stale check can see the disable.
# Limitation: the match also refuses a comment that only names the directive.
git grep -n --untracked -E 'Stryker (disable|restore)' -- web/src >"$work/disabled.txt" || [ $? -eq 1 ]
if [ -s "$work/disabled.txt" ]; then
  cat "$work/disabled.txt"
  echo "mutation gate: FAIL, a Stryker disable or restore comment hides mutants. Use a mutation-ignore waiver" >&2
  exit 1
fi

# gremlins copies its module root with no filter, so it runs on a copy of the working tree without the ignored paths, such as web/node_modules.
# A file that the working tree deleted is not in the copy, so tar skips it.
mkdir "$work/tree"
git ls-files -z --cached --others --exclude-standard | tar --null --ignore-failed-read -T - -c | tar -x -C "$work/tree"
(
  cd "$work/tree"
  # The test cache makes the baseline time too short, and then most mutants time out.
  export GOFLAGS=-count=1
  # This compiles each test binary and runs no test, so the measured baseline includes no cold compile.
  go test -cover -run '^$' ./... >/dev/null
  # Limitation: the default mutators change only conditions and arithmetic.
  # Each mutant's timeout must cover the CLAUDE.md guard cap plus a test build under full worker load. The baseline holds neither.
  # Limitation: the timeout still scales with host speed. A baseline under about 1s fails toward a false TIMED OUT.
  # The code that oapi-codegen generates gets no mutants. The path is relative to the module root. A second generated file adds its own flag.
  go run "$tool" unleash --timeout-coefficient 15 --exclude-files '^internal/api/api\.gen\.go$' -o "$report" .
)

# Limitation: the gate does not run on Windows. gremlins marks each mutant NOT COVERED there, and this check fails.
killed="$(jq '.mutants_killed' "$report")"
[ "$killed" -gt 0 ] || { echo "mutation gate: FAIL, the gremlins run killed no mutant, so it measured nothing" >&2; exit 1; }

# Stryker runs the browser-mode tests, so it needs the dependencies of web/package.json and the Chromium of Playwright.
# Limitation: a stale web/node_modules on a dev host drives Stryker. This fails toward a local run on stale dependencies, never a false CI pass, because in CI the dir comes from npm ci of the same commit.
[ -d web/node_modules ] || npm --prefix web ci
npx --prefix web playwright install chromium
# A report from an earlier run must not stand in for this run.
rm -f "$stryker_report"
npm --prefix web run mutate

stryker_killed="$(jq '[.files[].mutants[] | select(.status == "Killed")] | length' "$stryker_report")"
[ "$stryker_killed" -gt 0 ] || { echo "mutation gate: FAIL, the Stryker run killed no mutant, so it measured nothing" >&2; exit 1; }

# One survivors file holds the rows of both tools, so one loop checks each waiver. Each file is relative to the repo root.
# A pipeline with pipefail fails the script when jq fails. A process substitution does not.
jq -r '.files[] | .file_name as $f | .mutations[]
  | select(.status == "LIVED" or .status == "TIMED OUT")
  | [$f, .line, .type, .status] | @tsv' "$report" >"$work/survivors.raw"
# Stryker names each file relative to web/. A NoCoverage mutant goes to the web coverage gate, as NOT COVERED goes to the Go one.
jq -r '.files | to_entries[] | .key as $f | .value.mutants[]
  | select(.status == "Survived" or .status == "Timeout")
  | ["web/" + $f, .location.start.line, .mutatorName, .status] | @tsv' "$stryker_report" >>"$work/survivors.raw"
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

# A waived type with no surviving mutant on its line waives nothing.
# Limitation: gremlins reads only the files that Linux builds, so a waiver in a _windows.go file counts as stale.
# A git pathspec `*` also matches a `/`, so web/src/*.ts reaches each subdir.
git grep -n --untracked -E "$waiver" -- '*.go' 'web/src/*.ts' 'web/src/*.svelte' >"$work/waivers.txt" || [ $? -eq 1 ]
while IFS=: read -r file line src; do
  [[ "$src" =~ $waiver ]] || { echo "mutation gate: bash does not match the waiver that git grep found at $file:$line" >&2; exit 1; }
  for type in ${BASH_REMATCH[2]//,/}; do
    if ! awk -F'\t' -v f="$file" -v l="$line" -v t="$type" '$1 == f && $2 == l && $3 == t { found = 1 } END { exit !found }' "$work/survivors.tsv"; then
      echo "$file:$line $type stale waiver"
      findings=$((findings + 1))
    fi
  done
done <"$work/waivers.txt"

echo "mutants killed: gremlins $killed, Stryker $stryker_killed, findings: $findings"
if [ "$findings" -gt 0 ]; then
  echo "mutation gate: FAIL, a mutant survived with no waiver on its line, or a waiver names no surviving mutant" >&2
  exit 1
fi
echo "mutation gate: PASS"
