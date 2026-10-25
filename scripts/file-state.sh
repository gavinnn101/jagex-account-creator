#!/usr/bin/env bash
# Sourced by the coverage gates. It is the one home of their file list.

# Prints the working dir and each path below it, except .git and the paths in the arguments, with its mtime. A file also has its size.
# A dir that holds a skipped path has no mtime, because a write in the skipped path changes it.
# The mtime of any other dir shows a file that a test creates and then deletes there.
# Limitation: a file that a test creates and then deletes in a dir with no mtime passes.
# A gate compares the list before and after its tests. A list, not a time stamp, so a clock that steps back hides no write.
file_state() {
  local prune=(-path ./.git) parents=(-false) path dir
  for path in "$@"; do
    prune+=(-o -path "$path")
    dir="${path%/*}"
    while [[ "$dir" == ./* ]]; do
      parents+=(-o -path "$dir")
      dir="${dir%/*}"
    done
  done
  find . \( "${prune[@]}" \) -prune \
    -o -type d \( "${parents[@]}" \) -printf '%p/\n' \
    -o -type d -printf '%p/ %T@\n' \
    -o -printf '%p %T@ %s\n' | LC_ALL=C sort
}
