#!/usr/bin/env bash
# The credential steps of o4-venue.sh act only on the containers of THIS run: a second
# acr-e2e-o4venue-* project on the same host (another run nonce, or none) is refused.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
script="$root/scripts/e2e/o4-venue.sh"
bash -n "$script"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/state"
printf 'nonce-of-this-run' > "$work/state/run-nonce"

# guard <service> <label the running container carries>: runs the guard of that service's step
# against stubbed compose and docker (a fake project of the same name prefix).
guard() {
  (
    export O4_VENUE_PROJECT=acr-e2e-o4venue-fake O4_VENUE_STATE_DIR="$work/state" O4_VENUE_API_PORT=28443 RUN_LABEL="$2"
    # shellcheck disable=SC1090,SC1091
    source <(sed '/^\[ \$# -eq 1 \]/,$d' "$script")
    # shellcheck disable=SC2329 # called by the sourced driver.
    compose() {
      case "$1 $2" in
        "ps -q") printf 'container-of-%s\n' "$3" ;;
        "port acr-api") printf '127.0.0.1:28443\n' ;;
      esac
    }
    # shellcheck disable=SC2329 # called by the sourced driver.
    docker() { [ "$1" = inspect ] && printf '%s\n' "$RUN_LABEL"; }
    if [ "$1" = acr-api ]; then own_api_base >/dev/null; else assert_own_run "$1"; fi
  ) 2>/dev/null
}

fail=0
for service in acr-api go-api; do
  if ! guard "$service" nonce-of-this-run; then
    echo "FAIL: $service of this run is refused"; fail=1
  fi
  for other in nonce-of-another-run ''; do
    if guard "$service" "$other"; then
      echo "FAIL: $service of another project (run label '${other}') is accepted"; fail=1
    fi
  done
done
# Both credential steps call the guard before any credential work.
for step in step_token:own_api_base step_routes:'assert_own_run go-api'; do
  name="${step%%:*}" call="${step#*:}"
  if ! awk "/^${name}\(\) \{/,/^}/" "$script" | grep -q "$call"; then
    echo "FAIL: ${name} does not call ${call}"; fail=1
  fi
done
[ "$fail" = 0 ] || exit 1
echo 'PASS: the credential steps act only on the containers of this run'
