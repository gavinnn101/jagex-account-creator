#!/usr/bin/env bash
# Fails when the code calls a symbol that has a known vulnerability.
# Usage: vuln-gate.sh.
set -euo pipefail

# The run adds no go.mod entry, so this line is the only pin.
tool="golang.org/x/vuln/cmd/govulncheck@v1.9.0"

cd "$(git rev-parse --show-toplevel)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# `go run` builds the tool for GOOS, so it cannot scan a different OS.
GOBIN="$work" go install "$tool"

# Limitation: govulncheck has no ignore flag. A called advisory with no fixed release blocks each merge.
# Limitation: the advisory database has no entry for the github.com/sardanioss/* forks. The scan misses an upstream advisory that applies to them.
# Limitation: the scan covers linux and windows. It misses a darwin-only finding.
failed=0
for os in linux windows; do
  echo "vuln gate: GOOS=$os"
  # Each scan runs, so one run lists the findings of each OS.
  GOOS="$os" "$work/govulncheck" ./... || failed=1
done

[ "$failed" -eq 0 ] || { echo "vuln gate: FAIL" >&2; exit 1; }
echo "vuln gate: PASS"
