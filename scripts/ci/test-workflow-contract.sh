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
  # v1.6: self-hosted legs share one GOMODCACHE/GOCACHE hostPath mount
  # (helm rev 6) across every concurrent pool job -- setup-go's own
  # cache:true would restore/save a tarball against those same paths and
  # can race the live mount, corrupting it for every other job on the
  # pool. So cache:false is now REQUIRED on self-hosted legs and
  # FORBIDDEN on hosted ones (which have no such shared mount and should
  # keep using setup-go's own cache).
  local job
  while IFS= read -r job; do
    local block
    block="$(job_block "$file" "$job")"
    if printf '%s' "$block" | grep -qE 'runs-on: *\[self-hosted'; then
      if printf '%s' "$block" | grep -q 'cache: true'; then
        printf 'self-hosted job "%s" has setup-go cache: true -- it shares a GOMODCACHE/GOCACHE mount with every other pool job; a cache restore here can race and corrupt it\n' \
          "$job" >&2
        status=1
      fi
    else
      if printf '%s' "$block" | grep -q 'cache: false'; then
        printf 'hosted job "%s" has setup-go cache: false -- only self-hosted legs skip setup-go'"'"'s cache (they share a hostPath mount instead)\n' \
          "$job" >&2
        status=1
      fi
    fi
  done < <(list_jobs "$file")
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

# The endpoint-profile contract gate is the one CI step that runs the
# real-tree auth-surface proof, and the proof's own fail-closed branch keys
# on the ACR_CONTRACT_GATE marker this step sets. That makes deleting or
# gutting the step invisible at runtime: with no step, nothing sets the
# marker, so the proof skips with a reason nobody reads and every job stays
# green while guardrail G-1 goes unenforced.
#
# The runtime guard proves the inputs ARRIVED where the gate runs. This
# check proves the step still EXISTS and still supplies all four values.
# Neither closes the class alone -- that is the whole reason both exist.
check_endpoint_profile_gate_step() {
  local file="$1" block
  block="$(awk '
    /Verify endpoint-profile inventory against the pinned ops contract/ { grab=1 }
    grab && /^      - / && !/Verify endpoint-profile inventory/ { grab=0 }
    grab { print }
  ' "$file")"

  if [ -z "$block" ]; then
    printf 'no "Verify endpoint-profile inventory against the pinned ops contract" step found in %s -- guardrail G-1 would go unenforced with every job green\n' "$file" >&2
    return 1
  fi

  local key
  for key in ACR_ENDPOINT_PROFILE_SCHEMA ACR_CREDENTIAL_CLASSES ACR_CREDENTIAL_CLASSES_SCHEMA; do
    if ! printf '%s' "$block" | grep -q "$key:"; then
      printf 'the endpoint-profile gate step does not set %s -- the gate cannot validate an input it is not given\n' "$key" >&2
      return 1
    fi
  done

  if ! printf '%s' "$block" | grep -q 'ACR_CONTRACT_GATE: required'; then
    printf 'the endpoint-profile gate step does not set ACR_CONTRACT_GATE: required -- without it the real-tree proof SKIPS instead of failing when its inputs are missing\n' >&2
    return 1
  fi

  if ! printf '%s' "$block" | grep -q 'go test ./ci/checkendpointprofiles/'; then
    printf 'the endpoint-profile gate step does not run go test ./ci/checkendpointprofiles/ -- it sets the environment for a proof it never invokes\n' >&2
    return 1
  fi

  # Running the gate is not enough: its FAILURE has to reach the job.
  # Merge-gate round 4 (EXECUTED): with only the text check above,
  # `go test ./ci/checkendpointprofiles/... || true` made a failing contract
  # gate exit 0 and this script still printed both PASS lines. Asserting the
  # command is PRESENT is not asserting the property HOLDS.
  if printf '%s' "$block" | grep -Eq '\|\|[[:space:]]*true|continue-on-error:[[:space:]]*true|;[[:space:]]*exit[[:space:]]+0|\|\|[[:space:]]*:'; then
    printf 'the endpoint-profile gate step swallows its own failure (|| true, continue-on-error, or an unconditional exit 0) -- a failing gate would leave the job green\n' >&2
    return 1
  fi
}

