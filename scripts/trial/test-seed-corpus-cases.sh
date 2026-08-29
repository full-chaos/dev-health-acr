#!/usr/bin/env bash
# Guard suite for scripts/trial/seed-corpus-cases.sh (CHAOS-4525), covering the
# three findings codex raised on PR #330 that are testable without a live graph.
#
# The producer writes to artifacts several lanes read, so its failure modes
# matter more than its happy path: every check below is about what happens when
# something goes WRONG, and every one of them asserts that the ORIGINALS were
# left untouched.
#
# The live graph is stubbed through ACR_TRIAL_SEED_FALKOR_BIN (a fake kubectl),
# the same testability-hook shape ACR_TRIAL_KIAC_DSN_BIN already uses in
# common.sh -- never a reimplementation of the script's own logic.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repo_root="$(cd "$script_dir/../.." && pwd -P)"
tmp="$(mktemp -d)"
trap '[[ -n "${KEEP_TMP:-}" ]] || rm -rf "$tmp"' EXIT
[[ -n "${KEEP_TMP:-}" ]] && echo "tmp=$tmp"

# jq is the producer's own hard dependency; without it there is nothing to
# gate here. Skip cleanly rather than failing a CI runner that has no jq --
# the same posture the producer-pin section of test-kiac-dsn-reader.sh takes
# for its own live-cluster requirement.
if ! command -v jq >/dev/null 2>&1; then
  echo "skip: seed-corpus-cases checks (jq not installed)"
  exit 0
fi

failures=0
check() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" != "$want" ]]; then
    echo "FAIL: $label" >&2
    echo "  want: $want" >&2
    echo "  got:  $got" >&2
    failures=$((failures + 1))
  else
    echo "ok: $label"
  fi
}

# fake_kubectl writes a kubectl stand-in whose `exec ... redis-cli GRAPH.QUERY`
# prints the two-line raw shape FalkorDB actually returns (header, then the
# count), and whose `get pods` prints one pod name.
# fake_kubectl writes a kubectl stand-in whose `exec ... redis-cli GRAPH.QUERY`
# prints the two-line raw shape FalkorDB actually returns (header, then a
# count), and whose `get pods` prints one pod name. Every query is appended to
# queries.log so a check can assert on the Cypher the script EMITTED, not only
# on what it did with the answer.
#
# The org-purity query is answered separately (default 0 = a clean,
# single-org graph) so a check can drive the "this graph key belongs to
# another organization" refusal without disturbing the per-case counts.
fake_kubectl() {
  local count="$1" foreign="${2:-0}" kind="${3:-team}"
  printf '%s\n' "$count" >"$tmp/fake-count.txt"
  printf '%s\n' "$foreign" >"$tmp/fake-foreign.txt"
  printf '%s\n' "$kind" >"$tmp/fake-kind.txt"
  cat >"$tmp/fake-kubectl" <<FAKE
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$tmp/queries.log"
for a in "\$@"; do
  if [[ "\$a" == "pods" ]]; then echo "pod/fake-falkordb-0"; exit 0; fi
done
if printf '%s' "\$*" | grep -q 'RETURN n.subject_kind'; then
  echo "n.subject_kind"
  cat "$tmp/fake-kind.txt"
  exit 0
fi
echo "count(n)"
if printf '%s' "\$*" | grep -q 'org_id IS NULL OR n.org_id <>'; then
  cat "$tmp/fake-foreign.txt"
else
  cat "$tmp/fake-count.txt"
fi
FAKE
  chmod +x "$tmp/fake-kubectl"
}

fixture() {
  local spec_cases="$1"
  cp "$tmp/corpus.orig.json" "$tmp/corpus.json"
  cp "$tmp/annex.orig.json" "$tmp/annex.json"
  printf '%s\n' "{\"graph_key\":\"g\",\"org_id\":\"org-1\",\"cases\":$spec_cases}" >"$tmp/spec.json"
}

printf '%s\n' '[{"question":"fixture, never real corpus text","expect_kind":"team","expect_id":""}]' >"$tmp/corpus.orig.json"
printf '%s\n' '{"cases":{"0":{"question_class":"cohort_assessment"}},"provenance":{"org_id":"org-1","corpus_sha8":"aaaaaaaa"}}' >"$tmp/annex.orig.json"

originals_untouched() {
  if cmp -s "$tmp/corpus.orig.json" "$tmp/corpus.json" && cmp -s "$tmp/annex.orig.json" "$tmp/annex.json"; then
    echo "untouched"
  else
    echo "MODIFIED"
  fi
}

