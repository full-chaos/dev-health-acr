#!/usr/bin/env bash
#
# seed-corpus-cases.sh (CHAOS-4525) -- append trial-corpus cases and their
# oracle-annex entries from a LOCAL, UNCOMMITTED seed spec, after PROVING
# every oracle claim in the spec against the live read-only graph.
#
# Why this exists
# ---------------
# The two-turn trial corpus (.remember/acr-3778-corpus-ext65.json) and its
# oracle annex (.remember/trial-results/oracle-annex-v2-ext65.json) are two
# copies of the same information (CHAOS-4348, Run G): the harness reads
# expect_kind/expect_id from the CORPUS, while the ANNEX is authoritative.
# cmd/acr-corpus-annex-sync already reconciles the two mechanically. What
# did NOT exist was a producer for ADDING a case: every ext65 case predates
# this repository's trial tooling, so a new case had no path but hand-typed
# JSON -- which the standing fixture rule (CHAOS-4117: fixtures come from the
# producer, never hand-authored) forbids for oracle content.
#
# This script is that producer. It does three things a human editing JSON
# cannot be trusted to do:
#
#   1. EXECUTES the oracle claims. Every anchor canonical id the spec names
#      (positive and negative) must resolve to exactly one Subject node in
#      the named graph key, and every "absent_terms" entry a no_match case
#      claims has no lexical candidate must return ZERO matches across
#      label/search_text/aliases. A spec that cannot be proven is refused;
#      nothing is written. This is the same "live_graph_readonly_query"
#      method the annex's own provenance already records for ext65.
#   2. Derives the corpus rows' expect_kind/expect_id from the annex rather
#      than accepting them from the spec -- by leaving them empty and
#      handing off to cmd/acr-corpus-annex-sync, the existing tool of
#      record, which fills them from the annex's own oracles and updates
#      provenance.corpus_sha8 so the harness hash guard
#      (internal/runtime/hosted/chaos3742_two_turn_confirmation_test.go:
#      9100-9101) stays satisfiable.
#   3. Records what it proved, in the annex's own provenance, in the same
#      shape provenance.chaos4348_id_regenerations already uses.
#
# CORPUS QUESTION TEXT: the spec file carries it and the spec file is NEVER
# committed (standing rule: corpus question text never leaves local
# artifacts; cases are referred to by index and band). This script prints
# indices and bands only, never a question.
#
# Usage:
#   scripts/trial/seed-corpus-cases.sh \
#     --spec   <local seed spec json> \
#     --corpus <corpus json> \
#     --annex  <oracle annex json> \
#     [--namespace acr-trial-data] [--dry-run]
#
# Requires: kubectl (KUBECONFIG pointing at the trial cluster), jq, go.

set -euo pipefail

namespace="acr-trial-data"
selector="app.kubernetes.io/component=falkordb"
spec="" corpus="" annex="" dry_run=0

die() { echo "seed-corpus-cases.sh: $*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --spec) spec="${2:-}"; shift 2 ;;
    --corpus) corpus="${2:-}"; shift 2 ;;
    --annex) annex="${2:-}"; shift 2 ;;
    --namespace) namespace="${2:-}"; shift 2 ;;
    --selector) selector="${2:-}"; shift 2 ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) sed -n '2,50p' "$0"; exit 0 ;;
    *) die "unknown flag: $1" ;;
  esac
done

[[ -n "$spec"   ]] || die "--spec is required"
[[ -n "$corpus" ]] || die "--corpus is required"
[[ -n "$annex"  ]] || die "--annex is required"
for f in "$spec" "$corpus" "$annex"; do [[ -f "$f" ]] || die "not a file: $f"; done
command -v jq >/dev/null || die "jq is required"
command -v kubectl >/dev/null || die "kubectl is required"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"

graph_key="$(jq -r '.graph_key' "$spec")"
org_id="$(jq -r '.org_id' "$spec")"
[[ -n "$graph_key" && "$graph_key" != "null" ]] || die "spec is missing graph_key"
[[ -n "$org_id"    && "$org_id"    != "null" ]] || die "spec is missing org_id"

annex_org="$(jq -r '.provenance.org_id' "$annex")"
[[ "$annex_org" == "$org_id" ]] || die "spec org_id ($org_id) != annex provenance.org_id ($annex_org)"

pod="$(kubectl -n "$namespace" get pods -l "$selector" -o name | head -1)"
[[ -n "$pod" ]] || die "no pod matching $selector in namespace $namespace"

