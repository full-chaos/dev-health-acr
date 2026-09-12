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
#
# Usage: test-shard.sh --with-isolated <index> <total>
#   Same round-robin, but over the WHOLE `go list ./...` -- isolated_packages
#   included rather than excluded. This form exists for the non-race `unit`
#   matrix in ci.yml. The isolation below is a -race cost decision and only a
#   -race cost decision (see isolated_packages): the declaration-closure walk
#   is expensive under the race detector, which is why it gets its own
#   -timeout in its own race job. The plain coverage run has no such problem,
#   and it used to cover the isolated package as part of `./...` -- so the
#   unit matrix must keep covering it, or sharding `unit` would quietly drop
#   a package from the non-race suite. Never use this form for a -race job.
#
# Usage: test-shard.sh isolated
#   Prints the packages in isolated_packages (below) instead of sharding.
#
# Usage: test-shard.sh heavy
#   Prints the packages the round-robin below treats as HEAVY (see
#   is_heavy_package) instead of sharding.
#
# CHAOS-5653: two shards each running a package that keeps ONE real
# container alive for its whole test run (the CHAOS-5270 pattern: a
# package-scoped `sync.Once` + `TestMain` that starts a testcontainer once
# and tears it down after every test in the package has run) contend for
# the SAME runner's memory/CPU at the same time under -race, and on a
# hosted (not bigboy) runner that contention has taken the container down
# mid-suite (EOF / connection refused from every test still waiting on it).
# devhealthfacts and devhealthsource both landing in race shard 1 alongside
# each other is exactly that. `is_heavy_package` re-derives which packages
# carry this pattern FROM THE SOURCE on every invocation -- never a hand
# list -- and the round-robin below places each of them in its OWN shard
# before any light package is assigned, so two of them can never land
# together again as the module grows. This is deliberately narrower than
# "imports testcontainers-go anywhere": 18 packages do that today, most for
# a single short-lived per-test container (a real but much smaller cost,
# already spread thin by round-robin the way it always was) -- only a
# package that keeps ONE container alive for its ENTIRE run pays the
# sustained-memory cost this fix is for.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
list_err=""
# Set by compute_all_packages before any heavy-package check runs;
# is_heavy_package below reads module_path as a script-global the same way
# isolated_packages is read by is_isolated, rather than threading it through
# every call site.
module_path=""
all_packages=()