run_seed() {
  ( cd "$repo_root" && PATH="$tmp:$PATH" ACR_TRIAL_SEED_FALKOR_BIN="$tmp/fake-kubectl" \
      "$script_dir/seed-corpus-cases.sh" --spec "$tmp/spec.json" --corpus "$tmp/corpus.json" \
      --annex "$tmp/annex.json" "$@" ) >"$tmp/out.log" 2>&1 && echo "exit=0" || echo "exit=$?"
}

# 1. CHAOS-4525 / codex P2: a no_match case with NO absent_terms must be
#    REFUSED. Before the fix the `// []` fallback ran zero checks and the
#    script reported that every oracle claim was proved.
fake_kubectl 0
fixture '[{"question":"q","question_class":"existence_probe","band":"no_match","kind_positive":"project","kind_negatives":[],"anchor_positive_key":null,"anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":null,"authority":"derived","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
out="$(run_seed)"
check "no_match case without absent_terms is refused" "exit=1" "$out"
check "  ... and says why" "1" "$(grep -c 'MUST carry a non-empty absent_terms' "$tmp/out.log" || true)"
check "  ... and leaves the originals untouched" "untouched" "$(originals_untouched)"

# 2. Same, for a no_match case whose absent_terms is a STRING rather than an
#    array -- a plausible hand-edit that would otherwise iterate as one line
#    and read as satisfied.
fixture '[{"question":"q","question_class":"existence_probe","band":"no_match","absent_terms":"warehouse","kind_positive":"project","kind_negatives":[],"anchor_positive_key":null,"anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":null,"authority":"derived","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
out="$(run_seed)"
check "no_match case with a STRING absent_terms is refused" "exit=1" "$out"
check "  ... and leaves the originals untouched" "untouched" "$(originals_untouched)"

# 3. A census expectation the live graph contradicts must abort before any
#    write (the general fail-closed property).
fake_kubectl 0
fixture '[{"question":"q","question_class":"cohort_assessment","band":"paraphrase","kind_positive":"team","kind_negatives":[],"anchor_positive_key":null,"anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":{"must_run":true,"kind":"team","row_count_expectation":"one_or_more","terminal_expectation":"aggregate_assessment","commit_expectation":"never"},"authority":"annotation","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
out="$(run_seed)"
check "census expectation contradicted by the graph is refused" "exit=1" "$out"
check "  ... and leaves the originals untouched" "untouched" "$(originals_untouched)"

# 4. CHAOS-4525 / codex P2: a FAILING sync step must leave the originals
#    untouched. Before the fix the temporaries were installed first, so a
#    failure here published a corpus with empty expect_kind/expect_id
#    alongside an annex still pinning the old hash -- two shared artifacts
#    inconsistent with each other and with every backup.
printf '%s\n' '#!/usr/bin/env bash' 'exit 3' >"$tmp/failing-sync"
chmod +x "$tmp/failing-sync"
fake_kubectl 3
fixture '[{"question":"q","question_class":"cohort_assessment","band":"paraphrase","kind_positive":"team","kind_negatives":[],"anchor_positive_key":null,"anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":{"must_run":true,"kind":"team","row_count_expectation":"one_or_more","terminal_expectation":"aggregate_assessment","commit_expectation":"never"},"authority":"annotation","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
out="$(ACR_SEED_SYNC_CMD="$tmp/failing-sync" run_seed)"
check "a failing sync step aborts" "exit=1" "$out"
check "  ... and says the originals were left untouched" "1" "$(grep -c 'ORIGINALS LEFT UNTOUCHED' "$tmp/out.log" || true)"
check "  ... and the originals really are untouched" "untouched" "$(originals_untouched)"

# 5. --dry-run proves the claims and writes nothing.
fake_kubectl 3
out="$(run_seed --dry-run)"
check "--dry-run succeeds on a provable spec" "exit=0" "$out"
check "  ... and writes nothing" "untouched" "$(originals_untouched)"

# 6. CHAOS-4525 / codex P2: the proof must be scoped to the organization the
#    spec names. Implemented as ONE up-front org-purity query rather than a
#    filter on every per-case query -- stronger (it proves the graph key holds
#    nothing but that org's subjects, catching a mixed-tenancy graph a
#    per-query filter would tolerate) and dramatically cheaper (the filtered
#    three-way CONTAINS scan did not return within seven minutes on the live
#    36k-subject graph; this guard costs ~4ms).
rm -f "$tmp/queries.log"
fake_kubectl 1 0
fixture '[{"question":"q","question_class":"cohort_assessment","band":"paraphrase","kind_positive":"team","kind_negatives":[],"anchor_positive_key":"team:CHAOS","anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":{"must_run":true,"kind":"team","row_count_expectation":"one_or_more","terminal_expectation":"aggregate_assessment","commit_expectation":"never"},"authority":"annotation","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
run_seed --dry-run >/dev/null
check "an org-purity query is emitted before any per-case claim" "1" \
  "$(grep -c "n.org_id <> 'org-1'" "$tmp/queries.log" 2>/dev/null || echo 0)"

