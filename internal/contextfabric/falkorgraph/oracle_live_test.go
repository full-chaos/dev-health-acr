package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// embedQueryTerms embeds terms through the SAME query-prefixing path
// production uses (Adapter.queryPrefixed, vector.go's hybridSearchNodes) --
// codex round-5 FIX A: this is the single authority for query-side
// prefixing; the oracle harness must never re-derive it or skip it, or a
// report stamped with a prefixed identity (e.g. ACR_TEST_EMBED_PREFIX_FAMILY=nomic)
// would measure a DIFFERENT query space than what production actually
// embeds -- tau/K calibrated against unprefixed queries while production
// queries prefixed ones. Extracted as its own method (not inlined at the
// call site) so it is unit-testable with a fake embedder, without a live
// embedder/graph connection -- see TestEmbedQueryTerms_AppliesTheSameQueryPrefixProductionUses.
// CHAOS-3829 codex r1 F2 (accepted): production's hybridSearchNodes embeds
// a.queryPrefixed(vectorQueryText(term)) -- CHAOS-3838's domain-lexicon
// widening runs BEFORE the query prefix, not merely alongside it. This
// function previously embedded a.queryPrefixed(term) directly, skipping
// vectorQueryText entirely, so every similarity this harness ever measured
// for a lexicon-matched term was computed against a DIFFERENT embedding
// than what production actually embeds for that same term -- silently
// invalidating any calibration derived from it. vectorQueryText is a no-op
// for a term the lexicon has no opinion about (lexicon.go's own doc
// comment), so this is a behavior change ONLY for lexicon-matched terms --
// which is the whole point of routing through the SAME production
// text-composition authority rather than a second, driftable copy of it.
func (a *Adapter) embedQueryTerms(ctx context.Context, terms []string) ([][]float32, error) {
	prefixed := make([]string, len(terms))
	for i, term := range terms {
		prefixed[i] = a.queryPrefixed(vectorQueryText(term))
	}
	return a.embedder.Embed(ctx, prefixed)
}