# A pin that accepts a branch name is not a pin: the sparse checkout would
# follow ops's moving default branch with no acr commit recording it. The
# workflow must reject the shape BEFORE resolving the ref.
check_pin_requires_full_sha() {
  local file="$1" block
  # Anchored to the pin-validation STEP, not grepped from the whole document.
  # Merge-gate round 4 (EXECUTED): a document-wide grep was satisfied by a
  # decoy `# [0-9a-f]{40}` comment while the real command was loosened to
  # `^[a-z0-9]+$`, so the pin check passed and `main` in ci/ops-contract.pin
  # would have been accepted, fetched and checked out. The regex has to be
  # where the validation happens.
  block="$(awk '
    /Verify the pin names an immutable commit/ { grab=1 }
    grab && /^      - name: / && !/Verify the pin names an immutable commit/ { grab=0 }
    grab { print }
  ' "$file")"

  if [ -z "$block" ]; then
    printf 'no pin-shape validation step ("Verify the pin names an immutable commit") found in %s\n' "$file" >&2
    return 1
  fi
  # Strip comments so a decoy in a comment cannot satisfy the check.
  if ! printf '%s' "$block" | sed 's/#.*$//' | grep -qF '[0-9a-f]{40}'; then
    printf 'the pin-validation step does not require a full 40-hex commit SHA in its own command (a comment does not count) -- a branch name would resolve and silently float\n' >&2
    return 1
  fi
}

# Enforcing the pin's SHAPE is worthless if the checkout does not USE it.
# Merge-gate round 3 (EXECUTED): check_pin_requires_full_sha only greps the
# workflow for the regex, so changing the sparse-checkout ref to `main` left
# BOTH workflow-contract PASS lines intact while CI would have validated
# against ops's moving default branch. The pin was enforced and bypassed at
# the same time.
#
# That is the same mistake as the check above it: keying on the presence of
# the guard rather than on the property the guard exists to deliver. Bind the
# ref to the validated pin output explicitly.
check_pin_binds_checkout_ref() {
  local file="$1" block
  block="$(awk '
    /repository: full-chaos\/dev-health-ops/ { grab=1 }
    grab && /^      - / { grab=0 }
    grab { print }
  ' "$file")"

  if [ -z "$block" ]; then
    printf 'no checkout step for full-chaos/dev-health-ops found in %s\n' "$file" >&2
    return 1
  fi
  # SC2016 is exactly what we want here: `${{ ... }}` is a GitHub Actions
  # expression that must be matched LITERALLY in the workflow text. Expanding
  # it in the shell would compare against an empty string and the check would
  # pass on any workflow at all -- the same "guard present but property not
  # held" failure this function exists to catch.
  # shellcheck disable=SC2016
  if ! printf '%s' "$block" | grep -qF 'ref: ${{ steps.ops-pin.outputs.sha }}'; then
    # shellcheck disable=SC2016
    printf 'the ops-contract checkout does not use the validated pin (expected ref: ${{ steps.ops-pin.outputs.sha }}) -- the full-SHA check would pass while CI followed a floating ref\n' >&2
    return 1
  fi
}

# Runner-routing contract v1.6 pair invariants. Every entry names a
# `<base>-hosted` / `<base>-self-hosted` job pair: both must exist, both
# must carry the identical `name:` (the stable check name a PR sees
# across the SELF_HOSTED_RUNNERS flip), and their `if:` gates must be the
# canonical exact-complement pair (never both true, never both false,
# including the fork-PR carve-out that always falls back to hosted).
V16_PAIR_BASES="mirror-preflight scripts build contracts race-devhealthschema"

