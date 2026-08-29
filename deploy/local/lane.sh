#!/usr/bin/env bash
# One-command lane lifecycle for the full Dev Health stack (CHAOS-4428 phase 2).
#
# LOCAL ONLY. This is a developer-environment tool, not a deployment authority.
# Release composition for real environments lives in the deploy repository
# (deploy/AGENTS.md: Helm is the only supported application release manager).
# Everything here that is not Helm -- the @backups seed, NodePorts,
# imagePullPolicy Never, the kiac host IP -- exists because this is a laptop.
#
# It composes the two pieces that already exist rather than reimplementing
# them: kiac.sh owns the cluster and the image bridge, trial-data.sh owns the
# per-namespace Postgres/ClickHouse/FalkorDB and the @backups restores.
#
# Usage:
#   deploy/local/lane.sh up <lane> [--backups <dir>] [--nodeport-base <n>]
#   deploy/local/lane.sh down <lane>
#   deploy/local/lane.sh status [<lane>]
#
# `up` is IDEMPOTENT: every step checks whether it is already done, so a
# re-run repairs a partial lane instead of failing or duplicating work.
#
# Environment:
#   LANE_CLUSTER          kiac cluster to use/create (default: dev-full)
#   LANE_OPS_CHART        path to the ops chart   (default: $LANE_OPS_WT/deploy/helm/dev-health)
#   LANE_ACR_CHART        path to the acr chart   (default: this repo's deploy/helm/acr)
#   LANE_OPS_WT           ops worktree root, used to find the chart and scripts
#   LANE_OPS_CHART        ops chart path override (default:
#                         $LANE_OPS_WT/deploy/helm/dev-health)
#   LANE_OPS_IMAGE        side-loaded ops image ref (default: newest dev-health-ops-local:*)
#   LANE_WEB_IMAGE        web image ref           (default: ghcr.io/full-chaos/dev-health-web:0.1.0)
#   LANE_ACR_IMAGE        acr image ref           (default: dev-health-acr:dev)
#   LANE_ORG_ID           org allow-listed for the projector (default: the @backups dev org)
#   LANE_SKIP_ACR=1       bring up ops only (faster; no model key needed)
# MIGRATING AN EXISTING LANE (one-time):
#   Namespaces created before `up` started labelling will be REFUSED by `down`,
#   which is the guard working as intended -- it cannot tell them from a
#   namespace lane.sh never made. Adopt one deliberately:
#     kubectl label namespace <lane> app.kubernetes.io/managed-by=lane.sh
#   Only do that for a namespace you are certain is a disposable lane. Never
#   label the standing acr-trial-data plane: `down` would then delete it and its
#   PVCs, which is exactly what the guard exists to prevent.
#
#   LANE_DS_CPU_REQUEST   CPU request per datastore pod (default: 50m, vs the
#                         standing trial plane's 250m) -- this plus the ops
#                         tier is what caps lanes-per-node
#   LANE_MONO_ROOT        monorepo root override (auto-detected: the ancestor
#                         directory containing both backups/ and ops/)
#   LANE_KUBECONFIG       kubeconfig path override; default is one stable
#                         per-cluster path, $LANE_MONO_ROOT/.tmp/kiac/<cluster>/
#                         kubeconfig, so lane.sh works from any checkout
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# The monorepo root is where backups/, ops/ and .acr-dev/ live. It is NOT
# simply REPO_ROOT/.. -- run from a worktree under worktrees/acr/<branch>/ that
# would resolve to worktrees/acr, and every seed and secret path would be wrong
# (found on the first real run). Walk up looking for the actual marker instead,
# and let an operator override it outright.
find_mono_root() {
  local dir="$REPO_ROOT"
  while [[ "$dir" != "/" ]]; do
    if [[ -d "$dir/backups" && -d "$dir/ops" ]]; then printf '%s' "$dir"; return 0; fi
    dir="$(dirname "$dir")"
  done
  return 1
}
MONO_ROOT="${LANE_MONO_ROOT:-$(find_mono_root || true)}"
[[ -n "$MONO_ROOT" ]] \
  || { printf '[lane.sh] ERROR: could not locate the monorepo root (a directory containing both backups/ and ops/); set LANE_MONO_ROOT\n' >&2; exit 1; }

CLUSTER="${LANE_CLUSTER:-dev-full}"
OPS_WT="${LANE_OPS_WT:-}"
ACR_CHART="${LANE_ACR_CHART:-$REPO_ROOT/deploy/helm/acr}"
# Documented in the header, so it has to actually work (codex R2): the ops chart
# path was hardcoded under LANE_OPS_WT and silently ignored this override.
# Resolved after resolve_ops_wt, since the default depends on it.
OPS_CHART="${LANE_OPS_CHART:-}"
WEB_IMAGE="${LANE_WEB_IMAGE:-ghcr.io/full-chaos/dev-health-web:0.1.0}"
ACR_IMAGE="${LANE_ACR_IMAGE:-dev-health-acr:dev}"
ORG_ID="${LANE_ORG_ID:-70d529e0-3c06-4597-8480-794fd02328b6}"
PG_PASSWORD="${ACR_TRIAL_PG_PASSWORD:-acr-trial-dev}"
# A lane reserves what it uses, not what a deployment would. See the note in
# render_ops_values: REQUESTS, not usage, cap lanes-per-node.
DS_CPU_REQUEST="${LANE_DS_CPU_REQUEST:-50m}"

log()  { printf '[lane.sh] %s\n' "$*" >&2; }
step() { printf '[lane.sh] == %s\n' "$*" >&2; }
die()  { printf '[lane.sh] ERROR: %s\n' "$*" >&2; exit 1; }

