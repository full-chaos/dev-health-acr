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

POLL_INTERVAL_SECONDS="${POLL_INTERVAL_SECONDS:-15}"

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
#   workflow name AND path  -- `head_sha` alone returns every workflow that
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
  local want_name="$1" want_path="$2" want_sha="$3" want_event="$4" want_ref="$5"
  # `jq -s` slurps, so this accepts both a single response object and the
  # concatenated stream `gh api --paginate` emits.
  jq -s -r \
    --arg wf "$want_name" --arg path "$want_path" --arg sha "$want_sha" \
    --arg event "$want_event" --arg ref "$want_ref" '
      [ .[]?.workflow_runs[]? ]
      | map(select(
          .name == $wf
          and .path == $path
          and .head_sha == $sha
          and .event == $event
          and .head_branch == $ref
        ))
      | sort_by(.created_at)
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

# Reads the attempt's current state, or `not_created null` when the sibling
# run does not exist yet. Never fails: "I could not see it" is a state the
# phase loops interpret, not an error that aborts the leg under `set -e`.
current_state() {
  local run_id
  run_id="$(fetch_runs | select_run "$WORKFLOW_NAME" "$WORKFLOW_PATH" "$HEAD_SHA" "$EVENT" "$REF")"
  if [ -z "$run_id" ]; then
    printf 'not_created null\n'
    return 0
  fi
  fetch_jobs "$run_id" | attempt_status "$JOB_NAME"
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

  local queue_minutes="${QUEUE_TIMEOUT_MINUTES:-5}"
  local attempt_minutes="${ATTEMPT_TIMEOUT_MINUTES:-10}"
  local deadline status conclusion left_queue=false

  # Phase 1, the queue bound. This is the actual hang guard, and it runs on
  # GitHub-hosted infrastructure so it does not depend on the self-hosted
  # pool being healthy at all. `timeout-minutes` on the attempt job cannot
  # do this job: GitHub starts that clock when a job is PICKED UP, never
  # while it is queued, so an attempt nothing ever claims sits there until
  # GitHub's documented 24-hour queue backstop.
  deadline=$(( $(now) + queue_minutes * 60 ))
  while [ "$(now)" -lt "$deadline" ]; do
    read -r status conclusion <<<"$(current_state)"
    printf 'queue phase: attempt status=%s conclusion=%s\n' "$status" "$conclusion"
    if [ "$status" != queued ] && [ "$status" != not_created ]; then
      left_queue=true
      break
    fi
    poll_sleep
  done

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
  while [ "$(now)" -lt "$deadline" ]; do
    read -r status conclusion <<<"$(current_state)"
    printf 'result phase: attempt status=%s conclusion=%s\n' "$status" "$conclusion"
    if [ "$status" = completed ]; then
      if [ "$conclusion" = success ]; then
        emit_outcome claimed-success
      else
        emit_outcome claimed-failure
      fi
      return 0
    fi
    poll_sleep
  done

  printf 'self-hosted attempt left the queue but reached no terminal state within %sm; failing rather than reporting a silent pass\n' \
    "$(( attempt_minutes + 3 ))" >&2
  emit_outcome claimed-failure
}

usage() {
  printf 'usage: %s select-run <workflow_name> <workflow_path> <head_sha> <event> <ref>   (runs JSON on stdin)\n' "${0##*/}" >&2
  printf '       %s attempt-status <job_name>                                             (jobs JSON on stdin)\n' "${0##*/}" >&2
  printf '       %s wait                                                                  (env-driven)\n' "${0##*/}" >&2
}

main() {
  local subcommand="${1:-}"
  case "$subcommand" in
    select-run)
      shift
      [ "$#" -eq 5 ] || { usage; exit 2; }
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