check_v16_pairs() {
  local file="$1" base status=0
  for base in $V16_PAIR_BASES; do
    local hosted_key="${base}-hosted" pool_key="${base}-self-hosted"
    local hosted_block pool_block hosted_name pool_name

    if ! grep -qE "^  ${hosted_key}:" "$file"; then
      printf 'v1.6 pair "%s": no job "%s" found\n' "$base" "$hosted_key" >&2
      status=1
      continue
    fi
    if ! grep -qE "^  ${pool_key}:" "$file"; then
      printf 'v1.6 pair "%s": no job "%s" found\n' "$base" "$pool_key" >&2
      status=1
      continue
    fi

    hosted_block="$(job_block "$file" "$hosted_key")"
    pool_block="$(job_block "$file" "$pool_key")"

    # Both legs must declare an explicit `name:` (a bare job key is not
    # enough -- the whole point is a STABLE name across the flip), and
    # those two names must be identical.
    hosted_name="$(printf '%s' "$hosted_block" | sed -n -E 's/^    name: (.*)$/\1/p' | head -n1)"
    pool_name="$(printf '%s' "$pool_block" | sed -n -E 's/^    name: (.*)$/\1/p' | head -n1)"
    if [ -z "$hosted_name" ] || [ -z "$pool_name" ]; then
      printf 'v1.6 pair "%s": both "%s" and "%s" must declare an explicit name:\n' \
        "$base" "$hosted_key" "$pool_key" >&2
      status=1
    elif [ "$hosted_name" != "$pool_name" ]; then
      printf 'v1.6 pair "%s": "%s" is named "%s" but "%s" is named "%s" -- the check reported to a PR would move when the switch flips\n' \
        "$base" "$hosted_key" "$hosted_name" "$pool_key" "$pool_name" >&2
      status=1
    fi

    # The canonical exact-complement `if:` markers. Not a full logical
    # proof of exhaustiveness/exclusivity -- that would need a real
    # expression evaluator -- but pins the known-correct textual pattern
    # (both directions of the switch check, plus the fork-PR carve-out on
    # both legs) so a hand-edit that drops one clause is caught.
    if ! printf '%s' "$hosted_block" | grep -qF "vars.SELF_HOSTED_RUNNERS != 'enabled'"; then
      printf 'v1.6 pair "%s": hosted leg "%s" if: is missing the SELF_HOSTED_RUNNERS != enabled clause\n' \
        "$base" "$hosted_key" >&2
      status=1
    fi
    if ! printf '%s' "$hosted_block" | grep -qF 'head.repo.full_name != github.repository'; then
      printf 'v1.6 pair "%s": hosted leg "%s" if: is missing the fork-PR carve-out (forks must always fall back to hosted)\n' \
        "$base" "$hosted_key" >&2
      status=1
    fi
    if ! printf '%s' "$pool_block" | grep -qF "vars.SELF_HOSTED_RUNNERS == 'enabled'"; then
      printf 'v1.6 pair "%s": self-hosted leg "%s" if: is missing the SELF_HOSTED_RUNNERS == enabled clause\n' \
        "$base" "$pool_key" >&2
      status=1
    fi
    if ! printf '%s' "$pool_block" | grep -qF 'head.repo.full_name == github.repository'; then
      printf 'v1.6 pair "%s": self-hosted leg "%s" if: is missing the fork-PR exclusion (forks must never reach the pool)\n' \
        "$base" "$pool_key" >&2
      status=1
    fi
  done
  return "$status"
}

