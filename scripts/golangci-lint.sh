#!/usr/bin/env bash
# Builds golangci-lint from source at the version that .golangci-lint-version pins, and runs it with the arguments of this script.
# Usage: golangci-lint.sh <golangci-lint arguments>, for example `golangci-lint.sh run ./...`.
set -euo pipefail

version="$(tr -d '[:space:]' <"$(git rev-parse --show-toplevel)/.golangci-lint-version")"
exec go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$version" "$@"
