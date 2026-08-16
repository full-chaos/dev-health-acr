#!/usr/bin/env bash
# CHAOS-3742 trial runner shared setup. Sourced by every script in this
# directory -- never invoked directly.
#
# Repo-root-relative throughout (no hard-coded absolute paths), so these
# scripts work from any checkout, following the scripts/clients/*.sh
# convention. Secrets are ALWAYS sourced from ops/.env at RUNTIME, never
# baked into a script file (sol review F12).
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

# git-aware repo root (sol review R2 residual): correct regardless of the
# checkout's directory NAME, unlike a hard-coded "acr-wt-trial" path
# component would be. Falls back to the path-relative derivation only if
# git itself is unavailable.
#
# sol review F1 round 2: under `set -e` (line 9), a bare assignment whose
# RHS command substitution fails (`x=$(failing-cmd)`) kills the script on
# the spot -- that failure is NOT exempted from -e just because it
# happened inside `$(...)`. Every fallback below therefore lives inside an
# `if`/`elif` CONDITION, never a bare assignment: -e is suspended while
# evaluating the command list of an if/elif/while/until condition, so a
# failing probe there falls through to the next branch instead of exiting
# the script. (Sol proved the pre-fix version dead-code by simulating a
# missing git: it died on this exact line 127 before ever reaching the
# "if git itself is unavailable" branch that line's own comment promised.)
if repo_root="$(cd "$script_dir" && git rev-parse --show-toplevel 2>/dev/null)" && [[ -n "$repo_root" ]]; then
  :
else
  repo_root="$(cd "$script_dir/../.." && pwd -P)"
