#!/usr/bin/env bash
# Sourced by mutation-gate.sh. It is the one home of the files that a run of the gate mutates.
# It defines functions only, so no exit here can end the gate before its trap.

# Writes the scope of one run into the dir $2. $1 is a base commit, or `full` for every file. It runs in the repo root.
# go.mode and web.mode each hold `full` or `scoped`. go.files and web.files hold the files that each half mutates.
# go.outside holds the Go files of the copy that go.files does not hold. tree.z holds the files of the copy.
# Each list holds paths relative to the repo root, each with a NUL after it.
# Limitation: a change that weakens a test outside the dirs of its changed files passes. Only a full run catches it.
mutation_scope() {
  local base="$1" out="$2" path dir trigger
  # A change to one of these files can change the verdict on any file, so it runs both halves in full.
  local triggers=(scripts/mutation-gate.sh scripts/mutation-scope.sh web/stryker.config.json web/patch-stryker-runner.ts web/vite.config.ts web/package.json web/package-lock.json go.mod)
  local go_mode=scoped web_mode=scoped
  local -a tree=() changed=() go_files=() go_outside=() web_files=()
  local -A touched=() go_test_dirs=() web_test_dirs=()

  mkdir -p "$out" || return 1
  # The gate copies this list for gremlins, so the scope and the copy hold the same files.
  git ls-files -z --cached --others --exclude-standard >"$out/tree.z" || return 1
  mapfile -d '' tree <"$out/tree.z"

  if [ "$base" = full ]; then
    go_mode=full
    web_mode=full
  else
    # No --diff-filter, so a deleted file counts. With no rename detection, the old path of a rename counts as deleted.
    git diff --name-only -z --no-renames --merge-base "$base" >"$out/changed.z" || return 1
    git ls-files -z --others --exclude-standard >>"$out/changed.z" || return 1
    mapfile -d '' changed <"$out/changed.z"
  fi

  for path in "${changed[@]}"; do
    touched["$path"]=1
    for trigger in "${triggers[@]}"; do
      if [ "$path" = "$trigger" ]; then
        go_mode=full
        web_mode=full
      fi
    done
    mutation_dir "$path"
    case "$path" in
      *.go)
        # Go builds no testdata dir. So no test covers a file there, and a fixture dir is not a helper package.
        [[ "/$path" != */testdata/* ]] || continue
        # A *test package serves the tests of other packages, so a change there runs the full Go half.
        if [[ "${dir##*/}" == *test ]]; then
          go_mode=full
        elif [[ "$path" == *_test.go ]]; then
          go_test_dirs["$dir"]=1
        fi
        ;;
      web/src/*)
        if [[ "$path" =~ ^web/src/lib/[^/]*test/ ]]; then
          web_mode=full
        elif [[ "$path" == *.test.ts ]]; then
          web_test_dirs["$dir"]=1
        fi
        ;;
    esac
  done

  # A changed test, deleted ones included, puts each source file of its dir in the scope.
  for path in "${tree[@]}"; do
    [ -f "$path" ] || continue
    mutation_dir "$path"
    case "$path" in
      *_test.go | internal/api/api.gen.go | web/src/*.test.ts | web/src/lib/api.gen.d.ts) ;;
      *.go)
        # Limitation: a file behind a non-Linux build tag gets no covered mutant, as in a full run.
        if [ "$go_mode" = full ] || { [[ "/$path" != */testdata/* ]] && [ -n "${touched[$path]:-}${go_test_dirs[$dir]:-}" ]; }; then
          go_files+=("$path")
        else
          go_outside+=("$path")
        fi
        ;;
      web/src/*.ts | web/src/*.svelte)
        if [ "$web_mode" = full ] || [ -n "${touched[$path]:-}${web_test_dirs[$dir]:-}" ]; then
          web_files+=("$path")
        fi
        ;;
    esac
  done

  printf '%s\n' "$go_mode" >"$out/go.mode"
  printf '%s\n' "$web_mode" >"$out/web.mode"
  mutation_list "$out/go.files" "${go_files[@]}"
  mutation_list "$out/go.outside" "${go_outside[@]}"
  mutation_list "$out/web.files" "${web_files[@]}"
}

# Sets dir to the dir of the path $1, or to . for a file in the repo root.
mutation_dir() {
  dir=.
  [[ "$1" != */* ]] || dir="${1%/*}"
}

# Writes each argument after $1 to the file $1, each with a NUL after it. With no such argument, the file is empty.
mutation_list() {
  local file="$1"
  shift
  : >"$file"
  [ "$#" -eq 0 ] || printf '%s\0' "$@" >"$file"
}
