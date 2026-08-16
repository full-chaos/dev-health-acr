#!/usr/bin/env bash
# CHAOS-3742 trial runner shared setup. Sourced by every script in this
# directory -- never invoked directly.
#
# Repo-root-relative throughout (no hard-coded absolute paths), so these
# scripts work from any checkout, following the scripts/clients/*.sh
# convention. Secrets are ALWAYS sourced from ops/.env at RUNTIME, never
# baked into a script file (sol review F12).
set -euo pipefail

# git-aware repo root (sol review R2 residual): correct regardless of the
# checkout's directory NAME, unlike a hard-coded "acr-wt-trial" path
# component would be. Falls back to the path-relative derivation only if
# git itself is unavailable.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && git rev-parse --show-toplevel 2>/dev/null)"
if [[ -z "$repo_root" ]]; then
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
fi
dev_health_root="$(cd "$repo_root/.." && pwd -P)"

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
