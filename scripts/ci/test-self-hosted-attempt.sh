#!/usr/bin/env bash
set -euo pipefail

# test-self-hosted-attempt.sh -- offline proof for the self-hosted routing's
# run selection, status reading, timeout branches, and the cross-file
# agreement between .github/workflows/ci.yml and ci-self-hosted.yml.
#
# Why this file is as thorough as it is: every defect the routing can carry
# is SILENT in a green build. A lookup that matches nothing makes the hosted
# leg quietly do all the work on every run, forever, while the pilot report
# says the routing works. A lookup that matches a STALE run makes the hosted
# leg trust a conclusion computed over different code. Neither shows up as a
# red build, so neither would ever be found by running CI.
#
# Every assertion below is therefore paired with a NEGATIVE CONTROL that
# feeds the same fixture to the DEFECT SHAPE and requires the defect shape
# to produce the wrong answer -- proving the fixture actually exercises the
# hazard, rather than passing because it never reached it. The two defect
# shapes are not hypothetical: shape 1 is the `[...][0] | "\(.status)"`
# expression on the ops implementation of this same contract (executed:
# `[][0]` is null, `null | "\(.status)"` interpolates the STRING "null",
# which compares unequal to "queued", so the first poll concludes an attempt
# that does not exist has started); shape 2 is a lookup keyed on head SHA
# and workflow alone, which cannot tell this run's sibling from another
# trigger's run on the same commit.

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
subject="$repo_root/scripts/ci/self-hosted-attempt.sh"
ci_workflow="$repo_root/.github/workflows/ci.yml"

