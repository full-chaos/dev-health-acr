#!/usr/bin/env bash
# run-queue-report.sh -- how long one workflow run's jobs waited for a runner.
#
#   scripts/ci/run-queue-report.sh <run_id> [owner/repo]
#   scripts/ci/run-queue-report.sh --jobs-json <file>     (offline, for tests)
#
# Read only. Prints one line per job (created, started, completed, queue wait,
# run time) and the critical path: from the job that finished last, step back
# to the job whose completion released it (the latest completion at or before
# its creation), and so on to the start. Each hop's queue wait is the time the
# job sat created but not started. Under a saturated hosted queue that wait is
# per job, so the number of hops on the path is what CHAOS-9877 counts.
set -euo pipefail

usage() {
  printf 'usage: %s <run_id> [owner/repo] | --jobs-json <file>\n' "$0" >&2
  exit 2
}

if [ "${1:-}" = --jobs-json ]; then
  [ -n "${2:-}" ] || usage
  jobs_json="$(cat "$2")"
else
  run_id="${1:-}"
  repo="${2:-full-chaos/dev-health-acr}"
  [[ "$run_id" =~ ^[0-9]+$ ]] || usage
  jobs_json="$(gh api "repos/$repo/actions/runs/$run_id/jobs?per_page=100&filter=latest" --paginate --jq '.jobs[]' | jq -s '{jobs: .}')"
fi

printf '%s' "$jobs_json" | jq -r '
  def ts: if . == null or . == "" then null else (sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) end;
  def mins: if . == null then "-" else ((. / 60 * 10 | round) / 10 | tostring) end;
  def hm: if . == null then "--:--:--" else (todate | .[11:19]) end;
  [.jobs[] | {name, c: (.created_at | ts), s: (.started_at | ts), e: (.completed_at | ts)}]
  | map(select(.c != null)) as $jobs
  | ($jobs | sort_by(.c, .name)[] |
      "\(.c | hm) \(.s | hm) \(.e | hm)  queue \((if .s then .s - .c else null end) | mins)m  run \((if .s and .e then .e - .s else null end) | mins)m  \(.name)"),
    "",
    ( [$jobs[] | select(.e != null)] as $done
      | if ($done | length) == 0 then "critical path: no completed job"
        else
          ( [ ($done | max_by(.e)) ]
            | until(
                (.[-1].c) as $c | ([$done[] | select(.e <= $c)] | length) == 0;
                (.[-1].c) as $c | . + [ [$done[] | select(.e <= $c)] | max_by(.e) ]
              )
            | reverse ) as $path
          | ($path | map(if .s then .s - .c else 0 end) | add) as $wait
          | "critical path: \($path | length) hop(s), queue \($wait | mins)m of \((($path[-1].e) - ($path[0].c)) | mins)m wall",
            ($path[] | "  \(.c | hm) queue \((if .s then .s - .c else null end) | mins)m  \(.name)")
        end )
'
