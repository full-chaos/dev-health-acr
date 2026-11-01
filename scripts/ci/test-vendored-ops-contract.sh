#!/usr/bin/env bash
set -euo pipefail

# Proves check-vendored-ops-contract.sh catches each way the vendored copy of
# the ops-owned acr credential lifecycle event contract can drift from the
# ops commit ci/ops-contract.pin names. The "ops" tree here is built from the
# vendored copy itself, so the matching case passes and every mutation of
# either side must fail.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
check="$repo_root/scripts/ci/check-vendored-ops-contract.sh"
vendored="$repo_root/contracts/auth/v1/ops-vendored"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

schema_rel=jsonschema/acr-credential-lifecycle-event.v1.schema.json
examples_rel=examples/acr-credential-lifecycle-event

fail() { printf 'FAIL: %s\n' "$1" >&2; exit 1; }

new_ops_root() {
  local root="$tmpdir/$1"
  mkdir -p "$root/contracts/auth/v1/jsonschema" "$root/contracts/auth/v1/examples"
  cp "$vendored/$schema_rel" "$root/contracts/auth/v1/$schema_rel"
  cp -R "$vendored/$examples_rel" "$root/contracts/auth/v1/$examples_rel"
  printf '%s' "$root"
}

assert_passes() {
  "$check" "$2" >/dev/null 2>&1 || fail "$1: expected the check to pass"
}
assert_fails() {
  if "$check" "$2" >/dev/null 2>&1; then fail "$1: expected the check to fail"; fi
}

[ -x "$check" ] || fail "check-vendored-ops-contract.sh is missing or not executable"
[ -f "$vendored/$schema_rel" ] || fail "vendored schema is missing"
[ -f "$vendored/$examples_rel/manifest.json" ] || fail "vendored examples manifest is missing"

root="$(new_ops_root same)"
assert_passes 'identical trees' "$root"

root="$(new_ops_root schema-edited)"
printf ' ' >> "$root/contracts/auth/v1/$schema_rel"
assert_fails 'ops schema differs from the vendored copy' "$root"

root="$(new_ops_root example-edited)"
printf ' ' >> "$root/contracts/auth/v1/$examples_rel/valid-issued.json"
assert_fails 'ops example differs from the vendored copy' "$root"

root="$(new_ops_root example-added)"
printf '{}\n' > "$root/contracts/auth/v1/$examples_rel/valid-new-at-ops.json"
assert_fails 'ops gained an example the vendored copy lacks' "$root"

root="$(new_ops_root example-removed)"
rm "$root/contracts/auth/v1/$examples_rel/valid-issued.json"
assert_fails 'ops lost an example the vendored copy holds' "$root"

root="$(new_ops_root schema-missing)"
rm "$root/contracts/auth/v1/$schema_rel"
assert_fails 'ops tree has no schema' "$root"

assert_fails 'ops root does not exist' "$tmpdir/absent"

printf 'OK: vendored ops contract check\n'