// TestExactSearchOracleDecomposesRetrievalMisses is the CHAOS-3831 (embed-text
// spec §5 L1 / §6 T1) exact-search oracle measurement.
//
// Reuses loadAmbiguityCorpus and benchmarkLookup from
// ambiguity_benchmark_live_test.go, so it shares the exact same corpus format
// (including the L1 subject_terms parity field), the exact same dedicated
// ACR_TEST_* configuration surface, and never touches a production
// ACR_CONTEXT_FABRIC_* name.
//
//	ACR_TEST_AMBIGUITY_CORPUS=/path/to/corpus.json \
//	ACR_TEST_AMBIGUITY_ORG=<org-id> \
//	ACR_TEST_FALKOR_ADDR=host:port \
//	ACR_TEST_EMBED_BASE_URL=... ACR_TEST_EMBED_MODEL=... ACR_TEST_EMBED_DIMENSION=... \
//	[ACR_TEST_EMBED_API_KEY=...] \
//	[ACR_TEST_EMBED_TIMEOUT=45s] [ACR_TEST_EMBED_MAX_TRANSPORT_RETRIES=5] \
//	[ACR_TEST_EMBED_MAX_BATCH=...] [ACR_TEST_EMBED_MAX_TEXT_RUNES=...] \
//	[ACR_TEST_EMBED_PREFIX_FAMILY=...] [ACR_TEST_EMBED_EXPECT_RESPONSE_MODEL=...] \
//	[ACR_TEST_EMBED_PROVIDER_LOCALITY=local|remote] [ACR_TEST_EMBED_INCLUDE_BODIES=true|false] \
//	[ACR_TEST_ORACLE_TOPK=20] [ACR_TEST_ORACLE_HARD_NEGATIVES=5] [ACR_TEST_ORACLE_OUTPUT=/path/to/report.json] \
//	[ACR_TEST_ORACLE_INCLUDE_RAW_TEXT=false] \
//	  go test ./internal/contextfabric/falkorgraph -run ExactSearchOracle -v
//
// ACR_TEST_EMBED_API_KEY is OPTIONAL (see benchmarkLookup): keyless local
// embedders remain supported; set it only to reach a real remote embedder
// that requires a credential.
//
// ACR_TEST_EMBED_TIMEOUT and ACR_TEST_EMBED_MAX_TRANSPORT_RETRIES are also
// OPTIONAL (CHAOS-3849 round 2, see benchmarkLookup): unset, both fall
// through to embedprovider's loopback-tuned defaults (250ms / 0 retries),
// which are too tight for a real remote embedder call -- set both (production
// runs remote embedders at 45s / 5 retries) or the oracle's own embed calls
// fail with "context deadline exceeded" against real network latency.
//
// ACR_TEST_EMBED_MAX_BATCH, ACR_TEST_EMBED_MAX_TEXT_RUNES,
// ACR_TEST_EMBED_PREFIX_FAMILY, ACR_TEST_EMBED_EXPECT_RESPONSE_MODEL,
// ACR_TEST_EMBED_PROVIDER_LOCALITY, and ACR_TEST_EMBED_INCLUDE_BODIES are
// also OPTIONAL (CHAOS-3849 round 3, review finding 3, see benchmarkLookup):
// each maps to its production embedprovider.Env* counterpart and left unset
// falls through to embedprovider's own default. PREFIX_FAMILY and
// INCLUDE_BODIES are SEMANTIC, identity-bearing (CHAOS-3833/3836)
// configuration -- an oracle run against a production deployment that sets
// either needs the SAME value here, or fetchEmbedderFenceCorpus's stamped
// identity predicate will not match what that deployment actually wrote,
// and the corpus reads empty for reasons that have nothing to do with
// retrieval quality.
//
// PRECONDITION, checked once before any case is scored (codex round-1 finding
// 1): the ORG-LEVEL AC-3778-7 fence (ensureVectorReadable) must pass. That
// fence is what production's OWN read path checks before it will run vector
// retrieval AT ALL for this organization -- a single stale-identity vector
// anywhere in the org degrades production to lexical-only for every question,
// not just the one row. This test calls vectorSearchNodes directly (bypassing
// hybridSearchNodes' own fence check) precisely to isolate the vector
// mechanism from the lexical one, so it must replicate that org-level gate
// itself rather than silently measuring a mechanism production isn't even
// running. A failed fence is a hard failure, not a per-case degradation: it
// would be dishonest to score any case at all under it.
//
// What it measures, per corpus case that names a correct subject (a no-match
// control -- ExpectID=="" -- has nothing for an oracle to rank against, so
// controls are counted but not scored):
//
//   - subject_missing: no Subject node exists for (expect_kind, expect_id) at
//     all. A corpus-authoring error (wrong expected ID), not a retrieval
//     defect of any kind -- codex round-1 finding 4 split this out of the
//     single "no_vector" bucket because the two need entirely different
//     fixes (fix the corpus vs fix the projection).
//   - vector_missing: the subject exists but carries no usable embedding
//     under the current identity -- a PROJECTION coverage gap. No amount of
//     ANN tuning or text enrichment can find a vector that was never
//     written.
//   - gated: every one of the case's subject terms fails
//     hasLexicalContent (codex round-1 finding 5) -- production's
//     hybridSearchNodes skips BOTH mechanisms for such a term without ever
//     calling the embedder, so there is nothing for either side to have
//     found. Distinct from text_loss: this is a property of the QUESTION's
//     terms, not of embedding quality.
//   - floor_loss: the correct answer's best RAW similarity (across the
//     case's active terms) does not exceed tau (the configured similarity
//     floor) for even one term. This is the AC-3778-4 floor working exactly
//     as designed -- a deliberate policy rejection, not a text or ANN
//     defect (codex round-1 finding 2: without this bucket, an
//     intentionally-floor-rejected correct answer reads as ann_loss and
//     sends T2 chasing HNSW parameters for policy behavior).
//   - text_loss: the correct answer clears tau on some term, but the
//     oracle's OWN floor-filtered, tie-inclusive top-K (topKInclusive over
//     aboveFloor) does not contain it for any term -- other floor-passing
//     candidates rank ahead of it. An embed-TEXT problem (T3's territory).
//   - ann_loss: the oracle's floor-filtered top-K contains it, but
//     production's own vectorSearchNodes (same floor, same query, same
//     index) does not, for every term. An ANN-parameter problem (T2's
//     territory: efRuntime, over-fetch).
//   - hit: both find it.
//
// Per-kind S+/S- distributions and hard negatives are recorded only for
// cases that reached term-level evaluation (cause is floor_loss, text_loss,
// ann_loss, or hit): S+ is the best (max over the case's active terms) RAW
// true-cosine similarity between the query and the correct answer -- NOT
// floor-filtered, because L4 needs to see how many correct pairs the current
// floor discards; S- is the best (max) RAW true-cosine similarity between
// the query and any OTHER corpus member ("best imposter"), likewise
// unfiltered. Both feed L4's per-identity tau calibration and hard-negative
// mining directly; this test only harvests, it does not calibrate anything.
//
// PII (codex round-1 finding 8): corpus question text and graph subject
// labels are free text that can carry names, emails, or other
// person-identifying content (embed-text spec §3). Both are REDACTED to a
// stable, non-reversible digest in the written report by default -- set
// ACR_TEST_ORACLE_INCLUDE_RAW_TEXT=true to opt into raw text when a
// measurement genuinely needs it to be human-readable. oracleReport.RawTextIncluded
// records which mode produced the file. Kind and canonical ID are NOT
// redacted -- they are closed structural identifiers (e.g.
// "linear:CHAOS-100"), not free text.
func TestExactSearchOracleDecomposesRetrievalMisses(t *testing.T) {
	corpus := loadAmbiguityCorpus(t)
	address := os.Getenv("ACR_TEST_FALKOR_ADDR")
	if address == "" {
		t.Skip("ACR_TEST_FALKOR_ADDR is not set; the oracle measures against live data")
	}
	orgID := os.Getenv("ACR_TEST_AMBIGUITY_ORG")
	if orgID == "" {
		t.Skip("ACR_TEST_AMBIGUITY_ORG is not set")
	}
	ctx := context.Background()

	graphConfig, err := ConfigFromEnv(benchmarkLookup)
	if err != nil {
		t.Fatalf("graph configuration: %v", err)
	}
	embedderOptions, err := EmbedderFromEnv(benchmarkLookup)
	if err != nil {
		t.Fatalf("embedder configuration: %v", err)
	}
	if embedderOptions.Embedder == nil {
		// A hard failure, not a skip: an oracle with no embedder has neither
		// a query vector to rank nor an identity to fence the corpus by.
		t.Fatal("ACR_TEST_EMBED_BASE_URL is not set; the exact-search oracle needs a configured embedder")
	}
	adapter, err := NewWithEmbedder(graphConfig, embedderOptions)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("context: %v", err)
	}

	testLookup := func(name string) (string, bool) {
		v := os.Getenv(name)
		return v, v != ""
	}
	// Codex round-1 finding 6: normalized IDENTICALLY to vectorSearchNodes'
	// own limit handling (vector.go: `if limit <= 0 || limit > MaxResults`),
	// not merely capped on one side -- an unvalidated 0 would silently read
	// as "empty oracle recall" while ANN treats 0 the same as "unset" and
	// falls back to MaxResults, and a negative value would panic ranked[:k].
	topK, err := envInt(testLookup, "ACR_TEST_ORACLE_TOPK", 20)
	if err != nil {
		t.Fatalf("ACR_TEST_ORACLE_TOPK: %v", err)
	}
	if topK <= 0 || topK > graphConfig.MaxResults {
		t.Logf("ACR_TEST_ORACLE_TOPK=%d is <= 0 or exceeds MaxResults=%d; normalizing both sides to %d (matching vectorSearchNodes' own limit clamp)", topK, graphConfig.MaxResults, graphConfig.MaxResults)
		topK = graphConfig.MaxResults
	}
	hardNegativeCount, err := envInt(testLookup, "ACR_TEST_ORACLE_HARD_NEGATIVES", 5)
	if err != nil {
		t.Fatalf("ACR_TEST_ORACLE_HARD_NEGATIVES: %v", err)
	}
	if hardNegativeCount < 0 {
		t.Logf("ACR_TEST_ORACLE_HARD_NEGATIVES=%d is negative; defaulting to 5", hardNegativeCount)
		hardNegativeCount = 5
	}
	includeRawText := envBool(testLookup, "ACR_TEST_ORACLE_INCLUDE_RAW_TEXT", false)

	key := graphKey(graphConfig.GraphPrefix, orgID)

	// Codex round-1 finding 1: the ORG-LEVEL fence, checked ONCE before any
	// case is scored. See the doc comment above for why this must be a hard
	// failure rather than a per-case degradation.
	if !adapter.ensureVectorReadable(ctx, key, orgID) {
		t.Fatalf("the AC-3778-7 org-level vector read fence did not pass for org %s: production would be lexical-only for EVERY question against this org right now (a stale-identity vector, an unusable index, or an unverifiable fence), so an oracle-vs-ANN decomposition here would compare against a mechanism production is not actually running. Rebuild the org (acr-projector rebuild --org) or fix the index before running this measurement", orgID)
	}

	corpusVectors, err := adapter.fetchEmbedderFenceCorpus(ctx, key, orgID)
	if err != nil {
		t.Fatalf("fetch embedder-fence-passing corpus: %v", err)
	}
	if len(corpusVectors) == 0 {
		t.Fatal("the embedder-fence-passing corpus is empty; there is nothing for the oracle to rank against (has acr-projector run with this embedder configured?)")
	}
	t.Logf("oracle corpus: %d embedder-fence-passing subject vectors for org %s", len(corpusVectors), orgID)

	tau := adapter.similarityFloor
	// codex round-4 FIX A: stamp the SAME identity string
	// LookupRetrievalPolicy's caller composes (EmbedRetrievalIdentityFromEnv,
	// not EmbedderIdentity.String() alone -- that form excludes the
	// composition tag) plus the dimension the embedder that produced these
	// similarities actually reports, so CalibrateFromReport can refuse a
	// report minted against the wrong embedding space before trusting
	// anything else in it.
	embedIdentity, err := EmbedRetrievalIdentityFromEnv(benchmarkLookup)
	if err != nil {
		t.Fatalf("embed retrieval identity: %v", err)
	}
	report := &oracleReport{
		Total: len(corpus), TopK: topK, Tau: tau, RawTextIncluded: includeRawText,
		EmbedIdentity: embedIdentity, EmbedDimension: embedderOptions.Embedder.Identity().Dimension,
		PerKind: map[string]*kindDistribution{},
	}

	for _, testCase := range corpus {
		usedFallback := len(testCase.SubjectTerms) == 0
		if usedFallback {
			report.FallbackCount++
		}
		if testCase.ExpectID == "" {
			// CHAOS-3829 Phase 2(c) (team-lead dispatch, 2026-08-16): a
			// no-match CONTROL still runs the vector-arm/lexical-arm
			// measurement -- a CORROBORATED control top-1 is BY DEFINITION
			// wrong (a control has no correct subject at all), exactly the
			// negative-gate population CalibrateMarginFromReport's M must
			// dominate. See measureControlCase's doc comment.
			report.Controls++
			report.ControlCases = append(report.ControlCases, measureControlCase(ctx, t, adapter, key, orgID, testCase, tau, topK, includeRawText, usedFallback))
			continue
		}
		report.Scored++
		result := oracleCaseResult{
			Question: redactText(testCase.Question, includeRawText), ExpectKind: testCase.ExpectKind, ExpectID: testCase.ExpectID,
			UsedTermFallback: usedFallback,
		}

		exists, embedded, err := adapter.subjectExistence(ctx, key, orgID, testCase.ExpectKind, testCase.ExpectID)
		if err != nil {
			t.Fatalf("subject existence check for %s/%s: %v", testCase.ExpectKind, testCase.ExpectID, err)
		}
		if !exists {
			result.Cause = oracleCauseSubjectMissing
			report.SubjectMissing++
			report.Cases = append(report.Cases, result)
			continue
		}
		if !embedded {
			result.Cause = oracleCauseVectorMissing
			report.VectorMissing++
			report.Cases = append(report.Cases, result)
			continue
		}
		correctVector, foundInCorpus := findVector(corpusVectors, testCase.ExpectKind, testCase.ExpectID)
		if !foundInCorpus {
			// Defensive: see subjectExistence's doc comment -- unreachable
			// under a passed org-level fence, but reported as the same
			// projection-coverage cause rather than assumed impossible.
			result.Cause = oracleCauseVectorMissing
			report.VectorMissing++
			report.Cases = append(report.Cases, result)
			continue
		}

		activeTerms := filterActiveTerms(testCase.effectiveSubjectTerms())
		if len(activeTerms) == 0 {
			result.Cause = oracleCauseGated
			report.Gated++
			report.Cases = append(report.Cases, result)
			continue
		}

		vectors, err := adapter.embedQueryTerms(ctx, activeTerms)
		if err != nil {
			t.Fatalf("embed subject terms for %q: %v", testCase.Question, err)
		}
		if len(vectors) != len(activeTerms) {
			t.Fatalf("embedder returned %d vectors for %d active terms (%q)", len(vectors), len(activeTerms), testCase.Question)
		}

		annHit, oracleHit := false, false
		var bestCorrectSimilarity, bestWrongSimilarity *float64
		var hardNegatives []hardNegative
		// CHAOS-3829 Phase 1: vector-arm/lexical-arm bookkeeping, merged
		// across every active term the same way a real ResolveSubjects call
		// would merge per-term Search results into one candidatesBySubject
		// map (see mergeVectorArmSimilarity/mergeLexicalArmSubjects' doc
		// comments).
		vectorArmSimilarity := map[string]vectorArmSubject{}
		lexicalArmSubjects := map[string]bool{}
		vectorSearchTruncatedAnyTerm := false

		for i, term := range activeTerms {
			query64 := float64Vector(vectors[i])

			// ANN + lexical: measureOneTermVectorArm issues the SAME two
			// calls the 2(c) no-match CONTROL measurement uses (shared so
			// the two paths cannot independently drift), and returns
			// annCandidates back to THIS loop -- reused for the ann_loss/hit
			// bookkeeping below -- so the ANN call is issued exactly ONCE
			// per term, not twice.
			annCandidates, vectorTruncated := measureOneTermVectorArm(ctx, t, adapter, key, orgID, term, vectors[i], tau, topK, vectorArmSimilarity, lexicalArmSubjects)
			if containsANNCandidate(annCandidates, testCase.ExpectKind, testCase.ExpectID) {
				annHit = true
			}
			if vectorTruncated {
				vectorSearchTruncatedAnyTerm = true
			}

			// Oracle: RAW ranking for S+/S-/hard-negatives, floor-filtered +
			// tie-inclusive top-K for the retrievability check (findings 2
			// and 3).
			ranked := bruteForceRank(query64, corpusVectors)
			top := topKInclusive(aboveFloor(ranked, tau), topK)
			if containsSubject(top, testCase.ExpectKind, testCase.ExpectID) {
				oracleHit = true
			}

			correctSimilarity := trueCosineSimilarity(query64, correctVector.Vector)
			if bestCorrectSimilarity == nil || correctSimilarity > *bestCorrectSimilarity {
				bestCorrectSimilarity = &correctSimilarity
			}
			if wrong, ok := bestWrongNeighbor(ranked, testCase.ExpectKind, testCase.ExpectID); ok {
				if bestWrongSimilarity == nil || wrong.Similarity > *bestWrongSimilarity {
					bestWrongSimilarity = &wrong.Similarity
				}
			}
			for _, m := range ranked {
				if m.Kind == testCase.ExpectKind && m.CanonicalID == testCase.ExpectID {
					continue
				}
				hardNegatives = append(hardNegatives, hardNegative{
					Kind: m.Kind, CanonicalID: m.CanonicalID, Label: redactText(m.Label, includeRawText), Similarity: m.Similarity,
				})
			}
		}

		// Codex round-1 finding 2: floor rejection is checked FIRST and
		// takes priority over text_loss/ann_loss -- if the correct pair
		// never clears tau on any term, oracleHit is already false as a
		// direct consequence (aboveFloor dropped it), so reporting text_loss
		// here would blame embedding quality for what is actually the
		// similarity floor working as designed.
		switch {
		case *bestCorrectSimilarity <= tau:
			result.Cause = oracleCauseFloorLoss
			report.FloorLoss++
		case !oracleHit:
			result.Cause = oracleCauseTextLoss
			report.TextLoss++
		case !annHit:
			result.Cause = oracleCauseANNLoss
			report.ANNLoss++
		default:
			result.Cause = oracleCauseHit
			report.Hits++
		}
		result.CorrectSimilarity = bestCorrectSimilarity
		result.BestWrongSimilarity = bestWrongSimilarity
		capped, aboveTauCount, truncated := summarizeHardNegatives(hardNegatives, tau, hardNegativeCount)
		result.HardNegatives = capped
		result.HardNegativeAboveTauCount = &aboveTauCount
		result.HardNegativesTruncated = &truncated

		// CHAOS-3829 Phase 1: the disaggregated vector-arm truncation signal
		// and the vector-arm top-1/top-2 identities + raw similarities +
		// corroboration status, merged across every active term above.
		vst := vectorSearchTruncatedAnyTerm
		result.VectorSearchTruncated = &vst
		result.VectorTop1, result.VectorTop2 = finalizeVectorArmTop2(vectorArmSimilarity, lexicalArmSubjects)
		if result.VectorTop2 != nil {
			margin := result.VectorTop1.Similarity - result.VectorTop2.Similarity
			result.VectorMargin = &margin
		}

		report.Cases = append(report.Cases, result)

		dist := report.PerKind[testCase.ExpectKind]
		if dist == nil {
			dist = &kindDistribution{}
			report.PerKind[testCase.ExpectKind] = dist
		}
		dist.CorrectSimilarities = append(dist.CorrectSimilarities, *bestCorrectSimilarity)
		if bestWrongSimilarity != nil {
			dist.BestWrongSimilarities = append(dist.BestWrongSimilarities, *bestWrongSimilarity)
		}
	}

	t.Logf("AC-CHAOS-3831 oracle decomposition (top-%d, tau=%v): scored=%d hit=%d text_loss=%d ann_loss=%d floor_loss=%d subject_missing=%d vector_missing=%d gated=%d controls=%d fallback=%d/%d",
		topK, tau, report.Scored, report.Hits, report.TextLoss, report.ANNLoss, report.FloorLoss,
		report.SubjectMissing, report.VectorMissing, report.Gated, report.Controls, report.FallbackCount, report.Total)
	for _, kind := range sortedKinds(report.PerKind) {
		dist := report.PerKind[kind]
		t.Logf("  kind=%s S+(correct-pair) n=%d S-(best-wrong-neighbor) n=%d", kind, len(dist.CorrectSimilarities), len(dist.BestWrongSimilarities))
	}
	if report.FallbackCount > 0 {
		t.Logf("AC-3831 harness-parity NOTICE: %d/%d cases used the whole-question fallback (see oracleCaseResult.UsedTermFallback per case)", report.FallbackCount, report.Total)
	}

	// CHAOS-3829 Phase 1 summary: how many scored cases had a complete
	// (untruncated) vector arm, a measurable margin, and a corroborated
	// top-1 -- quick operator sanity-check ahead of CalibrateMarginFromReport
	// (Phase 2) actually sizing M from the full per-case data in report.Cases.
	vectorComplete, marginMeasured, top1Corroborated := 0, 0, 0
	for _, c := range report.Cases {
		if c.VectorSearchTruncated != nil && !*c.VectorSearchTruncated {
			vectorComplete++
		}
		if c.VectorMargin != nil {
			marginMeasured++
		}
		if c.VectorTop1 != nil && c.VectorTop1.Corroborated {
			top1Corroborated++
		}
	}
	t.Logf("CHAOS-3829 Phase 1 vector-arm summary: vector_search_complete=%d/%d margin_measured=%d/%d top1_corroborated=%d/%d",
		vectorComplete, report.Scored, marginMeasured, report.Scored, top1Corroborated, report.Scored)

	// CHAOS-3829 Phase 2(c): the SAME summary for the no-match CONTROL
	// population -- a corroborated control top-1 is, by definition, wrong
	// (see measureControlCase's doc comment), so this count alone is the
	// control-side negative-gate signal.
	controlsWithVectorArmData, controlsCorroborated := 0, 0
	for _, c := range report.ControlCases {
		if c.VectorTop1 != nil {
			controlsWithVectorArmData++
			if c.VectorTop1.Corroborated {
				controlsCorroborated++
			}
		}
	}
	t.Logf("CHAOS-3829 Phase 2(c) control-arm summary: controls_with_vector_arm_data=%d/%d controls_corroborated=%d/%d",
		controlsWithVectorArmData, report.Controls, controlsCorroborated, report.Controls)

	writeOracleReport(t, report)
}

