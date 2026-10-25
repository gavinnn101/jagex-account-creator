#!/usr/bin/env bash
# Runs the four CI jobs on the head of a pull request. It posts the result of each job as a commit status on that head.
# Usage: local-ci.sh <pull request number>. It runs in Git Bash on Windows and needs gh and the WSL distro below.
# It refuses a pull request that is not open, an active workflow, and a head that does not contain its base. A refusal exits 2 and posts no status.
set -euo pipefail

# The Linux jobs run in this distro, because it has Go, git, jq, gcc and nvm.
distro="Ubuntu-24.04"
# The protection on the default branch requires these names. The jobs run in this order, so mutation runs last and alone.
contexts=(ci-windows ci vuln mutation)

pr="${1:?usage: local-ci.sh <pull request number>}"

refuse() {
  echo "local-ci: refused: $1" >&2
  exit 2
}

# Prints a path in the Windows spelling, so a path in a status or a log line opens on Windows.
win_path() { cygpath -w "$1"; }

# Runs the script on stdin in the distro. Without MSYS_NO_PATHCONV, Git Bash rewrites an argument that starts with /.
wsl_bash() { MSYS_NO_PATHCONV=1 wsl.exe -d "$distro" -- bash -s -- "$@"; }

cd "$(git rev-parse --show-toplevel)"
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
pr_fields="$(gh pr view "$pr" --json state,headRefName,headRefOid,baseRefName -q '[.state, .headRefName, .headRefOid, .baseRefName] | @tsv')"
IFS=$'\t' read -r state head_ref sha base_ref <<<"$pr_fields"
[ "$state" = OPEN ] || refuse "pull request #$pr is $state"

# A failed check run of a workflow blocks the merge, also beside a passing status of the same name.
# Dependabot lists its updates as the active workflow dynamic/dependabot/dependabot-updates, which has no workflow file.
active="$(gh workflow list --all --json path,state -q '.[] | select(.state == "active" and (.path | startswith(".github/workflows/"))) | .path')"
[ -z "$active" ] || refuse "these workflows are active: $active"

git fetch --quiet origin
fetched="$(git rev-parse --verify --quiet "refs/remotes/origin/$head_ref^{commit}" || true)"
[ "$fetched" = "$sha" ] || refuse "origin/$head_ref is at ${fetched:-nothing}, not at the head $sha of the pull request"
base_sha="$(git rev-parse --verify "refs/remotes/origin/$base_ref^{commit}")"
# CI measures coverage against HEAD^1 of the merge commit. When the head contains the base, that commit is the base tip.
git merge-base --is-ancestor "$base_sha" "$sha" || refuse "the head $sha does not contain origin/$base_ref. Update the branch"

# The logs stay after the run, so a failed job can be read.
if [ -n "${CLAUDE_SCRATCHPAD_DIR:-}" ]; then
  run_dir="$(mktemp -d "$(cygpath -u "$CLAUDE_SCRATCHPAD_DIR")/local-ci-$pr-XXXXXX")"
else
  run_dir="$(mktemp -d)"
fi
echo "local-ci: pull request #$pr, head $sha, base $base_sha, logs in $(win_path "$run_dir")"

post() {
  gh api --silent "repos/$repo/statuses/$sha" -f context="$1" -f state="$2" -f description="$3"
}

# The index of the first context that has no result yet. The jobs run in the order of contexts.
next=0
win_dir=""
wsl_dir=""

# An early exit posts error for each context with no result, so no context stays pending.
finish() {
  local rc=$? context
  for context in "${contexts[@]:$next}"; do
    post "$context" error "the run stopped before this job ended" || true
  done
  [ -z "$wsl_dir" ] || wsl_bash "$wsl_dir" <<<'rm -rf "$1"' || true
  [ -z "$win_dir" ] || git worktree remove --force "$win_dir" || true
  exit "$rc"
}
trap finish EXIT

for context in "${contexts[@]}"; do
  post "$context" pending "running on the local host"
done

# Prints HEAD, then each path that the tree holds beyond the commit, in the spelling of coverage-gate.sh.
checkout_state='git -C "$1" rev-parse HEAD && git -C "$1" status --porcelain --ignored --untracked-files=all'