fi
# dev_health_root resolves via the git COMMON dir, not repo_root/.. --
# repo_root (--show-toplevel) is the CURRENT worktree's own root, which for
# a lane checked out under dev-health/worktrees/acr/<branch> sits two
# levels deeper than a plain dev-health/acr checkout; repo_root/.. would
# then land inside dev-health/worktrees/acr instead of dev-health, and
# ops/.env and .remember/ (both outside this repo) would never be found.
# --git-common-dir resolves to the SAME .git for every linked worktree of
# this repo, so its grandparent is dev-health regardless of which worktree
# sourced this script.
#
# sol review F1, round 3 (closing the class, not another spot-guard):
# round 1 found --git-common-dir alone insufficient (a plain checkout at
# a non-root cwd gets a path relative to the CALLER's cwd back); round 2
# found the fallback chain itself was dead code under `set -e` (a bare
# `x=$(failing-cmd)` aborts the script even though it "looks like" part
# of an if-condition, unless it truly is one); round 3's mutation showed
# a probe can SUCCEED (exit 0) while returning a well-formed but WRONG
# path (e.g. a --path-format=absolute shim pointing at a directory that
# does not exist) -- no per-step guard on any single git invocation can
# rule that out in general. Patching a fourth spot-guard would just be
# the fourth round of the same defect class.
#
# Instead: validate the RESULT, not the mechanics that produced it. Each
# tier below produces a CANDIDATE path, and a candidate is accepted only
# if it passes a landmark check (ops/.env exists under it) -- the one
# thing every consumer of dev_health_root actually needs. A wrong path,
# an empty path, a nonexistent path, and a cd failure are then all the
# SAME outcome (landmark check fails, fall through to the next tier), so
# no future regression in any one tier's git invocation can reopen this
# bug class. The whole resolution runs inside one function, called
# exactly once via the `if` below -- that `if` is the ONLY place in this
# routine `set -e` can see a failure; everything inside the function
# runs under an explicit `set +e` of its own (subshells/command
# substitutions INHERIT -e from the caller, so without this an early
# tier's failure would abort the function itself before trying the next
# tier, silently reintroducing round 2's exact bug one level down).
resolve_dev_health_root() (
  set +e
  local candidate legacy_common_git_dir

  # Tier 1: git --path-format=absolute --git-common-dir (git >= 2.31,
  # always an absolute path regardless of the caller's cwd).
  candidate="$(cd "$script_dir" && git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)"
  [[ -n "$candidate" ]] && candidate="$(cd "$candidate/../.." 2>/dev/null && pwd -P)"
  if [[ -n "$candidate" && -f "$candidate/ops/.env" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  # Tier 2: bare git --git-common-dir (older git rejects --path-format).
  # May print a path RELATIVE TO $script_dir's cwd, so it is resolved in
  # the same subshell/cwd that produced it.
  legacy_common_git_dir="$(cd "$script_dir" && git rev-parse --git-common-dir 2>/dev/null)"
  [[ -n "$legacy_common_git_dir" ]] && candidate="$(cd "$script_dir" && cd "$legacy_common_git_dir/../.." 2>/dev/null && pwd -P)"
  if [[ -n "$candidate" && -f "$candidate/ops/.env" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  # Tier 3: git unavailable entirely -- the pre-CHAOS-3855 path-relative
  # derivation. Correct for a plain, non-worktree checkout; cannot be
  # correct for a linked worktree (there is no way to find the shared
  # root without asking git), but that combination is not realistic --
  # git is required to create/use a worktree in the first place.
  candidate="$(cd "$repo_root/.." 2>/dev/null && pwd -P)"
  if [[ -n "$candidate" && -f "$candidate/ops/.env" ]]; then
    printf '%s\n' "$candidate"
    return 0
  fi

  return 1
)

if dev_health_root="$(resolve_dev_health_root)"; then
  :
else
  echo "common.sh: could not resolve the dev-health root -- tried git --path-format=absolute --git-common-dir, bare git --git-common-dir, and \$repo_root/.. (from $script_dir), and none of them contained ops/.env. Run from a checkout where ops/.env exists two levels above this repo, or export ACR_TRIAL_CORPUS/ACR_TRIAL_RESULTS_DIR explicitly to bypass this resolution." >&2
  exit 1
fi

# The withheld corpus and the trial-results output dir live in the parent
# dev-health checkout's .remember/ (outside this repo, never committed
# anywhere) -- overridable, defaulted to this trial's known paths.
: "${ACR_TRIAL_CORPUS:=$dev_health_root/.remember/acr-3778-corpus-frozen-annotated.json}"
: "${ACR_TRIAL_RESULTS_DIR:=$dev_health_root/.remember/trial-results}"
: "${ACR_TRIAL_ORG:=70d529e0-3c06-4597-8480-794fd02328b6}"
mkdir -p "$ACR_TRIAL_RESULTS_DIR"

ops_env="$dev_health_root/ops/.env"
if [[ ! -f "$ops_env" ]]; then
  echo "common.sh: $ops_env not found -- cannot source DB/model credentials" >&2
  exit 1
fi
trial_secret() {
  grep -E "^$1=" "$ops_env" | cut -d= -f2- | tr -d '"'
}

trial_wire_common_env() {
  export ACR_POSTGRES_CONNECTION_KIND=direct
  export ACR_TEST_TRIAL_CORPUS="$ACR_TRIAL_CORPUS"
  export ACR_TEST_TRIAL_ORG="$ACR_TRIAL_ORG"
  export ACR_TEST_TRIAL_FALKOR_ADDR=127.0.0.1:16379
  export ACR_TEST_TRIAL_POSTGRES_DSN="postgres://$(trial_secret POSTGRES_USER):$(trial_secret POSTGRES_PASSWORD)@127.0.0.1:5432/acr?sslmode=disable"
  export ACR_TEST_TRIAL_CLICKHOUSE_DSN="clickhouse://$(trial_secret CLICKHOUSE_USER):$(trial_secret CLICKHOUSE_PASSWORD)@127.0.0.1:9000/$(trial_secret CLICKHOUSE_DB)"
  export ACR_TEST_TRIAL_EMBED_MODEL=text-embedding-3-large
  export ACR_TEST_TRIAL_EMBED_DIMENSION=3072
  export ACR_TEST_TRIAL_EMBED_API_KEY="$(trial_secret OPENAI_API_KEY)"
}

trial_run_go_test() {
  ( cd "$repo_root" && go test -run TestGenerativeTrialCorpus -count=1 -v -timeout "${1:?timeout required}" ./internal/runtime/hosted )
}