// oracleMissCause classifies why a corpus case did or did not resolve
// correctly through the exact-search oracle vs the real ANN path. See the
// TestExactSearchOracleDecomposesRetrievalMisses doc comment for what each
// value means and which downstream ticket (T2/T3/T4) it points at.
type oracleMissCause string

const (
	oracleCauseHit      oracleMissCause = "hit"
	oracleCauseTextLoss oracleMissCause = "text_loss"
	oracleCauseANNLoss  oracleMissCause = "ann_loss"
	// oracleCauseFloorLoss means the correct answer never clears the
	// configured similarity floor tau on any term -- the AC-3778-4 floor
	// deliberately rejecting it, not a text or ANN defect.
	oracleCauseFloorLoss oracleMissCause = "floor_loss"
	// oracleCauseSubjectMissing means no Subject node exists for the
	// expected (kind, id) at all -- a corpus-authoring error.
	oracleCauseSubjectMissing oracleMissCause = "subject_missing"
	// oracleCauseVectorMissing means the subject exists but carries no
	// usable embedding under the current identity -- a projection coverage
	// gap.
	oracleCauseVectorMissing oracleMissCause = "vector_missing"
	// oracleCauseGated means every one of the case's subject terms failed
	// hasLexicalContent -- production would never have embedded any of
	// them either, so neither mechanism had anything to search.
	oracleCauseGated oracleMissCause = "gated"
	// oracleCauseControl marks a no-match CONTROL case's oracleCaseResult
	// (CHAOS-3829 Phase 2(c), report.ControlCases) -- distinct from every
	// cause above, which classify a SCORED case's retrieval outcome; a
	// control has no correct subject to score against at all.
	oracleCauseControl oracleMissCause = "control"
)