# A status names a commit, so each job runs only in a clean checkout of that commit.
check_checkout() {
  local at
  at="$(head -n 1 <<<"$2")"
  [ "$at" = "$sha" ] || { echo "local-ci: the $1 checkout is at ${at:-nothing}, not at $sha" >&2; exit 1; }
  [ -z "$(tail -n +2 <<<"$2")" ] || { printf 'local-ci: the %s checkout is not clean:\n%s\n' "$1" "$(tail -n +2 <<<"$2")" >&2; exit 1; }
}

# Each run uses its own checkouts, so a killed run and the next run never share a tree.
make_win_checkout() {
  git worktree prune
  git worktree add --quiet --detach "$run_dir/win" "$sha"
  win_dir="$run_dir/win"
  check_checkout Windows "$(bash -c "$checkout_state" _ "$win_dir")"
}

# The clone fetches the head and base branches by name from the Windows clone, so the coverage gate finds the base commit.
make_wsl_checkout() {
  wsl_dir="$(wsl_bash "$(git rev-parse --path-format=absolute --git-common-dir)" "$head_ref" "$base_ref" "$sha" <<'EOF'
set -euo pipefail
dir="$(mktemp -d /tmp/local-ci.XXXXXX)"
echo "$dir"
git -c init.defaultBranch=main init --quiet "$dir"
git -C "$dir" fetch --quiet "$(wslpath -u "$1")" "+refs/remotes/origin/$2:refs/remotes/origin/$2" "+refs/remotes/origin/$3:refs/remotes/origin/$3"
git -C "$dir" -c advice.detachedHead=false checkout --quiet --detach "$4"
EOF
)" || {
    # The first line is the dir of mktemp, so the exit trap removes the dir of a failed checkout.
    wsl_dir="$(head -n 1 <<<"$wsl_dir")"
    [[ "$wsl_dir" == /tmp/local-ci.* ]] || wsl_dir=""
    exit 1
  }
  check_checkout WSL "$(wsl_bash "$wsl_dir" <<<"$checkout_state")"
}

run_job() {
  if [ "$1" = ci-windows ]; then
    (cd "$win_dir" && bash scripts/ci-job.sh ci-windows "$base_sha")
  else
    # When the stdout and the stderr of wsl.exe go to one file, lines of one stream overwrite the other. So the job merges them in the distro.
    # The distro has no Node on the PATH of `bash -s`, so the job installs the pin of the checkout's web/.nvmrc through nvm.
    wsl_bash "$wsl_dir" "$1" "$base_sha" <<'EOF'
exec 2>&1
export NVM_DIR="$HOME/.nvm"
. "$NVM_DIR/nvm.sh" &&
  nvm install "$(cat "$1/web/.nvmrc")" &&
  nvm use "$(cat "$1/web/.nvmrc")" &&
  cd "$1" &&
  bash scripts/ci-job.sh "$2" "$3"
EOF
  fi
}

# A status description holds at most 140 characters, so a long log path keeps only its end.
failure_description() {
  local step text path room
  step="$(sed -n 's/^ci-job: [^:]*: FAIL at step //p' "$1" | tail -n 1)"
  text="failed${step:+ at step $step}. Log: "
  path="$(win_path "$1")"
  room=$((140 - ${#text}))
  [ "${#path}" -le "$room" ] || path="...${path: -$((room - 3))}"
  printf '%s%s' "$text" "$path"
}

failed=0
for context in "${contexts[@]}"; do
  if [ "$context" = ci-windows ]; then
    make_win_checkout
  elif [ -z "$wsl_dir" ]; then
    # Each start of the distro clears /tmp, so the Linux jobs run back to back, right after the checkout.
    make_wsl_checkout
  fi
  log="$run_dir/$context.log"
  echo "local-ci: $context: running, log $(win_path "$log")"
  if run_job "$context" >"$log" 2>&1; then
    post "$context" success "passed on the local host"
    echo "local-ci: $context: success"
  else
    post "$context" failure "$(failure_description "$log")"
    echo "local-ci: $context: failure"
    failed=1
  fi
  next=$((next + 1))
done
exit "$failed"
