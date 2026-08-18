#!/usr/bin/env bash
set -euo pipefail

# test-shard.sh partitions `go list ./...` round-robin across N shard
# runners so CI can run the race suite in parallel. Packages are sorted
# (LC_ALL=C, so ordering is stable across machines/locales) and then package
# at 0-based position P is assigned to shard (P % total) + 1. That means
# every package lands in exactly one shard, and the union of all `total`
# shards -- shard 1 through shard `total`, each invoked with the same
# `total` -- is exactly `go list ./...`, no more and no less.
#
# The script refuses to shard unless `go list ./...` itself succeeds: its
# exit status is captured explicitly (not read through a process
# substitution pipeline, which `set -o pipefail` cannot see into) so a
# partial listing from a broken package can never be silently partitioned
# and shipped to shards as if it were the whole suite.
#
# Usage: test-shard.sh <index> <total>
#   index  1-based shard number to emit packages for (1 <= index <= total)
#   total  total number of shards

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
list_err=""

usage() {
  printf 'usage: %s <index> <total>\n' "${0##*/}" >&2
  printf '  index  1-based shard number (1 <= index <= total)\n' >&2
  printf '  total  total number of shards\n' >&2
}

is_positive_int() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]]
}

main() {
  if [ "$#" -ne 2 ]; then
    usage
    exit 2
  fi

  local index="$1" total="$2"

  if ! is_positive_int "$index" || ! is_positive_int "$total"; then
    printf '%s: index and total must be positive integers\n' "${0##*/}" >&2
    usage
    exit 2
  fi

  if [ "$index" -gt "$total" ]; then
    printf '%s: index (%s) must be <= total (%s)\n' "${0##*/}" "$index" "$total" >&2
    usage
    exit 2
  fi

  # stderr is captured to its own file, never merged into stdout: on a cold
  # module cache `go list` writes "go: downloading <mod> <version>" progress
  # lines to stderr while still exiting 0. Folding those into stdout would
  # feed bare version strings such as "v1.2.3" to `go test` as if they were
  # package paths, which is exactly what a warm local cache cannot reproduce.
  local list_output list_status=0
  list_err="$(mktemp)"
  # shellcheck disable=SC2064  # expand list_err now, at trap-install time
  trap "rm -f '$list_err'" EXIT
  list_output="$(go list ./... 2>"$list_err")" || list_status=$?
  if [ "$list_status" -ne 0 ]; then
    # shellcheck disable=SC2016  # backticked `go list ./...` is prose, not a shell expansion
    printf '%s: `go list ./...` failed (exit %s); refusing to shard a possibly partial package list\n' \
      "${0##*/}" "$list_status" >&2
    cat "$list_err" >&2
    exit 1
  fi

  # Closed vocabulary: every emitted line must be an import path inside this
  # module. Anything else means the listing was polluted (tool progress noise,
  # a warning, a changed `go list` output format), and sharding it would hand
  # `go test` arguments it would silently mis-parse.
  local module_path
  module_path="$(awk '/^module / { print $2; exit }' "$repo_root/go.mod" 2>/dev/null || true)"
  if [ -z "$module_path" ]; then
    printf '%s: could not read the module path from %s/go.mod\n' "${0##*/}" "$repo_root" >&2
    exit 1
  fi

  local -a all_packages=()
  while IFS= read -r pkg; do
    [ -n "$pkg" ] || continue
    case "$pkg" in
      "$module_path" | "$module_path"/*) ;;
      *)
        # shellcheck disable=SC2016  # backticked `go list ./...` is prose, not a shell expansion
        printf '%s: `go list ./...` emitted a line that is not a package in %s: %s\n' \
          "${0##*/}" "$module_path" "$pkg" >&2
        exit 1
        ;;
    esac
    all_packages+=("$pkg")
  done < <(printf '%s\n' "$list_output" | LC_ALL=C sort)

  local -a shard_packages=()
  local pos=0 want=$((index - 1))
  for pkg in "${all_packages[@]}"; do
    if [ "$((pos % total))" -eq "$want" ]; then
      shard_packages+=("$pkg")
    fi
    pos=$((pos + 1))
  done

  if [ "${#shard_packages[@]}" -eq 0 ]; then
    # shellcheck disable=SC2016  # backticked `go test` below is prose, not a shell expansion
    printf '%s: shard %s/%s selected zero packages out of %s total -- an empty package list would make `go test` silently test only the current directory, not fail loudly\n' \
      "${0##*/}" "$index" "$total" "${#all_packages[@]}" >&2
    exit 1
  fi

  printf '%s\n' "${shard_packages[*]}"
}

main "$@"