# 7. A graph key holding ANOTHER organization's subjects must be refused
#    outright -- this is the finding's own scenario ("the spec accidentally
#    names another organization's graph key").
fake_kubectl 1 4
fixture '[{"question":"q","question_class":"cohort_assessment","band":"paraphrase","kind_positive":"team","kind_negatives":[],"anchor_positive_key":"team:CHAOS","anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":{"must_run":true,"kind":"team","row_count_expectation":"one_or_more","terminal_expectation":"aggregate_assessment","commit_expectation":"never"},"authority":"annotation","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
out="$(run_seed --dry-run)"
check "a graph key holding foreign-org subjects is refused" "exit=1" "$out"
check "  ... and names the count and both ids" "1" "$(grep -c 'belonging to an organization other than org-1' "$tmp/out.log" || true)"
check "  ... and leaves the originals untouched" "untouched" "$(originals_untouched)"

# 8. CHAOS-4525 / codex R2 P2: the census block is hand-authored, and exactly
#    one of its values decides whether the case is measurable -- the harness
#    admits a cohort case into the answer-rate denominator on the exact string
#    "aggregate_assessment". A typo is silent AND total: the case is written,
#    the script reports success, and the row never enters the denominator.
census_case() {
  printf '%s' '[{"question":"q","question_class":"cohort_assessment","band":"paraphrase","kind_positive":"team","kind_negatives":[],"anchor_positive_key":null,"anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":{"must_run":'"$3"',"kind":"team","row_count_expectation":"one_or_more","terminal_expectation":"'"$1"'","commit_expectation":"'"$2"'"},"authority":"annotation","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
}

fake_kubectl 3 0
fixture "$(census_case aggregate_assessmnt never true)"
out="$(run_seed --dry-run)"
check "a MISSPELLED census terminal_expectation is refused" "exit=1" "$out"
check "  ... and names the closed vocabulary" "1" "$(grep -c 'closed vocabulary' "$tmp/out.log" || true)"

fixture "$(census_case '' never true)"
check "a MISSING census terminal_expectation is refused" "exit=1" "$(run_seed --dry-run)"

fixture "$(census_case aggregate_assessment always true)"
check "a census commit_expectation other than never is refused" "exit=1" "$(run_seed --dry-run)"

fixture "$(census_case aggregate_assessment never false)"
check "a census must_run of false is refused" "exit=1" "$(run_seed --dry-run)"

for te in aggregate_assessment witnessed_no_match clarification_required; do
  fixture "$(census_case "$te" never true)"
  check "the closed vocabulary value $te is accepted" "exit=0" "$(run_seed --dry-run)"
done

# 9. CHAOS-4525 / codex R2 P2: installation is two `cp`s and is therefore not
#    atomic. A failure of the SECOND one must not leave the corpus replaced
#    beside an annex still pinning the old hash -- the exact inconsistent
#    state the temporary validation exists to prevent. Simulated by making the
#    annex read-only so its copy fails after the corpus copy has succeeded.
fake_kubectl 3 0
fixture "$(census_case aggregate_assessment never true)"
# A minimal stand-in for cmd/acr-corpus-annex-sync: it does the one thing the
# hash guard downstream depends on -- repin the annex to the corpus it was
# handed -- so the run actually REACHES the installation step this check is
# about. A no-op stub aborts at the guard instead and would make this check
# vacuously pass.
sync_stub="$tmp/repin-sync"
cat >"$sync_stub" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
while [[ $# -gt 0 ]]; do
  case "$1" in
    -annex) a="$2"; shift 2 ;;
    -corpus) k="$2"; shift 2 ;;
    *) shift ;;
  esac
done
sha8="$(shasum -a 256 "$k" | cut -c1-8)"
jq --arg s "$sha8" '.provenance.corpus_sha8 = $s' "$a" >"$a.stub" && mv "$a.stub" "$a"
# The real tool writes its audit record beside the corpus it was HANDED.
printf '%s\n' '[{"stub":"audit"}]' >"$k.sync-audit.json"
STUB
chmod +x "$sync_stub"
chmod 0444 "$tmp/annex.json"
out="$(ACR_SEED_SYNC_CMD="$sync_stub" run_seed)"
chmod 0644 "$tmp/annex.json"
check "a failed annex install rolls the corpus back" "untouched" "$(originals_untouched)"
check "  ... and says both files were rolled back" "1" "$(grep -c 'rolled both files back' "$tmp/out.log" || true)"