# The aggregator's own pair-tolerant logic: verify's assertion script must
# actually implement "one success + partner skipped = pass, both skipped
# = fail" for every v1.6 pair, not just list the pair's two job keys in
# `needs:` (check_needs_matches_jobs already proves that half). Scoped to
# the verify job block so a copy of this text living only in a comment
# elsewhere in the file cannot satisfy it.
check_verify_pair_logic() {
  local file="$1" block status=0
  block="$(job_block "$file" verify)"

  if ! printf '%s' "$block" | grep -qF 'PAIRS'; then
    printf 'verify job has no PAIRS-driven pair check -- the v1.6 hosted/self-hosted pairs would be asserted as if they were plain single jobs, and a by-design skip on the leg that did not run would fail the whole gate\n' >&2
    return 1
  fi
  # shellcheck disable=SC2016
  if ! printf '%s' "$block" | grep -qF '!= success ] && [ "$hosted_result" != skipped'; then
    printf 'verify'"'"'s pair check does not tolerate a skipped hosted leg\n' >&2
    status=1
  fi
  if ! printf '%s' "$block" | grep -qF 'neither'; then
    printf 'verify has no "neither ... ran" guard -- if both pair members were somehow skipped, the gate would not notice that lane never ran at all\n' >&2
    status=1
  fi
  return "$status"
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
  check_endpoint_profile_gate_step "$file"
  check_pin_requires_full_sha "$file"
  check_pin_binds_checkout_ref "$file"
  check_v16_pairs "$file"
  check_verify_pair_logic "$file"
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
awk '/^ {6}- build-hosted$/ { next } { print }' "$workflow" > "$needs_missing_job"
assert_check_fails 'dropped "build-hosted" from verify.needs' check_needs_matches_jobs "$needs_missing_job"

# (b) remove "if: always()" from verify.
missing_if_always="$tmpdir/missing-if-always.yml"
awk '/^ {4}if: always\(\)$/ { next } { print }' "$workflow" > "$missing_if_always"
assert_check_fails 'removed if: always() from verify' check_verify_if_always "$missing_if_always"

# (c) flip a HOSTED job's "cache: true" to "cache: false" -- hosted legs
# have no shared mount and should keep using setup-go's own cache.
cache_false="$tmpdir/cache-false.yml"
awk '!done && /cache: true/ { sub(/cache: true/, "cache: false"); done=1 } { print }' "$workflow" > "$cache_false"
assert_check_fails 'flipped a hosted job'"'"'s cache: true to cache: false' check_go_cache "$cache_false"

# (c2) flip a SELF-HOSTED job's "cache: false" to "cache: true" -- v1.6
# self-hosted legs share one GOMODCACHE/GOCACHE mount, and setup-go's own
# cache would race it.
cache_true_on_pool="$tmpdir/cache-true-on-pool.yml"
awk '!done && /cache: false/ { sub(/cache: false/, "cache: true"); done=1 } { print }' "$workflow" > "$cache_true_on_pool"
assert_check_fails 'flipped a self-hosted job'"'"'s cache: false to cache: true' check_go_cache "$cache_true_on_pool"

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

# (j) remove BOTH race-devhealthschema-{hosted,self-hosted} pair members
# so the isolated package's dedicated scope silently disappears while
# test-shard.sh still excludes it from the round-robin shards. Removing
# only one member would not trip this check -- the other still satisfies
# it, which is the whole point of the v1.6 pair (exactly one runs).
isolated_job_removed="$tmpdir/isolated-job-removed.yml"
awk '
  /^  race-devhealthschema-hosted:/ { skip=1 }
  /^  race-devhealthschema-self-hosted:/ { skip=1 }
  skip && /^  [A-Za-z0-9_-]+:/ && !/^  race-devhealthschema-hosted:/ && !/^  race-devhealthschema-self-hosted:/ { skip=0 }
  !skip { print }
' "$workflow" > "$isolated_job_removed"
assert_check_fails 'removed both race-devhealthschema pair members entirely' \
  check_isolated_devhealthschema_job "$isolated_job_removed"

# (k) keep the job but drop its explicit timeout override, so it would
# silently fall back to the shared race-matrix default this isolation
# exists to stop growing.
isolated_job_no_timeout="$tmpdir/isolated-job-no-timeout.yml"
sed 's/ GOTEST_TIMEOUT=900s//' "$workflow" > "$isolated_job_no_timeout"
assert_check_fails 'dropped the isolated job'"'"'s explicit GOTEST_TIMEOUT override' \
  check_isolated_devhealthschema_job "$isolated_job_no_timeout"

# (l) delete the endpoint-profile gate step. Nothing then sets
# ACR_CONTRACT_GATE, so the real-tree proof skips rather than fails and the
# build stays green with the auth-surface contract unchecked.
gate_step_removed="$tmpdir/gate-step-removed.yml"
awk '
  /Verify endpoint-profile inventory against the pinned ops contract/ { skip=1 }
  skip && /^      - / && !/Verify endpoint-profile inventory/ { skip=0 }
  !skip { print }
' "$workflow" > "$gate_step_removed"
assert_check_fails 'deleted the endpoint-profile gate step' \
  check_endpoint_profile_gate_step "$gate_step_removed"

# (m) keep the step but drop the ACR_CONTRACT_GATE marker, which downgrades
# the real-tree proof from FAIL to SKIP exactly where it is meant to run.
gate_marker_removed="$tmpdir/gate-marker-removed.yml"
awk '/ACR_CONTRACT_GATE: required/ { next } { print }' "$workflow" > "$gate_marker_removed"
assert_check_fails 'dropped ACR_CONTRACT_GATE: required from the gate step' \
  check_endpoint_profile_gate_step "$gate_marker_removed"

# (n) keep the step and the marker but stop supplying the credential-class
# schema, so the vocabulary document goes back to being unvalidated.
gate_input_removed="$tmpdir/gate-input-removed.yml"
awk '/ACR_CREDENTIAL_CLASSES_SCHEMA:/ { next } { print }' "$workflow" > "$gate_input_removed"
assert_check_fails 'dropped ACR_CREDENTIAL_CLASSES_SCHEMA from the gate step' \
  check_endpoint_profile_gate_step "$gate_input_removed"

# (o) remove the pin's full-SHA requirement, restoring the floating-ref hole.
pin_regex_removed="$tmpdir/pin-regex-removed.yml"
grep -vF '[0-9a-f]{40}' "$workflow" > "$pin_regex_removed"
assert_check_fails 'removed the full-SHA requirement on ci/ops-contract.pin' \
  check_pin_requires_full_sha "$pin_regex_removed"

# (p) keep the pin regex but point the checkout at a floating ref. The
# shape check still passes; the checkout no longer uses the thing it checked.
checkout_floating_ref="$tmpdir/checkout-floating-ref.yml"
sed 's/ref: ${{ steps.ops-pin.outputs.sha }}/ref: main/' "$workflow" > "$checkout_floating_ref"
assert_check_fails 'pointed the ops-contract checkout at a floating ref while keeping the pin regex' \
  check_pin_binds_checkout_ref "$checkout_floating_ref"

# (q) keep the gate step but swallow its failure, so a failing contract gate
# leaves the job green.
gate_swallows_failure="$tmpdir/gate-swallows-failure.yml"
sed 's|run: go test ./ci/checkendpointprofiles/...|run: go test ./ci/checkendpointprofiles/... \|\| true|' \
  "$workflow" > "$gate_swallows_failure"
assert_check_fails 'made the gate step swallow its own failure with || true' \
  check_endpoint_profile_gate_step "$gate_swallows_failure"

# (r) loosen the real pin regex but leave a decoy in a comment, which is what
# defeated the document-wide grep.
pin_decoy_comment="$tmpdir/pin-decoy-comment.yml"
sed "s|grep -Eq '\^\[0-9a-f\]{40}\$'|grep -Eq '^[a-z0-9]+\$' # [0-9a-f]{40}|" \
  "$workflow" > "$pin_decoy_comment"
assert_check_fails 'loosened the pin regex while leaving a decoy [0-9a-f]{40} in a comment' \
  check_pin_requires_full_sha "$pin_decoy_comment"

# (s) rename one pair member's name: away from its partner's -- the check
# reported to a PR would move when the switch flips.
pair_name_mismatch="$tmpdir/pair-name-mismatch.yml"
awk '!done && /^    name: scripts$/ { sub(/name: scripts/, "name: scripts-renamed"); done=1 } { print }' \
  "$workflow" > "$pair_name_mismatch"
assert_check_fails 'renamed scripts-hosted'"'"'s name: away from scripts-self-hosted'"'"'s' \
  check_v16_pairs "$pair_name_mismatch"

# (t) drop the fork-PR carve-out from a self-hosted leg's if: -- would let
# a fork PR reach the pool.
pair_fork_guard_dropped="$tmpdir/pair-fork-guard-dropped.yml"
sed "s|&& (github.event_name != 'pull_request'\$|\&\& (true|" "$workflow" \
  | awk '!done && /head.repo.full_name == github.repository/ { sub(/head.repo.full_name == github.repository/, "true"); done=1 } { print }' \
  > "$pair_fork_guard_dropped"
assert_check_fails 'dropped the fork-PR exclusion from a self-hosted leg'"'"'s if:' \
  check_v16_pairs "$pair_fork_guard_dropped"

# (u) a pair base with a hosted leg but no self-hosted leg at all.
pair_member_missing="$tmpdir/pair-member-missing.yml"
awk '
  /^  scripts-self-hosted:/ { skip=1 }
  skip && /^  [A-Za-z0-9_-]+:/ && !/^  scripts-self-hosted:/ { skip=0 }
  !skip { print }
' "$workflow" > "$pair_member_missing"
assert_check_fails 'removed scripts-self-hosted while scripts-hosted remains' \
  check_v16_pairs "$pair_member_missing"

# (v) verify's pair check keeps the pair's two keys in `needs:` but drops the
# both-skipped guard -- a pair where neither leg ran would silently pass.
pair_neither_guard_dropped="$tmpdir/pair-neither-guard-dropped.yml"
awk '/printf .neither/ { next } /neither .base.-hosted/ { next } { print }' "$workflow" \
  > "$pair_neither_guard_dropped"
assert_check_fails 'dropped verify'"'"'s "neither ... ran" guard' \
  check_verify_pair_logic "$pair_neither_guard_dropped"

# (w) verify's pair check loses the PAIRS-driven loop entirely, reverting to
# treating every need as a plain job that must be a literal success -- which
# would fail the gate on every run, since the leg that did not run always
# reports skipped.
pair_check_removed="$tmpdir/pair-check-removed.yml"
awk '!/PAIRS/ { print }' "$workflow" > "$pair_check_removed"
assert_check_fails 'removed every reference to PAIRS from verify'"'"'s pair check' \
  check_verify_pair_logic "$pair_check_removed"

printf 'PASS: all negative controls correctly failed their check\n'
