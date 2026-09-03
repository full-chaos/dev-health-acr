#!/usr/bin/env bash
set -euo pipefail

# self-hosted-attempt.sh -- the GitHub-hosted fallback leg's view of a
# self-hosted attempt running in the sibling `ci-self-hosted` workflow.
#
# Runner-routing contract v1 (fleet-shared; see the PR body) puts the real
# work on [self-hosted, oci-arc-runners] behind the repository variable
# SELF_HOSTED_RUNNERS, and requires a GitHub-hosted leg that takes the work
# back if the self-hosted attempt never starts. acr differs from ops in ONE
# respect: the attempt lives in a SEPARATE workflow file rather than as a
# sibling job inside ci.yml, because ci.yml's `verify` gate `needs` every job
# and rejects any result that is not `success`, and
# scripts/ci/test-workflow-contract.sh asserts set equality between the job
# list and verify.needs. A switch-off attempt job would resolve `skipped` and
# turn the required gate red. Keeping the attempt in its own workflow leaves
# ci.yml's job graph identical -- same names, same count -- whether the
# switch is on or off, and leaves `verify` and the contract test untouched.
#
# The cost of that choice is that the fallback leg cannot poll
# `runs/{GITHUB_RUN_ID}/jobs`: the attempt is in a DIFFERENT run. It has to
# find that run first. Getting that lookup wrong is silent in both
# directions -- match a stale run and the leg trusts a conclusion that
# describes different code; match nothing and the leg quietly does all the
# work forever while the self-hosted pool sits idle and every report says
# the routing "works". So the lookup and the status read are implemented
# here as pure stdin->stdout filters with no network and no clock, and
# scripts/ci/test-self-hosted-attempt.sh drives them from fixtures,
# including the shapes that bit the ops implementation of the same contract.
#
# THE FAILURE THIS FILE EXISTS TO AVOID (executed, ops PR #2145 tip
# 402725f29): that leg ran
#   jq '[.jobs[] | select(.name==$job)][0] | "\(.status) \(.conclusion)"'
# `[][0]` on no match is `null`, and `null | "\(.status)"` does not abort --
# it interpolates the STRING "null":
#   $ echo '{"jobs":[]}' | jq -r '[.jobs[]|select(.name=="x")][0] | "\(.status)"'
#   null
# "null" != "queued", so the very first poll concluded the attempt had left
# the queue, and the leg then burned its whole result budget waiting for a
# terminal state no job would ever report, and failed the required check.
# The absent case is therefore a NAMED state here (`not_created`), treated
# as still-queued, and it has its own test.
#
# Subcommands (all read their JSON from stdin so they can be tested offline):
#   select-run       print the sibling run id for this exact commit, or "".
#   attempt-status   print "<status> <conclusion>" for the attempt job.
#   wait             the two-phase poll; prints outcome=<...>.
#
# `wait` outcomes, using contract v1's vocabulary unchanged:
#   unclaimed        the attempt never left the queue inside the queue bound
#                    (or never existed at all) -- the hosted leg does the work.
#   claimed-success  the attempt reached a real `success` conclusion.
#   claimed-failure  the attempt reached a non-success terminal state, or left
#                    the queue and never reached one inside the result bound.

# DECLARED RUNNER TOOLSET (contract v1.5.1). An attempt leg may rely only on
# tooling the contract declares. Measured on oci-arc-runners (arm64, 8 vCPU,
# 125 GiB), 2026-09-03:
#   present  git, jq, curl, and a Docker daemon (arm64 29.7.2, Alpine 3.24
#            containerized, overlayfs) able to pull this repo's own ghcr
#            mirror unauthenticated
#   absent   make, node, psql, pg_isready
#   unset    GOMAXPROCS, GOFLAGS -- a leg wanting the repo's bounds exports
#            them itself
# Anything outside "present" is installed in the image first or invoked
# another way. A leg that assumes an undeclared tool exits 127 with no routing
# signal at all: that is not hypothetical, it is how the first arm64 pickup
# died, on `make: command not found`, after the shard script had correctly
# printed its package.

POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-15}"
# How far before this run's own start a sibling run may have been created and
# still count as ours. Covers the ordering jitter between two workflows started
# by one webhook event; anything older is a previous trigger's run.
SIBLING_CREATED_SKEW_SECONDS="${SIBLING_CREATED_SKEW_SECONDS:-90}"
CREATED_FLOOR=""
# Empty on attempt 1; on a re-run, the instant a candidate's own run_started_at
# must reach to be this attempt's sibling rather than a prior attempt's.
RERUN_FLOOR=""

