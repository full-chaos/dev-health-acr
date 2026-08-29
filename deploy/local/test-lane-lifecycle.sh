#!/usr/bin/env bash
# CHAOS-4428 / codex R3: lifecycle guards in lane.sh, exercised against the
# REAL lane.sh -- never a reimplementation of its logic. Same PATH-fake shape as
# test-lane-ownership.sh beside it: `kubectl`/`kiac`/`helm`/`docker`/`container`
# are replaced with fake binaries first on PATH. `kiac.sh` and `trial-data.sh`
# are siblings lane.sh calls by absolute path, so they are faked by running
# lane.sh through a SYMLINK in a sandbox directory -- lane.sh resolves its
# siblings from ${BASH_SOURCE[0]}'s directory, so the symlink puts fakes beside
# the very same file without copying it. No cluster, no daemon, no network.
#
# Every check here must FAIL when its guard is reverted -- see the table in the
# PR body. A check that cannot fail is worse than no check.
#
# HOST TRAP, and why the fakes below are written in small chunks: bash 5.3.15 as
# installed by Homebrew on the development Mac HANGS on any here-document larger
# than about 512 bytes. Reproduced in a clean environment against a plain
# `read -r x <<EOF`, so it is the shell, not `cat`, and not this script. The
# system /bin/bash is unaffected. Every here-document here is kept small so the
# suite runs under either shell, and lane.sh is invoked with the SAME
# interpreter that is running this file.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
lane_sh="$script_dir/lane.sh"
tmp="$(mktemp -d)"
# lane.sh renders its Helm values to /tmp/lane-<lane>-{ops,acr}.yaml, a path it
# does not let a caller override; the lane names here share a prefix so the
# cleanup can be exact rather than a glob over other people's files.
trap 'rm -rf "$tmp"; rm -f /tmp/lane-lanefake-*-ops.yaml /tmp/lane-lanefake-*-acr.yaml' EXIT

failures=0
ok()   { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n       %s\n' "$1" "$2"; failures=$((failures + 1)); }

check_contains() {
  local label="$1" needle="$2" haystack="$3"
  if [[ "$haystack" == *"$needle"* ]]; then ok "$label"
  else fail "$label" "expected to contain: $needle"; fi
}

check_absent() {
  local label="$1" needle="$2" haystack="$3"
  if [[ "$haystack" != *"$needle"* ]]; then ok "$label"
  else fail "$label" "expected NOT to contain: $needle"; fi
}

events() { cat "$tmp/events"; }
# Line number of the first recorded event containing a fixed string, or empty.
event_line() { grep -nF -- "$1" "$tmp/events" 2>/dev/null | head -1 | cut -d: -f1 || true; }

# ---------------------------------------------------------------------------
# Fixture tree
# ---------------------------------------------------------------------------
mkdir -p "$tmp/bin" "$tmp/repo/deploy/local" "$tmp/mono/backups" \
         "$tmp/mono/.acr-dev" "$tmp/opswt/scripts/worker" \
         "$tmp/opswt/deploy/helm/dev-health"
sandbox="$tmp/repo/deploy/local"
ln -s "$lane_sh" "$sandbox/lane.sh"
: >"$tmp/kubeconfig"
: >"$tmp/mono/backups/postgres-all-20260823-220102.sql.gz"
: >"$tmp/mono/backups/clickhouse-default-20260823-220102.zip"
: >"$tmp/opswt/scripts/worker/provision_river_roles.sql"

# ---------------------------------------------------------------------------
# kubectl fake. It models just enough of a cluster to drive the guards, from
# fixture values named by KFAKE_* so one fake serves every scenario.
# ---------------------------------------------------------------------------
cat >"$tmp/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
printf 'kubectl %s\n' "$*" >>"$EVENTS"
args="$*"
case "$args" in
  "create namespace"*)
    exit "${KFAKE_CREATE_NS_RC:-0}" ;;
  *"seeded-from"*)
    printf '%s' "${KFAKE_SEED_MARKER:-}"; exit 0 ;;