type oracleCaseResult struct {
	Question            string          `json:"question"`
	ExpectKind          string          `json:"expect_kind"`
	ExpectID            string          `json:"expect_id"`
	Cause               oracleMissCause `json:"cause"`
	CorrectSimilarity   *float64        `json:"correct_similarity,omitempty"`
	BestWrongSimilarity *float64        `json:"best_wrong_similarity,omitempty"`
	// HardNegatives is CAPPED at ACR_TEST_ORACLE_HARD_NEGATIVES (default 5)
	// -- see HardNegativeAboveTauCount below for the complete count this
	// list may be a truncated view of.
	HardNegatives []hardNegative `json:"hard_negatives,omitempty"`
	// HardNegativeAboveTauCount is the COMPLETE count of DISTINCT wrong
	// subjects (deduped across this case's terms, same rule
	// dedupeHardNegatives applies) whose similarity clears this run's OWN
	// tau (aboveSimilarityFloor's strict predicate) -- codex round-2 P2
	// sibling finding: HardNegatives above is capped at hardNegativeCount
	// (default 5) with NO metadata distinguishing "this case genuinely has
	// few near-duplicates" from "this case has many, only the top 5 got
	// serialized". tau_calibration.go's near-duplicate density estimate
	// (OverFetchMultiplier) reads len(case.HardNegatives) as if it were the
	// complete count; on a truncated case that silently UNDER-sizes K. This
	// field carries the true total so the calibration tool can read it
	// directly INSTEAD of the possibly-censored list length. Computed from
	// the FULL per-case harvest before HardNegatives is capped, so it is
	// exact regardless of hardNegativeCount.
	//
	// A POINTER, deliberately: nil (omitted from JSON) means "this run did
	// not compute the total" (a report from before this field existed, or a
	// future harness variant that skips it), distinguishable from a
	// present-and-zero count ("computed, and genuinely zero negatives clear
	// tau"). The tool-side fix (tau_calibration.go) trusts a present value
	// and refuses to size K from a truncated, saturated case that has none.
	//
	// Cross-tau caveat (codex round-3 P2): this count is measured at THIS
	// run's tau (report.Tau), not whatever tau CalibrateFromReport
	// ultimately recommends for the SAME report -- a case-count computed at
	// one tau cannot know exactly how many negatives would clear a
	// DIFFERENT one (negatives sitting BETWEEN the two floors are invisible
	// to it). The tool-side fix does NOT attempt to adjust for the mismatch
	// mathematically -- it requires report.Tau to EXACTLY equal the
	// recommended tau before trusting this field at all, and refuses (fails
	// closed on K) otherwise. See tau_calibration.go's doc comment on how it
	// consumes this field. In practice this means the total is usable only
	// when the harness is RE-RUN at a previously recommended tau, not on
	// the first pass against a report's original default floor.
	HardNegativeAboveTauCount *int `json:"hard_negative_above_tau_count,omitempty"`
	// HardNegativesTruncated is true when HardNegativeAboveTauCount's full
	// deduped list exceeds hardNegativeCount, i.e. HardNegatives above is
	// genuinely a truncated view for this case, not the complete set.
	//
	// A POINTER (codex round-4 FIX B, tightening round-2 P2): this driver
	// ALWAYS sets it explicitly (see summarizeHardNegatives below), so every
	// report this harness writes carries a present value. The pointer type
	// exists so the calibration tool can tell "this run explicitly measured
	// completeness" apart from "no report ever set this at all" (a
	// pre-CHAOS-3834 report, including a prior baseline run before this
	// field existed) -- a plain bool's zero value (false) made every legacy
	// report silently read as "complete", resurrecting the exact
	// censored-list under-sizing bug round-2 P2 closed. See
	// CalibrationCase.HardNegativesTruncated's doc comment for how the tool
	// now treats nil as "assume truncated", the worst case, not the
	// optimistic pre-fix default.
	HardNegativesTruncated *bool `json:"hard_negatives_truncated,omitempty"`
	// UsedTermFallback is true when this case had no authored subject_terms
	// and therefore ran through the pre-CHAOS-3831 whole-question fallback
	// -- NOT production parity (codex round-1 finding 7: this must be
	// durable per-case, not only a run-level t.Logf count, so a mixed run
	// cannot masquerade as full parity).
	UsedTermFallback bool `json:"used_term_fallback"`
	// VectorSearchTruncated is CHAOS-3829 Phase 1's disaggregated vector-arm
	// truncation signal: true if the ANN call (vectorSearchNodes, i.e.
	// vectorSearchNodesWithOverFetch at multiplier=1 -- today's deployed
	// default for this identity, see retrieval_policy.go's OverFetchMultiplier:0)
	// reported truncated=true for ANY of this case's active terms.
	//
	// Unlike hybridSearchNodes' combined `truncated` return (an OR of the
	// LEXICAL and VECTOR arms, which is what searchTruncated ultimately
	// carries into graphrank.ResolveFromMergedCandidates), this field is the
	// VECTOR arm ALONE -- exactly the disaggregated signal CHAOS-3829's
	// ratified commit-path carve-out needs (vectorSearchComplete =
	// !VectorSearchTruncated): an untruncated vector arm's k-NN ranking is
	// complete (globally distance-ordered) even when the lexical arm
	// truncated, so the two truncation facts must not be collapsed into one
	// bit for this measurement the way production's own combined signal
	// does for retrieval purposes.
	//
	// A POINTER: nil for a case that never reached term-level evaluation
	// (subject_missing/vector_missing/gated causes, mirroring
	// CorrectSimilarity's own nil convention) -- there was no vector search
	// to report a truncation status for.
	VectorSearchTruncated *bool `json:"vector_search_truncated,omitempty"`
	// VectorTop1 and VectorTop2 are the two highest-RAW-similarity subjects
	// the vector arm proposed for this case, merged by MAX similarity across
	// the case's active terms (mirroring graphrank.MergeCandidates' max-
	// confidence-wins rule), each carrying its own corroboration status. nil
	// when the vector arm proposed fewer than 1 (VectorTop1) or 2
	// (VectorTop2) DISTINCT subjects across every active term.
	VectorTop1 *vectorArmSubject `json:"vector_top1,omitempty"`
	VectorTop2 *vectorArmSubject `json:"vector_top2,omitempty"`
	// VectorMargin is VectorTop1.Similarity - VectorTop2.Similarity -- the
	// EXACT quantity CHAOS-3829's ratified VectorMarginCommitThreshold (M)
	// gates on. nil whenever VectorTop2 is nil (fewer than two distinct
	// vector-arm subjects -- margin is undefined, not zero: a case with only
	// one vector-arm candidate has no competitor to measure a gap against,
	// which is a different situation from a measured, arbitrarily small
	// gap).
	VectorMargin *float64 `json:"vector_margin,omitempty"`
}

