#!/usr/bin/env bash
# Run ONE battery arm and emit its verdict as JSON.
#
# Usage: run_arm.sh <repo-root> <arm-id> <spec-json|-> <package-path-list> <floor> <out-json>
#
#   <arm-id>   a mutant id from the table, or one of the two harness arms:
#                _BASELINE  pristine tree, must be GREEN
#                _SENTINEL  an inert comment applied, must stay GREEN
#   <spec-json> the single-mutant JSON file; "-" for _BASELINE/_SENTINEL.
#
# THE THREE-STATE RULE, and it is the whole point of this file:
#
#   KILLED         the suite failed AND at least one line matches
#                  "--- FAIL: Test". A named failing test is what a kill IS.
#   SURVIVED       the suite passed over at least <floor> tests.
#   HARNESS_ERROR  everything else: the mutant did not apply, the file digest
#                  did not move, the tree does not build or vet, the run did not
#                  reach the floor, the suite timed out, or the suite failed
#                  with NO named failing test.
#
# rc != 0 ALONE IS NOT A KILL. A build error, a cache eviction, a disk-full or a
# timeout all exit non-zero, and counting those as kills turns infrastructure
# noise into a mutation score -- silently, and always in the flattering
# direction. That is why the named-FAIL grep exists and why it reads a FILE.
#
# EVERY CLASSIFICATION READS A FILE, never a shell variable. A variable can be
# truncated by a subshell limit or mangled by a nested quote, and the failure
# mode is "no FAIL line found", which reads as SURVIVED.
#
# BUILD AND VET BOTH GATE THE TEST. `go vet` type-checks _test.go and `go build`
# does not, so without vet a mutant that breaks a test file's imports reads as
# SURVIVED rather than as the harness error it is.
#
# This file is the ONE AUTHORITY for arm classification: the bigboy harness and
# the hosted matrix workflow both call it, so the two venues cannot drift into
# disagreeing about what a battery measured.
set -uo pipefail

ROOT="${1:?usage: run_arm.sh <repo-root> <arm-id> <spec-json|-> <packages> <floor> <out-json>}"
ARM_ID="${2:?arm id}"
SPEC="${3:?spec json or -}"
PKGS="${4:?package list}"
FLOOR="${5:?floor}"
OUT="${6:?out json}"

# The go test timeout is EXPLICIT and applied ONLY to `go test`. It is never
# routed through $PKGS (which is also the floor and vet input, where a stray
# flag corrupts the count and fails vet on every arm) and never through GOFLAGS
# (which the caller may already be using for -p). Default 20m because a
# whole-package acr suite has been measured at 559-600 s against go's 600 s
# default, and a suite that trips the default reports `panic: test timed out`
# with no named FAIL -- which this script must classify as HARNESS_ERROR, but
# which is better avoided than classified.
GO_TEST_TIMEOUT="${MB_GO_TEST_TIMEOUT:-20m}"

