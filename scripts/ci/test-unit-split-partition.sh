#!/usr/bin/env bash
set -euo pipefail

# test-unit-split-partition.sh proves the devhealthsource unit legs (6, 8, 9 in
# ci.yml) run every test exactly once, and that the check saying so can fail.
# Controls run race-split.sh from a scratch copy of scripts/ci and ci.yml with
# the real module symlinked in, so each mutation touches only the copy.
#
# Usage: test-unit-split-partition.sh

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "positive: real tree"
"$root/scripts/ci/race-split.sh" devhealthsource partition

fixture() {
  local dir="$tmp/$1" entry
  mkdir -p "$dir/scripts" "$dir/.github"
  for entry in "$root"/* "$root"/.[!.]*; do
    case "$(basename "$entry")" in
      scripts | .github | .git) ;;
      *) ln -s "$entry" "$dir/$(basename "$entry")" ;;
    esac
  done
  cp -R "$root/scripts/ci" "$dir/scripts/ci"
  cp -R "$root/.github/workflows" "$dir/.github/workflows"
  printf '%s' "$dir"
}

expect_fail() {
  local label="$1" dir="$2"
  if "$dir/scripts/ci/race-split.sh" devhealthsource partition >"$tmp/out" 2>&1; then
    printf 'NEGATIVE CONTROL FAILED: partition accepted: %s\n' "$label" >&2
    exit 1
  fi
  printf 'CONTROL OK: partition rejects %s\n' "$label"
}

# A leg dropped from ci.yml: the mid tests would run in no leg.
dir="$(fixture no-mid-leg)"
sed 's/race-split\.sh devhealthsource unit-mid)/race-split.sh devhealthsource unit-mid-gone)/' "$dir/.github/workflows/ci.yml" >"$dir/ci.new"
cmp -s "$dir/ci.new" "$dir/.github/workflows/ci.yml" && { echo "CONTROL SETUP FAILED: no-mid-leg" >&2; exit 1; }
mv "$dir/ci.new" "$dir/.github/workflows/ci.yml"
expect_fail 'a workflow with no unit-mid leg' "$dir"

# A leg wired twice: its tests would run twice.
dir="$(fixture doubled-leg)"
awk '{ print } /race-split\.sh devhealthsource unit-mid\)/ { print "# race-split.sh devhealthsource unit-mid)" }' "$dir/.github/workflows/ci.yml" >"$dir/ci.new"
cmp -s "$dir/ci.new" "$dir/.github/workflows/ci.yml" && { echo "CONTROL SETUP FAILED: doubled-leg" >&2; exit 1; }
mv "$dir/ci.new" "$dir/.github/workflows/ci.yml"
expect_fail 'a workflow wiring unit-mid twice' "$dir"

# A leg's -run swapped for -skip: the selector text stays, the tests double.
dir="$(fixture swapped-flag)"
sed 's/-run=\(.*devhealthsource unit-mid)\)/-skip=\1/' "$dir/.github/workflows/ci.yml" >"$dir/ci.new"
cmp -s "$dir/ci.new" "$dir/.github/workflows/ci.yml" && { echo "CONTROL SETUP FAILED: swapped-flag" >&2; exit 1; }
mv "$dir/ci.new" "$dir/.github/workflows/ci.yml"
expect_fail 'leg 9 using -skip instead of -run' "$dir"

# A mid list that names no real test: leg 9 would select nothing.
dir="$(fixture empty-mid)"
printf 'TestNoSuchTestExists\n' >"$dir/scripts/ci/devhealthsource-race-mid-tests.txt"
expect_fail 'a mid list that matches no test' "$dir"

echo "PASS: unit split partition and its negative controls"
