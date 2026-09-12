#!/usr/bin/env bash
set -euo pipefail

# test-shard-closure.sh proves, at runtime, that the sharded test matrices in
# .github/workflows/ci.yml actually cover the whole module: for each sharded
# job it reads the shard indices and the shard total straight out of the
# workflow, invokes scripts/ci/test-shard.sh exactly the way CI does for every
# index, and requires the union of those invocations to equal `go list ./...`
# with no package listed twice.
#
# Two matrices are proven, because a package dropped from either one is a
# package CI stops testing in that mode:
#   race  -- round-robin WITHOUT the isolated packages, which run in their own
#            dedicated race job (see race-devhealthschema in ci.yml); the
#            isolated list therefore joins the union here.
#   unit  -- round-robin WITH the isolated packages (`--with-isolated`), since
#            the isolation is a -race cost decision and the plain coverage run
#            has always covered them as part of `./...`.
# Which of the two shapes a job uses is read from the job's own test-shard.sh
# invocation, not assumed, so a job that changes shape is measured as it is.
#
# This is deliberately separate from test-workflow-contract.sh. That script
# is a static check that runs in a Go-less job; this one needs a Go toolchain
# because only running the real partition against the real package list can
# show that no package escapes the suite. A static agreement check can prove
# the matrix is well-formed, but not that the partition is total -- and a
# lane that silently tests fewer packages still reports success.
#
# Every assertion below is paired with a negative control that mutates a copy
# of the union and requires the SAME comparison to reject it, so a green run
# means the comparison can actually see a missing or doubled package rather
# than that it happened to have nothing to say.
#
# Usage: test-shard-closure.sh [path-to-workflow]

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
workflow="${1:-$repo_root/.github/workflows/ci.yml}"

