#!/usr/bin/env bash
# Runs the steps of one CI job in order and stops at the first failed step. The last line names that step.
# Usage: ci-job.sh <ci|ci-windows|mutation|vuln> [base commit]. The base commit goes to the coverage gate.
# The workflows and scripts/local-ci.sh both call this script, so it is the one list of the steps of each job.
set -euo pipefail

usage="usage: ci-job.sh <ci|ci-windows|mutation|vuln> [base commit]"
job="${1:?$usage}"
base="${2:-}"

cd "$(git rev-parse --show-toplevel)"

# Each host builds and tests with the Go of the `go` line, as setup-go does on CI.
GOTOOLCHAIN="go$(go list -m -f '{{.GoVersion}}')"
export GOTOOLCHAIN

step() {
  local name="$1"
  shift
  echo "ci-job: $job: step $name"
  "$@" || { echo "ci-job: $job: FAIL at step $name" >&2; exit 1; }
}

# web/.nvmrc is the one Node pin. Each host installs it, and this check fails a host that runs another Node.
node_pin() {
  local want have
  want="v$(tr -d '[:space:]' <web/.nvmrc)"
  have="$(node --version)"
  [ "$have" = "$want" ] || { echo "ci-job: node is $have, web/.nvmrc pins $want" >&2; return 1; }
}

# Fails when internal/api/api.gen.go or the web types are not the generator output of the committed spec.
api_drift() { go generate ./internal/api && npm --prefix web run gen:api && git diff --exit-code -- internal/api web/src; }

coverage() { bash scripts/coverage-gate.sh ${base:+"$base"}; }

web_coverage() { bash scripts/web-coverage-gate.sh ${base:+"$base"}; }

# The binary goes to a temp dir, so the run leaves the tree as the checkout made it.
e2e() {
  local dir rc=0
  dir="$(mktemp -d)"
  go build -o "$dir/creator" ./cmd/creator && CREATOR_BIN="$dir/creator" npm --prefix web run test:e2e || rc=$?
  rm -rf "$dir"
  return "$rc"
}

case "$job" in
  ci)
    step node node_pin
    # The Go steps run before the first npm step, so they see the tree of the checkout alone, as in ci-windows.
    # They need no dashboard build, because the embed builds with the tracked web/dist/.gitkeep alone.
    step tidy go mod tidy -diff
    step build go build ./...
    step coverage coverage
    # `run` checks only the files that the GOOS of this job builds. `fmt --diff` reads
    # every Go file.
    step lint bash scripts/golangci-lint.sh run
    step fmt bash scripts/golangci-lint.sh fmt --diff
    step deadcode bash scripts/deadcode-gate.sh
    step npm-ci npm --prefix web ci
    step api-drift api_drift
    step web-check npm --prefix web run check
    step web-lint npm --prefix web run lint
    step web-format npm --prefix web run format:check
    step knip npm --prefix web run knip
    step web-coverage web_coverage
    # Only the e2e binary embeds the build, so the build runs with the e2e steps, after the web gates.
    step web-build npm --prefix web run build
    step playwright npx --prefix web playwright install chromium
    step e2e e2e
    ;;
  # The `_windows.go` files sit behind OS build tags, so the Linux job never
  # compiles, tests, lints, reachability-checks, or measures the coverage of them.
  # The job needs no Node, because the embed of web/dist builds with the tracked .gitkeep alone.
  ci-windows)
    step build go build ./...
    step coverage coverage
    step lint bash scripts/golangci-lint.sh run
    step deadcode bash scripts/deadcode-gate.sh
    ;;
  # A separate job, so a pull request waits for the slower job and not for the sum.
  mutation)
    step node node_pin
    step mutation bash scripts/mutation-gate.sh
    ;;
  vuln)
    step node node_pin
    step vuln bash scripts/vuln-gate.sh
    step npm-ci npm --prefix web ci
    # The bundled libraries of the dashboard are dev dependencies, so the audit covers them too.
    step npm-audit npm --prefix web audit --audit-level=high
    ;;
  *)
    echo "$usage" >&2
    exit 2
    ;;
esac
echo "ci-job: $job: PASS"