# falkor_count runs ONE read-only Cypher query and returns the single
# integer it projects. redis-cli's raw output puts the column header on
# line 1 and the value on line 2 (verified against this cluster), so the
# parse is a fixed line, never a regex over prose. A non-numeric line 2
# means the query errored (FalkorDB reports errors on stdout), which is a
# hard failure -- an unproven oracle must never be written.
falkor_count() {
  local cypher="$1" out value
  out="$(kubectl -n "$namespace" exec "$pod" -- redis-cli GRAPH.QUERY "$graph_key" "$cypher" 2>&1)" \
    || die "graph query failed: $cypher"
  value="$(printf '%s\n' "$out" | sed -n '2p' | tr -d '[:space:]')"
  [[ "$value" =~ ^[0-9]+$ ]] || die "graph query did not return a count (got: $(printf '%s' "$out" | head -2 | tr '\n' ' ')) for: $cypher"
  printf '%s' "$value"
}

# reject_unsafe fails closed on any value that would need quoting inside a
# Cypher single-quoted literal. Every real subject id and lexical term in
# this corpus is [A-Za-z0-9 ._:%@/()+-]; anything else is a spec mistake,
# not a value to escape our way around.
reject_unsafe() {
  [[ "$1" =~ ^[A-Za-z0-9\ ._:%@/\(\)+-]+$ ]] || die "unsafe value for a Cypher literal (refusing to escape): $1"
}

echo "seed-corpus-cases.sh: graph=$graph_key org=$org_id namespace=$namespace pod=${pod#pod/}"

# ---------------------------------------------------------------- verify
verifications="[]"
case_count="$(jq -r '.cases | length' "$spec")"
[[ "$case_count" -gt 0 ]] || die "spec has no cases"

for (( c=0; c<case_count; c++ )); do
  band="$(jq -r ".cases[$c].band" "$spec")"
  klass="$(jq -r ".cases[$c].question_class" "$spec")"

  while IFS= read -r id; do
    [[ -n "$id" && "$id" != "null" ]] || continue
    reject_unsafe "$id"
    n="$(falkor_count "MATCH (n:Subject) WHERE n.canonical_id = '$id' RETURN count(n)")"
    [[ "$n" == "1" ]] || die "case $c ($klass/$band): anchor id resolves to $n nodes, want exactly 1: $id"
    verifications="$(jq -c --arg id "$id" --arg n "$n" --argjson c "$c" \
      '. + [{"spec_case": $c, "check": "anchor_id_resolves", "value": $id, "matches": ($n|tonumber)}]' <<<"$verifications")"
    echo "  case $c ($klass/$band): anchor id resolves x$n"
  done < <(jq -r ".cases[$c] | [.anchor_positive_key] + (.anchor_negatives // []) | .[] | select(. != null)" "$spec")

  while IFS= read -r term; do
    [[ -n "$term" ]] || continue
    reject_unsafe "$term"
    lower="$(printf '%s' "$term" | tr '[:upper:]' '[:lower:]')"
    # aliases is a LIST property, not a string -- coalesce(n.aliases,'')
    # makes FalkorDB raise "Type mismatch: expected String or Null but was
    # List" and the query returns no count at all. any(... IN ...) is the
    # list form. Both spellings were run against the live graph: the string
    # form errored, this one returns a count, and a positive control
    # ('health' -> 32299) proves the predicate is not silently matching
    # nothing.
    n="$(falkor_count "MATCH (n:Subject) WHERE toLower(coalesce(n.label,'')) CONTAINS '$lower' OR toLower(coalesce(n.search_text,'')) CONTAINS '$lower' OR any(a IN coalesce(n.aliases,[]) WHERE toLower(a) CONTAINS '$lower') RETURN count(n)")"
    [[ "$n" == "0" ]] || die "case $c ($klass/$band): claims no lexical candidate for '$term' but the graph has $n"
    verifications="$(jq -c --arg t "$term" --argjson c "$c" \
      '. + [{"spec_case": $c, "check": "term_absent", "value": $t, "matches": 0}]' <<<"$verifications")"
    echo "  case $c ($klass/$band): term absent (0 matches)"
  done < <(jq -r ".cases[$c].absent_terms // [] | .[]" "$spec")

  # A census case's row_count_expectation is itself a live claim: the
  # registry must actually hold the number of members of that kind the
  # expectation names. "one_or_more" is proven by a non-zero count.
  census_kind="$(jq -r ".cases[$c].census.kind // empty" "$spec")"
  if [[ -n "$census_kind" ]]; then
    expectation="$(jq -r ".cases[$c].census.row_count_expectation" "$spec")"
    reject_unsafe "$census_kind"
    n="$(falkor_count "MATCH (n:Subject) WHERE n.subject_kind = '$census_kind' RETURN count(n)")"
    case "$expectation" in
      one_or_more)        [[ "$n" -ge 1 ]] || die "case $c: census expects one_or_more $census_kind, graph has $n" ;;
      multiple_claimants) [[ "$n" -ge 2 ]] || die "case $c: census expects multiple_claimants $census_kind, graph has $n" ;;
      zero)               [[ "$n" -eq 0 ]] || die "case $c: census expects zero $census_kind, graph has $n" ;;
      *) die "case $c: unknown row_count_expectation: $expectation" ;;
    esac
    verifications="$(jq -c --arg k "$census_kind" --arg e "$expectation" --arg n "$n" --argjson c "$c" \
      '. + [{"spec_case": $c, "check": "census_row_count", "value": ($k + ":" + $e), "matches": ($n|tonumber)}]' <<<"$verifications")"
    echo "  case $c ($klass/$band): census $census_kind $expectation -> $n live members"
  fi