test -r "$workflow" || {
  printf '%s: cannot read workflow file: %s\n' "${0##*/}" "$workflow" >&2
  exit 2
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

# Print the lines of one top-level job block: its header through the line
# before the next top-level job (or EOF).
job_block() {
  awk -v job="$1" '
    $0 ~ "^  " job ":" { grab=1; print; next }
    grab && /^  [A-Za-z0-9_-]+:/ { grab=0 }
    grab { print }
  ' "$workflow"
}

# Write the union of one job's shards, one package per line, to $2.
# Reads the indices, the total, and whether the job shards with or without the
# isolated packages out of the job's own block.
job_shard_union() {
  local job="$1" out="$2"
  local block shard_line shard_call total with_isolated=0
  local -a indices=()

  block="$(job_block "$job")"
  if [ -z "$block" ]; then
    printf '%s: no job "%s" in %s\n' "${0##*/}" "$job" "$workflow" >&2
    return 1
  fi

  shard_line="$(printf '%s\n' "$block" | grep -E 'shard: *\[' | head -n1 || true)"
  if [ -z "$shard_line" ]; then
    printf '%s: %s job has no "shard: [...]" matrix in %s\n' "${0##*/}" "$job" "$workflow" >&2
    return 1
  fi

  shard_call="$(printf '%s\n' "$block" \
    | grep -E '^[[:space:]]*[a-z_]+="?\$\(scripts/ci/test-shard\.sh' | head -n1 || true)"
  if [ -z "$shard_call" ]; then
    printf '%s: %s job does not invoke scripts/ci/test-shard.sh in %s\n' "${0##*/}" "$job" "$workflow" >&2
    return 1
  fi

  case "$shard_call" in
    *--with-isolated*) with_isolated=1 ;;
  esac

  total="$(printf '%s\n' "$shard_call" | grep -oE '[0-9]+' | tail -n1 || true)"
  if [ -z "$total" ]; then
    printf '%s: could not read the shard total from: %s\n' "${0##*/}" "$shard_call" >&2
    return 1
  fi

  mapfile -t indices < <(
    printf '%s' "$shard_line" \
      | sed -E 's/.*\[([^]]*)\].*/\1/' \
      | tr ',' '\n' | tr -d '[:blank:]' | grep -v '^$'
  )

  : >"$out"
  local index
  for index in "${indices[@]}"; do
    if [ "$with_isolated" -eq 1 ]; then
      "$repo_root/scripts/ci/test-shard.sh" --with-isolated "$index" "$total" | tr ' ' '\n' >>"$out"
    else
      "$repo_root/scripts/ci/test-shard.sh" "$index" "$total" | tr ' ' '\n' >>"$out"
    fi
  done

  if [ "$with_isolated" -eq 0 ]; then
    # test-shard.sh excludes isolated_packages from the round-robin
    # split above and runs them in their own dedicated CI job instead (see
    # race-devhealthschema in ci.yml). They are still part of the suite this
    # script proves total, so they join the union here rather than being
    # missed as a package no shard covers.
    "$repo_root/scripts/ci/test-shard.sh" isolated | tr ' ' '\n' >>"$out"
  fi

  printf '%s\t%s\t%s\n' "$total" "$with_isolated" "$(printf '%s,' "${indices[@]}" | sed 's/,$//')"
}

# Compare one sorted union against the sorted package list. Returns non-zero
# (and explains) when a package is covered twice or when the union is not
# exactly the package list. Diagnostics go to stderr so a caller running this
# as a negative control can discard them.
compare_union() {
  local job="$1" union_sorted="$2" all_sorted="$3"
  local duplicates

  duplicates="$(LC_ALL=C uniq -d "$union_sorted")"
  if [ -n "$duplicates" ]; then
    printf '%s: these packages are covered by more than one %s shard:\n%s\n' \
      "${0##*/}" "$job" "$duplicates" >&2
    return 1
  fi

  if ! diff -u "$all_sorted" "$union_sorted" >"$tmpdir/diff.$job"; then
    # shellcheck disable=SC2016  # backticked `go list ./...` is prose, not a shell expansion
    printf '%s: the union of the %s shards is not exactly `go list ./...`\n' "${0##*/}" "$job" >&2
    printf '  (-) listed by go but covered by no shard; (+) covered but not listed\n' >&2
    cat "$tmpdir/diff.$job" >&2
    return 1
  fi
}

# For one job's shards (their indices, total, and whether --with-isolated
# applies): fails if any single shard's OWN package list -- not the union --
# contains two or more of the packages named in $5 (a nameref to an array),
# or if there are more of them than shards (already fatal in test-shard.sh
# itself when a real shard call hits it, but checked here too so the closure
# job reports it the same way it reports every other sharding defect, with
# every shard's assignment visible in one place). The set to check is a
# parameter, not always the global heavy_packages, so the negative control
# below can hand it two packages KNOWN to land in the same real shard and
# prove the per-shard counting loop itself can reject, not only the "more
# heavy packages than shards" guard above it.
check_no_heavy_collision() {
  local job="$1" total="$2" with_isolated="$3" shard_indices="$4"
  local -n _check_set="$5"
  local -a indices=()
  mapfile -t indices < <(printf '%s' "$shard_indices" | tr ',' '\n')

  if [ "${#_check_set[@]}" -gt "$total" ]; then
    printf '%s: %s job: %s package(s) but only %s shard(s) -- cannot guarantee no two share a shard\n' \
      "${0##*/}" "$job" "${#_check_set[@]}" "$total" >&2
    return 1
  fi

  local index shard_out heavy_here count
  for index in "${indices[@]}"; do
    if [ "$with_isolated" -eq 1 ]; then
      shard_out="$("$repo_root/scripts/ci/test-shard.sh" --with-isolated "$index" "$total")"
    else
      shard_out="$("$repo_root/scripts/ci/test-shard.sh" "$index" "$total")"
    fi
    heavy_here=""
    for pkg in "${_check_set[@]}"; do
      case " $shard_out " in
        *" $pkg "*) heavy_here="$heavy_here $pkg" ;;
      esac
    done
    # shellcheck disable=SC2086  # word-splitting heavy_here on purpose to count entries
    count=$(set -- $heavy_here; echo "$#")
    if [ "$count" -gt 1 ]; then
      printf '%s: %s job shard %s/%s carries more than one:%s\n' \
        "${0##*/}" "$job" "$index" "$total" "$heavy_here" >&2
      return 1
    fi
  done
  printf 'PASS: %s job -- no shard carries more than one of %s (%s)\n' \
    "$job" "${#_check_set[@]}" "${_check_set[*]:-none}"
}

(cd "$repo_root" && go list ./...) | LC_ALL=C sort >"$tmpdir/all.sorted"
package_count="$(wc -l <"$tmpdir/all.sorted" | tr -d ' ')"

# CHAOS-5653: no HEAVY package (scripts/ci/test-shard.sh's own is_heavy_package
# -- a package-scoped container kept alive for the package's whole run) may
# share a shard with another one, in EITHER matrix. That collision is what let
# devhealthfacts and devhealthsource contend for one hosted runner's
# memory/CPU under -race at the same time and take a shared ClickHouse
# container down mid-suite (main@6e6b2022, #520@2fb2d9b7). Read from
# test-shard.sh itself, never a second hand list here.
mapfile -t heavy_packages < <("$repo_root/scripts/ci/test-shard.sh" heavy | tr ' ' '\n' | grep -v '^$')