# 10. CHAOS-4525 / codex R3 P2: the purity guard must treat a MISSING org_id
#     as foreign. In Cypher `n.org_id <> '<org>'` against a missing property
#     evaluates to null, so such a node is excluded from the count and the
#     guard reports a clean graph -- while the unscoped anchor/census queries
#     can still use it to certify an oracle production cannot retrieve.
rm -f "$tmp/queries.log"
fake_kubectl 1 0
fixture "$(census_case aggregate_assessment never true)"
run_seed --dry-run >/dev/null
check "the purity guard counts a MISSING org_id as foreign" "1" \
  "$(grep -c "n.org_id IS NULL OR n.org_id <> 'org-1'" "$tmp/queries.log" 2>/dev/null || echo 0)"

# 11. CHAOS-4525 / codex R3 P2: existence is not agreement. A kind_positive
#     that disagrees with the anchor's live subject_kind publishes an
#     impossible pair which still passes sync and the hash guard.
kind_case() {
  printf '%s' '[{"question":"q","question_class":"subject_status","band":"literal","kind_positive":"'"$1"'","kind_negatives":[],"anchor_positive_key":"team:CHAOS","anchor_negatives":[],"window_positive_band":"all_time","window_negatives":[],"census":null,"authority":"annotation","kind_basis":"x","anchor_basis":"x","baseline":{}}]'
}
fake_kubectl 1 0 team
fixture "$(kind_case project)"
out="$(run_seed --dry-run)"
check "kind_positive disagreeing with the anchor's live subject_kind is refused" "exit=1" "$out"
check "  ... and names both kinds" "1" "$(grep -c 'impossible kind/anchor pair' "$tmp/out.log" || true)"

fixture "$(kind_case team)"
check "kind_positive matching the live subject_kind is accepted" "exit=0" "$(run_seed --dry-run)"

# 12. CHAOS-4525 / codex R3 P2: an INTERIOR annex gap passes a max-based
#     check. The sync tool skips the missing index and the harness measures
#     from annex entries only, so that corpus row is published unmeasured.
cat >"$tmp/corpus.orig.json" <<'GAPC'
[{"question":"a","expect_kind":"","expect_id":""},
 {"question":"b","expect_kind":"","expect_id":""},
 {"question":"c","expect_kind":"","expect_id":""}]
GAPC
printf '%s\n' '{"cases":{"0":{"question_class":"x"},"2":{"question_class":"x"}},"provenance":{"org_id":"org-1","corpus_sha8":"aaaaaaaa"}}' >"$tmp/annex.orig.json"
fake_kubectl 1 0 team
fixture "$(kind_case team)"
out="$(run_seed)"
check "an annex with an INTERIOR index gap is refused" "exit=1" "$out"
check "  ... and names the missing index" "1" "$(grep -c 'has no case at corpus index(es) 1' "$tmp/out.log" || true)"
check "  ... and leaves the originals untouched" "untouched" "$(originals_untouched)"

# 13. CHAOS-4525 / codex R3 P2: the sync tool writes its audit beside the
#     corpus it was handed -- the TEMPORARY. Without an explicit merge the
#     canonical corpus's audit history never records these corrections and a
#     stray file is left behind.
# Check 12 replaced the base fixtures with its gapped pair; restore the
# single-row pair so this check exercises a SUCCESSFUL install.
printf '%s\n' '[{"question":"fixture, never real corpus text","expect_kind":"team","expect_id":""}]' >"$tmp/corpus.orig.json"
printf '%s\n' '{"cases":{"0":{"question_class":"cohort_assessment"}},"provenance":{"org_id":"org-1","corpus_sha8":"aaaaaaaa"}}' >"$tmp/annex.orig.json"
fake_kubectl 1 0 team
fixture "$(kind_case team)"
rm -f "$tmp/corpus.json.sync-audit.json"
printf '%s\n' '[{"prior":"entry"}]' >"$tmp/corpus.json.sync-audit.json"
out="$(ACR_SEED_SYNC_CMD="$sync_stub" run_seed)"
check "a successful install merges the sync audit into the canonical sibling" "2" \
  "$(jq -r 'length' "$tmp/corpus.json.sync-audit.json" 2>/dev/null || echo 0)"
check "  ... appending, never overwriting the prior history" "entry" \
  "$(jq -r '.[0].prior // "MISSING"' "$tmp/corpus.json.sync-audit.json" 2>/dev/null || echo MISSING)"

if [[ "$failures" -gt 0 ]]; then
  echo "seed-corpus-cases checks FAILED ($failures)" >&2
  exit 1
fi
echo "seed-corpus-cases checks passed"
