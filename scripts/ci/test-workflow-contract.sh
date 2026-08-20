#!/usr/bin/env bash
set -euo pipefail

# test-workflow-contract.sh makes offline, static assertions about the
# shape of .github/workflows/ci.yml's job graph: that a single `verify`
# job exists and depends on (`needs:`) every other job, that it can only
# read green when every dependency actually succeeded, that Go caching is
# on, and that the race-suite shard count agrees with what test-shard.sh
# is actually invoked with.
#
# Each assertion is paired with a negative control below that mutates a
# temp copy of the file and requires the SAME assertion to fail against
# the mutated copy -- proving the check can actually catch the defect it
# claims to catch, not just that the current file happens to pass it.
#
# Usage: test-workflow-contract.sh [path-to-workflow]
#   Defaults to <repo root>/.github/workflows/ci.yml. An explicit path lets
#   the negative controls below point this same script's checks at
#   mutated copies of the file.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
default_workflow="$repo_root/.github/workflows/ci.yml"
workflow="${1:-$default_workflow}"

test -r "$workflow" || {
  printf '%s: cannot read workflow file: %s\n' "${0##*/}" "$workflow" >&2
  exit 2
}

# ---- YAML helpers (offline, no yq dependency) ------------------------------

# Print the top-level job names: 2-space-indented keys directly under `jobs:`.
list_jobs() {
  awk '
    /^jobs:/ { in_jobs=1; next }
    in_jobs && /^[^[:space:]]/ { in_jobs=0 }
    in_jobs && /^  [A-Za-z0-9_-]+:/ {
      line=$0
      sub(/^  /, "", line)
      sub(/:.*/, "", line)
      print line
    }
  ' "$1"
}

# Print the lines of one top-level job block: its own header line through
# the line before the next top-level job (or EOF).
job_block() {
  local file="$1" job="$2"
  awk -v job="$job" '
    $0 ~ "^  " job ":" { grab=1; print; next }
    grab && /^  [A-Za-z0-9_-]+:/ { grab=0 }
    grab { print }
  ' "$file"
}

# Print the `needs:` list items from a job block piped in on stdin.
list_needs() {
  awk '
    /^ {4}needs:/ { in_needs=1; next }
    in_needs && /^ {6}- / {
      line=$0
      sub(/^ {6}- /, "", line)
      print line
      next
    }
    in_needs { in_needs=0 }
  '
}

# ---- checks -----------------------------------------------------------
# Each check prints a diagnostic to stderr and returns 1 on failure, or
# returns 0 silently on success.

check_verify_job_exists() {
  local file="$1"
  list_jobs "$file" | grep -qx verify || {
    printf 'no top-level "verify" job found in %s\n' "$file" >&2
    return 1
  }
}

check_needs_matches_jobs() {
  local file="$1"
  local all_jobs other_jobs verify_needs missing extra status
  all_jobs="$(list_jobs "$file")"
  other_jobs="$(printf '%s\n' "$all_jobs" | grep -vx verify || true)"
  verify_needs="$(job_block "$file" verify | list_needs)"

  missing="$(comm -23 <(printf '%s\n' "$other_jobs" | LC_ALL=C sort) <(printf '%s\n' "$verify_needs" | LC_ALL=C sort))"
  extra="$(comm -13 <(printf '%s\n' "$other_jobs" | LC_ALL=C sort) <(printf '%s\n' "$verify_needs" | LC_ALL=C sort))"

  status=0
  if [ -n "$missing" ]; then
    printf 'job(s) missing from verify.needs: %s\n' "$(printf '%s' "$missing" | tr '\n' ' ')" >&2
    status=1
  fi
  if [ -n "$extra" ]; then
    printf 'verify.needs names job(s) that do not exist: %s\n' "$(printf '%s' "$extra" | tr '\n' ' ')" >&2
    status=1
  fi
  return "$status"
}