EOF
# shellcheck disable=SC2129  # separate appends are deliberate: see HOST TRAP
cat >>"$tmp/bin/kubectl" <<'EOF'
  *"managed-by"*)
    if [[ -n "${KFAKE_NS_LABEL_ERR:-}" ]]; then
      printf '%s\n' "$KFAKE_NS_LABEL_ERR" >&2
      exit "${KFAKE_NS_LABEL_RC:-1}"
    fi
    printf '%s' "${KFAKE_NS_LABEL_OUT:-}"; exit 0 ;;
  *"get svc trial-postgres"*)
    printf '%s' "${KFAKE_LANE_PG_NODEPORT:-}"; exit 0 ;;
EOF
# KFAKE_SVC_TABLE deliberately answers BOTH Service query shapes -- the current
# `go-template` sweep over every port AND the superseded `jsonpath` filter over
# each Service's FIRST port only. Without that, reverting the NodePort fix would
# make the check fail because the fake stopped understanding the query rather
# than because the resolver picked an occupied range, and the revert would prove
# nothing. Fixture rows are `<namespace> <nodePort> first|later`.
cat >>"$tmp/bin/kubectl" <<'EOF'
  "get svc -A"*)
    table="${KFAKE_SVC_TABLE:-/dev/null}"
    if [[ "$args" == *go-template* ]]; then
      awk '{print $1, $2}' "$table"
    else
      want="$(printf '%s' "$args" | sed -E 's/.*nodePort==([0-9]+).*/\1/')"
      awk -v w="$want" '$2 == w && $3 == "first" {print $1}' "$table"
    fi
    exit 0 ;;
EOF
cat >>"$tmp/bin/kubectl" <<'EOF'
  "get nodes"*)
    cat "${KFAKE_NODE_IMAGES:-/dev/null}"; exit 0 ;;
  "get namespace"*|"get ns"*)
    printf 'ok\n'; exit 0 ;;
  *) exit 0 ;;
esac
EOF

# kiac (the binary ensure_cluster asks whether the cluster exists) reports one
# only when KFAKE_CLUSTER_EXISTS=1. A blanket `exit 0` makes `kiac get clusters`
# print nothing, kiac.sh concludes the cluster is absent and tries to create
# one, and the run dies before the guard under test -- the trap already recorded
# in test-lane-ownership.sh.
cat >"$tmp/bin/kiac" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  "get clusters")
    printf 'NAME       STATUS\n'
    [[ "${KFAKE_CLUSTER_EXISTS:-0}" = "1" ]] && printf 'dev-full   1/1 running\n'
    ;;
  "version") printf 'kiac v0.5.1\n' ;;
esac
exit 0
EOF

for fake in container docker helm; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tmp/bin/$fake"
done

# kiac.sh and trial-data.sh record argv into the one ordered event log, which is
# what the ordering assertions read. trial-data.sh can be told to fail on a
# chosen verb, which is how each scenario stops the run once the events it cares
# about have been recorded.
cat >"$sandbox/kiac.sh" <<'EOF'
#!/usr/bin/env bash
printf 'kiac.sh %s\n' "$*" >>"$EVENTS"
exit 0
EOF
cat >"$sandbox/trial-data.sh" <<'EOF'
#!/usr/bin/env bash
printf 'trial-data %s\n' "$*" >>"$EVENTS"
[[ "${1:-}" == "${KFAKE_TRIALDATA_FAIL_ON:-}" ]] && exit 1
exit 0
EOF
chmod +x "$tmp/bin/"* "$sandbox/kiac.sh" "$sandbox/trial-data.sh"

# Runs the real lane.sh under the fakes, with the interpreter running this file
# (see the HOST TRAP note above). Scenario variables are exported by the caller.
# The exit status is captured but rarely asserted: most scenarios stop the run
# deliberately once the interesting events are recorded.
LAST_OUT=""
run_lane() {
  : >"$tmp/events"
  set +e
  LAST_OUT="$(
    PATH="$tmp/bin:$PATH" \
    EVENTS="$tmp/events" \
    LANE_KUBECONFIG="$tmp/kubeconfig" \
    LANE_MONO_ROOT="$tmp/mono" \
    LANE_OPS_WT="$tmp/opswt" \
    LANE_OPS_IMAGE="dev-health-ops-local:test" \
    "${BASH:-bash}" "$sandbox/lane.sh" "$@" 2>&1
  )"
  set -e
}

