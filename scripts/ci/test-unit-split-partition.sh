#!/usr/bin/env bash
set -euo pipefail

# test-unit-split-partition.sh proves the devhealthsource unit legs (6, 8, 9 in
# ci.yml) and the devhealthfacts unit legs (5, 7, 10) run every test exactly once, and that the check saying so can fail.
# Controls run race-split.sh from a scratch copy of scripts/ci and ci.yml with
# the real module symlinked in, so each mutation touches only the copy.
#
# Usage: test-unit-split-partition.sh

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fails=0
bad() { printf 'FAIL: %s\n' "$1" >&2; fails=$((fails + 1)); }

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
  local label="$1" dir="$2" pkg="${3:-devhealthsource}"
  if "$dir/scripts/ci/race-split.sh" "$pkg" partition >"$tmp/out" 2>&1; then
    bad "NEGATIVE CONTROL: partition accepted: $label"
    return 0
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

# devhealthfacts runs the same three-way split: leg 5 skips heavy, mid and
# fourth, leg 7 runs heavy minus mid/fourth, leg 10 runs mid plus fourth.
echo "positive: real tree, devhealthfacts"
"$root/scripts/ci/race-split.sh" devhealthfacts partition || bad "devhealthfacts partition rejects the real tree"

split_entries="$("$root/scripts/ci/test-shard.sh" --print-unit-split-packages | tr ' ' '\n' | grep -c '/devhealthfacts$' || true)"
[ "$split_entries" -eq 2 ] || bad "unit_split_packages names devhealthfacts $split_entries time(s), want 2"

dir="$(fixture facts-no-mid-leg)"
sed 's/race-split\.sh devhealthfacts unit-mid)/race-split.sh devhealthfacts unit-mid-gone)/' "$dir/.github/workflows/ci.yml" >"$dir/ci.new"
cmp -s "$dir/ci.new" "$dir/.github/workflows/ci.yml" && bad "CONTROL SETUP: facts-no-mid-leg did not change the workflow (no devhealthfacts unit-mid leg to remove)"
mv "$dir/ci.new" "$dir/.github/workflows/ci.yml"
expect_fail 'a workflow with no devhealthfacts unit-mid leg' "$dir" devhealthfacts

# One test named in both the mid and the fourth list would run twice in the
# unit-mid leg's union with the heavy leg or in two race jobs.
dir="$(fixture facts-test-in-two-lists)"
dup="$(grep -v '^[[:space:]]*\(#\|$\)' "$dir/scripts/ci/devhealthfacts-race-mid-tests.txt" | head -n1)"
printf '%s\n' "$dup" >>"$dir/scripts/ci/devhealthfacts-race-fourth-tests.txt"
expect_fail 'a test named in both the mid and the fourth list' "$dir" devhealthfacts

# The unit-only extra list moves named mid/fourth tests from leg 10 to leg 7.
# A name outside the mid and fourth lists would run in leg 7 and again in leg 5
# or the heavy half; a name listed twice is a typo that hides a dropped test.
echo "positive: real tree, extra list"
[ -f "$root/scripts/ci/devhealthfacts-unit-heavy-extra-tests.txt" ] || bad "devhealthfacts-unit-heavy-extra-tests.txt is missing"
heavy_only="$(LC_ALL=C comm -23 <(grep -v '^[[:space:]]*\(#\|$\)' "$root/scripts/ci/devhealthfacts-heavy-tests.txt" | LC_ALL=C sort -u) <(cat "$root"/scripts/ci/devhealthfacts-race-mid-tests.txt "$root"/scripts/ci/devhealthfacts-race-fourth-tests.txt | grep -v '^[[:space:]]*\(#\|$\)' | LC_ALL=C sort -u) | head -n1)"
[ -n "$heavy_only" ] || bad "CONTROL SETUP: no heavy-only test name to use"

dir="$(fixture facts-extra-not-in-mid-fourth)"
printf '%s\n' "$heavy_only" >>"$dir/scripts/ci/devhealthfacts-unit-heavy-extra-tests.txt"
expect_fail 'an extra name that is in neither the mid nor the fourth list' "$dir" devhealthfacts
dir="$(fixture facts-extra-no-such-test)"
printf 'TestNoSuchTestExists\n' >>"$dir/scripts/ci/devhealthfacts-unit-heavy-extra-tests.txt"
expect_fail 'an extra name that is no real test' "$dir" devhealthfacts
dir="$(fixture facts-extra-twice)"
first="$(grep -v '^[[:space:]]*\(#\|$\)' "$dir/scripts/ci/devhealthfacts-unit-heavy-extra-tests.txt" | head -n1)"
printf '%s\n' "$first" >>"$dir/scripts/ci/devhealthfacts-unit-heavy-extra-tests.txt"
expect_fail 'an extra name listed twice' "$dir" devhealthfacts
dir="$(fixture facts-extra-and-mid-no-such-test)"
printf 'TestNoSuchTestExists\n' >>"$dir/scripts/ci/devhealthfacts-race-mid-tests.txt"
printf 'TestNoSuchTestExists\n' >>"$dir/scripts/ci/devhealthfacts-unit-heavy-extra-tests.txt"
expect_fail 'an extra name that is in the mid list but is no real test' "$dir" devhealthfacts

[ "$fails" -eq 0 ] || { printf '%s violation(s)\n' "$fails" >&2; exit 1; }
echo "PASS: unit split partition and its negative controls"