# Fake-clock support exists so the timeout paths are TESTED rather than
# asserted. A five-minute queue bound cannot be exercised in a test suite
# that runs in the `scripts` job; with ACR_ATTEMPT_FAKE_CLOCK set, time
# advances only when the poll loop sleeps, so a full bound expires in
# milliseconds and the expiry branch is real code, not a comment.
_fake_now=0

now() {
  if [ -n "${ACR_ATTEMPT_FAKE_CLOCK:-}" ]; then
    printf '%s\n' "$_fake_now"
  else
    date -u +%s
  fi
}

poll_sleep() {
  if [ -n "${ACR_ATTEMPT_FAKE_CLOCK:-}" ]; then
    _fake_now=$(( _fake_now + POLL_INTERVAL_SECONDS ))
    return 0
  fi
  sleep "$POLL_INTERVAL_SECONDS"
}

die() {
  printf '%s: %s\n' "${0##*/}" "$*" >&2
  exit 1
}

# ---- pure filters ---------------------------------------------------------

# select-run <workflow_name> <workflow_path> <head_sha> <event> <ref>
#
# Prints the id of the newest workflow run that matches ALL FIVE of those,
# or an empty line when nothing matches. Every field is required to match,
# and the reason each one is here is a way the lookup can silently pick the
# wrong run:
#   workflow name AND path  -- measured, not assumed: review round 1 called it
#     Critical that `workflow_runs[].path` carries an `@<ref>` suffix, which
#     would make this exact match never fire and leave the routing silently
#     dead. The live API disagrees, for this repository and for the sibling
#     run of the PR that introduced this file:
#       $ gh api "repos/full-chaos/dev-health-acr/actions/runs?per_page=5" \
#           --jq '.workflow_runs[] | "\(.name)\t\(.path)"'
#       ci-self-hosted   .github/workflows/ci-self-hosted.yml
#       ci               .github/workflows/ci.yml
#     The `@<ref>` form belongs to `referenced_workflows[].path`. Keeping the
#     match exact: `head_sha` alone returns every workflow that
#     ran on this commit, ci.yml's own run included. Matching the path as
#     well as the display name means renaming one without the other stops
#     matching (safe direction: no match => the hosted leg does the work)
#     rather than matching something else, and the cross-file check in
#     test-self-hosted-attempt.sh fails the build on that rename.
#   head_sha  -- exact, full 40 characters, never a prefix. `?head_sha=` with
#     an abbreviated SHA returns an empty list, which is indistinguishable
#     from "the sibling has not been created yet"; the caller validates the
#     length before ever reaching the API.
#   event AND ref -- the same commit can carry runs from more than one
#     trigger (a branch push and the pull_request for that same head), and
#     those are different runs of different job sets. Without both, a
#     re-run or a second trigger on one commit can hand this leg a stale
#     sibling's conclusion.
# Ties on all five (a genuine re-run of the same workflow) resolve to the
# newest by created_at, which is the one this run's push actually started.
select_run() {
  local want_name="$1" want_path="$2" want_sha="$3" want_event="$4" want_ref="$5" min_created="$6"
  # v1.5.1: when this run is a re-run, a candidate whose own run_started_at
  # predates this attempt's (less the tolerance) is not ours -- a re-run must
  # produce a fresh result, never replay a prior attempt's verdict. Empty on
  # attempt 1, where the constraint does not apply. Compared on the
  # CANDIDATE's run_started_at rather than its created_at, because a sibling
  # workflow that was itself re-run keeps created_at at the original trigger
  # while its run_started_at moves; comparing created_at would wrongly exclude
  # that fresh attempt-2 sibling.
  local min_run_started="${7:-}"
  # `jq -s` slurps, so this accepts both a single response object and the
  # concatenated stream `gh api --paginate` emits.
  jq -s -r \
    --arg wf "$want_name" --arg path "$want_path" --arg sha "$want_sha" \
    --arg event "$want_event" --arg ref "$want_ref" --arg floor "$min_created" \
    --arg minstarted "$min_run_started" '
      [ .[]?.workflow_runs[]? ]
      | map(select(
          .name == $wf
          and .path == $path
          and .head_sha == $sha
          and .event == $event
          and .head_branch == $ref
          and (.created_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
          and .created_at >= $floor
          and ($minstarted == "" or (.run_started_at // .created_at) >= $minstarted)
        ))
      | sort_by(.created_at, .id)
      | last
      | if . == null then "" else (.id | tostring) end
    '
}

# attempt-status <job_name>
#
# Prints "<status> <conclusion>". A job that is not in the response -- the
# run has not been created, or the API has not listed its jobs yet -- is
# `not_created null`, NOT an error and NOT the string "null" (see the header).
# A job present with a null status reads `not_created` too: same meaning to
# every caller, one state to reason about.
attempt_status() {
  local want_job="$1"
  jq -s -r --arg job "$want_job" '
    [ .[]?.jobs[]? ]
    | map(select(.name == $job))
    | sort_by(.id)
    | last
    | if . == null then "not_created null"
      else "\(.status // "not_created") \(.conclusion // "null")"
      end
  '
}

# ---- API access (overridable for tests) -----------------------------------

fetch_runs() {
  if [ -n "${ACR_ATTEMPT_RUNS_SRC:-}" ]; then
    cat "${ACR_ATTEMPT_RUNS_SRC}"
    return 0
  fi
  gh api --paginate \
    "repos/${REPOSITORY}/actions/runs?head_sha=${HEAD_SHA}&event=${EVENT}&per_page=100"
}

# In tests ACR_ATTEMPT_JOBS_SRC may name a DIRECTORY holding 1.json, 2.json,
# ... -- one per poll, so a run that is queued, then in_progress, then
# completed can be replayed and the phase-2 branches are actually executed
# rather than merely described. The last file is reused once they run out.
#
# The poll counter lives in a FILE, not a shell variable: the phase loops
# read this through `$(current_state)`, so every call happens inside a
# command substitution and any variable incremented here would be discarded
# with that subshell. The first version used a variable, every poll replayed
# fixture 1, and both progression tests reported `unclaimed` -- the loop's
# entire "the attempt started" half had never run. Caught by the fixtures;
# it would not have been caught by review.
fetch_jobs() {
  local run_id="$1" file counter index
  if [ -n "${ACR_ATTEMPT_JOBS_SRC:-}" ]; then
    if [ -d "${ACR_ATTEMPT_JOBS_SRC}" ]; then
      counter="${ACR_ATTEMPT_JOBS_SRC}/.poll-index"
      index=$(( $(cat "$counter" 2>/dev/null || echo 0) + 1 ))
      printf '%s\n' "$index" >"$counter"
      file="${ACR_ATTEMPT_JOBS_SRC}/${index}.json"
      while [ ! -f "$file" ] && [ "$index" -gt 1 ]; do
        index=$(( index - 1 ))
        file="${ACR_ATTEMPT_JOBS_SRC}/${index}.json"
      done
      cat "$file"
    else
      cat "${ACR_ATTEMPT_JOBS_SRC}"
    fi
    return 0
  fi
  gh api --paginate "repos/${REPOSITORY}/actions/runs/${run_id}/jobs?per_page=100"
}

emit_outcome() {
  printf 'outcome=%s\n' "$1"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'outcome=%s\n' "$1" >>"${GITHUB_OUTPUT}"
  fi
}

# This run's own identity instants: "<created_at> <run_started_at> <run_attempt>".
#
# v1.5.1 is explicit that both are RUN-LEVEL instants, never job-level ones.
# That distinction is load-bearing and easy to get backwards: `run_started_at`
# is stamped at run CREATION, not at runner pickup -- measured across 20 recent
# runs of every workflow in this repo, it equals `created_at` on every
# `run_attempt=1` run, including ones read while still `queued`. The pilot's
# fallback JOB started 16 minutes after its run was created and neither field
# moved. The two diverge only on a re-run, where `created_at` stays at the
# original trigger and `run_started_at` moves (measured: +42m55s).
current_run_identity() {
  if [ -n "${ACR_ATTEMPT_RUN_IDENTITY:-}" ]; then
    printf '%s\n' "${ACR_ATTEMPT_RUN_IDENTITY}"
    return 0
  fi
  gh api "repos/${REPOSITORY}/actions/runs/${GITHUB_RUN_ID}" \
    --jq '"\(.created_at) \(.run_started_at) \(.run_attempt)"'
}

# Reads the attempt's current state, or `not_created null` when the sibling
# run does not exist yet. Never fails: "I could not see it" is a state the
# phase loops interpret, not an error that aborts the leg under `set -e`.
current_state() {
  local run_id
  run_id="$(fetch_runs | select_run "$WORKFLOW_NAME" "$WORKFLOW_PATH" "$HEAD_SHA" "$EVENT" "$REF" "$CREATED_FLOOR" "$RERUN_FLOOR")"
  if [ -z "$run_id" ]; then
    printf 'not_created null -\n'
    return 0
  fi
  printf '%s %s\n' "$(fetch_jobs "$run_id" | attempt_status "$JOB_NAME")" "$run_id"
}

# Has the attempt actually been picked up by a runner?
#
# F4 (review round 1, EXECUTED): this used to be "anything that is not `queued`
# and not `not_created`", which is a BLACKLIST -- and it fails UNSAFE. GitHub's
# job status vocabulary is open and already carries non-terminal values beyond
# `queued` (`waiting`, `requested`, `pending` are documented for checks/jobs
# awaiting approval or deployment gates). Under the blacklist a `waiting` job
# counted as started, so the leg entered the result phase, waited out the whole
# result budget for a terminal state that was never coming, and failed the
# required check -- a red build for a job no runner had even begun. Measured:
#   $ printf '{"jobs":[{"id":9,"name":"…-self-hosted","status":"waiting"}]}' \
#       | self-hosted-attempt.sh attempt-status …   ->  waiting null
#   ... wait ...                                    ->  outcome=claimed-failure
#
# Enumerate the STARTED states positively instead. Anything unrecognised --
# a status GitHub adds next year included -- counts as not yet started, which
# degrades to "the hosted leg does the work": slower, never wrong.
attempt_has_started() {
  case "$1" in
    in_progress|completed) return 0 ;;
    *) return 1 ;;
  esac
}

# Maps one result-phase observation to a final outcome, printing it and
# returning 0, or returning 1 when the attempt is still legitimately running
# and the caller should keep waiting.
#
# Factored out so the loop body and the final read at the deadline cannot
# drift apart: the queue phase grew its own deadline read in round 1 and the
# result phase did not, which is precisely the defect this exists to prevent
# recurring. One decision, two call sites.
result_phase_outcome() {
  local status="$1" conclusion="$2"
  if [ "$status" = completed ]; then
    if [ "$conclusion" = success ]; then
      printf 'claimed-success\n'
    else
      printf 'claimed-failure\n'
    fi
    return 0
  fi
  # An attempt that is no longer in a started state -- a lost runner that
  # GitHub re-queued, or a run that vanished -- leaves nothing to trust, so
  # the hosted leg claims the work rather than waiting out a terminal state
  # that may never arrive and then failing the required check.
  if ! attempt_has_started "$status"; then
    printf 'unclaimed\n'
    return 0
  fi
  return 1
}

# ---- the two-phase wait ---------------------------------------------------

cmd_wait() {
  local var
  for var in REPOSITORY HEAD_SHA EVENT REF WORKFLOW_NAME WORKFLOW_PATH JOB_NAME; do
    if [ -z "${!var:-}" ]; then
      die "$var is required"
    fi
  done
  # Fail closed on a short SHA rather than poll for five minutes and report
  # "the pool never picked it up": `?head_sha=` with an abbreviated SHA
  # returns an empty list, which is byte-identical to a pool that is down.
  if ! printf '%s' "${HEAD_SHA}" | grep -Eq '^[0-9a-f]{40}$'; then
    die "HEAD_SHA must be a full 40-character lowercase commit SHA, got: ${HEAD_SHA}"
  fi

  # F2 (review round 1, EXECUTED): head SHA + workflow + event + ref do not
  # identify THIS invocation -- they identify the commit and trigger, which a
  # previous attempt on the same commit shares. On a re-run of this workflow,
  # or a reopened PR, a sibling run from the EARLIER trigger is already listed
  # and already terminal, so the very first poll found it and propagated its
  # conclusion: the leg skipped its own work on the strength of a run it had
  # no evidence belonged to it, and a genuinely failing new attempt could be
  # reported as success. Measured: with one completed/success sibling in the
  # listing, `wait` returned outcome=claimed-success on poll 1.
  #
  # The discriminator is time: a sibling that belongs to this invocation
  # cannot have been created meaningfully before this run started. The two
  # runs are created by the same webhook event, normally within the same
  # second, so a small tolerance covers ordering jitter. Erring tight is the
  # safe direction -- rejecting a real sibling costs latency (the hosted leg
  # does the work), while accepting a stale one costs correctness.
  local created_at started_at attempt floor_epoch rerun_epoch
  read -r created_at started_at attempt <<<"$(current_run_identity)"
  if [ -z "$created_at" ] || [ "$created_at" = null ]; then
    die "could not read this run's own created_at; refusing to poll without a staleness floor"
  fi
  floor_epoch=$(( $(date -u -d "$created_at" +%s) - SIBLING_CREATED_SKEW_SECONDS ))
  CREATED_FLOOR="$(date -u -d "@${floor_epoch}" +%Y-%m-%dT%H:%M:%SZ)"
  printf 'this run was created at %s (attempt %s); ignoring sibling runs created before %s\n' \
    "$created_at" "$attempt" "$CREATED_FLOOR"

  # The re-run conjunct applies only from attempt 2 onward. On attempt 1 the
  # bound is empty and the identity check skips it entirely, so an ordinary
  # run pays nothing for it.
  RERUN_FLOOR=""
  if [ "${attempt:-1}" != 1 ]; then
    if [ -z "$started_at" ] || [ "$started_at" = null ]; then
      die "run_attempt is $attempt but run_started_at is unreadable; refusing to poll without the re-run bound"
    fi
    rerun_epoch=$(( $(date -u -d "$started_at" +%s) - SIBLING_CREATED_SKEW_SECONDS ))
    RERUN_FLOOR="$(date -u -d "@${rerun_epoch}" +%Y-%m-%dT%H:%M:%SZ)"
    printf 'this is attempt %s, started %s; a candidate whose own run_started_at predates %s is a prior attempt and is not ours\n' \
      "$attempt" "$started_at" "$RERUN_FLOOR"
  fi

  local queue_minutes="${QUEUE_TIMEOUT_MINUTES:-5}"
  local attempt_minutes="${ATTEMPT_TIMEOUT_MINUTES:-10}"
  local deadline status conclusion run_id left_queue=false

  # Review round 2, EXECUTED: the created_at floor alone does NOT close the
  # stale-sibling class, it only narrows it. Any tolerance wide enough to
  # absorb webhook ordering jitter is also wide enough to admit a genuinely
  # separate trigger inside it -- measured, a prior sibling 60 s older was
  # selected under the 90 s allowance. A time window cannot tell those apart,
  # so the floor is demoted to a cheap pre-filter and the actual discriminator
  # is a property no clock tolerance can fake:
  #
  #   A sibling that is ALREADY TERMINAL the first time we look belongs to an
  #   earlier invocation.
  #
  # Our first poll happens seconds after this run starts; the attempt takes
  # minutes. A genuinely concurrent sibling therefore cannot be `completed`
  # when we first see it. One that is, is somebody else's -- so drop it and
  # look again for ours. This holds no matter how wide the skew is set, which
  # is what makes it a property rather than a tuning.
  # v1.5.1 F2: NO first look that discards. A candidate satisfying the
  # identity conjunction is ours whatever its state, so a terminal one found
  # immediately is this invocation's result and falls through to the same
  # decision the result phase uses -- its success adopted, its failure
  # HONOURED as claimed-failure.
  #
  # The rule this replaces did the opposite, and the cost was measured rather
  # than argued: an attempt that failed in 24 seconds was terminal before the
  # hosted leg -- queued 16 minutes behind it -- had looked even once, so its
  # failure would have been discarded and the build reported green. A
  # fast-failing attempt is precisely the one most likely to be terminal at
  # first look, which made the old rule most likely to hide exactly the
  # signal this routing exists to obtain.

  # Phase 1, the queue bound. This is the actual hang guard, and it runs on
  # GitHub-hosted infrastructure so it does not depend on the self-hosted
  # pool being healthy at all. `timeout-minutes` on the attempt job cannot
  # do this job: GitHub starts that clock when a job is PICKED UP, never
  # while it is queued, so an attempt nothing ever claims sits there until
  # GitHub's documented 24-hour queue backstop.
  deadline=$(( $(now) + queue_minutes * 60 ))
  while [ "$(now)" -lt "$deadline" ]; do
    read -r status conclusion run_id <<<"$(current_state)"
    printf 'queue phase: attempt status=%s conclusion=%s run=%s\n' "$status" "$conclusion" "$run_id"
    if attempt_has_started "$status"; then
      left_queue=true
      break
    fi
    poll_sleep
  done

  # F3 (review round 1, EXECUTED): the loop above polls at 0:00, 0:15 … 4:45 and
  # then exits on the deadline WITHOUT reading the state at 5:00, so an attempt
  # picked up anywhere in that last 15-second window was invisible and both legs
  # would run the real suite. One final read closes the blind spot to zero
  # rather than leaving a guaranteed one-poll gap.
  if [ "$left_queue" != true ]; then
    read -r status conclusion run_id <<<"$(current_state)"
    printf 'queue phase (final read at the deadline): attempt status=%s conclusion=%s run=%s\n' \
      "$status" "$conclusion" "$run_id"
    if attempt_has_started "$status"; then
      left_queue=true
    fi
  fi

  if [ "$left_queue" != true ]; then
    printf 'self-hosted attempt did not leave the queue within %sm -- claiming the work here\n' \
      "$queue_minutes"
    emit_outcome unclaimed
    return 0
  fi

  # Phase 2, the result bound. A start is not a pass: reporting success the
  # moment the attempt is `in_progress` would green the required check
  # before its tests had run. Only a real terminal conclusion counts, and
  # running out of budget is a failure rather than a silent pass -- the
  # attempt's own timeout-minutes should have ended it well inside this.
  deadline=$(( $(now) + (attempt_minutes + 3) * 60 ))
  local outcome
  while [ "$(now)" -lt "$deadline" ]; do
    read -r status conclusion run_id <<<"$(current_state)"
    printf 'result phase: attempt status=%s conclusion=%s run=%s\n' "$status" "$conclusion" "$run_id"
    if outcome="$(result_phase_outcome "$status" "$conclusion")"; then
      if [ "$outcome" = unclaimed ]; then
        printf 'the attempt returned to %s after starting (runner lost, or the run vanished); claiming the work here rather than failing a job that may still succeed\n' \
          "$status"
      fi
      emit_outcome "$outcome"
      return 0
    fi
    poll_sleep
  done

  # The result phase has the same deadline blind spot the queue phase had, and
  # it fails in the worse direction: contract v1.3's F3 clause says the final
  # read applies to EVERY deadline-bounded phase, and reviewing that clause is
  # what exposed this. Measured before the fix -- an attempt still
  # `in_progress` through the bound and `completed/success` on the very next
  # poll produced `claimed-failure`: a red required check for an attempt that
  # had passed, its success never read.
  read -r status conclusion run_id <<<"$(current_state)"
  printf 'result phase (final read at the deadline): attempt status=%s conclusion=%s run=%s\n' \
    "$status" "$conclusion" "$run_id"
  if outcome="$(result_phase_outcome "$status" "$conclusion")"; then
    emit_outcome "$outcome"
    return 0
  fi

  printf 'self-hosted attempt left the queue but reached no terminal state within %sm; failing rather than reporting a silent pass\n' \
    "$(( attempt_minutes + 3 ))" >&2
  emit_outcome claimed-failure
}

usage() {
  printf 'usage: %s select-run <workflow_name> <workflow_path> <head_sha> <event> <ref> <min_created_at> [exclude_run_id]  (runs JSON on stdin)\n' "${0##*/}" >&2
  printf '       %s attempt-status <job_name>                                             (jobs JSON on stdin)\n' "${0##*/}" >&2
  printf '       %s wait                                                                  (env-driven)\n' "${0##*/}" >&2
}

main() {
  local subcommand="${1:-}"
  case "$subcommand" in
    select-run)
      shift
      # Spelled as an explicit `if` rather than `A && B || C`: with the
      # latter, C also runs when A is true and B is false, which is correct
      # here but is not obvious, and shellcheck flags it (SC2015).
      if [ "$#" -lt 6 ] || [ "$#" -gt 7 ]; then usage; exit 2; fi
      select_run "$@"
      ;;
    attempt-status)
      shift
      [ "$#" -eq 1 ] || { usage; exit 2; }
      attempt_status "$@"
      ;;
    wait)
      cmd_wait
      ;;
    *)
      usage
      exit 2
      ;;
  esac
}

main "$@"
