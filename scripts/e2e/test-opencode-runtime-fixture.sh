#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
helper="$root/scripts/e2e/opencode-runtime-fixture.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# shellcheck disable=SC1090
source "$helper"

fail() { printf '[opencode-runtime-fixture] FAIL: %s\n' "$*" >&2; exit 1; }

expect_failure() {
  local label="$1"
  shift
  if "$@" >"$tmp/${label}.log" 2>&1; then
    fail "${label} unexpectedly succeeded"
  fi
}

write_manifest() {
  local fixture="$1"
  (
    cd "$fixture"
    find config/opencode -type f -print | LC_ALL=C sort | while IFS= read -r path; do
      shasum -a 256 "$path"
    done
  ) > "$fixture/tree-hashes.sha256"
}

make_fixture() {
  local fixture="$1"
  mkdir -p "$fixture/config/opencode/node_modules/pkg"
  printf '%s\n' '{"name":"fixture"}' > "$fixture/config/opencode/package.json"
  printf '%s\n' '{"lockfileVersion":3}' > "$fixture/config/opencode/package-lock.json"
  printf '%s\n' 'runtime bytes' > "$fixture/config/opencode/node_modules/pkg/index.js"
  write_manifest "$fixture"
}

make_destination() {
  local destination="$1"
  mkdir -p "$destination"
}

fixture="$tmp/fixture"
destination="$tmp/destination"
make_fixture "$fixture"
make_destination "$destination"
receipt="$(stage_opencode_runtime_fixture "$fixture" "$destination")"
[[ "$receipt" == "$(shasum -a 256 "$fixture/tree-hashes.sha256" | cut -d' ' -f1)" ]] \
  || fail 'valid fixture did not return its manifest provenance hash'
cmp "$fixture/config/opencode/package.json" "$destination/package.json"
cmp "$fixture/config/opencode/package-lock.json" "$destination/package-lock.json"
cmp "$fixture/config/opencode/node_modules/pkg/index.js" "$destination/node_modules/pkg/index.js"
[[ "$(opencode_runtime_fixture_receipt_json '')" == null ]] || fail 'no-fixture receipt is not JSON null'
[[ "$(opencode_runtime_fixture_receipt_json "$receipt")" == "\"$receipt\"" ]] \
  || fail 'fixture receipt did not preserve the manifest hash'

argv=()
while IFS= read -r -d '' argument; do argv+=("$argument"); done < <(
  opencode_task_argv task-7 /workspace contextfabric/model INFO 'prompt with spaces'
)
expected_argv=(run --title acr-fullstack-task-7 --pure --format json --print-logs --log-level INFO --dir /workspace --model contextfabric/model 'prompt with spaces')
[[ "${#argv[@]}" -eq "${#expected_argv[@]}" ]] || fail 'production OpenCode argv has the wrong length'
for index in "${!expected_argv[@]}"; do
  [[ "${argv[$index]}" == "${expected_argv[$index]}" ]] || fail "production OpenCode argv differs at ${index}"
done

tampered="$tmp/tampered"
make_fixture "$tampered"
printf '%s\n' 'changed' >> "$tampered/config/opencode/node_modules/pkg/index.js"
make_destination "$tmp/tampered-destination"
expect_failure tampered stage_opencode_runtime_fixture "$tampered" "$tmp/tampered-destination"
[[ ! -e "$tmp/tampered-destination/node_modules" ]] || fail 'tampered source was published'

for extra in extra.txt .hidden; do
  fixture_with_extra="$tmp/extra-${extra//./dot}"
  make_fixture "$fixture_with_extra"
  printf '%s\n' extra > "$fixture_with_extra/config/opencode/node_modules/pkg/$extra"
  make_destination "$tmp/extra-destination-${extra//./dot}"
  expect_failure "extra-${extra//./dot}" stage_opencode_runtime_fixture "$fixture_with_extra" "$tmp/extra-destination-${extra//./dot}"
done