test -x "$subject" || {
  printf '%s: %s is missing or not executable\n' "${0##*/}" "$subject" >&2
  exit 2
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

failures=0
checks=0

assert_eq() {
  local desc="$1" expected="$2" actual="$3"
  checks=$(( checks + 1 ))
  if [ "$expected" = "$actual" ]; then
    printf 'PASS: %s\n' "$desc"
  else
    printf 'FAIL: %s\n  expected: [%s]\n  actual:   [%s]\n' "$desc" "$expected" "$actual" >&2
    failures=$(( failures + 1 ))
  fi
}

assert_ne() {
  local desc="$1" unexpected="$2" actual="$3"
  checks=$(( checks + 1 ))
  if [ "$unexpected" != "$actual" ]; then
    printf 'PASS: %s\n' "$desc"
  else
    printf 'FAIL: %s\n  value should NOT have been: [%s]\n' "$desc" "$actual" >&2
    failures=$(( failures + 1 ))
  fi
}

assert_fails() {
  local desc="$1"
  shift
  checks=$(( checks + 1 ))
  if "$@" >/dev/null 2>&1; then
    printf 'NEGATIVE CONTROL FAILED: %s -- the command succeeded on input that should have failed it\n' "$desc" >&2
    failures=$(( failures + 1 ))
  else
    printf 'PASS (negative control): %s\n' "$desc"
  fi
}

SHA='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
WF_NAME='ci-self-hosted'
WF_PATH='.github/workflows/ci-self-hosted.yml'
EVENT='pull_request'
REF='some-topic-branch'
JOB='race-devhealthschema-self-hosted'
# Every fixture run is created at 10:00-13:30 on 2026-09-02, so a floor before
# that admits them all; the staleness tests below use a floor between them.
FLOOR='2026-09-01T00:00:00Z'
# "<created_at> <run_started_at> <run_attempt>" for the run under test.
# Attempt 1, so created_at == run_started_at, as measured on every real
# attempt-1 run.
RUN_IDENTITY='2026-09-02T10:30:00Z 2026-09-02T10:30:00Z 1'

# ---------------------------------------------------------------------------
# 1. select-run: which sibling run, if any, belongs to THIS commit and trigger
# ---------------------------------------------------------------------------

# One commit legitimately carries several runs. This fixture holds every way
# a wrong one can look right: another trigger's run, another branch's run,
# ci.yml's own run on the same commit, a run for a different commit, and a
# genuine re-run of the right workflow. Only the newest fully-matching run
# (105) is the sibling.
cat >"$tmpdir/runs-mixed.json" <<'JSON'
{"workflow_runs":[
 {"id":100,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T10:00:00Z"},
 {"id":101,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"push","head_branch":"some-topic-branch","created_at":"2026-09-02T12:00:00Z"},
 {"id":102,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"main","created_at":"2026-09-02T12:30:00Z"},
 {"id":103,"name":"ci","path":".github/workflows/ci.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T13:00:00Z"},
 {"id":104,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T13:30:00Z"},
 {"id":105,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T11:00:00Z"}
]}
JSON

assert_eq 'select-run picks the newest fully-matching sibling run' \
  '105' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-mixed.json")"

# Each identity field needs a candidate that differs in THAT FIELD ALONE, or
# the conjunct is not covered. The mixed fixture above has a decoy differing
# on both name and path at once, so dropping the path conjunct still excluded
# it by name -- review round 3 showed exactly that mutation surviving all 65
# checks. A same-named workflow at a different path would then be adopted and
# the fallback would skip its work on a stranger's conclusion.
cat >"$tmpdir/runs-path-only.json" <<'JSON'
{"workflow_runs":[
 {"id":999,"name":"ci-self-hosted","path":".github/workflows/unrelated.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T11:00:00Z","run_started_at":"2026-09-02T11:00:00Z"}
]}
JSON
assert_eq 'a candidate differing ONLY in workflow path is not selected' \
  '' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-path-only.json")"

# NEGATIVE CONTROL: the same candidate with the correct path IS selected, so
# the assertion above is the path conjunct working rather than the fixture
# failing identity on some other field.
sed 's|.github/workflows/unrelated.yml|.github/workflows/ci-self-hosted.yml|' \
  "$tmpdir/runs-path-only.json" >"$tmpdir/runs-path-fixed.json"
assert_eq 'negative control: the same candidate with the right path IS selected' \
  '999' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-path-fixed.json")"

# The stale-sibling hazard on its own: the ONLY run on this commit came from
# a different trigger. The right answer is "no sibling", so the hosted leg
# does the work. A lookup keyed on SHA + workflow alone answers 101 instead
# and would report that stale run's conclusion as this run's result.
cat >"$tmpdir/runs-wrong-event.json" <<'JSON'
{"workflow_runs":[
 {"id":101,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"push","head_branch":"some-topic-branch","created_at":"2026-09-02T12:00:00Z"}
]}
JSON

assert_eq 'select-run rejects a run on the same SHA from a different event' \
  '' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-wrong-event.json")"

# NEGATIVE CONTROL for the above: the defect shape (match on workflow + SHA,
# ignore event and ref) must return the stale run on this same fixture. If it
# did not, the fixture would not be exercising the hazard at all.
ops_shaped_select() {
  jq -s -r --arg wf "$WF_NAME" --arg sha "$SHA" '
    [ .[]?.workflow_runs[]? ]
    | map(select(.name == $wf and .head_sha == $sha))
    | sort_by(.created_at) | last
    | if . == null then "" else (.id | tostring) end
  ' <"$tmpdir/runs-wrong-event.json"
}
assert_eq 'negative control: a lookup ignoring event/ref DOES pick the stale run' \
  '101' "$(ops_shaped_select)"

cat >"$tmpdir/runs-wrong-branch.json" <<'JSON'
{"workflow_runs":[
 {"id":102,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"main","created_at":"2026-09-02T12:30:00Z"}
]}
JSON
assert_eq 'select-run rejects a run on the same SHA and event from another ref' \
  '' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-wrong-branch.json")"

# The empty list is the ordinary case for the first few polls (the sibling
# run exists but the API has not listed it yet) and the permanent case when
# the switch is off. It must be an empty answer, never an error: this whole
# script runs under `set -euo pipefail` inside a CI step, so an abort here
# would fail the required gate on a condition that is completely normal.
echo '{"workflow_runs":[]}' >"$tmpdir/runs-empty.json"
assert_eq 'select-run returns empty, not an error, when no run matches' \
  '' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-empty.json")"

# ---------------------------------------------------------------------------
# 2. attempt-status: absent must be distinguishable from started
# ---------------------------------------------------------------------------

echo '{"jobs":[]}' >"$tmpdir/jobs-empty.json"
assert_eq 'a job absent from the listing reads not_created' \
  'not_created null' \
  "$("$subject" attempt-status "$JOB" <"$tmpdir/jobs-empty.json")"

# NEGATIVE CONTROL: the ops-shaped expression on the identical fixture
# yields the literal string "null", which every caller compares unequal to
# "queued" and therefore reads as "the attempt has started".
assert_eq 'negative control: the [..][0] shape reports a started attempt for an absent job' \
  'null null' \
  "$(jq -s -r --arg job "$JOB" '[ .[]?.jobs[]? | select(.name == $job) ][0] | "\(.status) \(.conclusion // "null")"' <"$tmpdir/jobs-empty.json")"

# A job row that exists with null fields (observed while GitHub is still
# populating a freshly created job) must mean the same thing as absent --
# one state for callers to reason about, not two.
echo '{"jobs":[{"id":1,"name":"race-devhealthschema-self-hosted","status":null,"conclusion":null}]}' >"$tmpdir/jobs-null.json"
assert_eq 'a job row with a null status reads not_created' \
  'not_created null' \
  "$("$subject" attempt-status "$JOB" <"$tmpdir/jobs-null.json")"

assert_ne 'negative control: the [..][0] shape does NOT report not_created for a null status' \
  'not_created null' \
  "$(jq -s -r --arg job "$JOB" '[ .[]?.jobs[]? | select(.name == $job) ][0] | "\(.status) \(.conclusion // "null")"' <"$tmpdir/jobs-null.json")"

echo '{"jobs":[{"id":1,"name":"race-devhealthschema-self-hosted","status":"queued","conclusion":null}]}' >"$tmpdir/jobs-queued.json"
assert_eq 'a queued attempt reads queued' 'queued null' \
  "$("$subject" attempt-status "$JOB" <"$tmpdir/jobs-queued.json")"

echo '{"jobs":[{"id":1,"name":"race-devhealthschema-self-hosted","status":"in_progress","conclusion":null}]}' >"$tmpdir/jobs-running.json"
assert_eq 'a running attempt reads in_progress' 'in_progress null' \
  "$("$subject" attempt-status "$JOB" <"$tmpdir/jobs-running.json")"

echo '{"jobs":[{"id":1,"name":"race-devhealthschema-self-hosted","status":"completed","conclusion":"success"}]}' >"$tmpdir/jobs-success.json"
assert_eq 'a passing attempt reads completed success' 'completed success' \
  "$("$subject" attempt-status "$JOB" <"$tmpdir/jobs-success.json")"

echo '{"jobs":[{"id":1,"name":"race-devhealthschema-self-hosted","status":"completed","conclusion":"failure"}]}' >"$tmpdir/jobs-failure.json"
assert_eq 'a failing attempt reads completed failure' 'completed failure' \
  "$("$subject" attempt-status "$JOB" <"$tmpdir/jobs-failure.json")"

# Another job in the same run must not be mistaken for the attempt.
echo '{"jobs":[{"id":1,"name":"some-other-job","status":"completed","conclusion":"success"}]}' >"$tmpdir/jobs-other.json"
assert_eq 'a different job in the same run does not answer for the attempt' \
  'not_created null' \
  "$("$subject" attempt-status "$JOB" <"$tmpdir/jobs-other.json")"

# ---------------------------------------------------------------------------
# 3. the two-phase wait, driven by a fake clock so the timeout branches run
# ---------------------------------------------------------------------------

run_wait() {
  local runs="$1" jobs="$2"
  rm -f "$jobs/.poll-index" 2>/dev/null || true
  env \
    ACR_ATTEMPT_FAKE_CLOCK=1 \
    ACR_ATTEMPT_RUNS_SRC="$runs" \
    ACR_ATTEMPT_JOBS_SRC="$jobs" \
    REPOSITORY='full-chaos/dev-health-acr' \
    HEAD_SHA="$SHA" EVENT="$EVENT" REF="$REF" \
    WORKFLOW_NAME="$WF_NAME" WORKFLOW_PATH="$WF_PATH" JOB_NAME="$JOB" \
    ACR_ATTEMPT_RUN_IDENTITY="${ACR_ATTEMPT_RUN_IDENTITY:-$RUN_IDENTITY}" \
    QUEUE_TIMEOUT_MINUTES=5 ATTEMPT_TIMEOUT_MINUTES=10 \
    "$subject" wait 2>/dev/null | sed -n 's/^outcome=//p'
}

cat >"$tmpdir/runs-match.json" <<'JSON'
{"workflow_runs":[
 {"id":105,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T11:00:00Z"}
]}
JSON

# GWC's hazard 1, as a test: no sibling run at all -- the switch was flipped
# off between the two workflows, or the pool is gone -- must resolve to "do
# the work here" once the queue bound expires, and must not error.
assert_eq 'no sibling run at all resolves to unclaimed (the hosted leg does the work)' \
  'unclaimed' "$(run_wait "$tmpdir/runs-empty.json" "$tmpdir/jobs-empty.json")"

assert_eq 'a sibling run whose attempt never leaves the queue resolves to unclaimed' \
  'unclaimed' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-queued.json")"

# The sibling run EXISTS but the API has not listed its jobs yet -- the
# precise shape that broke the ops implementation of this contract, now
# asserted end-to-end and not only on the status filter. Added after a
# mutation run: replacing attempt-status with the `[..][0]` shape turned the
# unit checks red but left every wait-loop check green, so the loop's
# handling of "run exists, job not listed" had never actually been executed.
# A mutation that leaves a whole layer green is a coverage report, not a pass.
mkdir -p "$tmpdir/jobs-late-listing"
cp "$tmpdir/jobs-empty.json"  "$tmpdir/jobs-late-listing/1.json"
cp "$tmpdir/jobs-empty.json"  "$tmpdir/jobs-late-listing/2.json"
cp "$tmpdir/jobs-queued.json" "$tmpdir/jobs-late-listing/3.json"
assert_eq 'a sibling run whose job is not listed yet is still queued, not started' \
  'unclaimed' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-late-listing")"

# The ordinary happy path, replayed one poll at a time: not listed yet,
# queued, running, passed.
mkdir -p "$tmpdir/jobs-progression"
cp "$tmpdir/jobs-empty.json"   "$tmpdir/jobs-progression/1.json"
cp "$tmpdir/jobs-queued.json"  "$tmpdir/jobs-progression/2.json"
cp "$tmpdir/jobs-running.json" "$tmpdir/jobs-progression/3.json"
cp "$tmpdir/jobs-success.json" "$tmpdir/jobs-progression/4.json"
assert_eq 'an attempt that starts and passes resolves to claimed-success' \
  'claimed-success' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-progression")"

mkdir -p "$tmpdir/jobs-progression-fail"
cp "$tmpdir/jobs-empty.json"   "$tmpdir/jobs-progression-fail/1.json"
cp "$tmpdir/jobs-running.json" "$tmpdir/jobs-progression-fail/2.json"
cp "$tmpdir/jobs-failure.json" "$tmpdir/jobs-progression-fail/3.json"
assert_eq 'an attempt that starts and fails resolves to claimed-failure' \
  'claimed-failure' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-progression-fail")"

# Started, then never terminal. A start is not a pass: this must be a
# failure, never a silent green, and never an infinite wait.
assert_eq 'an attempt that starts and never finishes resolves to claimed-failure' \
  'claimed-failure' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-running.json")"

# An abbreviated SHA returns an empty list from the API, which is
# byte-identical to "the pool is down" -- so it must be rejected on its face
# rather than spending the queue bound and reporting a takeover.
short_sha_wait() {
  env ACR_ATTEMPT_FAKE_CLOCK=1 \
    ACR_ATTEMPT_RUNS_SRC="$tmpdir/runs-match.json" \
    ACR_ATTEMPT_JOBS_SRC="$tmpdir/jobs-queued.json" \
    REPOSITORY='full-chaos/dev-health-acr' \
    ACR_ATTEMPT_RUN_IDENTITY="${ACR_ATTEMPT_RUN_IDENTITY:-$RUN_IDENTITY}" \
    HEAD_SHA='aaaaaaa' EVENT="$EVENT" REF="$REF" \
    WORKFLOW_NAME="$WF_NAME" WORKFLOW_PATH="$WF_PATH" JOB_NAME="$JOB" \
    "$subject" wait
}
assert_fails 'an abbreviated HEAD_SHA is rejected instead of read as "no sibling"' short_sha_wait

missing_env_wait() {
  env ACR_ATTEMPT_FAKE_CLOCK=1 \
    ACR_ATTEMPT_RUNS_SRC="$tmpdir/runs-match.json" \
    REPOSITORY='full-chaos/dev-health-acr' HEAD_SHA="$SHA" \
    "$subject" wait
}
assert_fails 'a missing required input is rejected rather than defaulted' missing_env_wait

# ---------------------------------------------------------------------------
# 3b. defects found by review round 1, each EXECUTED against this code first
# ---------------------------------------------------------------------------

# F2. head SHA + workflow + event + ref identify the COMMIT AND TRIGGER, not
# this invocation. A previous attempt on the same commit -- a re-run, a
# reopened PR -- shares all four, is already terminal, and is listed before the
# current sibling exists. Measured before the fix: `wait` returned
# claimed-success on poll 1 against a stale completed run, so the leg skipped
# its own work on the strength of a run it had no evidence owned.
# Same as run_wait but keeps the progress trace, so a test can assert which
# phase a case actually reached instead of assuming it.
run_wait_verbose() {
  local runs="$1" jobs="$2"
  rm -f "$jobs/.poll-index" 2>/dev/null || true
  env \
    ACR_ATTEMPT_FAKE_CLOCK=1 \
    ACR_ATTEMPT_RUNS_SRC="$runs" \
    ACR_ATTEMPT_JOBS_SRC="$jobs" \
    ACR_ATTEMPT_RUN_IDENTITY="${ACR_ATTEMPT_RUN_IDENTITY:-$RUN_IDENTITY}" \
    REPOSITORY='full-chaos/dev-health-acr' \
    HEAD_SHA="$SHA" EVENT="$EVENT" REF="$REF" \
    WORKFLOW_NAME="$WF_NAME" WORKFLOW_PATH="$WF_PATH" JOB_NAME="$JOB" \
    QUEUE_TIMEOUT_MINUTES=5 ATTEMPT_TIMEOUT_MINUTES=10 \
    "$subject" wait 2>/dev/null
}

cat >"$tmpdir/runs-stale.json" <<'JSON'
{"workflow_runs":[
 {"id":41,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T09:00:00Z"}
]}
JSON
assert_eq 'select-run rejects a sibling created before this run started' \
  '' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" '2026-09-02T10:29:30Z' <"$tmpdir/runs-stale.json")"

# NEGATIVE CONTROL: the same fixture with an early floor DOES select it, so the
# assertion above is the floor working and not the fixture failing to match on
# some other field.
assert_eq 'negative control: the same stale run is selected when the floor is early' \
  '41' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-stale.json")"

echo '{"jobs":[{"id":9,"name":"race-devhealthschema-self-hosted","status":"completed","conclusion":"success"}]}' >"$tmpdir/jobs-old-success.json"
assert_eq 'a stale completed sibling does not let the hosted leg skip its work' \
  'unclaimed' "$(run_wait "$tmpdir/runs-stale.json" "$tmpdir/jobs-old-success.json")"

# F3. The queue loop polled at 0:00, 0:15 … 4:45 and then exited ON the
# deadline without reading 5:00, so an attempt picked up inside that last
# 15-second window was invisible and BOTH legs ran the real suite. 20 empty
# polls then a started job is exactly that boundary; before the fix this
# returned `unclaimed`.
#
# Poll accounting, which is load-bearing and drifted once already: the first-look
# read is poll 1, the queue loop then polls at t=0,15,…,285 (polls 2..21), and
# the final read at the deadline is poll 22. The started state must therefore
# land on poll 22 to exercise the deadline read; at poll 21 it is caught by the
# loop's last iteration and the test passes with the deadline read deleted.
# That is exactly what happened when the first-look read was added in round 2 --
# every replay fixture shifted by one and this test silently stopped testing
# anything. Caught by a surviving mutation, not by review. The reach assertion
# below is the guard against it drifting again.
#
# RE-INDEXED for v1.5.1: deleting the first-look read shifted every replay
# fixture down by one poll. The queue phase now polls 1..20 and takes its
# final deadline read as poll 21, so the started state must land on 21.
# Measured, not derived -- deriving it is what silently broke this fixture and
# the requeue one when a read was ADDED ahead of them, and the same arithmetic
# breaks the other way when a read is removed. The reach assertion below is
# what keeps it honest either way.
mkdir -p "$tmpdir/jobs-at-deadline"
for i in $(seq 1 20); do cp "$tmpdir/jobs-empty.json" "$tmpdir/jobs-at-deadline/$i.json"; done
cp "$tmpdir/jobs-success.json" "$tmpdir/jobs-at-deadline/21.json"
assert_eq 'an attempt picked up exactly at the queue deadline is still seen' \
  'claimed-success' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-at-deadline")"

deadline_trace="$(run_wait_verbose "$tmpdir/runs-match.json" "$tmpdir/jobs-at-deadline")"
case "$deadline_trace" in
  *"final read at the deadline"*) deadline_reached=yes ;;
  *) deadline_reached=no ;;
esac
assert_eq 'that case genuinely exercised the final read at the deadline' \
  'yes' "$deadline_reached"

# F4. GitHub's job-status vocabulary is open and already carries non-terminal
# values beyond `queued`. The old blacklist ("not queued and not not_created
# means started") sent `waiting` into the result phase, which then failed the
# required check for a job no runner had begun -- it failed UNSAFE. Every
# unrecognised status must degrade to "not started yet".
for status in waiting requested pending some_status_github_adds_in_2027; do
  printf '{"jobs":[{"id":9,"name":"%s","status":"%s","conclusion":null}]}\n' "$JOB" "$status" \
    >"$tmpdir/jobs-$status.json"
  assert_eq "a '$status' attempt is not treated as started" \
    'unclaimed' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-$status.json")"
done

# NEGATIVE CONTROL for F4: the blacklist predicate the code used to carry calls
# `waiting` started, which is what produced the red build.
blacklist_says_started() {
  local status="$1"
  if [ "$status" != queued ] && [ "$status" != not_created ]; then echo started; else echo queued; fi
}
assert_eq 'negative control: the old blacklist predicate calls "waiting" started' \
  'started' "$(blacklist_says_started waiting)"

# F5. `sort_by` is stable, so two runs created in the same second kept API
# order and an OLDER run could win. Ids break the tie deterministically.
cat >"$tmpdir/runs-same-second.json" <<'JSON'
{"workflow_runs":[
 {"id":2,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T11:00:00Z"},
 {"id":1,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T11:00:00Z"}
]}
JSON
assert_eq 'two runs created in the same second resolve to the newer id' \
  '2' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-same-second.json")"

# F1 was REFUTED, and the measurement is recorded here so it is not re-raised:
# review round 1 called it Critical that `workflow_runs[].path` carries an
# `@<ref>` suffix, which would make the exact path match never fire. The live
# API for this repository returns the bare path -- including for the sibling
# run of the very PR that introduced this file:
#   $ gh api "repos/full-chaos/dev-health-acr/actions/runs?per_page=5" \
#       --jq '.workflow_runs[] | "\(.name)\t\(.path)"'
#   ci-self-hosted   .github/workflows/ci-self-hosted.yml
#   ci               .github/workflows/ci.yml
# The `@<ref>` form belongs to `referenced_workflows[].path`, not here. The
# finding's own repro asserted the shape it set out to prove, so it proved only
# that the selector rejects a path it should reject.

# ---------------------------------------------------------------------------
# 3c. defects found by review round 2, each EXECUTED against this code first
# ---------------------------------------------------------------------------

# R2-F1. The created_at floor narrows the stale-sibling class but cannot close
# it: any tolerance wide enough for webhook ordering jitter is wide enough for
# a separate trigger inside it. Measured before the fix -- a prior sibling 60 s
# older was selected under the 90 s allowance. The discriminator is now a
# property instead of a tuning: a sibling already TERMINAL at first look
# belongs to an earlier invocation, because our first poll happens seconds
# after this run starts and an attempt takes minutes.
cat >"$tmpdir/runs-recent-prior.json" <<'JSON'
{"workflow_runs":[
 {"id":41,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T10:29:00Z"}
]}
JSON
assert_eq 'a sibling already completed at first look IS ours and its success is adopted' \
  'claimed-success' "$(run_wait "$tmpdir/runs-recent-prior.json" "$tmpdir/jobs-success.json")"

# And the property does not depend on the tolerance. Round 2 showed all 36
# checks passing with SIBLING_CREATED_SKEW_SECONDS widened tenfold, which meant
# the safety property was pinned by the fixture's age rather than by the code.
# Re-assert the same case with the skew widened far past any plausible value:
# if this still holds, the guarantee is not a function of the constant.
assert_eq 'adoption of an in-floor terminal sibling does not depend on the tolerance' \
  'claimed-success' \
  "$(SIBLING_CREATED_SKEW_SECONDS=9000 run_wait "$tmpdir/runs-recent-prior.json" "$tmpdir/jobs-success.json")"

# NEGATIVE CONTROL: the same recent sibling IS selectable by the selector --
# the assertions above are the first-look rule working, not the fixture
# failing to match on some other field.
assert_eq 'negative control: that same sibling is selectable on identity alone' \
  '41' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR" <"$tmpdir/runs-recent-prior.json")"


# R2-F1b. The floor comparison is lexical, so a non-`Z` timestamp could sort
# later than it occurred: an offset timestamp denoting the previous day passed
# the floor. Only the exact `...Z` shape GitHub emits is accepted now;
# anything else is rejected rather than compared.
cat >"$tmpdir/runs-offset-ts.json" <<'JSON'
{"workflow_runs":[
 {"id":42,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T10:29:31+23:00"}
]}
JSON
assert_eq 'a non-Z timestamp is rejected rather than lexically compared' \
  '' \
  "$("$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" '2026-09-02T10:29:30Z' <"$tmpdir/runs-offset-ts.json")"

# R2-F2. A runner lost mid-job sends the attempt `in_progress` -> `queued`.
# Waiting out the result budget for a terminal state that is not coming and
# then failing turns a recoverable infrastructure event into a red required
# check; measured, that is exactly what happened. There is no running attempt
# left to trust, so the work comes back to this leg.
#
# The ORDER of these fixtures is load-bearing, and the first attempt at them
# was vacuous. `wait` takes one read BEFORE the queue loop (the first-look
# rule), so a fixture starting at `in_progress` spends that observation on the
# first look and the queue loop then sees only `queued` -- the assertion passed
# from the QUEUE phase and never entered the result phase at all. It was a
# mutation that survived (disabling the requeue branch changed nothing) that
# exposed it, not review. Poll 1 must therefore be `queued` so the first look
# is uneventful, poll 2 `in_progress` so the queue loop hands over to the
# result phase, and poll 3 `queued` so the requeue branch is the thing under
# test.
mkdir -p "$tmpdir/jobs-requeued"
cp "$tmpdir/jobs-queued.json"  "$tmpdir/jobs-requeued/1.json"
cp "$tmpdir/jobs-running.json" "$tmpdir/jobs-requeued/2.json"
cp "$tmpdir/jobs-queued.json"  "$tmpdir/jobs-requeued/3.json"
assert_eq 'an attempt whose runner is lost hands the work back instead of failing the check' \
  'unclaimed' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-requeued")"

# Reach check: the assertion above is only about the requeue branch if the run
# actually got into the result phase. Assert that it did, rather than trusting
# the fixture ordering to stay correct through future edits.
#
# NB: this probe captures the trace into a variable before matching it. The
# first spelling piped into `grep -q`, which exits on its first match, SIGPIPEs
# the producer, and under `set -o pipefail` makes the whole pipeline exit 141 --
# so the `||` branch fired and the probe reported "no" for a case that plainly
# does reach the result phase. A probe whose failure branch can be reached for a
# reason unrelated to what it measures is worse than no probe.
requeue_trace="$(run_wait_verbose "$tmpdir/runs-match.json" "$tmpdir/jobs-requeued")"
case "$requeue_trace" in
  *"result phase"*) requeue_reached=yes ;;
  *) requeue_reached=no ;;
esac
assert_eq 'that requeue case genuinely reached the result phase' \
  'yes' "$requeue_reached"

# The genuine hang is still a failure: an attempt that stays `in_progress`
# past the result bound is not recoverable and must not read as a pass.
assert_eq 'an attempt still running past the result bound is still a failure' \
  'claimed-failure' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-running.json")"

# The RESULT phase had the same deadline blind spot the queue phase had, and it
# fails in the worse direction -- a red required check for an attempt that
# passed. Found by reviewing the fleet contract's F3 clause ("applies to every
# poll phase with a wall-clock deadline"), not by either review round, because
# both rounds' fixtures targeted the queue phase.
#
# The poll index below was MEASURED, not derived: poll 1 is the first look,
# poll 2 the queue phase's single reading (in_progress, so it hands straight
# over), polls 3..54 the result loop, and poll 55 the result phase's final read
# at its deadline. Deriving it by arithmetic is what silently broke two earlier
# fixtures when a read was added ahead of them, so the reach assertion below is
# what keeps this honest rather than the comment.
mkdir -p "$tmpdir/jobs-late-success"
cp "$tmpdir/jobs-queued.json" "$tmpdir/jobs-late-success/1.json"
for i in $(seq 2 54); do cp "$tmpdir/jobs-running.json" "$tmpdir/jobs-late-success/$i.json"; done
cp "$tmpdir/jobs-success.json" "$tmpdir/jobs-late-success/55.json"
assert_eq 'an attempt that finishes exactly at the result deadline is not failed' \
  'claimed-success' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-late-success")"

late_trace="$(run_wait_verbose "$tmpdir/runs-match.json" "$tmpdir/jobs-late-success")"
case "$late_trace" in
  *"result phase (final read at the deadline)"*) late_reached=yes ;;
  *) late_reached=no ;;
esac
assert_eq 'that case genuinely exercised the result phase final read' \
  'yes' "$late_reached"

# Contract v1.4's F3 clause ends: "if that last read reveals a lost runner, it
# resolves via F6, not a bare timeout failure." That is the one cell where two
# clauses intersect, and mapping the clauses to tests is what showed it had
# none -- the F3 tests and the F6 tests each exercise their own clause and meet
# only in the code. The implementation conforms because the loop body and the
# final read share `result_phase_outcome`, so F6's handling is reachable from
# both; but a conformance that holds only because of a refactor done for an
# unrelated reason is exactly the kind that regresses silently. Pin it.
#
# Poll accounting, measured: 1 first look (queued), 2 queue phase (in_progress,
# hands straight over), 3..54 the result loop, 55 the result phase's final read
# -- which is where the lost runner appears.
mkdir -p "$tmpdir/jobs-lost-at-deadline"
cp "$tmpdir/jobs-queued.json" "$tmpdir/jobs-lost-at-deadline/1.json"
for i in $(seq 2 54); do cp "$tmpdir/jobs-running.json" "$tmpdir/jobs-lost-at-deadline/$i.json"; done
cp "$tmpdir/jobs-queued.json" "$tmpdir/jobs-lost-at-deadline/55.json"
assert_eq 'a lost runner revealed only by the result final read resolves via F6, not a timeout failure' \
  'unclaimed' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-lost-at-deadline")"

lost_trace="$(run_wait_verbose "$tmpdir/runs-match.json" "$tmpdir/jobs-lost-at-deadline")"
case "$lost_trace" in
  *"result phase (final read at the deadline)"*) lost_reached=yes ;;
  *) lost_reached=no ;;
esac
assert_eq 'that F3-F6 case genuinely reached the result phase final read' \
  'yes' "$lost_reached"

# ---------------------------------------------------------------------------
# 3d. pagination: a listing that spans pages must still be searched whole
# ---------------------------------------------------------------------------
# `gh api --paginate` does not return one merged document -- it emits one JSON
# object PER PAGE, concatenated. Both consumers therefore slurp (`jq -s`) and
# flatten across objects. If either half of that regresses -- the fetcher
# dropping `--paginate`, or a consumer reading a single object -- everything
# past page 1 becomes invisible: the sibling run or the attempt job simply is
# not found, the hosted leg quietly does all the work, and nothing goes red.
# Same silent-in-a-green-build shape as the rest of this file.
#
# NOTE on what these can and cannot prove. Every other test here drives the
# script through `ACR_ATTEMPT_RUNS_SRC`/`ACR_ATTEMPT_JOBS_SRC`, which replace
# the `gh api` call entirely -- so no fixture-driven test can detect a missing
# `--paginate`. The two halves need different instruments: the stream tests
# below pin the CONSUMER (a multi-object stream is parsed whole), and the
# static check pins the PRODUCER (the flag is actually passed). The static one
# asserts the guard is PRESENT, not that it works, which is weaker on purpose
# and is the best available without a live API call.

cat >"$tmpdir/runs-page1.json" <<'JSON'
{"total_count":150,"workflow_runs":[{"id":1,"name":"ci","path":".github/workflows/ci.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T10:55:00Z"}]}
JSON
cat >"$tmpdir/runs-page2.json" <<'JSON'
{"total_count":150,"workflow_runs":[{"id":2,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T11:00:00Z"}]}
JSON
assert_eq 'a sibling run found only on page 2 of a paginated listing is still selected' \
  '2' \
  "$(cat "$tmpdir/runs-page1.json" "$tmpdir/runs-page2.json" | "$subject" select-run "$WF_NAME" "$WF_PATH" "$SHA" "$EVENT" "$REF" "$FLOOR")"

cat >"$tmpdir/jobs-page1.json" <<'JSON'
{"total_count":150,"jobs":[{"id":1,"name":"some-other-job","status":"completed","conclusion":"success"}]}
JSON
cat >"$tmpdir/jobs-page2.json" <<'JSON'
{"total_count":150,"jobs":[{"id":2,"name":"race-devhealthschema-self-hosted","status":"in_progress","conclusion":null}]}
JSON
assert_eq 'an attempt job found only on page 2 of a paginated listing is still read' \
  'in_progress null' \
  "$(cat "$tmpdir/jobs-page1.json" "$tmpdir/jobs-page2.json" | "$subject" attempt-status "$JOB")"

# Negative control: the multi-page path must not manufacture a match either.
cat >"$tmpdir/jobs-page2-other.json" <<'JSON'
{"total_count":150,"jobs":[{"id":2,"name":"yet-another-job","status":"queued","conclusion":null}]}
JSON
assert_eq 'a job absent from every page reads not_created, not a false match' \
  'not_created null' \
  "$(cat "$tmpdir/jobs-page1.json" "$tmpdir/jobs-page2-other.json" | "$subject" attempt-status "$JOB")"

# The producer half. Both collection listings must request every page; the
# single-object endpoint (this run's own record) correctly does not.
#
# Scoped to the two FETCHER FUNCTION BODIES, with comments stripped. The first
# version of this check grepped the whole file for a line carrying both `gh
# api` and a collection URL, and was wrong in both directions at once: it
# matched the example command inside the F1 refutation COMMENT (the same decoy
# trap scripts/ci/test-workflow-contract.sh documents), and it missed the runs
# fetcher entirely, because that call puts its URL on a continuation line. It
# reported a failure it had invented while not checking the call it existed
# for. Anchoring on the function body fixes both.
paginate_body() {
  local file="$1" fn="$2"
  awk -v fn="$fn" '$0 ~ "^" fn "\\(\\) \\{" { grab=1; next } grab && /^}/ { grab=0 } grab' "$file" \
    | sed 's/#.*$//'
}

check_paginate_flags() {
  local file="$1" status=0 fn body
  for fn in fetch_runs fetch_jobs; do
    body="$(paginate_body "$file" "$fn")"
    if ! printf '%s' "$body" | grep -q 'gh api'; then
      printf '%s() does not call gh api at all -- this check is pointed at the wrong function\n' "$fn" >&2
      status=1
      continue
    fi
    if ! printf '%s' "$body" | grep -q -- '--paginate'; then
      printf '%s() fetches a collection without --paginate, so nothing past page 1 is visible\n' "$fn" >&2
      status=1
    fi
  done
  return "$status"
}
checks=$(( checks + 1 ))
if check_paginate_flags "$subject"; then
  printf 'PASS: every collection listing is fetched with --paginate\n'
else
  printf 'FAIL: a collection listing is missing --paginate\n' >&2
  failures=$(( failures + 1 ))
fi

sed 's/gh api --paginate/gh api/' "$subject" >"$tmpdir/no-paginate.sh"
assert_fails 'dropping --paginate from the fetchers breaks the pagination check' \
  check_paginate_flags "$tmpdir/no-paginate.sh"

# ---------------------------------------------------------------------------
# 3f. contract v1.5.1's two REQUIRED fixtures
# ---------------------------------------------------------------------------

# REQUIRED FIXTURE 1 — fast-fail. A completed/FAILURE sibling inside the floor
# at first look is this invocation's result and its failure must be HONOURED.
# The rule this replaced discarded it, and the discard was not theoretical: an
# attempt that failed in 24 seconds was terminal before the hosted leg --
# sixteen minutes behind it in the queue -- looked even once, so its failure
# would have been thrown away and the build reported green. A fast-failing
# attempt is exactly the one most likely to be terminal at first look, which
# made the old rule most likely to hide the signal the routing exists for.
echo '{"jobs":[{"id":9,"name":"race-devhealthschema-self-hosted","status":"completed","conclusion":"failure"}]}' >"$tmpdir/jobs-first-look-failure.json"
assert_eq 'a terminal FAILURE inside the floor is honoured, not discarded' \
  'claimed-failure' "$(run_wait "$tmpdir/runs-recent-prior.json" "$tmpdir/jobs-first-look-failure.json")"

# Every terminal conclusion that is NOT success must reach claimed-failure.
# The tip already does this for all of them, but nothing pinned it: review
# round 2 showed that widening the success test to also accept `cancelled`
# survived at 58/58, which would let a sibling cancelled before its tests ran
# be reported as a pass. `success` is the ONLY conclusion that may skip this
# leg's own work, and that is now a fixture rather than a reading of the code.
#
# `skipped` is in the list deliberately even though it should not normally
# reach the waiter -- both workflows share the same switch and fork gate, so a
# skipped attempt means the fallback was not polling in the first place. If
# those gates ever drift apart, this pins the safe answer rather than leaving
# it to whichever way the drift happens to fall.
for conclusion in cancelled skipped timed_out neutral action_required stale; do
  printf '{"jobs":[{"id":9,"name":"%s","status":"completed","conclusion":"%s"}]}\n' \
    "$JOB" "$conclusion" >"$tmpdir/jobs-term-$conclusion.json"
  assert_eq "a terminal '$conclusion' sibling is a failure, never an adopted pass" \
    'claimed-failure' "$(run_wait "$tmpdir/runs-recent-prior.json" "$tmpdir/jobs-term-$conclusion.json")"
done

# A `completed` job with a NULL conclusion is the same class: unknown is not
# success. It fails closed, and a re-run can clear it because the re-run bound
# then rejects the old sibling and this leg does the work itself.
printf '{"jobs":[{"id":9,"name":"%s","status":"completed","conclusion":null}]}\n' "$JOB" \
  >"$tmpdir/jobs-term-null.json"
assert_eq 'a completed sibling with a null conclusion is a failure, not a pass' \
  'claimed-failure' "$(run_wait "$tmpdir/runs-recent-prior.json" "$tmpdir/jobs-term-null.json")"

# REQUIRED FIXTURE 2 — re-run. On attempt 2, a sibling from attempt 1 passes
# the created_at floor (created_at does not move on a re-run) but its own
# run_started_at predates this attempt's, so it is NOT ours: a re-run must
# produce a fresh result rather than replay a prior attempt's verdict.
# Without the conjunct the floor alone would adopt it, which is the round-2
# stale-sibling defect arriving back through the composition of two
# separately-reasonable rules.
cat >"$tmpdir/runs-prior-attempt.json" <<'JSON'
{"workflow_runs":[
 {"id":77,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T10:29:50Z","run_started_at":"2026-09-02T10:29:50Z"}
]}
JSON
assert_eq 'on a re-run, a prior attempt sibling is NOT ours even though it passes the created_at floor' \
  'unclaimed' \
  "$(ACR_ATTEMPT_RUN_IDENTITY='2026-09-02T10:29:45Z 2026-09-02T11:30:00Z 2' \
      run_wait "$tmpdir/runs-prior-attempt.json" "$tmpdir/jobs-success.json")"

# NEGATIVE CONTROL for fixture 2: the same sibling on ATTEMPT 1 (no re-run
# bound) IS ours and its success is adopted. Without this, the assertion above
# could pass because the fixture fails identity on some unrelated field.
assert_eq 'negative control: the same sibling on attempt 1 IS adopted' \
  'claimed-success' \
  "$(ACR_ATTEMPT_RUN_IDENTITY='2026-09-02T10:29:45Z 2026-09-02T10:29:45Z 1' \
      run_wait "$tmpdir/runs-prior-attempt.json" "$tmpdir/jobs-success.json")"

# The re-run branch must apply to EVERY attempt beyond the first, not just
# attempt 2. Review round 1 raised this as an argued coverage gap and it was
# real when executed: narrowing the branch to `attempt = 2`, which exempts
# attempt 3 and beyond, left all 57 checks green. Both prior fixtures use
# attempt 2, so nothing pinned the general case. This one does.
assert_eq 'the re-run rule applies at attempt 3, not only at attempt 2' \
  'unclaimed' \
  "$(ACR_ATTEMPT_RUN_IDENTITY='2026-09-02T10:29:45Z 2026-09-02T12:30:00Z 3' \
      run_wait "$tmpdir/runs-prior-attempt.json" "$tmpdir/jobs-success.json")"

# And a sibling that was ITSELF re-run keeps created_at at the original
# trigger while its run_started_at moves forward -- it must still be ours.
# This is why the conjunct compares run_started_at rather than created_at.
cat >"$tmpdir/runs-fresh-attempt2.json" <<'JSON'
{"workflow_runs":[
 {"id":78,"name":"ci-self-hosted","path":".github/workflows/ci-self-hosted.yml","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event":"pull_request","head_branch":"some-topic-branch","created_at":"2026-09-02T10:29:50Z","run_started_at":"2026-09-02T11:30:05Z"}
]}
JSON
assert_eq 'a sibling that was itself re-run is still ours on our own re-run' \
  'claimed-success' \
  "$(ACR_ATTEMPT_RUN_IDENTITY='2026-09-02T10:29:45Z 2026-09-02T11:30:00Z 2' \
      run_wait "$tmpdir/runs-fresh-attempt2.json" "$tmpdir/jobs-success.json")"

# ---------------------------------------------------------------------------
# 4. cross-file agreement between ci.yml and ci-self-hosted.yml
# ---------------------------------------------------------------------------
# Without this, renaming the attempt job or the sibling workflow leaves both
# files individually valid, every job green, and the routing permanently
# dead: the lookup would match nothing, the hosted leg would silently do all
# the work, and the switch would look enabled while buying nothing.

# Reads one `KEY: value` from the ci.yml wait step, asserting it appears
# exactly once first -- a value read from an ambiguous match is not evidence.
ci_env_value() {
  local key="$1" count value
  count="$(grep -c "^          ${key}: " "$ci_workflow" || true)"
  if [ "$count" != 1 ]; then
    printf 'expected exactly one "%s:" in the wait step of %s, found %s\n' \
      "$key" "$ci_workflow" "$count" >&2
    return 1
  fi
  value="$(grep "^          ${key}: " "$ci_workflow" | sed -E "s/^ *${key}: //")"
  printf '%s\n' "$value"
}

check_cross_file() {
  local sibling="$1" job_name wf_name declared_name status=0
  job_name="$(ci_env_value JOB_NAME)" || return 1
  wf_name="$(ci_env_value WORKFLOW_NAME)" || return 1

  if [ ! -r "$sibling" ]; then
    printf 'ci.yml names the sibling workflow %s, which does not exist\n' "$sibling" >&2
    return 1
  fi

  declared_name="$(sed -n -E 's/^name: (.*)$/\1/p' "$sibling" | head -n1)"
  if [ "$declared_name" != "$wf_name" ]; then
    printf 'ci.yml polls for workflow name "%s" but %s declares "%s" -- the lookup would match nothing and the hosted leg would silently do all the work\n' \
      "$wf_name" "$sibling" "$declared_name" >&2
    status=1
  fi

  if ! grep -qE "^  ${job_name}:" "$sibling"; then
    printf 'ci.yml polls for job "%s" but %s defines no such job -- the attempt would never be found\n' \
      "$job_name" "$sibling" >&2
    status=1
  fi
  return "$status"
}

sibling_path="$(ci_env_value WORKFLOW_PATH)"
checks=$(( checks + 1 ))
if check_cross_file "$repo_root/$sibling_path"; then
  printf 'PASS: ci.yml and %s agree on the workflow name and the attempt job name\n' "$sibling_path"
else
  printf 'FAIL: ci.yml and %s disagree\n' "$sibling_path" >&2
  failures=$(( failures + 1 ))
fi

# Negative controls: each mutation is a rename someone could plausibly make,
# and each must break the check above.
sed -E 's/^name: ci-self-hosted$/name: ci-self-hosted-renamed/' \
  "$repo_root/$sibling_path" >"$tmpdir/sibling-renamed-workflow.yml"
assert_fails 'renaming the sibling workflow breaks the cross-file check' \
  check_cross_file "$tmpdir/sibling-renamed-workflow.yml"

sed -E 's/^  race-devhealthschema-self-hosted:/  race-devhealthschema-attempt:/' \
  "$repo_root/$sibling_path" >"$tmpdir/sibling-renamed-job.yml"
assert_fails 'renaming the attempt job breaks the cross-file check' \
  check_cross_file "$tmpdir/sibling-renamed-job.yml"

assert_fails 'a missing sibling workflow breaks the cross-file check' \
  check_cross_file "$tmpdir/no-such-workflow.yml"

# ---------------------------------------------------------------------------

printf '\n%s checks run, %s failed\n' "$checks" "$failures"
test "$failures" -eq 0 || exit 1
printf 'PASS: self-hosted attempt selector satisfies every check and negative control\n'
