#!/usr/bin/env bash
set -euo pipefail

# test-verify-mirrored-images-retry.sh (CHAOS-5624): exercises
# verify-mirrored-images.sh's `inspect_with_retry` against a fake `docker`
# so the bounded-attempts, backoff-stops-on-success, and
# fails-after-exactly-N-attempts behavior is proven without touching a real
# registry. See that script's own CHAOS-5624 comment for the incident this
# closes.
#
# The real script is SOURCED, not executed, for this: a guard in
# verify-mirrored-images.sh (`if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
# return 0; fi`) makes `source`-ing it define `inspect_with_retry` and its
# config variables without running the main preflight loop, which would
# otherwise require a real `$1` repo argument and reach out to
# resolve-mirrored-images.sh.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
verify_script="${repo_root}/scripts/ci/verify-mirrored-images.sh"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

fake_bin_dir="${work_dir}/bin"
mkdir -p "$fake_bin_dir"
counter_file="${work_dir}/counter"

# Writes a fake `docker` that counts its own invocations in $counter_file
# and starts returning a digest once the count reaches $1 (0 = never).
write_fake_docker() {
  local succeed_at="$1"
  cat > "${fake_bin_dir}/docker" <<EOF
#!/usr/bin/env bash
n=0
[ -f "${counter_file}" ] && n=\$(cat "${counter_file}")
n=\$((n + 1))
echo "\$n" > "${counter_file}"
if [ "${succeed_at}" -gt 0 ] && [ "\$n" -ge "${succeed_at}" ]; then
  echo "sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
  exit 0
fi
exit 1
EOF
  chmod +x "${fake_bin_dir}/docker"
}

fail=0

# Case 1: the first 3 calls fail, the 4th succeeds -- within the default
# 6-attempt budget. Must return that digest and take exactly 4 calls, never
# more (retrying past a success would be its own bug).
rm -f "$counter_file"
write_fake_docker 4
digest="$(
  PATH="${fake_bin_dir}:${PATH}" \
  MIRROR_INSPECT_INITIAL_DELAY=0 MIRROR_INSPECT_MAX_DELAY=0 \
  bash -c 'source "'"${verify_script}"'" && inspect_with_retry "ghcr.io/full-chaos/dev-health-acr/clickhouse/clickhouse-server:26.7"'
)"
attempts="$(cat "$counter_file")"
if [ "$digest" != "sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef" ]; then
  printf 'FAIL case 1: expected the digest from the 4th attempt, got %s\n' "$digest" >&2
  fail=1
elif [ "$attempts" -ne 4 ]; then
  printf 'FAIL case 1: expected exactly 4 docker invocations, got %s\n' "$attempts" >&2
  fail=1
else
  echo "OK   case 1: succeeds on attempt 4/6, stops immediately"
fi

# Case 2: docker never succeeds -- must return non-zero after EXACTLY the
# configured number of attempts, never fewer (giving up early hides a real
# outage) and never more (retrying forever hangs CI on a genuinely deleted
# image).
rm -f "$counter_file"
write_fake_docker 0
set +e
PATH="${fake_bin_dir}:${PATH}" \
  MIRROR_INSPECT_MAX_ATTEMPTS=5 MIRROR_INSPECT_INITIAL_DELAY=0 MIRROR_INSPECT_MAX_DELAY=0 \
  bash -c 'source "'"${verify_script}"'" && inspect_with_retry "ghcr.io/full-chaos/dev-health-acr/docker/dockerfile:mirror-deadbeefdead"'
rc=$?
set -e
attempts="$(cat "$counter_file")"
if [ "$rc" -eq 0 ]; then
  printf 'FAIL case 2: expected non-zero rc when docker never succeeds, got 0\n' >&2
  fail=1
elif [ "$attempts" -ne 5 ]; then
  printf 'FAIL case 2: expected exactly 5 attempts, got %s\n' "$attempts" >&2
  fail=1
else
  echo "OK   case 2: fails with non-zero rc after exactly 5/5 attempts"
fi

exit "$fail"
