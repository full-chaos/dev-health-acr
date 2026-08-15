package falkorgraph

import (
	"context"
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
//	[ACR_TEST_ORACLE_TOPK=20] [ACR_TEST_ORACLE_HARD_NEGATIVES=5] [ACR_TEST_ORACLE_OUTPUT=/path/to/report.json] \
//	  go test ./internal/contextfabric/falkorgraph -run ExactSearchOracle -v
//
// What it measures, per corpus case that names a correct subject (a no-match
// control -- ExpectID=="" -- has nothing for an oracle to rank against, so
// controls are counted but not scored):
//
//   - no_vector: the correct subject has no usable vector under the
//     currently configured embedder identity at all (never embedded, or
//     embedded under a different/stale identity). This is a PROJECTION
//     coverage gap, not a retrieval-quality one -- no amount of ANN tuning or
//     text enrichment can find a vector that was never written.
//   - text_loss: the exact-cosine oracle itself does not rank the correct
//     subject in the top-K for any of the case's subject terms. The
//     embedding is too far from the query embedding for ANY retrieval
//     mechanism to have found it -- an embed-TEXT problem (T3's territory),
//     not an ANN problem.
//   - ann_loss: the oracle finds the correct subject in the top-K but
//     production's own vectorSearchNodes (same floors, same query, same
//     index) does not -- an ANN-parameter problem (T2's territory: efRuntime,
//     over-fetch), not a text problem.
//   - hit: both find it.
//
// Per-kind S+/S- distributions and hard negatives are recorded only for
// cases with a usable correct-answer vector (cause != no_vector): S+ is the
// best (max over the case's terms) true-cosine similarity between the query
// and the correct answer; S- is the best (max) true-cosine similarity
// between the query and any OTHER corpus member ("best imposter"). Both feed
// L4's per-identity tau calibration and hard-negative mining directly; this
// test only harvests, it does not calibrate anything.
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
	topK, err := envInt(testLookup, "ACR_TEST_ORACLE_TOPK", 20)
	if err != nil {
		t.Fatalf("ACR_TEST_ORACLE_TOPK: %v", err)
	}
	if topK > graphConfig.MaxResults {
		// vectorSearchNodes silently clamps limit to config.MaxResults
		// (vector.go); clamping here too keeps the ANN and oracle sides
		// comparing the SAME K rather than one silently narrower than the
		// other.
		t.Logf("ACR_TEST_ORACLE_TOPK=%d exceeds MaxResults=%d; clamping both sides to %d", topK, graphConfig.MaxResults, graphConfig.MaxResults)
		topK = graphConfig.MaxResults
	}
	hardNegativeCount, err := envInt(testLookup, "ACR_TEST_ORACLE_HARD_NEGATIVES", 5)
	if err != nil {
		t.Fatalf("ACR_TEST_ORACLE_HARD_NEGATIVES: %v", err)
	}

	key := graphKey(graphConfig.GraphPrefix, orgID)
	corpusVectors, err := adapter.fetchEmbedderFenceCorpus(ctx, key, orgID)
	if err != nil {
		t.Fatalf("fetch embedder-fence-passing corpus: %v", err)
	}
	if len(corpusVectors) == 0 {
		t.Fatal("the embedder-fence-passing corpus is empty; there is nothing for the oracle to rank against (has acr-projector run with this embedder configured?)")
	}
	t.Logf("oracle corpus: %d embedder-fence-passing subject vectors for org %s", len(corpusVectors), orgID)

	report := &oracleReport{Total: len(corpus), TopK: topK, PerKind: map[string]*kindDistribution{}}

	for _, testCase := range corpus {
		if testCase.ExpectID == "" {
			report.Controls++
			continue
		}
		report.Scored++
		result := oracleCaseResult{Question: testCase.Question, ExpectKind: testCase.ExpectKind, ExpectID: testCase.ExpectID}

		correctVector, foundInCorpus := findVector(corpusVectors, testCase.ExpectKind, testCase.ExpectID)
		if !foundInCorpus {
			result.Cause = oracleCauseNoVector
			report.NoVector++
			report.Cases = append(report.Cases, result)
			continue
		}

		terms := testCase.effectiveSubjectTerms()
		vectors, err := adapter.embedder.Embed(ctx, terms)
		if err != nil {
			t.Fatalf("embed subject terms for %q: %v", testCase.Question, err)
		}
		if len(vectors) != len(terms) {
			t.Fatalf("embedder returned %d vectors for %d terms (%q)", len(vectors), len(terms), testCase.Question)
		}

		annHit, oracleHit := false, false
		var bestCorrectSimilarity, bestWrongSimilarity *float64
		var hardNegatives []hardNegative

		for i, term := range terms {
			query64 := float64Vector(vectors[i])

			annCandidates, _, err := adapter.vectorSearchNodes(ctx, key, orgID, vectors[i], adapter.similarityFloor, topK)
			if err != nil {
				t.Fatalf("vectorSearchNodes(%q): %v", term, err)
			}
			if containsANNCandidate(annCandidates, testCase.ExpectKind, testCase.ExpectID) {
				annHit = true
			}

			ranked := bruteForceRank(query64, corpusVectors)
			top := ranked
			if len(top) > topK {
				top = top[:topK]
			}
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
				hardNegatives = append(hardNegatives, hardNegative{Kind: m.Kind, CanonicalID: m.CanonicalID, Label: m.Label, Similarity: m.Similarity})
			}
		}

		switch {
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
		if bestCorrectSimilarity != nil {
			dist.CorrectSimilarities = append(dist.CorrectSimilarities, *bestCorrectSimilarity)
		}
		if bestWrongSimilarity != nil {
			dist.BestWrongSimilarities = append(dist.BestWrongSimilarities, *bestWrongSimilarity)
		}
	}

	t.Logf("AC-CHAOS-3831 oracle decomposition (top-%d): scored=%d hit=%d text_loss=%d ann_loss=%d no_vector=%d controls=%d",
		topK, report.Scored, report.Hits, report.TextLoss, report.ANNLoss, report.NoVector, report.Controls)
	for _, kind := range sortedKinds(report.PerKind) {
		dist := report.PerKind[kind]
		t.Logf("  kind=%s S+(correct-pair) n=%d S-(best-wrong-neighbor) n=%d", kind, len(dist.CorrectSimilarities), len(dist.BestWrongSimilarities))
	}

	writeOracleReport(t, report)
}

