#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HARNESS="${ROOT}/scripts/e2e/kind-helm.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

test_import_tracking_records_every_new_alias() {
  local state_root fake_bin output expected
  state_root="$(mktemp -d)"
  fake_bin="${state_root}/bin"
  mkdir -p "${fake_bin}"
  cat >"${fake_bin}/docker" <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" ctr -n k8s.io images list -q "*)
    printf '%s\n' \
      'acr-e2e.local/acr-api-test:v1' \
      'acr-e2e.local/acr-api-test@sha256:manifest' \
      'acr-e2e.local/acr-api-test@sha256:index'
    ;;
  *) exit 1 ;;
esac
EOF
  chmod +x "${fake_bin}/docker"
  output="$(PATH="${fake_bin}:${PATH}" ACR_E2E_LIB_ONLY=1 bash -c '
    harness="$1"
    shift
    source "${harness}"
    cluster=fake
    kind_image_refs_before=""
    imported_image_refs=()
    record_imported_image_refs acr-e2e.local/acr-api-test
    printf "%s\n" "${imported_image_refs[@]}"
  ' -- "${HARNESS}")"
  rm -rf "${state_root}"

  expected=$'acr-e2e.local/acr-api-test:v1\nacr-e2e.local/acr-api-test@sha256:manifest\nacr-e2e.local/acr-api-test@sha256:index'
  [[ "${output}" == "${expected}" ]] || fail 'OCI import aliases were not all tracked for cleanup'
}

test_import_tracking_records_every_new_alias
printf 'RESULT: Kind Helm harness contract tests passed\n'