usage() { sed -n '2,/^set -euo pipefail$/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 2; }

# A lane name is a Kubernetes namespace AND a Helm release name, so it must be
# valid as both, and must not start with '-' (which kubectl would read as a
# flag -- the same fail-closed reasoning as trial-data.sh's validate_namespace).
validate_lane() {
  [[ "$1" =~ ^[a-z0-9]([-a-z0-9]{0,40}[a-z0-9])?$ ]] \
    || die "lane name '$1' is not a valid namespace/release name (lowercase alphanumeric and '-', 1-42 chars)"
}

# Deterministic NodePort base per lane so two lanes never collide and a re-run
# of the same lane always lands on the same ports. trial-data.sh requires a
# multiple of 10 in 30000-30990, which gives 100 slots.
# A hash over an unrestricted name into 100 slots is NOT unique -- lane-2 and
# lane-11 collide (codex), and the second lane then fails deep inside Service
# creation with a message about ports rather than about lanes. So the hash is
# only a starting point: probe what is actually allocated on the cluster and
# walk forward to the first free slot, skipping any base already held by
# ANOTHER namespace. Deterministic for a given cluster state, and a lane that
# already owns a base keeps it.
# A base owns FOUR consecutive NodePorts (trial-data.sh: base, +1, +2, +3), so
# the slot is free only when none of the four is published anywhere.
nodeport_range_is_free() {
  local base="$1" taken="$2"
  printf '%s\n' "$taken" | awk -v b="$base" '$1 ~ /^[0-9]+$/ && $1 >= b && $1 <= b + 3 { exit 1 }'
}

lane_nodeport_base() {
  local name="$1" lane_ns="$1" digest start slot base taken existing
  # An EXISTING lane keeps the base it already published. Without this, a lane
  # whose hash slot differs from the base it was originally given (because the
  # hash slot was taken at the time, or the base was passed explicitly) would be
  # handed a different base on the next `up` -- and NodePorts on a live Service
  # are not freely mutable, so the idempotent re-run would fail. Found by
  # running the resolver against a lane that already owned 30500 while hashing
  # to 30920.
  existing="$(kubectl -n "$lane_ns" get svc trial-postgres -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || true)"
  if [[ "$existing" =~ ^[0-9]+$ ]]; then
    printf '%d' "$existing"
    return 0
  fi
  # EVERY nodePort of EVERY Service, not just each Service's first (codex R3).
  # The old query compared the candidate base against .spec.ports[0].nodePort
  # only, so a Service holding base+1..base+3, or publishing the base as a later
  # port, was invisible -- the resolver handed out an occupied range and
  # `trial-data.sh apply` died on a NodePort collision instead. Collect the lot
  # once, then reject a base if any of its four ports is taken. A lane's own
  # ports never block it, so a partially created lane can still re-run onto its
  # own range.
  # shellcheck disable=SC2016  # $ns is a Go template variable, not a shell one
  taken="$(kubectl get svc -A -o go-template='{{range .items}}{{$ns := .metadata.namespace}}{{range .spec.ports}}{{if .nodePort}}{{$ns}} {{.nodePort}}{{"\n"}}{{end}}{{end}}{{end}}' 2>/dev/null \
    | awk -v self="$lane_ns" '$1 != self { print $2 }' || true)"
  digest="$(printf '%s' "$name" | cksum | awk '{print $1}')"
  start=$(( digest % 100 ))
  for (( slot = 0; slot < 100; slot++ )); do
    base=$(( 30000 + ((start + slot) % 100) * 10 ))
    if nodeport_range_is_free "$base" "$taken"; then
      printf '%d' "$base"
      return 0
    fi
  done
  die "no free NodePort base in 30000-30990: all 100 lane slots are taken"
}

resolve_ops_wt() {
  [[ -n "$OPS_WT" ]] && { [[ -d "$OPS_WT" ]] || die "LANE_OPS_WT=$OPS_WT does not exist"; return; }
  # Default to the sibling ops checkout in the monorepo layout.
  if [[ -d "$MONO_ROOT/ops/deploy/helm/dev-health" ]]; then
    OPS_WT="$MONO_ROOT/ops"
  else
    die "could not find the ops chart; set LANE_OPS_WT to an ops worktree root"
  fi
}

# Two image stores exist on this machine and they are unrelated (codex): the
# Docker daemon, and Apple's `container` store that kiac.sh build-image writes
# to and kiac load-image reads from. Querying only Docker reported "no image"
# on a machine that had built one the kiac.sh way. Check both, Docker first
# because the buildx -> save -> load bridge in the runbook starts there.
resolve_ops_image() {
  [[ -n "${LANE_OPS_IMAGE:-}" ]] && return
  LANE_OPS_IMAGE="$(docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null \
    | grep '^dev-health-ops-local:' | head -1 || true)"
  if [[ -z "$LANE_OPS_IMAGE" ]] && command -v container >/dev/null 2>&1; then
    LANE_OPS_IMAGE="$(container image list 2>/dev/null \
      | awk '$1 == "dev-health-ops-local" {print $1 ":" $2; exit}' || true)"
    [[ -n "$LANE_OPS_IMAGE" ]] && log "ops image resolved from the apple/container store"
  fi
  [[ -n "$LANE_OPS_IMAGE" ]] \
    || die "no dev-health-ops-local:* image in the Docker daemon or the apple/container store; build one (docs/contribute/development/lane-isolation-kiac.md step 2) or set LANE_OPS_IMAGE"
}

# kiac.sh defaults its kubeconfig to <its own repo>/.tmp/kiac/<cluster>/, so a
# lane.sh invoked from a worktree would look in that worktree and miss a cluster
# created from another checkout (found on the first real run). Pin it to one
# stable per-cluster path under the monorepo root and hand the SAME value to
# every kiac.sh call, so `up` and `kubeconfig` can never disagree.
KUBECONFIG_PATH="${LANE_KUBECONFIG:-$MONO_ROOT/.tmp/kiac/$CLUSTER/kubeconfig}"
kubeconfig_path() { printf '%s' "$KUBECONFIG_PATH"; }

ensure_cluster() {
  step "cluster '$CLUSTER'"
  if kiac get clusters 2>/dev/null | awk -v n="$CLUSTER" 'NR>1 && $1==n {found=1} END {exit !found}'; then
    log "cluster '$CLUSTER' already exists (reusing)"
  else
    mkdir -p "$(dirname "$KUBECONFIG_PATH")"
    ACR_KIAC_CLUSTER_NAME="$CLUSTER" ACR_KIAC_WORKERS="${LANE_NODES:-0}" \
    ACR_KIAC_CPUS="${LANE_CPUS:-4}" ACR_KIAC_CP_MEMORY="${LANE_MEMORY:-24G}" \
    ACR_KIAC_KUBECONFIG="$KUBECONFIG_PATH" \
    ACR_KIAC_ALLOW_VERSION_DRIFT=1 "$SCRIPT_DIR/kiac.sh" up
    CLUSTER_WAS_CREATED=1
  fi
  export KUBECONFIG="$KUBECONFIG_PATH"
  [[ -f "$KUBECONFIG" ]] \
    || die "cluster '$CLUSTER' exists but no kubeconfig at $KUBECONFIG — it was created from another checkout; point LANE_KUBECONFIG at that file (or LANE_CLUSTER at a new name)"
}

# The ACR image is required only when the acr release is actually installed
# (codex R3): LANE_SKIP_ACR=1 is the documented ops-only path, and an operator
# taking it has no reason to have built that image -- demanding it made
# `kiac.sh load-image` fail on an image the run would never use, before the
# ops-only lane could start. ensure_acr_release reads the same flag.
lane_required_images() {
  printf '%s\n' "$LANE_OPS_IMAGE" "$WEB_IMAGE"
  [[ "${LANE_SKIP_ACR:-0}" = "1" ]] || printf '%s\n' "$ACR_IMAGE"
  printf '%s\n' \
    dev-health-go-worker:latest \
    dev-health-go-scheduler:latest \
    dev-health-go-reconciler:latest \
    dev-health-go-stream-ingest:latest \
    dev-health-go-stream-external:latest \
    dev-health-go-stream-pagerduty:latest \
    dev-health-go-worker-migrate:latest
}

# What containerd on the lane's nodes actually holds. kubelet publishes it on
# the Node, which is the only view of the node image store reachable from here.
# It is a CAPPED list (kubelet's --node-status-max-images, 50 by default) and it
# omits nothing we care about at that size -- but if it ever did, the error is
# in the safe direction: an image reported missing that is really present costs
# one redundant load-image, whereas the opposite costs ErrImageNeverPull.
node_image_names() {
  kubectl get nodes -o go-template='{{range .items}}{{range .status.images}}{{range .names}}{{.}}{{"\n"}}{{end}}{{end}}{{end}}' 2>/dev/null || true
}

# containerd normalizes a bare `name:tag` to `docker.io/library/name:tag`, so
# match the full reference OR the part after the last slash.
image_is_loaded() {
  local want="$1" have="$2"
  printf '%s\n' "$have" | awk -v want="$want" '
    $0 == want { found = 1; next }
    { n = $0; sub(/^.*\//, "", n); if (n == want) found = 1 }
    END { exit !found }'
}

# Every workload here renders imagePullPolicy: Never, so host images reach kiac
# nodes only through `kiac.sh load-image` (codex R2). A cluster this run created
# has an EMPTY containerd, so everything goes in. A REUSED cluster was not
# necessarily populated by a successful prior `up` (codex R3) -- it may have
# been made by kiac.sh directly, or by a lane that died before its load, or
# loaded only in part -- and returning early on that assumption is how a reused
# cluster dies with ErrImageNeverPull. Reconcile against what the nodes really
# hold and load only what is missing, so the common case still skips re-pushing
# several GB through the bridge.
load_lane_images() {
  local required=() missing=() img have
  while IFS= read -r img; do
    [[ -n "$img" ]] && required+=("$img")
  done < <(lane_required_images)

  if [[ "${CLUSTER_WAS_CREATED:-0}" = "1" ]]; then
    missing=("${required[@]}")
    step "loading images into the new cluster '$CLUSTER'"
  else
    have="$(node_image_names)"
    for img in "${required[@]}"; do
      image_is_loaded "$img" "$have" || missing+=("$img")
    done
    if (( ${#missing[@]} == 0 )); then
      log "every lane image is already on the '$CLUSTER' nodes — nothing to load"
      return 0
    fi
    step "loading ${#missing[@]} image(s) missing from the reused cluster '$CLUSTER'"
  fi

  ACR_KIAC_CLUSTER_NAME="$CLUSTER" ACR_KIAC_KUBECONFIG="$KUBECONFIG_PATH" \
  ACR_KIAC_ALLOW_VERSION_DRIFT=1 "$SCRIPT_DIR/kiac.sh" load-image "${missing[@]}" \
    || die "could not load images into '$CLUSTER'; every workload here uses imagePullPolicy: Never, so the lane cannot start without them"
}

ensure_datastores() {
  local lane="$1" base="$2" backups="$3"
  step "datastores in namespace '$lane' (NodePort base $base)"
  # Ownership marker, written before anything else and checked by `down`
  # (codex): validate_lane accepts any legal namespace name, so `down
  # acr-trial-data` would have deleted the STANDING trial data plane, PVCs and
  # all. A name is not ownership. Only namespaces this script created carry
  # this label, and `down` refuses anything without it.
  # Label ONLY a namespace we actually created (codex R2). The first version
  # did `create || true` then labelled unconditionally, which meant
  # `up acr-trial-data` would stamp the STANDING plane as lane.sh-owned and a
  # later `down` would pass its ownership check and delete it with its PVCs.
  # That is worse than the bug the label was added to fix, because it looks
  # safe. An existing namespace must ALREADY carry the label or we refuse.
  local ns_owner
  if kubectl create namespace "$lane" >/dev/null 2>&1; then
    kubectl label namespace "$lane" "app.kubernetes.io/managed-by=lane.sh" --overwrite >/dev/null
    log "created namespace '$lane' and marked it lane.sh-owned"
  else
    kubectl get namespace "$lane" >/dev/null 2>&1 \
      || die "namespace '$lane' could not be created and does not exist -- check the cluster connection"
    ns_owner="$(kubectl get ns "$lane" -o "jsonpath={.metadata.labels['app\.kubernetes\.io/managed-by']}" 2>/dev/null || true)"
    [[ "$ns_owner" == "lane.sh" ]] \
      || die "namespace '$lane' already exists and is not lane.sh-owned (managed-by='${ns_owner:-<unset>}'). Refusing to adopt it: see MIGRATING AN EXISTING LANE in this script's header if it really is a disposable lane."
  fi
  # Re-apply unconditionally rather than probing one deployment (codex): a
  # `kubectl apply` interrupted after trial-postgres but before ClickHouse or
  # FalkorDB left the old check permanently satisfied while two deployments
  # were missing, so `wait` then failed forever. apply IS idempotent -- the
  # cheap correct move is to always run it.
  if true; then
    ACR_TRIAL_DATA_NAMESPACE="$lane" ACR_TRIAL_NODEPORT_BASE="$base" \
    ACR_TRIAL_DS_CPU_REQUEST="$DS_CPU_REQUEST" \
      "$SCRIPT_DIR/trial-data.sh" apply
  fi
  ACR_TRIAL_DATA_NAMESPACE="$lane" ACR_TRIAL_NODEPORT_BASE="$base" \
  ACR_TRIAL_DS_CPU_REQUEST="$DS_CPU_REQUEST" \
    "$SCRIPT_DIR/trial-data.sh" wait

  # Seed once, and gate on an EXPLICIT completion marker rather than on
  # inferred content. The first version checked "does organizations have a
  # row?", which a PARTIALLY restored database also satisfies: a restore
  # interrupted midway (here, by the node running out of schedulable CPU) left
  # organizations populated but worker_instances absent, this check declared
  # the lane seeded, and the lane then failed much later and much less
  # obviously -- go-worker-migrate reporting
  # `runtime grant posture gap … worker_instances: table does not exist`.
  # A marker written only AFTER the restores return success cannot be
  # satisfied by a half-finished one. It records which @backups set was used,
  # so pointing a lane at a different set re-seeds instead of silently mixing.
  local marker_key="lane.dev-health/seeded-from"
  local want_marker seen_marker
  # Identify the set by the timestamp embedded in the dump filename, so a
  # marker means "seeded from THIS data", not merely "from a directory that
  # happened to have this name".
  want_marker="$(basename "$(ls "$backups"/postgres-all-*.sql.gz 2>/dev/null | head -1)" .sql.gz)"
  want_marker="${want_marker#postgres-all-}"
  [[ -n "$want_marker" ]] || die "could not identify the backups set in $backups"
  seen_marker="$(kubectl get ns "$lane" -o "jsonpath={.metadata.annotations['lane\.dev-health/seeded-from']}" 2>/dev/null || true)"
  if [[ -n "$seen_marker" && "$seen_marker" == "$want_marker" ]]; then
    log "postgres already seeded from '$seen_marker' — skipping restore"
  else
    [[ -n "$seen_marker" ]] \
      && log "namespace was seeded from '$seen_marker' but '$want_marker' was requested — re-seeding"
    local pg_dump ch_zip
    pg_dump="$(ls "$backups"/postgres-all-*.sql.gz 2>/dev/null | head -1)"
    ch_zip="$(ls "$backups"/clickhouse-default-*.zip 2>/dev/null | head -1)"
    [[ -f "$pg_dump" ]] || die "no postgres-all-*.sql.gz in $backups"
    [[ -f "$ch_zip"  ]] || die "no clickhouse-default-*.zip in $backups"
    # Replaying a pg_dumpall onto a server that already holds a previous
    # attempt's roles fails immediately: the archive opens with unconditional
    # CREATE ROLE and restore-postgres runs with ON_ERROR_STOP=1 (codex R2).
    # So an interrupted restore, or a --backups pointed at a different snapshot,
    # could never be repaired by re-running -- exactly the case the seed marker
    # is meant to handle. Drop the roles the dump will recreate first; they are
    # lane-local and the dump is about to redefine them anyway.
    if kubectl -n "$lane" exec deploy/trial-postgres -- \
         psql -U postgres -d postgres -Atc "SELECT 1 FROM pg_roles WHERE rolname='devhealth'" 2>/dev/null | grep -q 1; then
      log "previous restore state found — dropping lane-local roles so the dump can replay"
      kubectl -n "$lane" exec deploy/trial-postgres -- psql -U postgres -d postgres -q \
        -c "DROP DATABASE IF EXISTS devhealth;" \
        -c "DROP DATABASE IF EXISTS acr;" \
        -c "DROP ROLE IF EXISTS devhealth_domain;" \
        -c "DROP ROLE IF EXISTS devhealth_queue;" \
        -c "DROP ROLE IF EXISTS devhealth_coordinator;" \
        -c "DROP ROLE IF EXISTS devhealth;" >/dev/null 2>&1 || true
    fi
    ACR_TRIAL_DATA_NAMESPACE="$lane" ACR_TRIAL_NODEPORT_BASE="$base" \
      "$SCRIPT_DIR/trial-data.sh" restore-postgres "$pg_dump"
    # The same repair problem on the ClickHouse side (codex R3). `RESTORE
    # DATABASE` refuses a table that already exists, so replaying the backup
    # over a database left behind by an interrupted seed -- or by a --backups
    # pointed at a different snapshot -- fails, and the lane is stranded on the
    # NEW postgres snapshot with the OLD clickhouse data and no re-run that can
    # fix it. Postgres is cleared above by dropping its databases and roles;
    # clear the clickhouse target the same way, and let RESTORE recreate it.
    # Namespace-scoped by construction: this line is reachable only past the
    # ownership guard at the top of this function, so it can never touch the
    # standing acr-trial-data plane.
    local ch_base ch_db
    ch_base="$(basename "$ch_zip")"
    ch_db="$(sed -E 's/^clickhouse-(.+)-[0-9]{8}-[0-9]{6}\.zip$/\1/' <<<"$ch_base")"
    [[ -n "$ch_db" && "$ch_db" != "$ch_base" ]] \
      || die "could not parse a clickhouse database name out of $ch_base (expected clickhouse-<dbname>-<timestamp>.zip)"
    # Drop AND recreate, and pin both statements to `--database system`. The
    # lane's clickhouse database is `default`, which is also the database every
    # clickhouse-client connects to when it is not told otherwise -- including
    # trial-data.sh's own restore client on the very next line. Dropping it and
    # walking away would replace this bug with a worse one: the restore could no
    # longer connect at all. `system` always exists, so the drop cannot saw off
    # the branch it is standing on, and the recreate puts `default` back before
    # anything else needs it.
    log "clearing clickhouse database '$ch_db' in '$lane' so the backup can replay"
    kubectl -n "$lane" exec deploy/trial-clickhouse -- \
      clickhouse-client -u ch --password "$PG_PASSWORD" --database system \
      --query "DROP DATABASE IF EXISTS \`$ch_db\` SYNC"
    kubectl -n "$lane" exec deploy/trial-clickhouse -- \
      clickhouse-client -u ch --password "$PG_PASSWORD" --database system \
      --query "CREATE DATABASE IF NOT EXISTS \`$ch_db\`"
    ACR_TRIAL_DATA_NAMESPACE="$lane" ACR_TRIAL_NODEPORT_BASE="$base" \
      "$SCRIPT_DIR/trial-data.sh" restore-clickhouse "$ch_zip"
    # Only now, with both restores returned successfully, is the lane seeded.
    kubectl annotate namespace "$lane" "${marker_key}=${want_marker}" --overwrite >/dev/null
    log "seed marker written: ${marker_key}=${want_marker}"
  fi
}

# The dump restores the three River roles with whatever passwords the SOURCE
# stack had, which nothing here knows -- pin them to the documented locals.
# Then run the canonical provisioning SQL. ORDER MATTERS: provisioning REVOKES
# the runtime grants and dev-health-worker-migrate is what GRANTS them, so
# provision must come BEFORE the migrate chain, never after (CHAOS-4428).
ensure_roles() {
  local lane="$1"
  step "river roles in '$lane'"
  kubectl -n "$lane" exec deploy/trial-postgres -- psql -U postgres -d postgres -q \
    -c "ALTER ROLE devhealth_domain      WITH LOGIN PASSWORD 'devhealth_domain';" \
    -c "ALTER ROLE devhealth_queue       WITH LOGIN PASSWORD 'devhealth_queue';" \
    -c "ALTER ROLE devhealth_coordinator WITH LOGIN PASSWORD 'devhealth_coordinator';" >/dev/null
  local sql="$OPS_WT/scripts/worker/provision_river_roles.sql"
  [[ -f "$sql" ]] || die "provisioning SQL not found at $sql"
  kubectl -n "$lane" cp "$sql" "$(pg_pod "$lane"):/tmp/provision_river_roles.sql"
  kubectl -n "$lane" exec deploy/trial-postgres -- psql -U devhealth -d devhealth -q \
    --set=ON_ERROR_STOP=1 \
    --set=domain_role=devhealth_domain --set=queue_role=devhealth_queue \
    --set=coordinator_role=devhealth_coordinator \
    --set=domain_password=devhealth_domain --set=queue_password=devhealth_queue \
    --set=coordinator_password=devhealth_coordinator \
    --file=/tmp/provision_river_roles.sql >/dev/null
  log "river roles provisioned"
}

pg_pod() { kubectl -n "$1" get pod -l app.kubernetes.io/component=postgres -o jsonpath='{.items[0].metadata.name}'; }

render_ops_values() {
  local lane="$1" replicas="$2"
  cat <<YAML
image: { repository: ${LANE_OPS_IMAGE%%:*}, tag: "${LANE_OPS_IMAGE##*:}", pullPolicy: Never }
webImage: { repository: ${WEB_IMAGE%%:*}, tag: "${WEB_IMAGE##*:}", pullPolicy: Never }
postgresql: { enabled: false }
clickhouse: { enabled: false }
valkey: { enabled: true, persistence: { enabled: false }, resources: { requests: { cpu: 25m, memory: 64Mi } } }
# Lane-sized resource REQUESTS. The chart's defaults are sized for a real
# deployment; on a laptop node they are what actually caps lanes-per-node.
# Measured on this cluster: a full lane REQUESTED 2300m against 5000m
# allocatable, so two lanes reached 97% of requests and a third would not
# schedule ("Insufficient cpu") -- while actual usage across both lanes was
# 21%. Requests, not consumption, are the binding constraint, so a lane asks
# for what it really uses. Limits are left at the chart defaults: this caps
# what a lane RESERVES, not what it may burst to.
api: { enabled: true, replicas: 1, autoscaling: { enabled: false }, resources: { requests: { cpu: 50m, memory: 256Mi } } }
metricsApi: { enabled: true, replicas: 1, resources: { requests: { cpu: 50m, memory: 256Mi } } }
web: { enabled: true, replicas: 1, autoscaling: { enabled: false }, resources: { requests: { cpu: 25m, memory: 128Mi } } }
billingEdge: { enabled: false }
cronjobs: { dailyMetrics: { enabled: false }, syncGithub: { enabled: false } }
networkPolicy: { enabled: false }
ingress: { enabled: false }
config: { LOG_LEVEL: "DEBUG", OTEL_ENABLED: "false" }
secrets:
  create: true
  data:
    DATABASE_URI: "postgresql+asyncpg://devhealth:${PG_PASSWORD}@trial-postgres:5432/devhealth"
    CLICKHOUSE_URI: "clickhouse://ch:${PG_PASSWORD}@trial-clickhouse:8123/default"
    REDIS_URL: "redis://${lane}-dev-health-valkey:6379/1"
    VALKEY_URI: "redis://${lane}-dev-health-valkey:6379/1"
    JWT_SECRET_KEY: "dev-jwt-secret-min-32-chars-lane-local"
    ADMIN_API_KEY: "lane-local-admin"
    WORKER_OPERATIONAL_BRIDGE_TOKEN: "local-go-worker-bridge-token"
    EMAIL_PROVIDER: "console"
migrations:
  hook:
    enabled: true
    events: [pre-install, pre-upgrade]
    localBundledPostgres: false
    secretData:
      # MUST stay empty: the migrate Job never passes it to Alembic (CHAOS-4454).
      MIGRATION_DATABASE_URI: ""
      POSTGRES_URI: "postgresql://postgres:${PG_PASSWORD}@trial-postgres:5432/devhealth"
      CLICKHOUSE_URI: "clickhouse://ch:${PG_PASSWORD}@trial-clickhouse:8123/default"
goWorkers:
  enabled: true
  # sync-provider is held at 0 (no route-activation path in the chart,
  # CHAOS-4455) so it must leave the expected set or /health/workers 503s.
  expectedWorkerGroups: [heavy, ops, sync]
  clickhouseURI: "clickhouse://ch:${PG_PASSWORD}@trial-clickhouse:9000/default"
  pgbouncer:
    enabled: true
    postgres: { host: "trial-postgres", port: 5432, database: "devhealth" }
    resources: { requests: { cpu: 10m, memory: 32Mi }, limits: { cpu: 250m, memory: 256Mi } }
    secret:
      create: true
      data:
        RIVER_DOMAIN_DATABASE_PASSWORD: "devhealth_domain"
        RIVER_QUEUE_DATABASE_PASSWORD: "devhealth_queue"
        RIVER_COORDINATOR_DATABASE_PASSWORD: "devhealth_coordinator"
  groups:
    - { name: heavy, image: dev-health-go-worker:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, queues: [investment, metrics, reports, workgraph], queueConcurrency: {investment: 1, metrics: 2, reports: 2, workgraph: 1}, replicas: ${replicas}, terminationGracePeriodSeconds: 7260, autoscaling: {enabled: false}, bridgeUrl: "" }
    - { name: ops, image: dev-health-go-worker:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, queues: [coverage, heartbeat, retention, webhooks], queueConcurrency: {coverage: 1, heartbeat: 1, retention: 1, webhooks: 4}, replicas: ${replicas}, terminationGracePeriodSeconds: 960, autoscaling: {enabled: false} }
    - { name: sync, image: dev-health-go-worker:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, queues: [sync], queueConcurrency: {sync: 4}, replicas: ${replicas}, terminationGracePeriodSeconds: 960, autoscaling: {enabled: false} }
    - { name: sync-provider, image: dev-health-go-worker:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, queues: [sync_provider], queueConcurrency: {sync_provider: 2}, replicas: 0, terminationGracePeriodSeconds: 960, autoscaling: {enabled: false} }
    - { name: reconciler, image: dev-health-go-reconciler:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, replicas: ${replicas}, terminationGracePeriodSeconds: 60, autoscaling: {enabled: false} }
    - { name: scheduler, image: dev-health-go-scheduler:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, replicas: ${replicas}, terminationGracePeriodSeconds: 60, autoscaling: {enabled: false} }
    - { name: stream-external, image: dev-health-go-stream-external:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, runtimeProfile: external, replicas: ${replicas}, terminationGracePeriodSeconds: 60, autoscaling: {enabled: false} }
    - { name: stream-ingest, image: dev-health-go-stream-ingest:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, runtimeProfile: ingest, replicas: ${replicas}, terminationGracePeriodSeconds: 60, autoscaling: {enabled: false} }
    - { name: stream-pagerduty, image: dev-health-go-stream-pagerduty:latest, resources: {requests: {cpu: 25m, memory: 128Mi}, limits: {cpu: "1", memory: 1Gi}}, runtimeProfile: pagerduty, replicas: ${replicas}, terminationGracePeriodSeconds: 60, autoscaling: {enabled: false} }
YAML
}

# Install with the workers at zero, run the River grants, THEN scale up. Doing
# it the other way round starts workers before the grants exist: they fail
# readiness on domain_postgres, --wait times out, and Helm records the release
# FAILED and leaves it that way even after the pods recover (CHAOS-4428).
ensure_ops_release() {
  local lane="$1" chart="${OPS_CHART:-$OPS_WT/deploy/helm/dev-health}"
  [[ -d "$chart" ]] || die "ops chart not found at $chart (set LANE_OPS_CHART to override)"
  step "ops release '$lane' (workers at 0)"
  render_ops_values "$lane" 0 > "/tmp/lane-${lane}-ops.yaml"
  helm upgrade --install "$lane" "$chart" -n "$lane" -f "/tmp/lane-${lane}-ops.yaml" \
    --timeout 15m --wait

  step "river migrations + grants"
  kubectl -n "$lane" delete job go-worker-migrate --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "$lane" apply -f - >/dev/null <<JOB
apiVersion: batch/v1
kind: Job
metadata: { name: go-worker-migrate }
spec:
  backoffLimit: 1
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: dev-health-go-worker-migrate:latest
          imagePullPolicy: Never
          env:
            - { name: MIGRATION_DATABASE_URI, value: "postgresql://devhealth:${PG_PASSWORD}@trial-postgres:5432/devhealth" }
            - { name: RIVER_DATABASE_SCHEMA, value: "river" }
            - { name: RIVER_DOMAIN_DATABASE_ROLE, value: "devhealth_domain" }
            - { name: RIVER_QUEUE_DATABASE_ROLE, value: "devhealth_queue" }
            - { name: RIVER_COORDINATOR_DATABASE_ROLE, value: "devhealth_coordinator" }
JOB
  kubectl -n "$lane" wait --for=condition=complete job/go-worker-migrate --timeout=300s

  step "ops release '$lane' (workers up)"
  render_ops_values "$lane" 1 > "/tmp/lane-${lane}-ops.yaml"
  helm upgrade "$lane" "$chart" -n "$lane" -f "/tmp/lane-${lane}-ops.yaml" --timeout 15m --wait
}

ensure_acr_release() {
  local lane="$1"
  [[ "${LANE_SKIP_ACR:-0}" = "1" ]] && { log "LANE_SKIP_ACR=1 — skipping acr"; return; }
  step "acr release '${lane}-acr'"
  local kid="$MONO_ROOT/.acr-dev/evidence-kid" keys="$MONO_ROOT/.acr-dev/evidence-keys"
  [[ -f "$kid" && -f "$keys" ]] || die "evidence keys not found under $MONO_ROOT/.acr-dev (see the runbook's secrets step)"
  kubectl -n "$lane" create secret generic acr-runtime \
    --from-literal=ACR_POSTGRES_DSN="postgres://devhealth:${PG_PASSWORD}@trial-postgres:5432/acr?sslmode=disable" \
    --from-literal=ACR_CLICKHOUSE_DSN="clickhouse://ch:${PG_PASSWORD}@trial-clickhouse:9000/default" \
    --from-file=ACR_EVIDENCE_ID_ACTIVE_KID="$kid" \
    --from-file=ACR_EVIDENCE_ID_KEYS="$keys" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$lane" create secret generic acr-migration \
    --from-literal=ACR_POSTGRES_MIGRATION_DSN="postgres://devhealth:${PG_PASSWORD}@trial-postgres:5432/acr?sslmode=disable" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  # The model key is read from ops/.env, where it is QUOTED -- docker compose
  # strips those quotes and `kubectl create secret --from-literal` does not, so
  # a quoted key reaches the provider as an invalid credential (CHAOS-4428).
  local key=""
  if [[ -f "$MONO_ROOT/ops/.env" ]]; then
    # `|| true` on the pipeline, not just the assignment: with pipefail a
    # no-match grep makes the whole pipeline non-zero and set -e kills the
    # script here, so the warning branch below was unreachable (codex R2).
    key="$(grep -m1 '^OPENAI_API_KEY=' "$MONO_ROOT/ops/.env" 2>/dev/null | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//" || true)"
  fi
  if [[ -n "$key" ]]; then
    kubectl -n "$lane" create secret generic acr-model \
      --from-literal=ACR_CONTEXT_FABRIC_MODEL_API_KEY="$key" \
      --from-literal=ACR_CONTEXT_FABRIC_EMBED_API_KEY="$key" \
      --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    log "acr-model secret set (value not printed)"
  else
    log "WARNING: no OPENAI_API_KEY in ops/.env — acr will serve model-unavailable"
  fi
  render_acr_values "$lane" "$key" > "/tmp/lane-${lane}-acr.yaml"
  # NOT --wait, and the reason is CHAOS-4465. On a freshly seeded lane the
  # projector is doing the org's FIRST full projection: it drains batches until
  # its tick deadline (~150 s), exits 0 with drain_yield_reason=context_done,
  # and is restarted to continue. That is forward progress -- lane-c climbed
  # 30,141 -> 41,465 graph nodes across those restarts -- but its readiness
  # never holds during it, so `--wait` blocks for the full timeout and then
  # reports a failure that is not one (it is what made an otherwise-healthy
  # bring-up take 781 s instead of 144 s).
  #
  # So: serving readiness is **acr-api's** `/readyz`, which verify_lane checks
  # and which does hold; graph completeness is reported as progress, never as a
  # gate. Anything else that `--wait`s on this Deployment against a cold org
  # will hit the same thing -- see CHAOS-4465.
  helm upgrade --install "${lane}-acr" "$ACR_CHART" -n "$lane" -f "/tmp/lane-${lane}-acr.yaml" \
    --timeout 10m
  kubectl -n "$lane" rollout status "deploy/${lane}-acr" --timeout=300s
}

render_acr_values() {
  local lane="$1" key="$2"
  cat <<YAML
image: { reference: "${ACR_IMAGE}", pullPolicy: Never }
deployment:
  replicaCount: 1
  topologySpreadConstraints: []
  resources: { requests: { cpu: 50m, memory: 128Mi }, limits: { cpu: "1", memory: 512Mi } }
config:
  environment: test
  logLevel: debug
  requireBackingStores: true
  postgresConnectionKind: direct
  requestTimeout: "490s"
  writeTimeout: "500s"
  deviceVerificationUrl: "http://localhost:3000/acr/device"
credentials:
  runtime:   { existingSecret: "acr-runtime" }
  migration: { existingSecret: "acr-migration" }
migration: { enabled: true }
networkPolicy: { enabled: false }
contextFabric:
  lifecycleEnabled: true
  readsEnabled: true
  falkor: { addr: "trial-falkordb:6379", tls: false, allowInsecure: true }
$( [[ -n "$key" ]] && cat <<INNER
  embed:
    baseURL: "https://api.openai.com/v1"
    provider: "openai"
    model: "text-embedding-3-large"
    dimension: "3072"
    timeout: "45s"
    maxTransportRetries: "5"
    existingSecret: "acr-model"
  model:
    enabled: true
    provider: "openai"
    baseURL: "https://api.openai.com/v1"
    model: "gpt-5-nano"
    fallbackModel: "gpt-5.6-luna"
    existingSecret: "acr-model"
INNER
)
  falkordb: { enabled: false }
  projector:
    enabled: true
    replicaCount: 1
    projectionEnabled: true
    orgIds: ["${ORG_ID}"]
    pollInterval: "1s"
    concurrency: 4
    resources: { requests: { cpu: 50m, memory: 128Mi }, limits: { cpu: "1", memory: 512Mi } }
YAML
}

# Readiness is asserted at the APPLICATION level, not by pod phase. A lane can
# be 21/21 Running with /health/workers returning 503 -- that is exactly how
# CHAOS-4455 hid on the first pass.
verify_lane() {
  local lane="$1" failed=0
  step "verifying '$lane'"
  local hw
  hw="$(kubectl -n "$lane" exec "deploy/${lane}-dev-health-api" -- python -c "
import urllib.request
try: print(urllib.request.urlopen('http://${lane}-dev-health-api:8000/health/workers',timeout=15).status)
except Exception as e: print(getattr(e,'code','ERR'))" 2>/dev/null || echo ERR)"
  [[ "$hw" = "200" ]] && log "  /health/workers 200" || { log "  /health/workers $hw (EXPECTED 200)"; failed=1; }
  for probe in "api:8000:/ready" "metrics-api:8000:/ready" "web:3000:/health"; do
    # Split across statements, not one `local`: bash expands every RHS in a
    # single `local` before binding any of them, so `port="${rest%%:*}"` on the
    # same line saw an unbound `rest` under `set -u` (found by running it).
    local svc rest port path code
    svc="${probe%%:*}"
    rest="${probe#*:}"
    port="${rest%%:*}"
    path="${rest#*:}"
    code="$(kubectl -n "$lane" exec "deploy/${lane}-dev-health-api" -- python -c "
import urllib.request
try: print(urllib.request.urlopen('http://${lane}-dev-health-${svc}:${port}${path}',timeout=15).status)
except Exception as e: print(getattr(e,'code','ERR'))" 2>/dev/null || echo ERR)"
    [[ "$code" = "200" ]] && log "  ${svc}${path} 200" || { log "  ${svc}${path} ${code} (EXPECTED 200)"; failed=1; }
  done
  if [[ "${LANE_SKIP_ACR:-0}" != "1" ]]; then
    local acr
    acr="$(kubectl -n "$lane" exec "deploy/${lane}-dev-health-api" -- python -c "
import urllib.request
try: print(urllib.request.urlopen('http://${lane}-acr:8080/readyz',timeout=15).status)
except Exception as e: print(getattr(e,'code','ERR'))" 2>/dev/null || echo ERR)"
    [[ "$acr" = "200" ]] && log "  acr /readyz 200" || { log "  acr /readyz ${acr} (EXPECTED 200)"; failed=1; }
  fi
  if [[ "${LANE_SKIP_ACR:-0}" != "1" ]]; then
    # Progress, not a gate: the first projection of a freshly seeded org takes
    # several projector cycles. Report the count so an operator can see it
    # climbing rather than wonder whether anything is happening.
    local gkey nodes
    gkey="$(kubectl -n "$lane" exec deploy/trial-falkordb -- redis-cli KEYS 'acr-cf*' 2>/dev/null | head -1 | tr -d '\r')"
    if [[ -n "$gkey" ]]; then
      nodes="$(kubectl -n "$lane" exec deploy/trial-falkordb -- redis-cli GRAPH.QUERY "$gkey" "MATCH (n) RETURN count(n)" 2>/dev/null | sed -n '2p' | tr -d '\r')"
      log "  graph ${gkey}: ${nodes:-?} nodes (projection continues in the background)"
    else
      log "  graph: not yet created (projector still on its first pass)"
    fi
  fi
  local alb
  alb="$(kubectl -n "$lane" exec deploy/trial-postgres -- psql -U postgres -d devhealth -Atc "SELECT version_num FROM alembic_version;" 2>/dev/null || echo "?")"
  log "  alembic_version ${alb}"
  local grants
  grants="$(kubectl -n "$lane" exec deploy/trial-postgres -- psql -U postgres -d devhealth -Atc "SELECT count(*) FROM information_schema.role_table_grants WHERE grantee='devhealth_domain';" 2>/dev/null || echo 0)"
  log "  devhealth_domain grants ${grants}"
  [[ "${grants:-0}" -gt 0 ]] || { log "  NO domain grants — the river migrate step did not apply them"; failed=1; }
  return "$failed"
}

cmd_up() {
  local lane="${1:?lane name required}"; shift || true
  local backups="$MONO_ROOT/backups" base=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --backups) backups="${2:?}"; shift 2 ;;
      --nodeport-base) base="${2:?}"; shift 2 ;;
      *) die "unknown flag: $1" ;;
    esac
  done
  validate_lane "$lane"
  # Newest COMPLETE @backups set unless told otherwise. "Complete" means a
  # directory holding BOTH a postgres-all-*.sql.gz and a
  # clickhouse-default-*.zip -- checking only for the postgres dump picked the
  # loose older files sitting directly in backups/ over the newer timestamped
  # subdirectory beside them, and seeded a lane from 20260817 while
  # 20260823-220102 was present (observed). Timestamped subdirectories are
  # searched newest-first and the top level is the last resort.
  if [[ -d "$backups" ]]; then
    local chosen="" candidate
    while IFS= read -r candidate; do
      [[ -n "$candidate" ]] || continue
      if compgen -G "$candidate/postgres-all-*.sql.gz" >/dev/null \
         && compgen -G "$candidate/clickhouse-default-*.zip" >/dev/null; then
        chosen="$candidate"; break
      fi
      log "skipping incomplete backups set: $candidate"
    done < <(find "$backups" -mindepth 1 -maxdepth 1 -type d 2>/dev/null | sort -r)
    if [[ -z "$chosen" ]]; then
      if compgen -G "$backups/postgres-all-*.sql.gz" >/dev/null \
         && compgen -G "$backups/clickhouse-default-*.zip" >/dev/null; then
        chosen="$backups"
      else
        die "no complete @backups set under $backups (need both postgres-all-*.sql.gz and clickhouse-default-*.zip)"
      fi
    fi
    backups="$chosen"
  fi
  log "lane=$lane cluster=$CLUSTER backups=$backups"
  resolve_ops_wt; resolve_ops_image
  log "ops image: $LANE_OPS_IMAGE"
  local started; started="$(date +%s)"
  ensure_cluster
  load_lane_images
  # After ensure_cluster: picking a free base requires talking to the cluster.
  [[ -n "$base" ]] || base="$(lane_nodeport_base "$lane")"
  log "nodeport base: $base"
  ensure_datastores "$lane" "$base" "$backups"
  ensure_roles "$lane"
  ensure_ops_release "$lane"
  ensure_acr_release "$lane"
  if verify_lane "$lane"; then
    log "lane '$lane' READY in $(( $(date +%s) - started ))s"
  else
    die "lane '$lane' came up but failed verification (see the EXPECTED 200 lines above)"
  fi
}

cmd_down() {
  local lane="${1:?lane name required}"
  validate_lane "$lane"
  export KUBECONFIG="$(kubeconfig_path)"
  # OWNERSHIP, not a name blocklist (codex). The old guard listed four system
  # namespaces, so `down acr-trial-data` -- a perfectly legal name -- would have
  # deleted the STANDING trial data plane and its PVCs. Refuse anything this
  # script did not create and label.
  # A failed lookup is NOT a missing namespace (codex R2). An unreachable API,
  # a stale kubeconfig or an RBAC denial would otherwise read as "absent", and
  # `down` would report the lane removed while every resource survived. Classify
  # explicitly: only a real NotFound is benign.
  # The classification has to be REACHED to matter (codex R3). As a bare
  # assignment, `lookup_err=$(kubectl …)` inherits kubectl's status under
  # `set -e`, so the script exited on the very line that was supposed to
  # tolerate the failure: `lookup_rc=$?` and everything below it were dead
  # code, and `down` on a nonexistent lane died silently instead of no-opping.
  # An `if` makes the failure a tested condition rather than a fatal one.
  local owner lookup_err lookup_rc=0
  if lookup_err="$(kubectl get ns "$lane" -o "jsonpath={.metadata.labels['app\.kubernetes\.io/managed-by']}" 2>&1 >/dev/null)"; then
    lookup_rc=0
  else
    lookup_rc=$?
  fi
  if (( lookup_rc != 0 )); then
    case "$lookup_err" in
    *NotFound* | *"not found"*)
      log "namespace '$lane' does not exist; nothing to tear down"
      rm -f "/tmp/lane-${lane}-ops.yaml" "/tmp/lane-${lane}-acr.yaml"
      return 0
      ;;
    *) die "could not look up namespace '$lane' (not a NotFound): ${lookup_err}" ;;
    esac
  fi
  owner="$(kubectl get ns "$lane" -o "jsonpath={.metadata.labels['app\.kubernetes\.io/managed-by']}" 2>/dev/null || true)"
  if [[ -z "${owner}" ]]; then
    die "refusing to delete namespace '$lane': it carries no app.kubernetes.io/managed-by=lane.sh label, so lane.sh did not create it. Delete it by hand if you are certain."
  fi
  [[ "${owner}" == "lane.sh" ]] \
    || die "refusing to delete namespace '$lane': managed-by is '${owner}', not lane.sh"

  step "tearing down lane '$lane'"
  helm uninstall "${lane}-acr" -n "$lane" >/dev/null 2>&1 || true
  helm uninstall "$lane" -n "$lane" >/dev/null 2>&1 || true
  # The namespace delete takes the PVCs with it -- this is a lane, not the
  # standing trial data plane, so its data is disposable by construction.
  # Failures are NOT swallowed (codex): `|| true` reported a lane removed while
  # every resource survived. Only a confirmed NotFound is benign.
  local delete_err
  delete_err="$(kubectl delete namespace "$lane" --wait=true 2>&1 >/dev/null)" || {
    case "$delete_err" in
    *NotFound* | *"not found"*) : ;;
    *) die "failed to delete namespace '$lane': ${delete_err}" ;;
    esac
  }
  rm -f "/tmp/lane-${lane}-ops.yaml" "/tmp/lane-${lane}-acr.yaml"
  log "lane '$lane' removed (cluster '$CLUSTER' left running)"
}

cmd_status() {
  export KUBECONFIG="$(kubeconfig_path)"
  if [[ $# -ge 1 ]]; then
    validate_lane "$1"; kubectl -n "$1" get pods; verify_lane "$1" || true; return
  fi
  printf '%-16s %-8s %s\n' LANE PODS RELEASES
  for ns in $(kubectl get ns -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null); do
    kubectl -n "$ns" get deploy trial-postgres >/dev/null 2>&1 || continue
    printf '%-16s %-8s %s\n' "$ns" \
      "$(kubectl -n "$ns" get pods --no-headers 2>/dev/null | grep -c 'Running')" \
      "$(helm list -n "$ns" -q 2>/dev/null | tr '\n' ' ')"
  done
}

case "${1:-}" in
  up)     shift; cmd_up "$@" ;;
  down)   shift; cmd_down "$@" ;;
  status) shift; cmd_status "$@" ;;
  -h|--help|help|"") usage ;;
  *) die "unknown command: $1 (try --help)" ;;
esac