scenario() {
  unset KFAKE_CREATE_NS_RC KFAKE_SEED_MARKER KFAKE_NS_LABEL_OUT KFAKE_NS_LABEL_ERR \
        KFAKE_NS_LABEL_RC KFAKE_LANE_PG_NODEPORT KFAKE_SVC_TABLE KFAKE_NODE_IMAGES \
        KFAKE_CLUSTER_EXISTS KFAKE_TRIALDATA_FAIL_ON LANE_SKIP_ACR
  printf '\n%s\n' "$1"
}

printf 'lane.sh lifecycle guards\n'

# ---------------------------------------------------------------------------
# [R3-1] Reset ClickHouse before reseeding
#
# The seed marker gates BOTH restores, so an absent or mismatched marker replays
# the ClickHouse backup as well. `RESTORE DATABASE` rejects tables that already
# exist, so replaying it over a database left behind by an interrupted seed
# fails and strands the lane on a new PostgreSQL snapshot with old ClickHouse
# data -- and no re-run can repair it. The target must be cleared first.
# ---------------------------------------------------------------------------
scenario '[R3-1] reset ClickHouse before reseeding'
export KFAKE_CLUSTER_EXISTS=1 KFAKE_CREATE_NS_RC=0 KFAKE_SEED_MARKER="" \
       KFAKE_TRIALDATA_FAIL_ON=restore-clickhouse
run_lane up lanefake-seed
drop_at="$(event_line 'DROP DATABASE IF EXISTS')"
restore_at="$(event_line 'trial-data restore-clickhouse')"

if [[ -n "$drop_at" ]]; then
  ok "the ClickHouse target is cleared before reseeding"
else
  fail "the ClickHouse target is cleared before reseeding" \
    "no 'DROP DATABASE IF EXISTS' was issued, so RESTORE replays onto existing tables"
fi

if [[ -n "$drop_at" && -n "$restore_at" && "$drop_at" -lt "$restore_at" ]]; then
  ok "the clear is ordered BEFORE restore-clickhouse"
else
  fail "the clear is ordered BEFORE restore-clickhouse" \
    "clear at line '${drop_at:-none}', restore-clickhouse at line '${restore_at:-none}'"
fi

# shellcheck disable=SC2016  # the backticks are ClickHouse quoting, not a shell expansion
check_contains "the clear names the database parsed from the backup zip" \
  'DROP DATABASE IF EXISTS `default`' "$(events)"
check_contains "the clear runs inside the lane namespace only" \
  'kubectl -n lanefake-seed exec deploy/trial-clickhouse' "$(events)"

# The ownership guard must stay UPSTREAM of the clear: a namespace lane.sh does
# not own is refused before anything destructive is issued against it. This is
# what keeps the clear from ever reaching the standing acr-trial-data plane.
scenario '[R3-1] the ownership guard stays upstream of the clear'
export KFAKE_CLUSTER_EXISTS=1 KFAKE_CREATE_NS_RC=1 KFAKE_NS_LABEL_OUT=""
run_lane up lanefake-unowned
check_contains "up still refuses a namespace it does not own" \
  "is not lane.sh-owned" "$LAST_OUT"
check_absent "no DROP DATABASE reaches a namespace lane.sh does not own" \
  "DROP DATABASE" "$(events)"