// hardNegative is one wrong-but-close neighbor harvested for L4's tau
// calibration input. Deliberately carries no embedding values, only the
// identity and the scalar similarity -- a harvest is diagnostic output, not
// a second copy of the vector store. Label is redacted by default; see
// redactText.
type hardNegative struct {
	Kind        string  `json:"kind"`
	CanonicalID string  `json:"canonical_id"`
	Label       string  `json:"label"`
	Similarity  float64 `json:"similarity"`
}

// vectorArmSubject is one subject the vector arm proposed for a scored case
// (CHAOS-3829 Phase 1), carrying only structural identity + a numeric
// similarity -- never label/question text, matching hardNegative's own
// provenance discipline (CHAOS-3834's report rules: identity/dimension
// stamped at the report level, no corpus text anywhere in it).
//
// Similarity is the RAW true-cosine similarity between this case's query
// vector and this subject's stored embedding (trueCosineSimilarity, the
// SAME oracle-side function that computes CorrectSimilarity/BestWrongSimilarity
// above) -- not production's transformed Relevance/Confidence, which clamps
// to the floor whenever the call truncated (vector.go's
// vectorSearchNodesWithOverFetch) and would make a margin computed from it
// meaningless on exactly the truncated cases CHAOS-3829's ratified geometry
// cares most about.
type vectorArmSubject struct {
	Kind        string  `json:"kind"`
	CanonicalID string  `json:"canonical_id"`
	Similarity  float64 `json:"similarity"`
	// Corroborated reports whether the LEXICAL arm (fulltextSearchNodesForResolution,
	// merged across this case's active terms, the same production function
	// hybridSearchNodes calls) ALSO proposed this exact subject for this
	// case -- production's DistinctMechanismCount>=2 test restricted to the
	// two mechanisms this harness runs directly (lexical, vector); a
	// traversal- or question-pass-sourced corroboration is out of scope for
	// this measurement, same as everywhere else in this harness.
	Corroborated bool `json:"corroborated"`
}