case "$ARM_ID" in
  */*|*' '*) echo "run_arm.sh: refusing an arm id with a path separator or space: [$ARM_ID]" >&2; exit 2 ;;
esac

LOG="${MB_ARM_LOG:-$(dirname "$OUT")/arm-$ARM_ID.log}"
mkdir -p "$(dirname "$OUT")" "$(dirname "$LOG")"
: > "$LOG"

say() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $*" | tee -a "$LOG"; }

# state, detail, ran, named -> the JSON verdict. Written on EVERY path, so a
# missing artifact means the job died, not that an arm was skipped quietly.
emit() {
  python3 - "$OUT" "$ARM_ID" "$1" "$2" "${3:-0}" "${4:-0}" "$PKGS" "$FLOOR" "$GO_TEST_TIMEOUT" <<'PYEOF'
import json, sys
out, arm, state, detail, ran, named, pkgs, floor, timeout = sys.argv[1:10]
json.dump({
    "id": arm, "state": state, "detail": detail,
    "ran": int(ran), "named_failures": int(named),
    "packages": pkgs, "floor": int(floor), "go_test_timeout": timeout,
}, open(out, "w"), sort_keys=True, indent=2)
PYEOF
  say "$ARM_ID: $1 -- $2"
}

cd "$ROOT" || { emit HARNESS_ERROR "cannot cd $ROOT"; exit 0; }

say "arm=$ARM_ID packages=[$PKGS] floor=$FLOOR go-test-timeout=$GO_TEST_TIMEOUT"

# ---------------------------------------------------------------- the mutation
case "$ARM_ID" in
  _BASELINE)
    say "BASELINE: pristine tree, no mutation. It must be GREEN or every other arm's KILLED is void."
    ;;
  _SENTINEL)
    # An INERT edit. If this arm goes red the harness false-kills and every
    # KILLED verdict in the run is suspect; if it cannot build it proves
    # nothing, so the build is checked apart from the suite below.
    SENT_FILE="${MB_SENTINEL_FILE:?_SENTINEL needs MB_SENTINEL_FILE}"
    [ -f "$SENT_FILE" ] || { emit HARNESS_ERROR "sentinel file $SENT_FILE does not exist"; exit 0; }
    before=$(sha256sum "$SENT_FILE" | cut -d' ' -f1)
    printf '\n// mutation-battery sentinel: an inert comment. If this arm goes red the\n// harness false-kills and every KILLED verdict in this run is void.\n' >> "$SENT_FILE"
    after=$(sha256sum "$SENT_FILE" | cut -d' ' -f1)
    [ "$before" != "$after" ] || { emit HARNESS_ERROR "sentinel digest did not move -- the apply path is broken"; exit 0; }
    say "sentinel digest moved ${before:0:12} -> ${after:0:12} on $SENT_FILE"
    ;;
  *)
    [ -f "$SPEC" ] || { emit HARNESS_ERROR "no spec file at $SPEC"; exit 0; }
    applied=$(python3 "$(dirname "$0")/apply_mutant.py" --root "$ROOT" --spec "$SPEC" 2>>"$LOG")
    if [ "$applied" != "APPLIED" ]; then
      emit HARNESS_ERROR "apply=$applied -- an unapplied mutant is unproven, never a pass"
      exit 0
    fi
    ;;
esac

# ------------------------------------------------------------- build then vet
# Build is whole-repo: a mutant that breaks a package outside the tested set is
# still a harness error, and no scoping is allowed to hide that.
brc=0; go build ./... >> "$LOG" 2>&1 || brc=$?
if [ "$brc" -ne 0 ]; then
  emit HARNESS_ERROR "BUILD_FAILED (go build rc=$brc) -- a non-compiling mutant is not a kill; re-aim at a compiling form"
  exit 0
fi
vrc=0; go vet $PKGS >> "$LOG" 2>&1 || vrc=$?
if [ "$vrc" -ne 0 ]; then
  emit HARNESS_ERROR "BUILD_FAILED (go vet rc=$vrc) -- vet type-checks _test.go, build does not"
  exit 0
fi
if [ "$ARM_ID" = "_SENTINEL" ]; then
  say "ok sentinel build+vet: rc=0 (checked apart from the suite)"
fi

# --------------------------------------------------------------------- the run
# WHOLE PACKAGES. No -run selector is derived anywhere: `go test -run` matching
# nothing exits 0 and reads as a survivor, so the floor replaces it.
trc=0
go test -count=1 -timeout="$GO_TEST_TIMEOUT" -v $PKGS >> "$LOG" 2>&1 || trc=$?

ran=$(grep -c '^=== RUN' "$LOG")
named=$(grep -cE '^[[:space:]]*--- FAIL: Test' "$LOG")
timedout=$(grep -cE 'panic: test timed out|test timed out after' "$LOG")
case "$ran" in ''|*[!0-9]*) ran=0 ;; esac
case "$named" in ''|*[!0-9]*) named=0 ;; esac
case "$timedout" in ''|*[!0-9]*) timedout=0 ;; esac
say "rc=$trc  === RUN $ran (floor $FLOOR)  named-FAIL $named  timed-out-lines $timedout"

# A TIMEOUT IS A HARNESS ERROR EVEN IF SOMETHING ELSE FAILED BY NAME. A suite
# that was cut off did not finish, so the arms it did not reach are unmeasured
# and "the ones that ran happened to fail" is not the same claim as a kill.
if [ "$timedout" -ne 0 ]; then
  emit HARNESS_ERROR "TIMED OUT (go test -timeout=$GO_TEST_TIMEOUT tripped; $named named FAIL line(s) present but the suite did not finish)" "$ran" "$named"
  exit 0
fi

if [ "$ran" -lt "$FLOOR" ]; then
  emit HARNESS_ERROR "=== RUN $ran below the floor $FLOOR -- the run did not cover the package list" "$ran" "$named"
  exit 0
fi

case "$ARM_ID" in
  _BASELINE|_SENTINEL)
    if [ "$trc" -ne 0 ]; then
      emit HARNESS_ERROR "$ARM_ID IS RED (rc=$trc, $named named failure(s)) -- every mutant verdict in this run would be void" "$ran" "$named"
    else
      emit PASS "green over $ran tests (floor $FLOOR)" "$ran" "$named"
    fi
    exit 0
    ;;
esac

if [ "$trc" -ne 0 ]; then
  if [ "$named" -eq 0 ]; then
    hint=$(grep -E 'build failed|cannot find|no space left|permission denied|signal: killed' "$LOG" | head -2 | tr '\n' ';')
    emit HARNESS_ERROR "BUILD_FAILED (rc=$trc over $ran tests, NO '--- FAIL: Test' line) $hint" "$ran" "$named"
  else
    first=$(grep -E '^[[:space:]]*--- FAIL: Test' "$LOG" | head -3 | tr '\n' ';')
    emit KILLED "rc=$trc, $ran ran, $named named failure(s): $first" "$ran" "$named"
  fi
else
  emit SURVIVED "rc=0 over $ran tests -- a surviving mutant is a FINDING, not a pass" "$ran" "$named"
fi
exit 0