# ---------------------------------------------------------------------------
# [R3-2] Load missing images when reusing a cluster
#
# "The cluster already exists" was read as "a previous successful `up` populated
# it". A cluster created any other way -- by kiac.sh directly, by another lane
# that failed before its load, by a `kiac load image` that only covered some of
# them -- has an incomplete containerd, and every workload here renders
# imagePullPolicy: Never, so Helm fails with ErrImageNeverPull. Reconcile
# against what the nodes actually hold instead of assuming.
# ---------------------------------------------------------------------------
scenario '[R3-2] load missing images when reusing a cluster'
cat >"$tmp/node-images-partial" <<'EOF'
docker.io/library/dev-health-ops-local:test
ghcr.io/full-chaos/dev-health-web:0.1.0
docker.io/library/dev-health-acr:dev
docker.io/library/dev-health-go-worker:latest
docker.io/library/dev-health-go-scheduler:latest
EOF
cat >>"$tmp/node-images-partial" <<'EOF'
docker.io/library/dev-health-go-stream-ingest:latest
docker.io/library/dev-health-go-stream-external:latest
docker.io/library/dev-health-go-stream-pagerduty:latest
docker.io/library/dev-health-go-worker-migrate:latest
EOF
export KFAKE_CLUSTER_EXISTS=1 KFAKE_CREATE_NS_RC=0 \
       KFAKE_NODE_IMAGES="$tmp/node-images-partial" KFAKE_TRIALDATA_FAIL_ON=apply
run_lane up lanefake-reuse
load_argv="$(grep -F 'kiac.sh load-image' "$tmp/events" || true)"
check_contains "a reused cluster still gets the image it is missing" \
  "dev-health-go-reconciler:latest" "$load_argv"
check_absent "images the nodes already hold are not re-loaded" \
  "dev-health-ops-local:test" "$load_argv"
check_absent "a registry-qualified node image counts as present" \
  "dev-health-web" "$load_argv"

# Regression guard on the other half of the same trade: the common case -- a
# reused cluster a prior `up` did populate -- must still skip the load rather
# than push several GB through the bridge on every run. This fails if the
# reconciliation degrades into "always load everything".
scenario '[R3-2] a fully populated reused cluster loads nothing'
cat "$tmp/node-images-partial" >"$tmp/node-images-full"
printf 'docker.io/library/dev-health-go-reconciler:latest\n' >>"$tmp/node-images-full"
export KFAKE_CLUSTER_EXISTS=1 KFAKE_CREATE_NS_RC=0 \
       KFAKE_NODE_IMAGES="$tmp/node-images-full" KFAKE_TRIALDATA_FAIL_ON=apply
run_lane up lanefake-full
check_absent "no image is loaded when the nodes already hold every one" \
  "kiac.sh load-image" "$(events)"

# ---------------------------------------------------------------------------
# [R3-3] Skip the ACR image in ops-only mode
#
# LANE_SKIP_ACR=1 is the documented ops-only path, and an operator taking it has
# no reason to have built the ACR image. The loader demanded it anyway, so
# `kiac.sh load-image` failed on an image the run was never going to use, before
# the ops-only lane could start.
# ---------------------------------------------------------------------------
scenario '[R3-3] ops-only mode does not require the ACR image'
export KFAKE_CREATE_NS_RC=0 KFAKE_TRIALDATA_FAIL_ON=apply LANE_SKIP_ACR=1
run_lane up lanefake-opsonly
load_argv="$(grep -F 'kiac.sh load-image' "$tmp/events" || true)"
check_contains "a fresh ops-only cluster still loads the ops image" \
  "dev-health-ops-local:test" "$load_argv"
check_absent "the ACR image is not loaded when ACR is disabled" \
  "dev-health-acr" "$load_argv"

# The other direction, so the fix cannot degrade into "never load ACR".
scenario '[R3-3] the ACR image is still loaded when ACR is enabled'
export KFAKE_CREATE_NS_RC=0 KFAKE_TRIALDATA_FAIL_ON=apply
run_lane up lanefake-withacr
load_argv="$(grep -F 'kiac.sh load-image' "$tmp/events" || true)"
check_contains "the ACR image is loaded on the default path" \
  "dev-health-acr:dev" "$load_argv"

# ---------------------------------------------------------------------------
if (( failures > 0 )); then
  printf '\n%d check(s) FAILED\n' "$failures"
  exit 1
fi
printf '\nall checks passed\n'
