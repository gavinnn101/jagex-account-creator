#!/usr/bin/env bash
# Fails when a function has no caller. Production code needs a caller from main. A `*test` helper package needs a caller in another package's test.
# Usage: deadcode-gate.sh.
set -euo pipefail

# The run adds no go.mod entry, so this line is the only pin. The pin must not need a newer Go than the `go` line of go.mod.
tool="golang.org/x/tools/cmd/deadcode@v0.52.0"

# Prints a regular expression that matches exactly the import paths on stdin.
paths_re() { sed 's/[][\\.^$*+?(){}|]/\\&/g' | paste -sd'|' - | sed 's/.*/^(&)$/'; }

cd "$(git rev-parse --show-toplevel)"
pkgs="$(go list ./...)"
# Limitation: a production package whose import path ends in "test" is checked as a helper, so a function that only tests call passes.
prod="$(awk '!/test$/' <<<"$pkgs")"

# deadcode exits 0 on a finding, so the gate fails on any output.
out="$(go run "$tool" -filter "$(paths_re <<<"$prod")" ./...)"
# The helper packages are not arguments, so their own test binaries are not roots.
helpers="$(go run "$tool" -test -filter "$(awk '/test$/' <<<"$pkgs" | paths_re)" $prod)"

if [ -n "$out$helpers" ]; then
  printf '%s\n' "$out" "$helpers" | sed '/^$/d'
  echo "deadcode gate: FAIL" >&2
  exit 1
fi
echo "deadcode gate: PASS"
