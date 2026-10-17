#!/usr/bin/env bash
# trial-data.sh dsn takes the ClickHouse database from ACR_TRIAL_CH_DB and
# defaults to "default". Runs the real script with kubectl replaced by a fake.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
: >"$tmp/kubeconfig"
cat >"$tmp/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
args="$*"
case "$args" in
  *"get nodes"*) printf '10.0.0.1' ;;
  *"get secret"*) printf '%s' 'cHc=' ;;
  *nodePort*) printf '30500' ;;
esac
FAKE
chmod +x "$tmp/bin/kubectl"

failures=0
check() {
  if [[ "$3" != "$2" ]]; then
    echo "FAIL: $1" >&2; echo "  want: $2" >&2; echo "  got:  $3" >&2
    failures=$((failures + 1))
  else
    echo "ok: $1"
  fi
}

run_dsn() {
  env -u ACR_TRIAL_CH_DB "$@" PATH="$tmp/bin:$PATH" KUBECONFIG="$tmp/kubeconfig" \
    bash "$script_dir/trial-data.sh" dsn --env 2>&1 || echo "ERR"
}

check "default database" "ACR_TEST_TRIAL_CH_DB=default" \
  "$(run_dsn | grep '^ACR_TEST_TRIAL_CH_DB=')"
check "override database" "ACR_TEST_TRIAL_CH_DB=dh_0906" \
  "$(run_dsn ACR_TRIAL_CH_DB=dh_0906 | grep '^ACR_TEST_TRIAL_CH_DB=')"
check "plain dsn carries override" "clickhouse://ch:pw@10.0.0.1:30500/dh_0906" \
  "$(env -u ACR_TRIAL_CH_DB ACR_TRIAL_CH_DB=dh_0906 PATH="$tmp/bin:$PATH" KUBECONFIG="$tmp/kubeconfig" bash "$script_dir/trial-data.sh" dsn | grep '^ACR_TEST_TRIAL_CLICKHOUSE_DSN=' | cut -d= -f2-)"
bad="$(run_dsn 'ACR_TRIAL_CH_DB=a/b' | tail -1)"
check "invalid database refused" "ERR" "$bad"

[[ "$failures" -eq 0 ]] || exit 1