check_verify_if_always() {
  local file="$1"
  job_block "$file" verify | grep -Eq '^ {4}if: always\(\)' || {
    printf 'verify job is missing "if: always()"\n' >&2
    return 1
  }
}

# Scoped to the verify job block, not the whole file: a whole-file grep would
# keep passing if the gate's assertion step were gutted while either string
# survived somewhere else in the workflow (a comment, another job's script).
# The gate's own rejection logic is the thing under test, so only its block
# counts as evidence for it.
check_gate_rejects_nonsuccess() {
  local file="$1"
  local block status=0
  block="$(job_block "$file" verify)"
  printf '%s\n' "$block" | grep -qF '!= success' || {
    printf 'verify job is missing a "!= success" check\n' >&2
    status=1
  }
  printf '%s\n' "$block" | grep -qF 'at least one lane did not succeed' || {
    printf 'verify job is missing the "at least one lane did not succeed" failure message\n' >&2
    status=1
  }
  return "$status"
}

# container-scan reads the .tmp/container-oci/*.tar archives that
# container-oci publishes -- the single cross-step data dependency in this
# workflow. Separate runners get separate checkouts and separate .tmp/, so
# splitting these two into different jobs would leave scan hard-failing on a
# missing tarball at runtime. This pins them to one job.
check_container_oci_scan_same_job() {
  local file="$1"
  local job scan_job=""
  while IFS= read -r job; do
    if job_block "$file" "$job" | grep -qF 'make container-scan'; then
      scan_job="$job"
      break
    fi
  done < <(list_jobs "$file")

  if [ -z "$scan_job" ]; then
    printf 'no job runs "make container-scan"\n' >&2
    return 1
  fi

  job_block "$file" "$scan_job" | grep -qF 'make container-oci' || {
    printf 'job "%s" runs "make container-scan" without "make container-oci": scan consumes the .tmp/container-oci/*.tar archives oci publishes, so they must stay in one job\n' \
      "$scan_job" >&2
    return 1
  }
}

# CHAOS-3974: scripts/ci/test-shard.sh excludes isolated_packages from the
# round-robin race matrix on the promise that they run in their own
# dedicated job with their own timeout instead. Nothing else enforces that
# promise -- a job rename or deletion that forgot to update the isolation
# list would silently drop the isolated package from CI coverage entirely,
# the exact failure mode test-shard-closure.sh's runtime proof does not
# catch (it proves the union is total; it can't tell a genuinely-run job
# apart from an isolation list edited to match one that no longer exists).
check_isolated_devhealthschema_job() {
  local file="$1" isolated job found_job=""
  isolated="$("$repo_root/scripts/ci/test-shard.sh" isolated)"
  if [ -z "$isolated" ]; then
    printf 'scripts/ci/test-shard.sh isolated printed nothing\n' >&2
    return 1
  fi

  while IFS= read -r job; do
    if job_block "$file" "$job" | grep -qF 'test-shard.sh isolated'; then
      found_job="$job"
      break
    fi
  done < <(list_jobs "$file")

  if [ -z "$found_job" ]; then
    printf 'no job in %s invokes "scripts/ci/test-shard.sh isolated" to run the isolated package(s): %s\n' \
      "$file" "$isolated" >&2
    return 1
  fi

  job_block "$file" "$found_job" | grep -qE 'GOTEST_TIMEOUT=|-timeout[= ]' || {
    printf 'job "%s" runs the isolated package(s) without its own explicit timeout override\n' \
      "$found_job" >&2
    return 1
  }
}

check_go_cache() {
  local file="$1"
  local status=0
  if grep -q 'cache: false' "$file"; then
    printf 'a setup-go step still has cache: false\n' >&2
    status=1
  fi
  if ! grep -q 'cache: true' "$file"; then
    printf 'no setup-go step has cache: true\n' >&2
    status=1
  fi
  return "$status"
}

