#!/usr/bin/env bash
# CHAOS-4186: confirms `kiac.sh up` actually forwards ACR_KIAC_CPUS/
# ACR_KIAC_CP_MEMORY onto the real `kiac create cluster` invocation as
# --cpus/--cp-memory, and omits both flags entirely when unset (letting
# kiac's own create-time default apply, per the array-building logic in
# cmd_up). Against the REAL kiac.sh, never a reimplementation of its
# argv-building -- `kiac`/`container`/`kubectl` are replaced with fake
# binaries placed first on PATH that record their own argv and return
# scripted output/exit codes, the same shape as test-connect-retry.sh's
# fake_psql elsewhere in this repo. No real cluster, no real VM, no
# network egress.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
kiac_sh="$script_dir/kiac.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

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

# fake_bins writes stand-ins for kiac/container/kubectl at "$tmp/bin/",
# just enough for cmd_doctor + cluster_exists + cmd_up's create call to
# complete without a real cluster. `kiac create cluster`'s full argv is
# appended to "$tmp/create-argv.log" (one invocation per run).
fake_bins() {
  mkdir -p "$tmp/bin"
  cat >"$tmp/bin/kiac" <<'FAKE'
#!/usr/bin/env bash
case "${1:-}" in
  version)
    echo "kiac v0.5.1"
    echo "apple/container 1.2.2"
    ;;
  doctor)
    exit 0
    ;;
  get)
    # cluster_exists: header row only -- no cluster named $CLUSTER_NAME
    # exists yet, so cmd_up proceeds to create.
    echo "NAME       STATUS"
    ;;
  create)
    echo "$*" >>"$FAKE_TMP/create-argv.log"
    # kiac.sh checks [[ -s "$KUBECONFIG_PATH" ]] right after this call --
    # KUBECONFIG is exported by kiac.sh to that exact path before invoking
    # `kiac create cluster`, same as the real kiac would write there.
    echo "apiVersion: v1" >"$KUBECONFIG"
    ;;
  *)
    exit 1
    ;;
esac
FAKE
  sed -i '' "s#\$FAKE_TMP#$tmp#" "$tmp/bin/kiac"
  chmod +x "$tmp/bin/kiac"

  cat >"$tmp/bin/container" <<'FAKE'
#!/usr/bin/env bash
[[ "${1:-}" == "system" && "${2:-}" == "status" ]] && exit 0
exit 1
FAKE
  chmod +x "$tmp/bin/container"

  cat >"$tmp/bin/kubectl" <<'FAKE'
#!/usr/bin/env bash
[[ "${1:-}" == "wait" ]] && exit 0
exit 1
FAKE
  chmod +x "$tmp/bin/kubectl"
}

# run_up runs `kiac.sh up` against a fresh fake-bin PATH and a
# fresh/unique cluster name (so cluster_exists never finds a real
# leftover), with the given CPUS/CP_MEMORY env, and returns the recorded
# `kiac create cluster` argv line.
run_up() {
  local cpus="$1" cp_memory="$2"
  fake_bins
  : >"$tmp/create-argv.log"
  local cluster_name="resize-passthrough-test-$$-$RANDOM"
  PATH="$tmp/bin:$PATH" \
    ACR_KIAC_CLUSTER_NAME="$cluster_name" \
    ACR_KIAC_KUBECONFIG="$tmp/kubeconfig" \
    ACR_KIAC_CPUS="$cpus" \
    ACR_KIAC_CP_MEMORY="$cp_memory" \
    "$kiac_sh" up >"$tmp/stdout.log" 2>"$tmp/stderr.log" \
    || { echo "kiac.sh up FAILED (see $tmp/stderr.log): $(cat "$tmp/stderr.log")" >&2; }
  # STATE_DIR (created by cmd_up's mkdir -p) lives under the real repo's
  # own .tmp/kiac/<name> -- clean it up so this test leaves no debris.
  rm -rf "$script_dir/../../.tmp/kiac/$cluster_name"
  cat "$tmp/create-argv.log" 2>/dev/null || true
}

# 1. Both set: both flags forwarded.
argv="$(run_up 4 16G)"
check "both set: --cpus 4 present" \
  "1" \
  "$(grep -c -- '--cpus 4' <<<"$argv")"
check "both set: --cp-memory 16G present" \
  "1" \
  "$(grep -c -- '--cp-memory 16G' <<<"$argv")"

# 2. Neither set: neither flag appears at all -- kiac's own create-time
# default applies, this script never re-asserts one.
argv="$(run_up "" "")"
check "neither set: no --cpus in argv" \
  "0" \
  "$(grep -c -- '--cpus' <<<"$argv")"
check "neither set: no --cp-memory in argv" \
  "0" \
  "$(grep -c -- '--cp-memory' <<<"$argv")"

# 3. Only CPUS set: only --cpus forwarded.
argv="$(run_up 8 "")"
check "only CPUS set: --cpus 8 present" \
  "1" \
  "$(grep -c -- '--cpus 8' <<<"$argv")"
check "only CPUS set: no --cp-memory" \
  "0" \
  "$(grep -c -- '--cp-memory' <<<"$argv")"

# 4. Only CP_MEMORY set: only --cp-memory forwarded.
argv="$(run_up "" 20G)"
check "only CP_MEMORY set: --cp-memory 20G present" \
  "1" \
  "$(grep -c -- '--cp-memory 20G' <<<"$argv")"
check "only CP_MEMORY set: no --cpus" \
  "0" \
  "$(grep -c -- '--cpus' <<<"$argv")"

if [[ "$failures" -gt 0 ]]; then
  echo "kiac-resize-passthrough checks FAILED ($failures)" >&2
  exit 1
fi
echo "kiac-resize-passthrough checks passed"
