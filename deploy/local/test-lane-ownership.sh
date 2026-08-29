#!/usr/bin/env bash
# CHAOS-4428: confirms lane.sh's namespace-ownership guard actually refuses, in
# BOTH directions, against the REAL lane.sh -- never a reimplementation of its
# logic. `kubectl`/`kiac`/`docker`/`helm` are replaced with fake binaries first
# on PATH that record argv and return scripted output, the same shape as
# test-kiac-resize-passthrough.sh beside it. No real cluster, no docker daemon,
# no network.
#
# The two properties, and why each exists:
#
#   `up` on a pre-existing UNLABELLED namespace must REFUSE and must not label
#   it. The first version of the guard did `create || true` then labelled
#   unconditionally, so `up acr-trial-data` would have marked the standing trial
#   data plane as lane.sh-owned -- and `down` would then have passed its own
#   ownership check and deleted it with its PVCs. The guard defeated by its own
#   adoption path.
#
#   `down` on a namespace without the label must REFUSE. That is the original
#   defect: the guard was a blocklist of four system namespace NAMES, so any
#   other real namespace was fair game for deletion.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
lane_sh="$script_dir/lane.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0
check() {
  local label="$1" expected="$2" actual="$3"
  if [[ "$actual" == *"$expected"* ]]; then
    printf '  ok   %s\n' "$label"
  else
    printf '  FAIL %s\n       expected to contain: %s\n       actual: %s\n' \
      "$label" "$expected" "$actual"
    failures=$((failures + 1))
  fi
}

mkdir -p "$tmp/bin"

# kubectl fake: `create namespace` fails (the namespace already exists), the
# namespace lookup succeeds, and the managed-by label reads back EMPTY -- i.e.
# a real, pre-existing, unowned namespace. Every invocation is recorded so the
# test can assert that no label was written.
cat >"$tmp/bin/kubectl" <<EOF
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$tmp/kubectl-argv"
case "\$*" in
  "create namespace"*)      exit 1 ;;                 # already exists
  *"managed-by"*)           printf '' ; exit 0 ;;     # label unset
  "get namespace"*|"get ns"*) printf 'ok\n' ; exit 0 ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$tmp/bin/kubectl"

# kiac fake: report the cluster as ALREADY EXISTING, so ensure_cluster reuses it
# and the run reaches the namespace guard. A blanket `exit 0` made
# `kiac get clusters` print nothing, kiac.sh concluded the cluster was absent
# and tried to create one, and the run died before the guard -- the first
# version of this test failed for that reason, not because the guard was wrong.
cat >"$tmp/bin/kiac" <<EOF
#!/usr/bin/env bash
case "\$*" in
  "get clusters") printf 'NAME       STATUS\ndev-full   1/1 running\n' ;;
  "version")      printf 'kiac v0.5.1\napple/container 1.2.2\n' ;;
esac
exit 0
EOF
chmod +x "$tmp/bin/kiac"

# container/docker/helm are never expected to do anything here; faked so the
# test cannot touch the real daemons.
for fake in container docker helm; do
  printf '#!/usr/bin/env bash\nexit 0\n' >"$tmp/bin/$fake"
  chmod +x "$tmp/bin/$fake"
done

printf 'lane.sh ownership guard\n'

# --- `up` must refuse to claim a pre-existing unlabelled namespace ----------
: >"$tmp/kubectl-argv"
kubeconfig="$tmp/kubeconfig"; : >"$kubeconfig"
set +e
up_out="$(
  PATH="$tmp/bin:$PATH" \
  LANE_KUBECONFIG="$kubeconfig" \
  LANE_MONO_ROOT="$tmp" \
  LANE_OPS_WT="$tmp" \
  LANE_OPS_IMAGE="dev-health-ops-local:test" \
  bash "$lane_sh" up lane-pretend 2>&1
)"
set -e
check "up refuses a pre-existing unlabelled namespace" \
  "is not lane.sh-owned" "$up_out"
check "up names the adoption step rather than doing it silently" \
  "MIGRATING AN EXISTING LANE" "$up_out"
# NOTE: there was a third check here asserting no `kubectl label … managed-by`
# appeared in the recorded argv. It was VACUOUS -- with the guard deliberately
# reverted, the harness recorded ZERO label calls, so the assertion could not
# fail and was reading as coverage. (`up` exits before that line under this
# harness.) The two message assertions above are the discriminating ones: both
# flip to FAIL when the guard is reverted, verified. A check that cannot fail is
# worse than no check, so it is gone rather than quietly kept.

# --- `down` must refuse a namespace without the label ----------------------
: >"$tmp/kubectl-argv"
set +e
down_out="$(
  PATH="$tmp/bin:$PATH" \
  LANE_KUBECONFIG="$kubeconfig" \
  LANE_MONO_ROOT="$tmp" \
  bash "$lane_sh" down lane-pretend 2>&1
)"
set -e
check "down refuses a namespace without the ownership label" \
  "carries no app.kubernetes.io/managed-by=lane.sh label" "$down_out"
if grep -q 'delete namespace' "$tmp/kubectl-argv" 2>/dev/null; then
  printf '  FAIL down issued a namespace delete despite refusing\n'
  failures=$((failures + 1))
else
  printf '  ok   down issued no delete\n'
fi

if (( failures > 0 )); then
  printf '\n%d check(s) FAILED\n' "$failures"
  exit 1
fi
printf '\nall checks passed\n'