check_race_shard_agreement() {
  local file="$1"
  local race_block shard_line shard_inside shard_count shard_call total
  race_block="$(job_block "$file" race)"

  shard_line="$(printf '%s\n' "$race_block" | grep -E 'shard: *\[' | head -n1 || true)"
  if [ -z "$shard_line" ]; then
    printf 'race job has no "shard: [...]" matrix\n' >&2
    return 1
  fi
  shard_inside="$(printf '%s' "$shard_line" | sed -E 's/.*\[([^]]*)\].*/\1/')"
  shard_count="$(printf '%s' "$shard_inside" | awk -F',' '{print NF}')"

  shard_call="$(printf '%s\n' "$race_block" | grep -E '^[[:space:]]*[a-z_]+="?\$\(scripts/ci/test-shard\.sh' | head -n1 || true)"
  if [ -z "$shard_call" ]; then
    printf 'race job does not invoke scripts/ci/test-shard.sh\n' >&2
    return 1
  fi
  total="$(printf '%s\n' "$shard_call" | grep -oE '[0-9]+' | tail -n1 || true)"

  if [ -z "$total" ] || [ "$shard_count" != "$total" ]; then
    printf 'race matrix has %s shard(s) but test-shard.sh is called with total=%s\n' \
      "$shard_count" "${total:-<none>}" >&2
    return 1
  fi

  # Counting entries is not enough: `shard: [1, 2, 3, 3]` has four entries and
  # would satisfy a count check while running shard 3 twice and shard 4 never,
  # silently dropping that shard's packages from the race suite with every job
  # still green. Require the matrix to be exactly the set 1..total.
  local expected actual
  expected="$(seq 1 "$total" | LC_ALL=C sort)"
  actual="$(printf '%s' "$shard_inside" | tr ',' '\n' | tr -d '[:blank:]' | grep -v '^$' | LC_ALL=C sort)"
  if [ "$expected" != "$actual" ]; then
    printf 'race matrix indices must be exactly 1..%s, got: %s\n' \
      "$total" "$(printf '%s' "$shard_inside" | tr -d '[:space:]')" >&2
    return 1
  fi
}

run_all_checks() {
  local file="$1"
  check_verify_job_exists "$file"
  check_needs_matches_jobs "$file"
  check_verify_if_always "$file"
  check_gate_rejects_nonsuccess "$file"
  check_go_cache "$file"
  check_race_shard_agreement "$file"
  check_container_oci_scan_same_job "$file"
  check_isolated_devhealthschema_job "$file"
}

# ---- positive run -------------------------------------------------------

run_all_checks "$workflow"
printf 'PASS: %s satisfies all workflow contract checks\n' "$workflow"

# ---- negative controls ---------------------------------------------------
# Each mutation below must make its corresponding check FAIL. mktemp -d
# output is cleaned up on exit regardless of how the script ends.

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

assert_check_fails() {
  local desc="$1" fn="$2" file="$3"
  if "$fn" "$file" 2>/dev/null; then
    printf 'NEGATIVE CONTROL FAILED: %s -- "%s" passed on a mutated file that should have failed it\n' \
      "$desc" "$fn" >&2
    exit 1
  fi
}

# (a) drop one job name from verify's needs.
needs_missing_job="$tmpdir/needs-missing-job.yml"
awk '/^ {6}- build$/ { next } { print }' "$workflow" > "$needs_missing_job"
assert_check_fails 'dropped "build" from verify.needs' check_needs_matches_jobs "$needs_missing_job"

# (b) remove "if: always()" from verify.
missing_if_always="$tmpdir/missing-if-always.yml"
awk '/^ {4}if: always\(\)$/ { next } { print }' "$workflow" > "$missing_if_always"
assert_check_fails 'removed if: always() from verify' check_verify_if_always "$missing_if_always"

# (c) flip one "cache: true" back to "cache: false".
cache_false="$tmpdir/cache-false.yml"
awk '!done && /cache: true/ { sub(/cache: true/, "cache: false"); done=1 } { print }' "$workflow" > "$cache_false"
assert_check_fails 'flipped one cache: true back to cache: false' check_go_cache "$cache_false"

# (d) shrink the race matrix to 3 shards while test-shard.sh is still
# called with total 4.
mismatched_shards="$tmpdir/mismatched-shards.yml"
sed 's/shard: \[1, 2, 3, 4\]/shard: [1, 2, 3]/' "$workflow" > "$mismatched_shards"
assert_check_fails 'shrank race matrix to 3 shards without updating test-shard.sh total' \
  check_race_shard_agreement "$mismatched_shards"

# (e) gut the gate's non-success rejection while leaving the job in place.
gate_accepts_anything="$tmpdir/gate-accepts-anything.yml"
awk '
  /^ {2}verify:/ { in_verify=1 }
  in_verify && /^ {2}[A-Za-z0-9_-]+:/ && !/^ {2}verify:/ { in_verify=0 }
  in_verify && /!= success/ { next }
  in_verify && /at least one lane did not succeed/ { next }
  { print }
' "$workflow" > "$gate_accepts_anything"
assert_check_fails 'stripped the non-success rejection out of the verify job' \
  check_gate_rejects_nonsuccess "$gate_accepts_anything"

# (f) rename the verify job away entirely.
no_verify_job="$tmpdir/no-verify-job.yml"
sed 's/^  verify:$/  verify-old:/' "$workflow" > "$no_verify_job"
assert_check_fails 'renamed the verify job away' check_verify_job_exists "$no_verify_job"

# (g) split container-oci out of the job that runs container-scan.
oci_split_from_scan="$tmpdir/oci-split-from-scan.yml"
awk '/make container-oci$/ { next } { print }' "$workflow" > "$oci_split_from_scan"
assert_check_fails 'removed "make container-oci" from the job that runs container-scan' \
  check_container_oci_scan_same_job "$oci_split_from_scan"

# (h) duplicate a shard index: same entry count, but one shard runs twice and
# another never runs, so its packages silently leave the race suite.
duplicate_shard="$tmpdir/duplicate-shard.yml"
sed 's/shard: \[1, 2, 3, 4\]/shard: [1, 2, 3, 3]/' "$workflow" > "$duplicate_shard"
assert_check_fails 'duplicated a shard index, dropping another shard entirely' \
  check_race_shard_agreement "$duplicate_shard"

# (i) shard indices outside 1..total.
out_of_range_shard="$tmpdir/out-of-range-shard.yml"
sed 's/shard: \[1, 2, 3, 4\]/shard: [1, 2, 5, 9]/' "$workflow" > "$out_of_range_shard"
assert_check_fails 'used shard indices outside 1..total' \
  check_race_shard_agreement "$out_of_range_shard"

# (j) remove the isolated-package job so the isolated package's dedicated
# scope silently disappears while test-shard.sh still excludes it from the
# round-robin shards.
isolated_job_removed="$tmpdir/isolated-job-removed.yml"
awk '
  /^  race-devhealthschema:/ { skip=1 }
  skip && /^  [A-Za-z0-9_-]+:/ && !/^  race-devhealthschema:/ { skip=0 }
  !skip { print }
' "$workflow" > "$isolated_job_removed"
assert_check_fails 'removed the race-devhealthschema job entirely' \
  check_isolated_devhealthschema_job "$isolated_job_removed"

# (k) keep the job but drop its explicit timeout override, so it would
# silently fall back to the shared race-matrix default this isolation
# exists to stop growing.
isolated_job_no_timeout="$tmpdir/isolated-job-no-timeout.yml"
sed 's/ GOTEST_TIMEOUT=900s//' "$workflow" > "$isolated_job_no_timeout"
assert_check_fails 'dropped the isolated job'"'"'s explicit GOTEST_TIMEOUT override' \
  check_isolated_devhealthschema_job "$isolated_job_no_timeout"

printf 'PASS: all negative controls correctly failed their check\n'