for malformed in traversal absolute; do
  malformed_fixture="$tmp/$malformed"
  make_fixture "$malformed_fixture"
  digest="$(shasum -a 256 "$malformed_fixture/config/opencode/package.json" | cut -d' ' -f1)"
  if [[ "$malformed" == traversal ]]; then
    printf '%s  ../outside\n' "$digest" > "$malformed_fixture/tree-hashes.sha256"
  else
    printf '%s  %s\n' "$digest" "$malformed_fixture/config/opencode/package.json" > "$malformed_fixture/tree-hashes.sha256"
  fi
  make_destination "$tmp/$malformed-destination"
  expect_failure "$malformed" stage_opencode_runtime_fixture "$malformed_fixture" "$tmp/$malformed-destination"
done

escaping_fixture="$tmp/escaping-symlink"
make_fixture "$escaping_fixture"
ln -s /tmp "$escaping_fixture/config/opencode/node_modules/escape"
write_manifest "$escaping_fixture"
make_destination "$tmp/escaping-destination"
expect_failure escaping_symlink stage_opencode_runtime_fixture "$escaping_fixture" "$tmp/escaping-destination"

internal_fixture="$tmp/internal-symlink"
make_fixture "$internal_fixture"
ln -s pkg "$internal_fixture/config/opencode/node_modules/internal"
write_manifest "$internal_fixture"
make_destination "$tmp/internal-destination"
expect_failure internal_symlink stage_opencode_runtime_fixture "$internal_fixture" "$tmp/internal-destination"

internal_extra_fixture="$tmp/internal-extra-symlink"
make_fixture "$internal_extra_fixture"
mkdir -p "$internal_extra_fixture/private-runtime"
printf '%s\n' private > "$internal_extra_fixture/private-runtime/unlisted.js"
ln -s ../../../private-runtime "$internal_extra_fixture/config/opencode/node_modules/internal"
make_destination "$tmp/internal-extra-destination"
expect_failure internal_symlink_extra stage_opencode_runtime_fixture "$internal_extra_fixture" "$tmp/internal-extra-destination"

fifo_fixture="$tmp/fifo"
make_fixture "$fifo_fixture"
mkfifo "$fifo_fixture/config/opencode/node_modules/pkg/input"
make_destination "$tmp/fifo-destination"
expect_failure fifo stage_opencode_runtime_fixture "$fifo_fixture" "$tmp/fifo-destination"

staged_fixture="$tmp/staged"
make_fixture "$staged_fixture"
make_destination "$tmp/staged-destination"
mkdir -p "$tmp/bin"
cat > "$tmp/bin/cp" <<EOF
#!/usr/bin/env bash
set -euo pipefail
/bin/cp "\$@"
if [[ "\$1" == '-R' && "\$2" == */config/opencode/. ]]; then
  printf '%s\n' tampered > "\$3/node_modules/pkg/index.js"
fi
EOF
chmod +x "$tmp/bin/cp"
saved_path="$PATH"
PATH="$tmp/bin:$PATH"
expect_failure staged_hash stage_opencode_runtime_fixture "$staged_fixture" "$tmp/staged-destination"
PATH="$saved_path"
[[ ! -e "$tmp/staged-destination/node_modules" ]] || fail 'unverified private stage was published'
[[ -z "$(compgen -G "$tmp/staged-destination/.opencode-runtime.*" || true)" ]] || fail 'failed private stage was retained'

for debug in 0 1; do
  client_home="$tmp/client-home-$debug"
  mkdir -p "$client_home/config/opencode/node_modules"
  : > "$client_home/config/opencode/package.json"
  : > "$client_home/config/opencode/package-lock.json"
  E2E_DEBUG="$debug" cleanup_opencode_runtime_fixture "$client_home"
  [[ -d "$client_home" && ! -e "$client_home/config/opencode/node_modules" \
    && ! -e "$client_home/config/opencode/package.json" && ! -e "$client_home/config/opencode/package-lock.json" ]] \
    || fail "runtime cleanup did not scrub the fixture payload for E2E_DEBUG=${debug}"
done

if bash "$helper" >"$tmp/direct.log" 2>&1; then
  fail 'helper unexpectedly executed directly'
fi
grep -Fq 'must be sourced' "$tmp/direct.log" || fail 'helper source guard did not explain direct execution'

printf '%s\n' 'opencode runtime fixture behavioral checks passed'
