package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"testing"
)

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
	report := &oracleReport{
		Total: len(corpus), TopK: topK, Tau: tau, RawTextIncluded: includeRawText,
		PerKind: map[string]*kindDistribution{},
	}

	for _, testCase := range corpus {
		usedFallback := len(testCase.SubjectTerms) == 0
		if usedFallback {
			report.FallbackCount++
		}
		if testCase.ExpectID == "" {
			report.Controls++
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

		terms := testCase.effectiveSubjectTerms()
		var activeTerms []string
		for _, term := range terms {
			// Codex round-1 finding 5: production's hybridSearchNodes skips
			// BOTH mechanisms for a term with no lexical content, and never
			// embeds it. Mirror that exactly rather than embedding every
			// term regardless.
			if hasLexicalContent(term) {
				activeTerms = append(activeTerms, term)
			}
		}
		if len(activeTerms) == 0 {
			result.Cause = oracleCauseGated
			report.Gated++
			report.Cases = append(report.Cases, result)
			continue
		}

		vectors, err := adapter.embedder.Embed(ctx, activeTerms)
		if err != nil {
			t.Fatalf("embed subject terms for %q: %v", testCase.Question, err)
		}
		if len(vectors) != len(activeTerms) {
			t.Fatalf("embedder returned %d vectors for %d active terms (%q)", len(vectors), len(activeTerms), testCase.Question)
		}

		annHit, oracleHit := false, false
		var bestCorrectSimilarity, bestWrongSimilarity *float64
		var hardNegatives []hardNegative

		for i, term := range activeTerms {
			query64 := float64Vector(vectors[i])

			// ANN: the real production function, same floor, same index.
			annCandidates, _, err := adapter.vectorSearchNodes(ctx, key, orgID, vectors[i], tau, topK)
			if err != nil {
				t.Fatalf("vectorSearchNodes(%q): %v", term, err)
			}
			if containsANNCandidate(annCandidates, testCase.ExpectKind, testCase.ExpectID) {
				annHit = true
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
		result.HardNegatives = dedupeHardNegatives(hardNegatives, hardNegativeCount)
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
)

type oracleCaseResult struct {
	Question            string          `json:"question"`
	ExpectKind          string          `json:"expect_kind"`
	ExpectID            string          `json:"expect_id"`
	Cause               oracleMissCause `json:"cause"`
	CorrectSimilarity   *float64        `json:"correct_similarity,omitempty"`
	BestWrongSimilarity *float64        `json:"best_wrong_similarity,omitempty"`
	HardNegatives       []hardNegative  `json:"hard_negatives,omitempty"`
	// UsedTermFallback is true when this case had no authored subject_terms
	// and therefore ran through the pre-CHAOS-3831 whole-question fallback
	// -- NOT production parity (codex round-1 finding 7: this must be
	// durable per-case, not only a run-level t.Logf count, so a mixed run
	// cannot masquerade as full parity).
	UsedTermFallback bool `json:"used_term_fallback"`
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
	// RawTextIncluded records whether Question/Label fields below are raw
	// text or a redacted digest (codex round-1 finding 8) -- see
	// ACR_TEST_ORACLE_INCLUDE_RAW_TEXT.
	RawTextIncluded bool                         `json:"raw_text_included"`
	PerKind         map[string]*kindDistribution `json:"per_kind"`
	Cases           []oracleCaseResult           `json:"cases"`
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