// kindDistribution is the raw per-kind S+/S- sample set (embed-text spec §5
// L4's input). Deliberately left unsummarized -- computing quantiles here
// would bake in a calibration decision that belongs to T4, not T1.
type kindDistribution struct {
	CorrectSimilarities   []float64 `json:"correct_similarities"`
	BestWrongSimilarities []float64 `json:"best_wrong_similarities"`
}

// oracleReport is the whole run's output, written to the path
// writeOracleReport resolves -- the artifact the orchestrator reads, and the
// T4/hard-negative-mining input.
type oracleReport struct {
	Total          int `json:"total"`
	Scored         int `json:"scored"`
	Controls       int `json:"controls"`
	Hits           int `json:"hits"`
	TextLoss       int `json:"text_loss"`
	ANNLoss        int `json:"ann_loss"`
	FloorLoss      int `json:"floor_loss"`
	SubjectMissing int `json:"subject_missing"`
	VectorMissing  int `json:"vector_missing"`
	Gated          int `json:"gated"`
	// FallbackCount is how many of Total used the pre-CHAOS-3831
	// whole-question fallback (codex round-1 finding 7) -- see
	// oracleCaseResult.UsedTermFallback for the per-case flag.
	FallbackCount int `json:"fallback_count"`
	TopK          int `json:"top_k"`
	// Tau is the similarity floor this run applied on both the ANN and the
	// oracle side -- recorded so a report is self-describing without
	// cross-referencing the run's environment.
	Tau float64 `json:"tau"`
	// EmbedIdentity and EmbedDimension stamp this report with the embed
	// retrieval identity string (EmbedRetrievalIdentityFromEnv's form) and
	// embedding width the similarities in this report were ACTUALLY
	// measured against (codex round-4 FIX A, exact-measurement class --
	// the artifact-side twin of round-1's composition-tag pin and round-3's
	// dimension pin). CalibrateFromReport requires BOTH to match its
	// caller's target identity/dimension before trusting anything else in
	// this report -- a recommendation minted from one embedding space must
	// never be silently applied to a DIFFERENT one.
	EmbedIdentity  string `json:"embed_identity"`
	EmbedDimension int    `json:"embed_dimension"`
	// RawTextIncluded records whether Question/Label fields below are raw
	// text or a redacted digest (codex round-1 finding 8) -- see
	// ACR_TEST_ORACLE_INCLUDE_RAW_TEXT.
	RawTextIncluded bool                         `json:"raw_text_included"`
	PerKind         map[string]*kindDistribution `json:"per_kind"`
	Cases           []oracleCaseResult           `json:"cases"`
	// ControlCases is CHAOS-3829 Phase 2(c)'s addition: one oracleCaseResult
	// per no-match CONTROL (ExpectID==""), carrying ONLY the
	// VectorSearchTruncated/VectorTop1/VectorTop2/VectorMargin/
	// UsedTermFallback/Question fields (Cause is always oracleCauseControl;
	// ExpectKind/ExpectID stay empty; CorrectSimilarity/BestWrongSimilarity/
	// HardNegatives* are never set -- a control has no correct answer for
	// any of those to be measured against). Kept SEPARATE from Cases
	// (Scored) rather than merged in, so every existing consumer reading
	// `cases` is unaffected by this addition, and so
	// CalibrateMarginFromReport can treat "any corroborated top-1 in this
	// slice is wrong" unconditionally (see measureControlCase's doc
	// comment) without a string-equality check against an expected subject
	// that does not exist for a control.
	ControlCases []oracleCaseResult `json:"control_cases,omitempty"`
}

