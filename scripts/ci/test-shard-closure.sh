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

(cd "$repo_root" && go list ./...) | LC_ALL=C sort >"$tmpdir/all.sorted"
package_count="$(wc -l <"$tmpdir/all.sorted" | tr -d ' ')"

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