done

if [[ "$dry_run" == "1" ]]; then
  echo "seed-corpus-cases.sh: --dry-run -- every oracle claim proved; nothing written"
  exit 0
fi

# ----------------------------------------------------------------- write
base_index="$(jq -r 'length' "$corpus")"
annex_max="$(jq -r '[.cases | keys[] | tonumber] | max' "$annex")"
[[ "$base_index" -eq $((annex_max + 1)) ]] || \
  die "corpus has $base_index cases but the annex's highest index is $annex_max -- refusing to append to a corpus/annex pair that is already out of step"

# Corpus rows carry the question ONLY. expect_kind/expect_id are left empty
# on purpose: cmd/acr-corpus-annex-sync fills them from the annex below,
# which is the tool of record for that field pair (CHAOS-4348). Writing
# them here would reintroduce exactly the two-copies drift that tool exists
# to close. subject_terms is deliberately omitted -- the harness's own
# trialCase reads question/expect_kind/expect_id and nothing else
# (internal/runtime/hosted/generative_trial_live_test.go:431-435), and the
# ext65 rows' subject_terms came from an Interpret() pass whose output is
# model-authored per run (cf-measurement-trials, 2026-08-23 10:35), so a
# regenerated value would not be reproducible.
corpus_tmp="$(mktemp)"; annex_tmp="$(mktemp)"
trap 'rm -f "$corpus_tmp" "$annex_tmp"' EXIT

jq --slurpfile spec "$spec" '
  . + ($spec[0].cases | map({question: .question, expect_kind: "", expect_id: ""}))
' "$corpus" > "$corpus_tmp"

jq --slurpfile spec "$spec" \
   --argjson base "$base_index" \
   --argjson verifications "$verifications" \
   --arg graph "$graph_key" \
   --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
  def build($c):
    {
      question_class: $c.question_class,
      band: $c.band,
      oracles: ({
        kind:   { positive: $c.kind_positive, negatives: ($c.kind_negatives // []) },
        anchor: { positive_key: $c.anchor_positive_key, negatives: ($c.anchor_negatives // []) },
        window: { positive_band: $c.window_positive_band, negatives: ($c.window_negatives // []) },
        handle: { positive: null, negatives: [] }
      } + (if $c.census then { census: $c.census } else {} end)),
      committable_negative_designations: ($c.committable_negative_designations // []),
      authority: $c.authority,
      kind_basis: $c.kind_basis,
      anchor_basis: $c.anchor_basis,
      baseline: $c.baseline
    };
  .cases += ( $spec[0].cases
              | to_entries
              | map({ key: (($base + .key) | tostring), value: build(.value) })
              | from_entries )
  | .provenance.chaos4525_seed_derivation =
      ((.provenance.chaos4525_seed_derivation // []) + [{
        tool: "scripts/trial/seed-corpus-cases.sh",
        ticket: "CHAOS-4525",
        applied_at_utc: $at,
        method: "live_graph_readonly_query",
        graph_key: $graph,
        indices_added: [ $spec[0].cases | to_entries[] | ($base + .key) ],
        verifications: $verifications
      }])
' "$annex" > "$annex_tmp"

jq empty "$corpus_tmp" && jq empty "$annex_tmp"
cp "$corpus_tmp" "$corpus"
cp "$annex_tmp" "$annex"

added_last=$((base_index + case_count - 1))
echo "seed-corpus-cases.sh: appended corpus + annex cases at indices ${base_index}..${added_last}"

# ------------------------------------------------------- mechanical sync
echo "seed-corpus-cases.sh: handing expect_kind/expect_id to cmd/acr-corpus-annex-sync"
( cd "$repo_root" && go run ./cmd/acr-corpus-annex-sync -annex "$annex" -corpus "$corpus" )

new_sha8="$(shasum -a 256 "$corpus" | cut -c1-8)"
pinned="$(jq -r '.provenance.corpus_sha8' "$annex")"
[[ "$new_sha8" == "$pinned" ]] || die "post-sync hash guard would FAIL: corpus sha8 $new_sha8 != annex pin $pinned"
echo "seed-corpus-cases.sh: hash guard satisfied -- corpus sha8 $new_sha8 == annex provenance.corpus_sha8"
echo "seed-corpus-cases.sh: signoff.approved_corpus_sha8 = $(jq -r '.provenance.signoff.approved_corpus_sha8 // "(unset)"' "$annex") -- chris re-ratification required before this pair carries a verdict"