// redactText returns raw unchanged when includeRaw is set (an explicit,
// opted-in measurement need), or otherwise a stable, non-reversible digest --
// codex round-1 finding 8. The digest is deterministic per input, so two
// report rows referencing the same question or label text are still
// recognizably the same without the report ever holding the text itself.
func redactText(raw string, includeRaw bool) string {
	if includeRaw {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// float64Vector widens an embedding response's []float32 into the []float64
// the oracle's true-cosine math runs in (the embedder port always returns
// float32; decodeVectorProperty is the parallel widener for the graph-stored
// side).
func float64Vector(vector []float32) []float64 {
	out := make([]float64, len(vector))
	for i, v := range vector {
		out[i] = float64(v)
	}
	return out
}

// dedupeHardNegatives collapses the (possibly repeated across the case's
// several subject terms) hard-negative list to one entry per subject, keeping
// each subject's HIGHEST observed similarity, then returns the top `limit` by
// similarity descending. limit is assumed already validated non-negative by
// the caller.
func dedupeHardNegatives(negatives []hardNegative, limit int) []hardNegative {
	best := make(map[string]hardNegative, len(negatives))
	for _, n := range negatives {
		key := n.Kind + "\x00" + n.CanonicalID
		if existing, ok := best[key]; !ok || n.Similarity > existing.Similarity {
			best[key] = n
		}
	}
	out := make([]hardNegative, 0, len(best))
	for _, n := range best {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].CanonicalID < out[j].CanonicalID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// summarizeHardNegatives is the codex round-2 P2 fix's harness-side pure
// function, extracted out of the big live-driver loop specifically so it is
// unit-testable without a real graph/embedder (see oracle_test.go). It dedupes
// the FULL harvest first via a limit that can never itself truncate (dedup
// only ever REDUCES count -- len(negatives) is a safe upper bound on distinct
// subjects), so aboveTauCount/truncated are computed from the COMPLETE
// per-case set, then caps what actually gets serialized in `capped`. See
// oracleCaseResult.HardNegativeAboveTauCount's doc comment for why the
// calibration tool needs the complete count instead of len(capped).
func summarizeHardNegatives(negatives []hardNegative, tau float64, cap int) (capped []hardNegative, aboveTauCount int, truncated bool) {
	full := dedupeHardNegatives(negatives, len(negatives))
	for _, n := range full {
		if aboveSimilarityFloor(n.Similarity, tau) {
			aboveTauCount++
		}
	}
	truncated = len(full) > cap
	if truncated {
		capped = full[:cap]
	} else {
		capped = full
	}
	return capped, aboveTauCount, truncated
}

// subjectMapKey is the SAME (kind, canonicalID) composite key
// dedupeHardNegatives uses, extracted as its own function for CHAOS-3829
// Phase 1's vector-arm/lexical-arm subject bookkeeping -- a NUL byte cannot
// occur in either a graph kind or a canonical ID (both closed structural
// identifiers, never free text), so this is collision-free.
func subjectMapKey(kind, canonicalID string) string {
	return kind + "\x00" + canonicalID
}

// mergeVectorArmSimilarity folds one term's vector-arm ANN result into
// bySubject, keeping each subject's HIGHEST observed raw similarity across
// this case's active terms -- CHAOS-3829 Phase 1, mirroring
// graphrank.MergeCandidates' max-confidence-wins rule (a monotonic function
// of similarity within one call's config, so "highest similarity" and
// "highest confidence" agree here). corpus is used to look up each ANN
// candidate's stored vector so its RAW true-cosine similarity against query
// can be computed -- see vectorArmSubject's doc comment for why this must be
// the raw similarity, not production's transformed/floor-clamped Relevance.
// A candidate absent from corpus (should not happen under a passed org-level
// fence, but defensively tolerated) is skipped rather than faulting the run.
// CHAOS-3829 codex r1 F6 (accepted): reads each candidate's OWN
// production-computed VectorSimilarity (vector.go's
// vectorSearchNodesWithOverFetch, set unconditionally/unclamped -- see
// CandidateNode.VectorSimilarity's doc comment) rather than independently
// recomputing true-cosine similarity against a separately-fetched corpus
// vector. The two are mathematically the SAME quantity in theory, but the
// commit-path carve-out's zero-tolerance, one-ULP-above-the-max
// construction (CalibrateMarginFromReport) is only meaningful if the
// calibration measures the EXACT arithmetic the runtime gate will later
// compare against -- a recomputed value that merely AGREES in the common
// case is not the same guarantee as reading the identical stored value. The
// true-cosine recompute (trueCosineSimilarity + corpus lookup) remains the
// correct tool for this harness's PRE-existing S+/S-/hard-negative
// bookkeeping (bestCorrectSimilarity, bestWrongNeighbor, dedupeHardNegatives),
// which measures recall over the FULL corpus via brute force -- a
// different question than "what did this ANN call actually return",
// unaffected by this fix.
func mergeVectorArmSimilarity(bySubject map[string]vectorArmSubject, candidates []graphrank.CandidateNode) {
	for _, c := range candidates {
		kind := propStringValue(c.Attributes[propKind])
		canonicalID := propStringValue(c.Attributes[propCanonicalID])
		if kind == "" || canonicalID == "" {
			continue
		}
		if c.VectorSimilarity == nil {
			// Defensive: every MatchVector candidate vector.go returns
			// carries this unconditionally; a nil value here means the
			// candidate did not actually come from the vector arm (or a
			// future backend change dropped the field), and there is
			// nothing to merge.
			continue
		}
		similarity := *c.VectorSimilarity
		key := subjectMapKey(kind, canonicalID)
		if existing, exists := bySubject[key]; !exists || similarity > existing.Similarity {
			bySubject[key] = vectorArmSubject{Kind: kind, CanonicalID: canonicalID, Similarity: similarity}
		}
	}
}

// mergeLexicalArmSubjects folds one term's lexical-arm result into the
// (kind,canonicalID)-keyed set bySubject exists in -- CHAOS-3829 Phase 1's
// corroboration-status input. Only identity is recorded: the lexical arm's
// own similarity/relevance is not part of this measurement.
func mergeLexicalArmSubjects(bySubject map[string]bool, candidates []graphrank.CandidateNode) {
	for _, c := range candidates {
		kind := propStringValue(c.Attributes[propKind])
		canonicalID := propStringValue(c.Attributes[propCanonicalID])
		if kind == "" || canonicalID == "" {
			continue
		}
		bySubject[subjectMapKey(kind, canonicalID)] = true
	}
}

// filterActiveTerms is the SINGLE authority for CHAOS-3831's own codex
// round-1 finding 5 rule, extracted so the SCORED case loop and the 2(c)
// no-match CONTROL measurement (measureControlCase) cannot independently
// drift on it: production's hybridSearchNodes skips BOTH mechanisms for a
// term with no lexical content, and never embeds it, so this harness must
// mirror that exactly rather than embedding every term regardless.
func filterActiveTerms(terms []string) []string {
	var active []string
	for _, term := range terms {
		if hasLexicalContent(term) {
			active = append(active, term)
		}
	}
	return active
}

// measureOneTermVectorArm runs ONE term's CHAOS-3829 vector-arm ANN call and
// lexical-arm corroboration call, folding both results into bySubject
// (mergeVectorArmSimilarity) and lexicalSubjects (mergeLexicalArmSubjects).
// Shared by the SCORED case loop (which also needs THIS call's own
// annCandidates for its pre-existing ann_loss/hit bookkeeping -- returned
// here so that loop never issues the SAME ANN query twice) and the 2(c)
// no-match CONTROL measurement (measureControlCase), so the two paths
// cannot independently drift on how a found node becomes part of this
// measurement.
func measureOneTermVectorArm(ctx context.Context, t *testing.T, adapter *Adapter, key, orgID, term string, rawVector []float32, tau float64, topK int, bySubject map[string]vectorArmSubject, lexicalSubjects map[string]bool) (annCandidates []graphrank.CandidateNode, vectorTruncated bool) {
	t.Helper()
	// ANN: the real production function, same floor, same index.
	annCandidates, vectorTruncated, err := adapter.vectorSearchNodes(ctx, key, orgID, rawVector, tau, topK)
	if err != nil {
		t.Fatalf("vectorSearchNodes(%q): %v", term, err)
	}
	mergeVectorArmSimilarity(bySubject, annCandidates)

	// Lexical: the SAME production function hybridSearchNodes calls for
	// this term, run independently here so this case's corroboration status
	// (did the lexical arm ALSO propose this vector-arm subject) can be
	// measured directly rather than inferred.
	lexicalCandidates, _, lexErr := adapter.fulltextSearchNodesForResolution(ctx, key, orgID, term, topK, temporalFilter{})
	if lexErr != nil {
		t.Fatalf("fulltextSearchNodesForResolution(%q): %v", term, lexErr)
	}
	mergeLexicalArmSubjects(lexicalSubjects, lexicalCandidates)

	return annCandidates, vectorTruncated
}

// finalizeVectorArmTop2 computes the top-2 vector-arm subjects and stamps
// each one's Corroborated status from lexicalSubjects -- the shared
// post-term-loop step both the SCORED case loop and measureControlCase use,
// so a subject's corroboration status is always derived the SAME way.
func finalizeVectorArmTop2(bySubject map[string]vectorArmSubject, lexicalSubjects map[string]bool) (top1, top2 *vectorArmSubject) {
	top1, top2 = vectorArmTop2(bySubject)
	if top1 != nil {
		top1.Corroborated = lexicalSubjects[subjectMapKey(top1.Kind, top1.CanonicalID)]
	}
	if top2 != nil {
		top2.Corroborated = lexicalSubjects[subjectMapKey(top2.Kind, top2.CanonicalID)]
	}
	return top1, top2
}

// measureControlCase is CHAOS-3829 Phase 2(c)'s no-match CONTROL measurement
// (team-lead dispatch, 2026-08-16): a control case (ExpectID=="") has NO
// correct subject at all, so a CORROBORATED control top-1 is BY DEFINITION
// wrong -- exactly the negative-gate population CalibrateMarginFromReport's
// VectorMarginCommitThreshold (M) must dominate, alongside a scored case's
// own wrong-top1. Shares measureOneTermVectorArm/finalizeVectorArmTop2 with
// the SCORED case loop so the two paths cannot independently drift.
//
// A control whose terms are all gated (filterActiveTerms returns none) gets
// a result with every Vector* field left nil/zero -- there was no vector
// search to report data for, mirroring the SCORED path's own nil convention
// for a case that never reached term-level evaluation.
func measureControlCase(ctx context.Context, t *testing.T, adapter *Adapter, key, orgID string, testCase ambiguityCase, tau float64, topK int, includeRawText, usedFallback bool) oracleCaseResult {
	t.Helper()
	result := oracleCaseResult{
		Question:         redactText(testCase.Question, includeRawText),
		Cause:            oracleCauseControl,
		UsedTermFallback: usedFallback,
	}
	activeTerms := filterActiveTerms(testCase.effectiveSubjectTerms())
	if len(activeTerms) == 0 {
		return result
	}
	vectors, err := adapter.embedQueryTerms(ctx, activeTerms)
	if err != nil {
		t.Fatalf("embed control terms for %q: %v", testCase.Question, err)
	}
	if len(vectors) != len(activeTerms) {
		t.Fatalf("embedder returned %d vectors for %d active control terms (%q)", len(vectors), len(activeTerms), testCase.Question)
	}

	bySubject := map[string]vectorArmSubject{}
	lexicalSubjects := map[string]bool{}
	anyTruncated := false
	for i, term := range activeTerms {
		_, vectorTruncated := measureOneTermVectorArm(ctx, t, adapter, key, orgID, term, vectors[i], tau, topK, bySubject, lexicalSubjects)
		if vectorTruncated {
			anyTruncated = true
		}
	}
	result.VectorSearchTruncated = &anyTruncated
	result.VectorTop1, result.VectorTop2 = finalizeVectorArmTop2(bySubject, lexicalSubjects)
	if result.VectorTop2 != nil {
		margin := result.VectorTop1.Similarity - result.VectorTop2.Similarity
		result.VectorMargin = &margin
	}
	return result
}

// vectorArmTop2 returns the two highest-similarity entries of bySubject,
// descending, with a deterministic tie-break (kind then canonical ID --
// bruteForceRank/dedupeHardNegatives' own convention) so two runs over the
// same input never disagree about which of an exact tie is "top". Returns
// nil for either slot bySubject does not have enough distinct entries for.
func vectorArmTop2(bySubject map[string]vectorArmSubject) (top1, top2 *vectorArmSubject) {
	ordered := make([]vectorArmSubject, 0, len(bySubject))
	for _, s := range bySubject {
		ordered = append(ordered, s)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Similarity != ordered[j].Similarity {
			return ordered[i].Similarity > ordered[j].Similarity
		}
		if ordered[i].Kind != ordered[j].Kind {
			return ordered[i].Kind < ordered[j].Kind
		}
		return ordered[i].CanonicalID < ordered[j].CanonicalID
	})
	if len(ordered) > 0 {
		v := ordered[0]
		top1 = &v
	}
	if len(ordered) > 1 {
		v := ordered[1]
		top2 = &v
	}
	return top1, top2
}

func sortedKinds(perKind map[string]*kindDistribution) []string {
	kinds := make([]string, 0, len(perKind))
	for kind := range perKind {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// writeOracleReport encodes report as JSON to ACR_TEST_ORACLE_OUTPUT, or --
// when that is unset -- next to the corpus file, so a run always leaves a
// discoverable artifact for the orchestrator without requiring one more
// mandatory env var. Mode 0600 (owner-only): even with the codex round-1
// finding 8 redaction default, Kind/CanonicalID/similarities are graph
// content, not meant for a shared-readable file.
//
// Codex round-2 finding: os.WriteFile's mode argument is applied by the
// OS-level open(2) call's CREATE flag only -- it has no effect on a file
// that already exists at outputPath (a rerun against a reused output path,
// e.g. an orchestrator overwriting yesterday's report). A pre-existing
// 0644 file -- or one created before this posture existed, or under a
// permissive umask -- would silently stay world-readable, including any
// opt-in raw text (ACR_TEST_ORACLE_INCLUDE_RAW_TEXT). writeFileMode0600
// closes this by opening with O_TRUNC (so a reused path is genuinely
// overwritten, not appended) and then EXPLICITLY Chmod-ing the open file
// descriptor to 0600 regardless of what permissions it already had -- this
// is the one step that fixes the file's mode independent of whether it was
// just created, already existed, or was affected by umask.
func writeOracleReport(t *testing.T, report *oracleReport) {
	t.Helper()
	outputPath := os.Getenv("ACR_TEST_ORACLE_OUTPUT")
	if outputPath == "" {
		outputPath = os.Getenv("ACR_TEST_AMBIGUITY_CORPUS") + ".oracle-report.json"
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("encode oracle report: %v", err)
	}
	if err := writeFileMode0600(outputPath, encoded); err != nil {
		t.Fatalf("write oracle report to %s: %v", outputPath, err)
	}
	t.Logf("oracle report written to %s (raw_text_included=%v)", outputPath, report.RawTextIncluded)
}

// writeFileMode0600 writes data to path and guarantees the result is mode
// 0600, whether path is newly created or already existed under some other
// (possibly world-readable) mode. O_CREATE's mode argument only governs a
// NEW file's initial permissions -- an existing file keeps its own mode no
// matter what is passed there -- so the explicit Chmod after open is not
// redundant with it; Chmod is the only call in this sequence that acts on
// an already-existing file's permissions.
func writeFileMode0600(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Close()
}
