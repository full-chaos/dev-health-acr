#!/usr/bin/env bash
set -euo pipefail

# plan.sh decides which CI lanes a run needs and writes the decision to
# $GITHUB_OUTPUT. ci.yml's `plan` job runs it; `verify` reads its outputs.
#
# Environment:
#   EVENT      github.event_name
#   DRAFT      github.event.pull_request.draft ("true" for a draft PR)
#   LABELS     JSON array of the PR's label names
#   BASE_SHA   github.event.pull_request.base.sha   (pull_request only)
#   HEAD_SHA   github.event.pull_request.head.sha   (pull_request only)
#   CHANGES_FILE  optional: a `git diff --name-status --no-renames` listing to
#                 use instead of running git (offline tests)
#   GITHUB_OUTPUT where the outputs go
#
# Outputs:
#   heavy      false only for a draft PR without the ci-full label (any case)
#   scope      subset only on a pull_request whose changes
#              scripts/ci/affected_packages.py proved narrow; otherwise full
#   affected   space-separated import paths (empty unless scope=subset)
#   race_schema, race_facts, race_source, race_factoracle
#              whether each dedicated race job has a package to run
#   skip_jobs  JSON array of the job keys that MUST report `skipped`; every
#              other job must report `success`. verify enforces both ways.
#
# Push to main, tags, schedules and every non-pull_request event: heavy=true,
# scope=full, skip_jobs=[] -- nothing is narrowed there.

# Every job that needs the plan's heavy=true. ci.yml gates each one on
# needs.plan.outputs.heavy; test-workflow-contract.sh compares this list with
# the jobs that carry that gate.
heavy_jobs=(
  race race-devhealthschema race-devhealthfacts race-devhealthfacts-heavy
  race-devhealthfacts-mid race-devhealthfacts-fourth race-devhealthsource
  race-devhealthsource-heavy race-devhealthsource-mid race-factoracle
  race-factoracle-heavy build contracts container-contract-smoke
  container-reproducible container-oci-scan
)
schema_jobs=(race-devhealthschema)
facts_jobs=(race-devhealthfacts race-devhealthfacts-heavy race-devhealthfacts-mid race-devhealthfacts-fourth)
source_jobs=(race-devhealthsource race-devhealthsource-heavy race-devhealthsource-mid)
factoracle_jobs=(race-factoracle race-factoracle-heavy)

if [ "${1:-}" = "--print-heavy-jobs" ]; then
  printf '%s\n' "${heavy_jobs[@]}"
  exit 0
fi

: "${EVENT:?EVENT is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"
DRAFT="${DRAFT:-}"
LABELS="${LABELS:-[]}"

heavy=true
if [ "$EVENT" = pull_request ] && [ "$DRAFT" = true ] \
  && ! printf '%s' "$LABELS" | jq -e 'any(.[]; ascii_downcase == "ci-full")' >/dev/null; then
  heavy=false
fi

scope=full
affected=""
selector="not a pull_request"
if [ "$EVENT" = pull_request ]; then
  changes="${CHANGES_FILE:-}"
  if [ -z "$changes" ]; then
    changes="$(mktemp)"
    trap 'rm -f "$changes"' EXIT
    git diff --name-status --no-renames "${BASE_SHA}...${HEAD_SHA}" >"$changes"
  fi
  selection="$(python3 -I "$(dirname "${BASH_SOURCE[0]}")/affected_packages.py" "$changes" 2>&1)" \
    || selection="full selector exited non-zero"
  selector="$(printf '%s\n' "$selection" | head -n1)"
  if [ "$selector" = subset ]; then
    scope=subset
    affected="$(printf '%s\n' "$selection" | sed 1d | tr '\n' ' ' | sed 's/ *$//')"
  fi
fi

race_schema=true race_facts=true race_source=true race_factoracle=true
if [ "$scope" = subset ]; then
  export AFFECTED_PACKAGES="$affected"
  shard="$(dirname "${BASH_SOURCE[0]}")/test-shard.sh"
  nonempty() { [ -n "$(printf '%s' "$1" | tr -d '[:space:]')" ]; }
  race_schema=false race_facts=false race_source=false race_factoracle=false
  nonempty "$("$shard" isolated-shared)" && race_schema=true
  nonempty "$("$shard" isolated-dedicated devhealthfacts)" && race_facts=true
  nonempty "$("$shard" isolated-dedicated devhealthsource)" && race_source=true
  nonempty "$("$shard" isolated-dedicated factoracle)" && race_factoracle=true
fi

skip=()
if [ "$heavy" = false ]; then
  skip+=("${heavy_jobs[@]}")
fi
if [ "$scope" = subset ]; then
  [ "$race_schema" = true ] || skip+=("${schema_jobs[@]}")
  [ "$race_facts" = true ] || skip+=("${facts_jobs[@]}")
  [ "$race_source" = true ] || skip+=("${source_jobs[@]}")
  [ "$race_factoracle" = true ] || skip+=("${factoracle_jobs[@]}")
fi
if [ "${#skip[@]}" -eq 0 ]; then
  skip_jobs='[]'
else
  skip_jobs="$(printf '%s\n' "${skip[@]}" | jq -R . | jq -cs 'unique')"
fi

printf 'event=%s draft=%s labels=%s\n' "$EVENT" "$DRAFT" "$LABELS"
printf 'heavy=%s scope=%s selector=%s\n' "$heavy" "$scope" "$selector"
printf 'affected (%s packages): %s\n' "$(printf '%s' "$affected" | wc -w | tr -d ' ')" "$affected"
printf 'dedicated race jobs: schema=%s facts=%s source=%s factoracle=%s\n' \
  "$race_schema" "$race_facts" "$race_source" "$race_factoracle"
printf 'skip_jobs=%s\n' "$skip_jobs"
{
  printf 'heavy=%s\n' "$heavy"
  printf 'scope=%s\n' "$scope"
  printf 'affected=%s\n' "$affected"
  printf 'race_schema=%s\n' "$race_schema"
  printf 'race_facts=%s\n' "$race_facts"
  printf 'race_source=%s\n' "$race_source"
  printf 'race_factoracle=%s\n' "$race_factoracle"
  printf 'skip_jobs=%s\n' "$skip_jobs"
} >>"$GITHUB_OUTPUT"
