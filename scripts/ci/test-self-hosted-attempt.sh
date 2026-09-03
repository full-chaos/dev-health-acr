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
RUN_STARTED='2026-09-02T10:30:00Z'

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
  env \
    ACR_ATTEMPT_FAKE_CLOCK=1 \
    ACR_ATTEMPT_RUNS_SRC="$runs" \
    ACR_ATTEMPT_JOBS_SRC="$jobs" \
    REPOSITORY='full-chaos/dev-health-acr' \
    HEAD_SHA="$SHA" EVENT="$EVENT" REF="$REF" \
    WORKFLOW_NAME="$WF_NAME" WORKFLOW_PATH="$WF_PATH" JOB_NAME="$JOB" \
    ACR_ATTEMPT_RUN_STARTED_AT="$RUN_STARTED" \
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
    ACR_ATTEMPT_RUN_STARTED_AT="$RUN_STARTED" \
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
mkdir -p "$tmpdir/jobs-at-deadline"
for i in $(seq 1 20); do cp "$tmpdir/jobs-empty.json" "$tmpdir/jobs-at-deadline/$i.json"; done
cp "$tmpdir/jobs-success.json" "$tmpdir/jobs-at-deadline/21.json"
assert_eq 'an attempt picked up exactly at the queue deadline is still seen' \
  'claimed-success' "$(run_wait "$tmpdir/runs-match.json" "$tmpdir/jobs-at-deadline")"

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