// oracleMissCause classifies why a corpus case did or did not resolve
// correctly through the exact-search oracle vs the real ANN path. See the
// TestExactSearchOracleDecomposesRetrievalMisses doc comment for what each
// value means and which downstream ticket (T2/T3) it points at.
type oracleMissCause string

const (
	oracleCauseHit      oracleMissCause = "hit"
	oracleCauseTextLoss oracleMissCause = "text_loss"
	oracleCauseANNLoss  oracleMissCause = "ann_loss"
	// oracleCauseNoVector is deliberately distinct from text_loss: it means
	// the correct subject was never a candidate for EITHER mechanism,
	// because no fence-passing vector exists for it at all (unembedded, or
	// embedded under a stale identity) -- a projection coverage gap, not an
	// embedding-quality one.
	oracleCauseNoVector oracleMissCause = "no_vector"
)

type oracleCaseResult struct {
	Question            string          `json:"question"`
	ExpectKind          string          `json:"expect_kind"`
	ExpectID            string          `json:"expect_id"`
	Cause               oracleMissCause `json:"cause"`
	CorrectSimilarity   *float64        `json:"correct_similarity,omitempty"`
	BestWrongSimilarity *float64        `json:"best_wrong_similarity,omitempty"`
	HardNegatives       []hardNegative  `json:"hard_negatives,omitempty"`
}

// hardNegative is one wrong-but-close neighbor harvested for L4's tau
// calibration input. Deliberately carries no embedding values, only the
// identity and the scalar similarity -- a harvest is diagnostic output, not
// a second copy of the vector store.
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
	Total    int                          `json:"total"`
	Scored   int                          `json:"scored"`
	Controls int                          `json:"controls"`
	Hits     int                          `json:"hits"`
	TextLoss int                          `json:"text_loss"`
	ANNLoss  int                          `json:"ann_loss"`
	NoVector int                          `json:"no_vector"`
	TopK     int                          `json:"top_k"`
	PerKind  map[string]*kindDistribution `json:"per_kind"`
	Cases    []oracleCaseResult           `json:"cases"`
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
// similarity descending.
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
	if limit >= 0 && len(out) > limit {
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
// mandatory env var.
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
	if err := os.WriteFile(outputPath, encoded, 0o644); err != nil {
		t.Fatalf("write oracle report to %s: %v", outputPath, err)
	}
	t.Logf("oracle report written to %s", outputPath)
}
