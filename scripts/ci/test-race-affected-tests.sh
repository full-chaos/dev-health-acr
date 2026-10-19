#!/usr/bin/env bash
set -euo pipefail

# Fixture test for scripts/ci/race-affected-tests.sh: a throwaway module whose
# tests reach the race signal by every route the detector claims to follow,
# plus decoys it must not count. Run with DETECTOR=<other script> to prove the
# test goes red against a detector that lacks a rule.

here="$(cd "$(dirname "$0")" && pwd)"
detector="${DETECTOR:-$here/race-affected-tests.sh}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
m="$tmp/mod"
mkdir -p "$m/racesupport" "$m/other" "$m/target"
printf 'module example.com/fixture\n\ngo 1.22\n' >"$m/go.mod"

cat >"$m/racesupport/on.go" <<'EOF'
//go:build race

package racesupport

const raceEnabled = true
EOF
cat >"$m/racesupport/off.go" <<'EOF'
//go:build !race

package racesupport

const raceEnabled = false
EOF
cat >"$m/racesupport/skip.go" <<'EOF'
package racesupport

import "testing"

// SkipUnderRace skips a test when the race detector is on.
func SkipUnderRace(t *testing.T) {
	if raceEnabled {
		t.Skip("race")
	}
}

// Unrelated names its own result and never reads the race signal.
func Unrelated() int { return 1 }
EOF
cat >"$m/other/other.go" <<'EOF'
package other

const raceDetectorEnabled = false

// seed is unexported here; target's seed below must not inherit it.
func seed() bool { return raceDetectorEnabled }
EOF
cat >"$m/target/target.go" <<'EOF'
package target

func Work() int { return 1 }
EOF
cat >"$m/target/race_off_test.go" <<'EOF'
//go:build !race

package target

const raceDetectorEnabled = false

func TestOnlyWithoutRace(t *testing.T) {}
EOF
sed -i 's/^package target$/package target\n\nimport "testing"/' "$m/target/race_off_test.go"
cat >"$m/target/race_on_test.go" <<'EOF'
//go:build race

package target

const raceDetectorEnabled = true
EOF
cat >"$m/target/target_test.go" <<'EOF'
package target

import (
	"testing"

	"example.com/fixture/other"
	"example.com/fixture/racesupport"
)

var aliasOfRace = raceDetectorEnabled

func skipIfRace(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("race")
	}
}

func outerHelper(t *testing.T) { skipIfRace(t) }

func seed() int { return 2 }

func TestDirect(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("race")
	}
}

func TestHelper(t *testing.T) { skipIfRace(t) }

func TestHelperChain(t *testing.T) { outerHelper(t) }

func TestAlias(t *testing.T) {
	if aliasOfRace {
		return
	}
}

func TestSubtestOnlyWithoutRace(t *testing.T) {
	if !raceDetectorEnabled {
		t.Run("plain", func(t *testing.T) {})
	}
}

func TestCrossPackageHelper(t *testing.T) { racesupport.SkipUnderRace(t) }

// TestCommentOnly mentions raceDetectorEnabled and skipIfRace only in a comment.
func TestCommentOnly(t *testing.T) {
	_ = "raceDetectorEnabled skipIfRace"
	_ = racesupport.Unrelated()
}

func TestSameNameInAnotherPackage(t *testing.T) {
	_ = seed()
	_ = other.Exported
}

func TestPlain(t *testing.T) { _ = Work() }
EOF
printf 'package other\n\nconst Exported = 1\n' >"$m/other/exported.go"

want="$(printf '%s\n' TestAlias TestCrossPackageHelper TestDirect TestHelper TestHelperChain TestOnlyWithoutRace TestSubtestOnlyWithoutRace | LC_ALL=C sort)"
rc=0
got="$(GOFLAGS='-buildvcs=false' GOWORK=off "$detector" --root "$m" target 2>"$tmp/err")" || rc=$?
if [ "$rc" -ne 0 ]; then
  printf 'FAIL: the detector exited %s\n' "$rc"
  sed 's/^/    /' "$tmp/err"
  exit 1
fi
got="$(printf '%s\n' "$got" | grep -v '^$' | LC_ALL=C sort || true)"
missing="$(LC_ALL=C comm -23 <(printf '%s\n' "$want") <(printf '%s\n' "$got"))"
extra="$(LC_ALL=C comm -13 <(printf '%s\n' "$want") <(printf '%s\n' "$got"))"
status=0
[ -z "$missing" ] || { printf 'FAIL: race-affected tests not detected: %s\n' "$(tr '\n' ' ' <<<"$missing")"; status=1; }
[ -z "$extra" ] || { printf 'FAIL: tests wrongly flagged: %s\n' "$(tr '\n' ' ' <<<"$extra")"; status=1; }
[ "$status" -eq 0 ] || exit 1
printf 'ok   detector finds %s race-affected tests and no decoy\n' "$(wc -l <<<"$got" | tr -d ' ')"

# A scan that cannot read its input must fail, never print an empty set.
rc=0
GOFLAGS='-buildvcs=false' GOWORK=off "$detector" --root "$m" no-such-package >/dev/null 2>&1 || rc=$?
[ "$rc" -ne 0 ] || { printf 'FAIL: the detector exited 0 on a package it cannot list\n'; exit 1; }
printf 'ok   detector fails on a package it cannot list (rc=%s)\n' "$rc"
printf 'PASS: race-affected test detection\n'
