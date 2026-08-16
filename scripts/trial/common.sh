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
# sol review F1 round 1: --git-common-dir alone is not enough. From a
# PLAIN (non-worktree) checkout with cwd anywhere other than the repo
# root, git prints a path RELATIVE TO THE CALLER'S CWD (e.g. "../../.git"
# from scripts/trial) -- resolving that against a *different* cwd than
# the one that produced it lands on the wrong root or fails outright.
# Prefer --path-format=absolute (git >= 2.31, always prints an absolute
# path regardless of cwd shape); if an older git rejects that flag, fall
# back to resolving the (possibly relative) output inside the SAME
# subshell/cwd that produced it; if git itself is unavailable, fall back
# to the pre-CHAOS-3855 path-relative derivation. As above, each attempt
# is an if/elif CONDITION so a failing probe falls through instead of
# aborting the script.
if common_git_dir="$(cd "$script_dir" && git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" \
    && [[ -n "$common_git_dir" ]]; then
  dev_health_root="$(cd "$common_git_dir/../.." && pwd -P)"
elif legacy_common_git_dir="$(cd "$script_dir" && git rev-parse --git-common-dir 2>/dev/null)" \
    && [[ -n "$legacy_common_git_dir" ]] \
    && dev_health_root="$(cd "$script_dir" && cd "$legacy_common_git_dir/../.." && pwd -P)" \
    && [[ -n "$dev_health_root" ]]; then
  :
else
  dev_health_root="$(cd "$repo_root/.." && pwd -P)"
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