for job in race unit; do
  meta="$(job_shard_union "$job" "$tmpdir/union.$job")"
  total="$(printf '%s' "$meta" | cut -f1)"
  with_isolated="$(printf '%s' "$meta" | cut -f2)"
  # Named apart from job_shard_union's own `indices` ARRAY: shellcheck reads
  # the two as one variable and flags the string assignment as an array being
  # expanded without an index.
  shard_indices="$(printf '%s' "$meta" | cut -f3)"

  LC_ALL=C sort "$tmpdir/union.$job" | grep -v '^$' >"$tmpdir/union.$job.sorted"
  compare_union "$job" "$tmpdir/union.$job.sorted" "$tmpdir/all.sorted"

  if [ "$with_isolated" -eq 1 ]; then
    shape='--with-isolated (isolated packages sharded in-line)'
  else
    shape='round-robin plus the isolated packages'
  fi
  printf 'PASS: %s shards %s of %s cover all %s packages exactly once [%s]\n' \
    "$job" "$shard_indices" "$total" "$package_count" "$shape"

  check_no_heavy_collision "$job" "$total" "$with_isolated" "$shard_indices" heavy_packages
done

# ---- negative controls ---------------------------------------------------
# A comparison that cannot fail proves nothing. Each control mutates a copy of
# a union that just passed and requires compare_union to REJECT it.

assert_comparison_rejects() {
  local label="$1" job="$2" union="$3"
  if compare_union "$job" "$union" "$tmpdir/all.sorted" 2>/dev/null; then
    printf 'CONTROL FAILED: %s -- the comparison accepted it\n' "$label" >&2
    exit 1
  fi
  printf 'CONTROL OK: %s -- rejected\n' "$label"
}

for job in race unit; do
  dropped="$tmpdir/control-dropped.$job"
  LC_ALL=C sed '1d' "$tmpdir/union.$job.sorted" >"$dropped"
  assert_comparison_rejects \
    "$job union missing $(head -n1 "$tmpdir/union.$job.sorted")" "$job" "$dropped"

  doubled="$tmpdir/control-doubled.$job"
  { cat "$tmpdir/union.$job.sorted"; head -n1 "$tmpdir/union.$job.sorted"; } \
    | LC_ALL=C sort >"$doubled"
  assert_comparison_rejects \
    "$job union covering $(head -n1 "$tmpdir/union.$job.sorted") twice" "$job" "$doubled"
done

printf 'PASS: every closure control was correctly rejected\n'

# check_no_heavy_collision's own controls: the real 4-shard layout passing
# proves nothing about whether the check can actually see a violation.

# Control 1 -- too many to separate: with today's 2 heavy packages, a shard
# count of 1 can never keep them apart, exactly the shape a future heavy
# package would hit if the module grew past the current shard count with no
# other change.
if check_no_heavy_collision "race" 1 0 "1" heavy_packages >/dev/null 2>&1; then
  printf 'CONTROL FAILED: check_no_heavy_collision accepted %s heavy package(s) in 1 shard\n' \
    "${#heavy_packages[@]}" >&2
  exit 1
fi
printf 'CONTROL OK: check_no_heavy_collision rejects %s heavy package(s) forced into 1 shard\n' \
  "${#heavy_packages[@]}"

# Control 2 -- the per-shard counting loop itself, not just the "too many"
# guard above it: two LIGHT packages that genuinely already share shard 1 of
# a real 4-way race split, handed in as a FAKE heavy set. If the loop cannot
# see two of its own list land in one real shard, it cannot see two real
# heavy packages do it either.
shard1_race="$("$repo_root/scripts/ci/test-shard.sh" 1 4)"
mapfile -t shard1_light < <(
  printf '%s\n' "$shard1_race" | tr ' ' '\n' | grep -v '^$' \
    | while IFS= read -r p; do
        for h in "${heavy_packages[@]}"; do [ "$p" = "$h" ] && continue 2; done
        printf '%s\n' "$p"
      done
)
if [ "${#shard1_light[@]}" -lt 2 ]; then
  printf '%s: control 2 needs at least 2 non-heavy packages in race shard 1/4 to fake a collision with, found %s\n' \
    "${0##*/}" "${#shard1_light[@]}" >&2
  exit 1
fi
fake_heavy=("${shard1_light[0]}" "${shard1_light[1]}")
if check_no_heavy_collision "race" 4 0 "1,2,3,4" fake_heavy >/dev/null 2>&1; then
  printf 'CONTROL FAILED: check_no_heavy_collision accepted %s and %s sharing shard 1\n' \
    "${fake_heavy[0]}" "${fake_heavy[1]}" >&2
  exit 1
fi
printf 'CONTROL OK: check_no_heavy_collision rejects %s and %s sharing shard 1\n' \
  "${fake_heavy[0]}" "${fake_heavy[1]}"
