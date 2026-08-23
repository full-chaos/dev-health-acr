#!/usr/bin/env bash
# CHAOS-4157 follow-up (2026-08-23 stale-checkout incident): pins
# common.sh's cwd-vs-script-repo fail-fast guard -- see that check's own
# doc comment (common.sh) for the incident it closes: a caller invoking a
# trial script by an ABSOLUTE path from a DIFFERENT worktree's cwd used to
# silently source, compile, and run entirely from whichever worktree the
# resolved file actually lived in, while a caller-cwd-independent
# provenance signal (origin/main) could still read the INTENDED commit --
# an artifact whose provenance looked plausible while the code that
# produced it was stale.
#
# NOT part of `make shard-plan`/`make verify` (same reasoning as
# test-connect-retry.sh, this directory): the SUCCESS path continues on
# into common.sh's own ops/.env requirement, which CI does not carry. The
# FAILURE path below is fully self-contained (the guard exits before ever
# reaching ops/.env) and is what actually needs pinning -- that is the new
# behavior this change adds.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
common_sh="$script_dir/common.sh"
repo_root="$(cd "$script_dir" && git rev-parse --show-toplevel)"

failures=0
check() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $label" >&2
    echo "  want: $want" >&2
    echo "  got:  $got" >&2
    failures=$((failures + 1))
  else
    echo "ok: $label"
  fi
}

# A worktree/checkout genuinely different from this one -- any other git
# worktree on the machine works; falls back to a plain non-repo tmpdir
# (the guard's own "caller_toplevel empty is not itself an error" branch
# is NOT what this test exercises, so a real mismatch needs a real
# worktree). Skips outright, rather than failing, when none is available
# (a single-worktree checkout, e.g. a fresh clone) -- this test's OWN job
# is the guard's mismatch behavior, not a claim that a second worktree
# must always exist.
other_worktree=""
while IFS= read -r line; do
  wt="${line#worktree }"
  [[ "$line" == worktree\ * ]] || continue
  wt_top="$(cd "$wt" 2>/dev/null && git rev-parse --show-toplevel 2>/dev/null || true)"
  if [[ -n "$wt_top" && "$wt_top" != "$repo_root" ]]; then
    other_worktree="$wt_top"
    break
  fi
done < <(cd "$repo_root" && git worktree list --porcelain 2>/dev/null || true)

if [[ -z "$other_worktree" ]]; then
  echo "test-repo-root-guard.sh: no second worktree found on this machine -- skipping (nothing to mismatch against)"
  exit 0
fi

# The failure shape: cwd resolves to $other_worktree, but the SOURCED
# FILE is THIS worktree's own common.sh (an absolute-path source, the
# same shape a caller invoking-by-absolute-path-from-elsewhere produces).
out="$(cd "$other_worktree" && bash -c "source '$common_sh'" 2>&1)" && rc=0 || rc=$?
check "mismatch exits non-zero" "1" "$rc"
case "$out" in
*"refusing to run"*"DIFFERENT worktree"*) check "mismatch message names the mismatch" "yes" "yes" ;;
*) check "mismatch message names the mismatch" "yes" "no (got: $out)" ;;
esac
case "$out" in
*"$other_worktree"*"$repo_root"*) check "mismatch message prints BOTH paths" "yes" "yes" ;;
*) check "mismatch message prints BOTH paths" "yes" "no (got: $out)" ;;
esac

# The non-failure shape: cwd and the sourced file's own worktree AGREE
# (this test's own repo_root, sourced from within it) -- the guard must
# not fire, so sourcing must reach at least past it. common.sh's own
# ops/.env requirement (unavailable in a bare CI-style environment) is
# expected to be the NEXT thing it hits if ops/.env is absent here -- so
# this check accepts either a clean source OR an ops/.env-shaped failure,
# and only FAILS if the guard's own "refusing to run" message appears
# (which would mean a false positive on the matching case).
out2="$(cd "$repo_root" && bash -c "source '$common_sh'" 2>&1)" || true
case "$out2" in
*"refusing to run"*"DIFFERENT worktree"*) check "matching cwd never trips the guard" "no false positive" "FALSE POSITIVE: $out2" ;;
*) check "matching cwd never trips the guard" "no false positive" "no false positive" ;;
esac

echo "repo-root-guard checks: $((failures == 0 ? 1 : 0)) suites clean, $failures failure(s)"
[[ "$failures" -eq 0 ]]