# CHAOS-3974: packages listed here are excluded from the round-robin split
# below and instead run in their own dedicated CI job with a package-scoped
# -timeout (the race-devhealthschema job in ci.yml). `test-shard.sh isolated`
# prints this list so that job and this script's exclusion always read from
# one source instead of drifting apart.
#
# Why: internal/contextfabric/devhealthschema's full-repo declaration sweeps
# (TestNoSecondPhysicalSourceOutsideTheDeclaration and its sibling) walk
# every file in the module, so their cost under -race scales with the
# module's TOTAL .go file count, not with this package's own size -- and
# whichever shard round-robin happened to draw this package paid rent on
# that growth out of a timeout budget shared with unrelated packages
# (including testcontainer-heavy ones). CHAOS-3972 already hit this once:
# the walk crept past the shared 300s ceiling as the repo grew, and the fix
# was to raise GOTEST_TIMEOUT for every package in every shard. That is the
# recurring growth mechanism this isolation removes: the walk keeps costing
# more as the repo grows, but now only its own dedicated job's timeout has
# to grow to absorb that -- the shared GOTEST_TIMEOUT other shards run under
# never has to move again on this package's account.
#
# CHAOS-5572: internal/contextfabric (the top-level package, not a
# subpackage) joins the list for a DIFFERENT reason -- not its own cost
# growing, but everyone else's. release.yml's `make verify` runs
# test-race-shared as ONE unsharded `go test` over every non-isolated
# package (unlike ci.yml's 4-way race matrix, which bounds each shard to
# ~1/4 of them); the more packages that single invocation covers, the more
# they all contend for the runner's CPU under -race at the same time, and
# internal/contextfabric -- whose suite leans on many t.Parallel() subtests,
# so it is unusually sensitive to how many cores it actually gets -- pays
# for that contention out of GOTEST_TIMEOUT (420s) even though its own
# -race cost did not grow. Measured proof (CHAOS-5572 TEST-EVIDENCE): a
# solo run (no contention) is flat across the two shas that broke the
# Release build (274-276s both), and so is its real ci.yml race-matrix
# shard (a bounded ~20-package contention set: 247.735s -> 229.427s); only
# the full ~90-package release.yml bucket times it out. Isolating it here
# removes it from that bucket the same way devhealthschema was removed, so
# release.yml's shared-bucket size can keep growing without spending this
# package's budget on rent for packages it has nothing to do with.
isolated_packages=(
  "github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
  "github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

usage() {
  printf 'usage: %s [--with-isolated] <index> <total>\n' "${0##*/}" >&2
  printf '  index  1-based shard number (1 <= index <= total)\n' >&2
  printf '  total  total number of shards\n' >&2
  printf '  --with-isolated  shard over the whole package list, isolated included\n' >&2
  printf 'usage: %s isolated\n' "${0##*/}" >&2
  printf '  prints the packages excluded from round-robin sharding\n' >&2
  printf 'usage: %s heavy\n' "${0##*/}" >&2
  printf '  prints the packages round-robin gives their own shard (see is_heavy_package)\n' >&2
}

# `go list ./...`, validated and sorted, plus module_path (the script-global
# set above). Shared by every subcommand that needs the package list, so
# "heavy" and the real sharding path can never read two different listings.
compute_all_packages() {
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
  module_path="$(awk '/^module / { print $2; exit }' "$repo_root/go.mod" 2>/dev/null || true)"
  if [ -z "$module_path" ]; then
    printf '%s: could not read the module path from %s/go.mod\n' "${0##*/}" "$repo_root" >&2
    exit 1
  fi

  all_packages=()
  local pkg
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
}

is_positive_int() {
  [[ "$1" =~ ^[1-9][0-9]*$ ]]
}

is_isolated() {
  local pkg="$1" candidate
  for candidate in "${isolated_packages[@]}"; do
    [ "$pkg" = "$candidate" ] && return 0
  done
  return 1
}

# A package is HEAVY iff it carries the CHAOS-5270 "one package-scoped
# container for the whole run" pattern: a `TestMain(m *testing.M)` in some
# _test.go file of the package, AND a testcontainers-go import somewhere in
# the package (grepped separately -- the shared-container helper file
# usually carries both, but nothing requires it to be the same file). Both
# conditions grepped fresh from $repo_root on every call: this is never a
# hand list, so a package that adopts or drops the pattern is picked up the
# next time this script runs, with no second place to remember to update.
#
# Deliberately narrower than "imports testcontainers-go": that matches 18
# packages today, nearly all of which start a short-lived container PER
# TEST (a real cost, but one round-robin already spreads thin, the way it
# always has). Only a package that keeps ONE container alive for its whole
# run pays the sustained-memory cost two of which colliding in one shard
# caused (CHAOS-5653).
is_heavy_package() {
  local pkg="$1" dir
  dir="$repo_root/${pkg#"$module_path"/}"
  [ -d "$dir" ] || return 1
  # `2>/dev/null` on each: a package with no _test.go file (or no plain .go
  # file at all, vanishingly rare but not impossible) must read as "not
  # heavy", never as a grep error this script mistakes for something else.
  grep -lq 'func TestMain(m \*testing\.M)' "$dir"/*_test.go >/dev/null 2>&1 || return 1
  grep -lq 'testcontainers-go' "$dir"/*.go >/dev/null 2>&1
}

# Every heavy package, sorted (LC_ALL=C, matching every other ordering in
# this script). Requires module_path to already be set (see main()).
heavy_packages_of() {
  local -n _hp_all="$1"
  local pkg
  for pkg in "${_hp_all[@]}"; do
    is_heavy_package "$pkg" && printf '%s\n' "$pkg"
  done
}

main() {
  if [ "$#" -eq 1 ] && [ "$1" = "isolated" ]; then
    printf '%s\n' "${isolated_packages[*]}"
    return 0
  fi

  if [ "$#" -eq 1 ] && [ "$1" = "heavy" ]; then
    compute_all_packages
    local -a heavy_list=()
    while IFS= read -r pkg; do
      [ -n "$pkg" ] || continue
      heavy_list+=("$pkg")
    done < <(heavy_packages_of all_packages)
    printf '%s\n' "${heavy_list[*]}"
    return 0
  fi

  # --with-isolated keeps isolated_packages IN the round-robin instead of
  # excluding them. Parsed as an explicit leading flag (not an optional third
  # positional) so a caller that fat-fingers an extra argument still trips the
  # arity check below rather than silently changing which packages run.
  local include_isolated=0
  if [ "$#" -ge 1 ] && [ "$1" = "--with-isolated" ]; then
    include_isolated=1
    shift
  fi

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

  compute_all_packages
  local pkg

  # CHAOS-3974: every isolated package must actually exist in the module --
  # a renamed or removed package left in isolated_packages would silently
  # exclude nothing (already caught below by round-robin as usual) while its
  # dedicated CI job also tested nothing, dropping the package from CI
  # coverage entirely without either side raising an error.
  local isolated found
  for isolated in "${isolated_packages[@]}"; do
    found=0
    for pkg in "${all_packages[@]}"; do
      if [ "$pkg" = "$isolated" ]; then
        found=1
        break
      fi
    done
    if [ "$found" -eq 0 ]; then
      # shellcheck disable=SC2016  # backticked `go list ./...` is prose, not a shell expansion
      printf '%s: isolated package not found by `go list ./...`: %s -- update isolated_packages in %s\n' \
        "${0##*/}" "$isolated" "${0##*/}" >&2
      exit 1
    fi
  done

  # CHAOS-5653: split into HEAVY (see is_heavy_package) and everything else,
  # then shard each set with its OWN round-robin counter. A heavy package
  # therefore lands in shard ((its own 0-based position among heavy
  # packages) % total) + 1 -- independent of how many light packages come
  # before or after it -- so as long as there are no more heavy packages
  # than shards, no two heavy packages can ever land in the same shard. The
  # light set fills in behind them with the round-robin this script always
  # used, so shard sizes stay balanced.
  local -a heavy=() light=()
  for pkg in "${all_packages[@]}"; do
    if [ "$include_isolated" -eq 0 ] && is_isolated "$pkg"; then
      continue
    fi
    if is_heavy_package "$pkg"; then
      heavy+=("$pkg")
    else
      light+=("$pkg")
    fi
  done

  # total=1 (Makefile's test-race-shared, release.yml's `make verify`) is a
  # single COMBINED invocation by design -- every non-isolated package,
  # heavy or not, has always run together there, and that is a separate,
  # already-accepted trade-off (CHAOS-5572's own docstring above) this fix
  # does not change. The guarantee below is for a REAL multi-shard split
  # (total > 1, ci.yml's race/unit matrices): only there does "which shard"
  # mean anything to guard.
  if [ "$total" -gt 1 ] && [ "${#heavy[@]}" -gt "$total" ]; then
    printf '%s: %s heavy package(s) (see "%s heavy") but only %s shard(s) -- cannot guarantee no two share a shard\n' \
      "${0##*/}" "${#heavy[@]}" "${0##*/}" "$total" >&2
    printf '%s\n' "${heavy[@]}" >&2
    exit 1
  fi

  local -a shard_packages=()
  local want=$((index - 1)) i
  for ((i = 0; i < ${#heavy[@]}; i++)); do
    if [ "$((i % total))" -eq "$want" ]; then
      shard_packages+=("${heavy[$i]}")
    fi
  done
  for ((i = 0; i < ${#light[@]}; i++)); do
    if [ "$((i % total))" -eq "$want" ]; then
      shard_packages+=("${light[$i]}")
    fi
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
