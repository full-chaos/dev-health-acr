package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

func TestTrueCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float64
		want float64
	}{
		{"identical unit vectors", []float64{1, 0, 0}, []float64{1, 0, 0}, 1},
		{"orthogonal", []float64{1, 0, 0}, []float64{0, 1, 0}, 0},
		{"opposite", []float64{1, 0, 0}, []float64{-1, 0, 0}, -1},
		{"empty a", nil, []float64{1, 0}, 0},
		{"mismatched lengths", []float64{1, 0}, []float64{1, 0, 0}, 0},
		{"zero vector b", []float64{1, 2, 3}, []float64{0, 0, 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trueCosineSimilarity(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("trueCosineSimilarity(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestTrueCosineSimilarityIsNotADotProduct proves the ORACLE'S defining
// property (embed-text spec §5 L1): stored vectors are unnormalized, so a
// bare dot product would rank a longer vector as "more similar" purely by
// magnitude. Scaling one operand must not move the cosine at all.
func TestTrueCosineSimilarityIsNotADotProduct(t *testing.T) {
	a := []float64{3, 4, 0} // norm 5
	b := []float64{1, 0, 0} // norm 1, same direction as neither -- angle matters
	baseline := trueCosineSimilarity(a, b)

	scaled := []float64{30, 40, 0} // same direction as a, 10x magnitude
	got := trueCosineSimilarity(scaled, b)

	if math.Abs(got-baseline) > 1e-9 {
		t.Fatalf("cosine changed under pure rescaling: baseline=%v scaled=%v -- this is measuring dot product, not cosine", baseline, got)
	}
	// A bare dot product WOULD have changed 10x here, so this also documents
	// what a regression to dot-product ranking would look like.
	dotBaseline := a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
	dotScaled := scaled[0]*b[0] + scaled[1]*b[1] + scaled[2]*b[2]
	if dotBaseline == dotScaled {
		t.Fatal("test fixture does not actually distinguish cosine from dot product")
	}
}

func TestTrueCosineSimilarityClampsFloatDrift(t *testing.T) {
	// A pair of parallel vectors with enough dimensions for accumulated
	// float64 rounding to occasionally push the ratio a hair over 1.
	a := make([]float64, 500)
	b := make([]float64, 500)
	for i := range a {
		a[i] = 1.0000001
		b[i] = 1.0000001
	}
	got := trueCosineSimilarity(a, b)
	if got > 1 || got < -1 {
		t.Fatalf("similarity %v escaped [-1, 1]", got)
	}
}

func TestDecodeVectorProperty(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
		want  []float64
		ok    bool
	}{
		{"typed float64 slice", []float64{1, 2, 3}, []float64{1, 2, 3}, true},
		{"typed float32 slice", []float32{1, 2, 3}, []float64{1, 2, 3}, true},
		{"decoded interface slice of float64", []interface{}{1.5, -2.5}, []float64{1.5, -2.5}, true},
		{"decoded interface slice with int64", []interface{}{int64(1), int64(2)}, []float64{1, 2}, true},
		{"nil", nil, nil, false},
		{"string", "not a vector", nil, false},
		{"mixed-type slice", []interface{}{1.0, "oops"}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := decodeVectorProperty(tt.value)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestBruteForceRankOrdersDescendingByTrueCosine(t *testing.T) {
	query := []float64{1, 0}
	corpus := []oracleVector{
		{Kind: "project", CanonicalID: "far", Vector: []float64{0, 1}},
		{Kind: "project", CanonicalID: "exact", Vector: []float64{5, 0}}, // same direction, different magnitude
		{Kind: "project", CanonicalID: "near", Vector: []float64{1, 0.1}},
	}
	ranked := bruteForceRank(query, corpus)
	if len(ranked) != 3 {
		t.Fatalf("got %d ranked entries, want 3", len(ranked))
	}
	if ranked[0].CanonicalID != "exact" {
		t.Fatalf("top match = %q, want %q", ranked[0].CanonicalID, "exact")
	}
	if ranked[0].Similarity < ranked[1].Similarity || ranked[1].Similarity < ranked[2].Similarity {
		t.Fatalf("ranking is not descending: %+v", ranked)
	}
	if ranked[2].CanonicalID != "far" {
		t.Fatalf("bottom match = %q, want %q", ranked[2].CanonicalID, "far")
	}
}

func TestBruteForceRankIsDeterministicOnTies(t *testing.T) {
	query := []float64{1, 0}
	corpus := []oracleVector{
		{Kind: "project", CanonicalID: "b", Vector: []float64{1, 0}},
		{Kind: "project", CanonicalID: "a", Vector: []float64{1, 0}},
	}
	first := bruteForceRank(query, corpus)
	second := bruteForceRank(query, corpus)
	if first[0].CanonicalID != "a" || second[0].CanonicalID != "a" {
		t.Fatalf("tie-break is not stable/deterministic: %+v / %+v", first, second)
	}
}

func TestFindVector(t *testing.T) {
	corpus := []oracleVector{{Kind: "project", CanonicalID: "p1", Vector: []float64{1}}}
	if _, ok := findVector(corpus, "project", "p1"); !ok {
		t.Fatal("expected to find p1")
	}
	if _, ok := findVector(corpus, "project", "missing"); ok {
		t.Fatal("expected not to find a canonical id absent from the corpus")
	}
}

func TestContainsSubject(t *testing.T) {
	matches := []oracleMatch{{oracleVector: oracleVector{Kind: "project", CanonicalID: "p1"}}}
	if !containsSubject(matches, "project", "p1") {
		t.Fatal("expected containsSubject to find p1")
	}
	if containsSubject(matches, "project", "p2") {
		t.Fatal("containsSubject found a subject that was not there")
	}
}

func TestContainsANNCandidate(t *testing.T) {
	candidates := []graphrank.CandidateNode{
		{Attributes: map[string]interface{}{propKind: "project", propCanonicalID: "p1"}},
	}
	if !containsANNCandidate(candidates, "project", "p1") {
		t.Fatal("expected containsANNCandidate to find p1")
	}
	if containsANNCandidate(candidates, "work_item", "p1") {
		t.Fatal("containsANNCandidate matched across kinds")
	}
}

func TestBestWrongNeighborSkipsTheCorrectAnswer(t *testing.T) {
	ranked := []oracleMatch{
		{oracleVector: oracleVector{Kind: "project", CanonicalID: "correct"}, Similarity: 0.9},
		{oracleVector: oracleVector{Kind: "project", CanonicalID: "imposter"}, Similarity: 0.8},
	}
	got, ok := bestWrongNeighbor(ranked, "project", "correct")
	if !ok {
		t.Fatal("expected a wrong neighbor")
	}
	if got.CanonicalID != "imposter" {
		t.Fatalf("got %q, want %q", got.CanonicalID, "imposter")
	}
}

func TestBestWrongNeighborReportsNoneForASingleNodeCorpus(t *testing.T) {
	ranked := []oracleMatch{{oracleVector: oracleVector{Kind: "project", CanonicalID: "correct"}, Similarity: 1}}
	if _, ok := bestWrongNeighbor(ranked, "project", "correct"); ok {
		t.Fatal("a corpus with only the correct answer has no wrong neighbor")
	}
}

func TestAboveFloorDropsAtOrBelowTauKeepsStrictlyGreater(t *testing.T) {
	ranked := []oracleMatch{
		{oracleVector: oracleVector{CanonicalID: "above"}, Similarity: 0.56},
		{oracleVector: oracleVector{CanonicalID: "at-tau"}, Similarity: 0.55},
		{oracleVector: oracleVector{CanonicalID: "below"}, Similarity: 0.10},
	}
	got := aboveFloor(ranked, 0.55)
	if len(got) != 1 || got[0].CanonicalID != "above" {
		t.Fatalf("aboveFloor(0.55) = %+v, want only %q (a similarity AT tau must be dropped, matching vector.go's <= tau rule)", got, "above")
	}
}

func TestTopKInclusiveIncludesBoundaryTies(t *testing.T) {
	// Two entries tied at the K=2 boundary similarity (0.5): a plain
	// ranked[:2] slice would arbitrarily keep one and drop the other.
	ranked := []oracleMatch{
		{oracleVector: oracleVector{CanonicalID: "first"}, Similarity: 0.9},
		{oracleVector: oracleVector{CanonicalID: "tied-a"}, Similarity: 0.5},
		{oracleVector: oracleVector{CanonicalID: "tied-b"}, Similarity: 0.5},
		{oracleVector: oracleVector{CanonicalID: "far"}, Similarity: 0.1},
	}
	got := topKInclusive(ranked, 2)
	if len(got) != 3 {
		t.Fatalf("topKInclusive(k=2) = %+v, want 3 entries (both boundary ties included)", got)
	}
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.CanonicalID] = true
	}
	if !ids["tied-a"] || !ids["tied-b"] {
		t.Fatalf("topKInclusive dropped a boundary tie: %+v", got)
	}
	if ids["far"] {
		t.Fatalf("topKInclusive included a row past the boundary: %+v", got)
	}
}

func TestTopKInclusiveNoTieMatchesPlainSlice(t *testing.T) {
	ranked := []oracleMatch{
		{oracleVector: oracleVector{CanonicalID: "a"}, Similarity: 0.9},
		{oracleVector: oracleVector{CanonicalID: "b"}, Similarity: 0.5},
		{oracleVector: oracleVector{CanonicalID: "c"}, Similarity: 0.1},
	}
	got := topKInclusive(ranked, 2)
	if len(got) != 2 || got[0].CanonicalID != "a" || got[1].CanonicalID != "b" {
		t.Fatalf("topKInclusive(k=2) with no tie = %+v, want [a b]", got)
	}
}

func TestTopKInclusiveKAtOrPastLength(t *testing.T) {
	ranked := []oracleMatch{{oracleVector: oracleVector{CanonicalID: "a"}, Similarity: 0.9}}
	if got := topKInclusive(ranked, 5); len(got) != 1 {
		t.Fatalf("k past length: got %+v, want the whole (1-entry) ranking", got)
	}
	if got := topKInclusive(ranked, 0); got != nil {
		t.Fatalf("k=0: got %+v, want nil", got)
	}
	if got := topKInclusive(nil, 5); got != nil {
		t.Fatalf("empty ranking: got %+v, want nil", got)
	}
}

func TestSubjectExistenceDistinguishesMissingFromUnembedded(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch params["id"] {
		case "exists-embedded":
			return []row{{"n": &node{Properties: map[string]interface{}{
				propKind: "project", propCanonicalID: "exists-embedded", propEmbedding: []interface{}{1.0, 0.0},
			}}}}, nil
		case "exists-unembedded":
			return []row{{"n": &node{Properties: map[string]interface{}{
				propKind: "project", propCanonicalID: "exists-unembedded",
			}}}}, nil
		default:
			return nil, nil // not found
		}
	}}
	adapter := newFakeAdapter(t, fake)

	exists, embedded, err := adapter.subjectExistence(context.Background(), "key", "org", "project", "exists-embedded")
	if err != nil || !exists || !embedded {
		t.Fatalf("exists-embedded: exists=%v embedded=%v err=%v, want true/true/nil", exists, embedded, err)
	}
	exists, embedded, err = adapter.subjectExistence(context.Background(), "key", "org", "project", "exists-unembedded")
	if err != nil || !exists || embedded {
		t.Fatalf("exists-unembedded: exists=%v embedded=%v err=%v, want true/false/nil", exists, embedded, err)
	}
	exists, embedded, err = adapter.subjectExistence(context.Background(), "key", "org", "project", "missing")
	if err != nil || exists || embedded {
		t.Fatalf("missing: exists=%v embedded=%v err=%v, want false/false/nil", exists, embedded, err)
	}
}

func TestRedactTextIncludeRawReturnsVerbatim(t *testing.T) {
	if got := redactText("what is the auth project doing", true); got != "what is the auth project doing" {
		t.Fatalf("includeRaw=true must return the text unchanged, got %q", got)
	}
}

func TestRedactTextDefaultHidesRawContent(t *testing.T) {
	raw := "contact jane.doe@example.com about the auth work"
	got := redactText(raw, false)
	if got == raw {
		t.Fatal("redactText(includeRaw=false) must not return the raw text")
	}
	if strings.Contains(got, "jane.doe") || strings.Contains(got, "example.com") {
		t.Fatalf("redacted output leaks raw content: %q", got)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("redacted output = %q, want a sha256: prefix", got)
	}
}

func TestRedactTextIsStableAndDistinguishing(t *testing.T) {
	a := redactText("question one", false)
	b := redactText("question one", false)
	c := redactText("question two", false)
	if a != b {
		t.Fatalf("redactText is not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Fatal("two different inputs redacted to the same digest")
	}
}

func TestDedupeHardNegativesKeepsHighestSimilarityPerSubjectAndCapsAtLimit(t *testing.T) {
	negatives := []hardNegative{
		{Kind: "project", CanonicalID: "p1", Similarity: 0.4},
		{Kind: "project", CanonicalID: "p1", Similarity: 0.7}, // same subject, higher similarity -- must win
		{Kind: "project", CanonicalID: "p2", Similarity: 0.6},
		{Kind: "project", CanonicalID: "p3", Similarity: 0.5},
	}
	got := dedupeHardNegatives(negatives, 2)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (capped at limit): %+v", len(got), got)
	}
	if got[0].CanonicalID != "p1" || got[0].Similarity != 0.7 {
		t.Fatalf("top entry = %+v, want p1 at 0.7 (the higher of its two duplicate observations)", got[0])
	}
	if got[1].CanonicalID != "p2" {
		t.Fatalf("second entry = %+v, want p2", got[1])
	}
}

func TestDedupeHardNegativesLimitZeroReturnsNone(t *testing.T) {
	negatives := []hardNegative{{Kind: "project", CanonicalID: "p1", Similarity: 0.9}}
	got := dedupeHardNegatives(negatives, 0)
	if len(got) != 0 {
		t.Fatalf("limit=0 must return no hard negatives, got %+v", got)
	}
}

// TestSummarizeHardNegatives_UnsaturatedCapDoesNotReportTruncation proves the
// common case is unaffected: fewer distinct negatives than the cap means
// truncated=false and aboveTauCount matches exactly what got serialized.
func TestSummarizeHardNegatives_UnsaturatedCapDoesNotReportTruncation(t *testing.T) {
	negatives := []hardNegative{
		{Kind: "project", CanonicalID: "p1", Similarity: 0.90},
		{Kind: "project", CanonicalID: "p2", Similarity: 0.10},
	}
	capped, aboveTauCount, truncated := summarizeHardNegatives(negatives, 0.55, 5)
	if truncated {
		t.Fatal("truncated = true, want false -- only 2 distinct negatives, well under the cap of 5")
	}
	if len(capped) != 2 {
		t.Fatalf("len(capped) = %d, want 2", len(capped))
	}
	if aboveTauCount != 1 {
		t.Fatalf("aboveTauCount = %d, want 1 (only p1 at 0.90 clears tau=0.55)", aboveTauCount)
	}
}

// TestSummarizeHardNegatives_TruncatedCaseReportsTheCompleteAboveTauCount is
// the codex round-2 P2 fix's harness-side pinning test: more distinct
// negatives than the cap, several above tau but NOT all serialized --
// aboveTauCount must reflect the COMPLETE count (4), not len(capped) (2).
func TestSummarizeHardNegatives_TruncatedCaseReportsTheCompleteAboveTauCount(t *testing.T) {
	negatives := []hardNegative{
		{Kind: "project", CanonicalID: "p1", Similarity: 0.99},
		{Kind: "project", CanonicalID: "p2", Similarity: 0.95},
		{Kind: "project", CanonicalID: "p3", Similarity: 0.90},
		{Kind: "project", CanonicalID: "p4", Similarity: 0.85},
		{Kind: "project", CanonicalID: "p5", Similarity: 0.10}, // below tau
	}
	capped, aboveTauCount, truncated := summarizeHardNegatives(negatives, 0.55, 2)
	if !truncated {
		t.Fatal("truncated = false, want true -- 5 distinct negatives exceed the cap of 2")
	}
	if len(capped) != 2 {
		t.Fatalf("len(capped) = %d, want 2 (still capped for serialization)", len(capped))
	}
	if aboveTauCount != 4 {
		t.Fatalf("aboveTauCount = %d, want 4 (p1-p4 all clear tau=0.55; the cap must not shrink this count)", aboveTauCount)
	}
	// The capped list itself must still be the HIGHEST-similarity entries
	// (dedupeHardNegatives' own contract), so a truncated-but-not-saturated
	// downstream case can trust it without the total (see
	// tau_calibration.go's "unsaturated needs no total" reasoning).
	if capped[0].CanonicalID != "p1" || capped[1].CanonicalID != "p2" {
		t.Fatalf("capped = %+v, want the top-2 by similarity (p1, p2)", capped)
	}
}

func TestWriteFileMode0600OverwritesAPreExistingLoosePermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	// Pre-create the path at a world-readable mode -- the exact scenario
	// codex round-2 flagged: os.WriteFile's mode argument only governs a
	// NEW file, so overwriting a report left behind at 0644 (an earlier
	// run, a permissive umask, a reused ACR_TEST_ORACLE_OUTPUT path) must
	// not silently keep that mode.
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("pre-create fixture: %v", err)
	}
	if err := writeFileMode0600(path, []byte(`{"fresh":true}`)); err != nil {
		t.Fatalf("writeFileMode0600: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0600 (must be enforced on overwrite, not only on creation)", got)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != `{"fresh":true}` {
		t.Fatalf("content = %q, want the fresh write (O_TRUNC must discard the stale content)", content)
	}
}

func TestWriteFileMode0600OnANewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeFileMode0600(path, []byte("data")); err != nil {
		t.Fatalf("writeFileMode0600: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0600", got)
	}
}

// TestVectorArmTop2OrdersDescendingWithDeterministicTieBreak is CHAOS-3829
// Phase 1's pinning test for the pure top-2 selection CalibrateMarginFromReport
// (Phase 2) will read VectorMargin from.
func TestVectorArmTop2OrdersDescendingWithDeterministicTieBreak(t *testing.T) {
	bySubject := map[string]vectorArmSubject{
		subjectMapKey("project", "low"):  {Kind: "project", CanonicalID: "low", Similarity: 0.3},
		subjectMapKey("project", "high"): {Kind: "project", CanonicalID: "high", Similarity: 0.9},
		subjectMapKey("project", "mid"):  {Kind: "project", CanonicalID: "mid", Similarity: 0.6},
	}
	top1, top2 := vectorArmTop2(bySubject)
	if top1 == nil || top1.CanonicalID != "high" {
		t.Fatalf("top1 = %+v, want high (0.9)", top1)
	}
	if top2 == nil || top2.CanonicalID != "mid" {
		t.Fatalf("top2 = %+v, want mid (0.6)", top2)
	}
}

func TestVectorArmTop2TiesBreakByKindThenCanonicalID(t *testing.T) {
	bySubject := map[string]vectorArmSubject{
		subjectMapKey("project", "b"): {Kind: "project", CanonicalID: "b", Similarity: 0.5},
		subjectMapKey("project", "a"): {Kind: "project", CanonicalID: "a", Similarity: 0.5},
	}
	top1, top2 := vectorArmTop2(bySubject)
	if top1 == nil || top1.CanonicalID != "a" {
		t.Fatalf("top1 = %+v, want a (deterministic tie-break)", top1)
	}
	if top2 == nil || top2.CanonicalID != "b" {
		t.Fatalf("top2 = %+v, want b", top2)
	}
}

func TestVectorArmTop2ReturnsNilSlotsWhenFewerThanTwoDistinctSubjects(t *testing.T) {
	top1, top2 := vectorArmTop2(nil)
	if top1 != nil || top2 != nil {
		t.Fatalf("empty input: got top1=%+v top2=%+v, want both nil", top1, top2)
	}
	one := map[string]vectorArmSubject{subjectMapKey("project", "only"): {Kind: "project", CanonicalID: "only", Similarity: 0.7}}
	top1, top2 = vectorArmTop2(one)
	if top1 == nil || top1.CanonicalID != "only" {
		t.Fatalf("single entry: top1 = %+v, want only", top1)
	}
	if top2 != nil {
		t.Fatalf("single entry: top2 = %+v, want nil -- margin is undefined with no competitor, not zero", top2)
	}
}

// TestMergeVectorArmSimilarityKeepsHighestAcrossTerms proves the max-wins
// merge rule: a subject the ANN result names twice (once per one of two
// simulated terms) keeps its HIGHEST observed raw similarity, mirroring
// graphrank.MergeCandidates. CHAOS-3829 codex r1 F6: similarity now comes
// from each CandidateNode's own VectorSimilarity (production's
// vector.go-computed value), not a recomputed one -- these fixtures set it
// directly.
func TestMergeVectorArmSimilarityKeepsHighestAcrossTerms(t *testing.T) {
	low, high := 0.10, 1.0
	bySubject := map[string]vectorArmSubject{}
	// Term 1: low similarity.
	mergeVectorArmSimilarity(bySubject, []graphrank.CandidateNode{
		{Attributes: map[string]interface{}{propKind: "project", propCanonicalID: "p1"}, VectorSimilarity: &low},
	})
	firstSimilarity := bySubject[subjectMapKey("project", "p1")].Similarity
	// Term 2: similarity 1.0 -- must WIN over the lower value.
	mergeVectorArmSimilarity(bySubject, []graphrank.CandidateNode{
		{Attributes: map[string]interface{}{propKind: "project", propCanonicalID: "p1"}, VectorSimilarity: &high},
	})
	got := bySubject[subjectMapKey("project", "p1")].Similarity
	if got != 1 {
		t.Fatalf("merged similarity = %v, want 1 (the higher of the two terms' observations, first was %v)", got, firstSimilarity)
	}
}

// TestMergeVectorArmSimilaritySkipsCandidatesWithNilVectorSimilarity is the
// F6-era defensive-tolerance case: a candidate with no VectorSimilarity at
// all (should not happen for a genuine MatchVector production result, but
// defensively tolerated -- see mergeVectorArmSimilarity's own doc comment)
// must not fault the run or be recorded.
func TestMergeVectorArmSimilaritySkipsCandidatesWithNilVectorSimilarity(t *testing.T) {
	bySubject := map[string]vectorArmSubject{}
	mergeVectorArmSimilarity(bySubject, []graphrank.CandidateNode{
		{Attributes: map[string]interface{}{propKind: "project", propCanonicalID: "no-similarity"}},
	})
	if len(bySubject) != 0 {
		t.Fatalf("bySubject = %+v, want empty -- a candidate with nil VectorSimilarity has nothing to merge", bySubject)
	}
}

func TestMergeLexicalArmSubjectsRecordsIdentityOnly(t *testing.T) {
	bySubject := map[string]bool{}
	mergeLexicalArmSubjects(bySubject, []graphrank.CandidateNode{
		{Attributes: map[string]interface{}{propKind: "project", propCanonicalID: "p1"}},
		{Attributes: map[string]interface{}{propKind: "work_item", propCanonicalID: "w1"}},
	})
	if !bySubject[subjectMapKey("project", "p1")] || !bySubject[subjectMapKey("work_item", "w1")] {
		t.Fatalf("bySubject = %+v, want both p1 and w1 present", bySubject)
	}
	if len(bySubject) != 2 {
		t.Fatalf("bySubject has %d entries, want exactly 2", len(bySubject))
	}
}

func TestFilterActiveTerms(t *testing.T) {
	got := filterActiveTerms([]string{"auth service", "???", "", "PR 52", "..."})
	want := []string{"auth service", "PR 52"}
	if len(got) != len(want) {
		t.Fatalf("filterActiveTerms = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterActiveTerms = %v, want %v", got, want)
		}
	}
}

func TestFilterActiveTermsAllGatedReturnsNil(t *testing.T) {
	if got := filterActiveTerms([]string{"???", "..."}); got != nil {
		t.Fatalf("filterActiveTerms = %v, want nil", got)
	}
}

// TestMeasureOneTermVectorArm_MergesANNAndLexicalResults is CHAOS-3829
// Phase 2(c)'s pinning test for the extracted per-term step: it must issue
// the vector-index query AND the fulltext query, and merge BOTH results into
// the caller's maps (mergeVectorArmSimilarity/mergeLexicalArmSubjects) --
// while still returning the raw annCandidates so a caller (the SCORED case
// loop) can reuse them without a second ANN query.
func TestMeasureOneTermVectorArm_MergesANNAndLexicalResults(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return []row{{
				"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Auth"}},
				"score": 0.10, // distance -- a small distance is a HIGH similarity
			}}, nil
		}
		// Fulltext branch: corroborates the SAME subject.
		return []row{{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "Auth"}}, "score": 1.0}}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0}}, 0.05)

	bySubject := map[string]vectorArmSubject{}
	lexicalSubjects := map[string]bool{}
	annCandidates, truncated := measureOneTermVectorArm(context.Background(), t, adapter, "key", "org-1", "auth", []float32{1, 0, 0}, 0.05, 10, bySubject, lexicalSubjects)

	if truncated {
		t.Fatal("truncated = true, want false")
	}
	if len(annCandidates) != 1 {
		t.Fatalf("annCandidates = %#v, want 1 entry (returned for the caller to reuse)", annCandidates)
	}
	entry, ok := bySubject[subjectMapKey("project", "p1")]
	if !ok {
		t.Fatal("bySubject missing p1 -- mergeVectorArmSimilarity was not applied")
	}
	if entry.Similarity <= 0 {
		t.Fatalf("bySubject[p1].Similarity = %v, want a positive similarity (identical vectors)", entry.Similarity)
	}
	if !lexicalSubjects[subjectMapKey("project", "p1")] {
		t.Fatal("lexicalSubjects missing p1 -- mergeLexicalArmSubjects was not applied")
	}
}

// TestMeasureControlCase_NoActiveTermsReturnsAnEmptyResultWithoutAnyLiveCall
// proves a control whose every term is gated never reaches the embedder or
// the graph -- mirrors the SCORED path's own oracleCauseGated short-circuit.
func TestMeasureControlCase_NoActiveTermsReturnsAnEmptyResultWithoutAnyLiveCall(t *testing.T) {
	embedder := &stubEmbedder{vector: []float32{1, 0, 0}}
	adapter := vectorAdapter(t, &fakeConn{}, embedder, 0.05)
	testCase := ambiguityCase{Question: "???", SubjectTerms: []string{"???", "..."}}
	result := measureControlCase(context.Background(), t, adapter, "key", "org-1", testCase, 0.05, 10, false, false)
	if result.Cause != oracleCauseControl {
		t.Fatalf("Cause = %q, want %q", result.Cause, oracleCauseControl)
	}
	if result.VectorSearchTruncated != nil || result.VectorTop1 != nil || result.VectorTop2 != nil || result.VectorMargin != nil {
		t.Fatalf("result = %+v, want every Vector* field nil (no active terms)", result)
	}
	if embedder.calls != 0 {
		t.Fatalf("embedder.calls = %d, want 0 -- a fully-gated control must never reach the embedder", embedder.calls)
	}
}

// TestMeasureControlCase_CorroboratedTopIsRecorded is CHAOS-3829 Phase 2(c)'s
// core pinning test: a no-match control whose vector arm AND lexical arm
// both propose the SAME subject records that subject as VectorTop1 with
// Corroborated=true -- BY DEFINITION wrong, since testCase.ExpectID=="" (a
// control has no correct subject at all). See measureControlCase's doc
// comment.
func TestMeasureControlCase_CorroboratedTopIsRecorded(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "db.idx.vector.queryNodes") {
			return []row{{
				"node":  &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "ghost", propLabel: "Ghost Project"}},
				"score": 0.10,
			}}, nil
		}
		return []row{{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "ghost", propLabel: "Ghost Project"}}, "score": 1.0}}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0}}, 0.05)
	testCase := ambiguityCase{Question: "does the ghost project exist", SubjectTerms: []string{"ghost project"}}

	result := measureControlCase(context.Background(), t, adapter, "key", "org-1", testCase, 0.05, 10, false, false)

	if result.VectorSearchTruncated == nil || *result.VectorSearchTruncated {
		t.Fatalf("VectorSearchTruncated = %v, want a present false", result.VectorSearchTruncated)
	}
	if result.VectorTop1 == nil || !result.VectorTop1.Corroborated {
		t.Fatalf("VectorTop1 = %+v, want a corroborated ghost entry", result.VectorTop1)
	}
	if result.VectorTop1.CanonicalID != "ghost" {
		t.Fatalf("VectorTop1.CanonicalID = %q, want %q", result.VectorTop1.CanonicalID, "ghost")
	}
	// Only one distinct subject was proposed -- no second candidate to
	// measure a margin against.
	if result.VectorTop2 != nil || result.VectorMargin != nil {
		t.Fatalf("VectorTop2/VectorMargin = %v/%v, want both nil (only one distinct subject)", result.VectorTop2, result.VectorMargin)
	}
}

func TestFetchEmbedderFenceCorpusRequiresAnEmbedder(t *testing.T) {
	adapter := newFakeAdapter(t, &fakeConn{})
	_, err := adapter.fetchEmbedderFenceCorpus(context.Background(), "key", "org")
	if !errors.Is(err, errOracleEmbedderRequired) {
		t.Fatalf("got %v, want errOracleEmbedderRequired", err)
	}
}

func TestFetchEmbedderFenceCorpusScopesToOrgAndIdentityAndDecodesRows(t *testing.T) {
	var capturedCypher string
	var capturedParams map[string]interface{}
	fenceRows := []row{
		// Valid.
		{"n": &node{Properties: map[string]interface{}{
			propKind: "project", propCanonicalID: "p1", propLabel: "Auth",
			propEmbedding: []interface{}{1.0, 0.0, 0.0},
		}}},
		// Missing canonical id -- skipped, not fatal.
		{"n": &node{Properties: map[string]interface{}{
			propKind: "project", propEmbedding: []interface{}{1.0, 0.0, 0.0},
		}}},
		// Malformed embedding -- skipped, not fatal.
		{"n": &node{Properties: map[string]interface{}{
			propKind: "project", propCanonicalID: "p2", propEmbedding: "not-a-vector",
		}}},
		// Not a node at all -- skipped, not fatal.
		{"n": "unexpected"},
	}
	// CHAOS-3849: fetchEmbedderFenceCorpus now runs a count(n) verification
	// query under the identical predicate BEFORE paginating, so the fake must
	// answer both query shapes it issues -- the aggregate count (1 usable +
	// 3 skipped-malformed = 4, matching fenceRows below) and the paginated
	// row-returning fetch -- rather than handing back node rows for every
	// call regardless of what was asked.
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		capturedCypher = cypher
		capturedParams = params
		if strings.Contains(cypher, "count(") {
			return []row{{"total": int64(len(fenceRows))}}, nil
		}
		return fenceRows, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0}}, 0.5)
	corpus, err := adapter.fetchEmbedderFenceCorpus(context.Background(), "key", "org-1")
	if err != nil {
		t.Fatalf("fetchEmbedderFenceCorpus: %v", err)
	}
	if len(corpus) != 1 {
		t.Fatalf("got %d corpus entries, want 1 (malformed rows must be skipped, not fatal): %+v", len(corpus), corpus)
	}
	if corpus[0].CanonicalID != "p1" || corpus[0].Kind != "project" {
		t.Fatalf("unexpected corpus entry: %+v", corpus[0])
	}
	if len(corpus[0].Vector) != 3 {
		t.Fatalf("vector not decoded: %+v", corpus[0].Vector)
	}
	if capturedParams["org"] != "org-1" {
		t.Fatalf("org predicate not bound: %+v", capturedParams)
	}
	// The corpus predicate must compare the TAGGED stamp -- the string
	// writeNodeVector writes and the read fence verifies (CHAOS-3833) --
	// asserted against the adapter's own stamp authority, not a repeated
	// literal, so a tag-composition change cannot silently split the two.
	if want := adapter.stampedEmbedderIdentity(adapter.embedder.Identity()); capturedParams["identity"] != want {
		t.Fatalf("identity predicate = %v, want the stamped identity %q", capturedParams["identity"], want)
	}
	if identity, _ := capturedParams["identity"].(string); !strings.HasPrefix(identity, "stub/stub-embed#") {
		t.Fatalf("identity predicate not bound to the configured embedder's tagged identity: %+v", capturedParams)
	}
	for _, want := range []string{propOrgID, propEmbedding, propEmbedderIdentity} {
		if !strings.Contains(capturedCypher, want) {
			t.Fatalf("cypher %q missing expected predicate on %q", capturedCypher, want)
		}
	}
}

// resultSetSizeFakeConn simulates FalkorDB's RESULTSET_SIZE behavior
// (CHAOS-3849) against an org of `total` fence-passing Subject rows: a
// row-returning query with no SKIP is silently capped at `serverCap` rows
// (no error -- the real server's own behavior, observed live), while a
// row-returning query carrying an explicit SKIP/LIMIT (fetchEmbedderFenceCorpus's
// post-fix pagination) is answered as a genuine page of the full `total`-row
// dataset. The count(*) aggregate always reports the true `total`, matching
// FalkorDB's documented exemption for aggregate scalar results.
func resultSetSizeFakeConn(total, serverCap int) *fakeConn {
	buildRow := func(i int) row {
		return row{"n": &node{Properties: map[string]interface{}{
			propKind: "project", propCanonicalID: fmt.Sprintf("p%06d", i),
			propEmbedding: []interface{}{1.0, 0.0, 0.0},
		}}}
	}
	return &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "count(") {
			return []row{{"total": int64(total)}}, nil
		}
		if strings.Contains(cypher, "SKIP") {
			skip, _ := params["skip"].(int)
			limit, _ := params["limit"].(int)
			if skip >= total {
				return nil, nil
			}
			end := skip + limit
			if end > total {
				end = total
			}
			rows := make([]row, 0, end-skip)
			for i := skip; i < end; i++ {
				rows = append(rows, buildRow(i))
			}
			return rows, nil
		}
		// The PRE-FIX shape: one unbounded `RETURN n` query, with no SKIP/LIMIT
		// at all -- exactly what the server itself silently truncates.
		n := total
		if n > serverCap {
			n = serverCap
		}
		rows := make([]row, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, buildRow(i))
		}
		return rows, nil
	}}
}

// TestFetchEmbedderFenceCorpusPaginatesPastResultSetSizeCap is the CHAOS-3849
// regression for the silent-truncation defect: fetchEmbedderFenceCorpus must
// return the FULL org- and identity-scoped corpus (here 12,000 rows) even
// though a single unbounded query against this fixture would silently come
// back with only serverCap=10,000 (FalkorDB's dev-default RESULTSET_SIZE) and
// no error at all -- observed live against a real 35,986-vector org. Run
// against the pre-fix single-query body, this fixture makes
// fetchEmbedderFenceCorpus return exactly 10,000 rows with err==nil (a
// silently shrunken universe, not a failure); run against the fix, it must
// paginate to all 12,000.
func TestFetchEmbedderFenceCorpusPaginatesPastResultSetSizeCap(t *testing.T) {
	const total = 12000
	const serverCap = 10000 // FalkorDB's dev-default RESULTSET_SIZE.

	fake := resultSetSizeFakeConn(total, serverCap)
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0, 0}}, 0.5)

	corpus, err := adapter.fetchEmbedderFenceCorpus(context.Background(), "key", "org-1")
	if err != nil {
		t.Fatalf("fetchEmbedderFenceCorpus: %v", err)
	}
	if len(corpus) != total {
		t.Fatalf("got %d corpus entries, want all %d -- the corpus IS the full org- and identity-scoped result set (oracle.go's own contract); a count stuck at the RESULTSET_SIZE cap (%d) means truncation is silently narrowing it again",
			len(corpus), total, serverCap)
	}
}

// TestFetchEmbedderFenceCorpusHardFailsOnCountMismatch is the CHAOS-3849
// closure-guarantee half of the fix: fetchEmbedderFenceCorpus must hard-fail
// with errOracleCorpusSizeMismatch (not silently return a short corpus)
// whenever the assembled row count disagrees with the independent count(n)
// verification -- the guard that turns ANY future silent cap (config drift,
// a driver change) into a loud failure instead of a shrunken universe.
func TestFetchEmbedderFenceCorpusHardFailsOnCountMismatch(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.Contains(cypher, "count(") {
			// The count query claims 5 rows exist...
			return []row{{"total": int64(5)}}, nil
		}
		// ...but the paginated fetch only ever hands back 3, and then a short
		// page, ending the loop -- simulating a fetch/count disagreement
		// (e.g. a future silent cap) rather than a clean pagination.
		return []row{
			{"n": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propEmbedding: []interface{}{1.0, 0.0}}}},
			{"n": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p2", propEmbedding: []interface{}{1.0, 0.0}}}},
			{"n": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p3", propEmbedding: []interface{}{1.0, 0.0}}}},
		}, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0}}, 0.5)

	_, err := adapter.fetchEmbedderFenceCorpus(context.Background(), "key", "org-1")
	if !errors.Is(err, errOracleCorpusSizeMismatch) {
		t.Fatalf("fetchEmbedderFenceCorpus() error = %v, want errOracleCorpusSizeMismatch", err)
	}
}

// TestFetchEmbedderFenceCorpusExcludesNullKeyedRowsFromBothQueries is the
// CHAOS-3849 round-3 (review finding 2) regression, hardened in round 4
// (review finding 2 continued -- the round-3 fixture only exercised the
// canonical_id guard, so removing JUST the subject_kind guard from both
// queries would have evaded it entirely): FalkorDB's uniqueness constraint
// on (org, kind, canonical_id) does not apply to a row whose kind OR
// canonical_id is NULL, so such a row is NOT covered by the ORDER BY
// n.kind, n.canonical_id totality guarantee fetchEmbedderFenceCorpus's other
// pagination proof rests on. Its position under that ORDER BY across two
// SEPARATE query executions (one per SKIP/LIMIT page) is unspecified, so it
// can be duplicated onto both sides of a page boundary while the row it
// displaced is never returned by either page -- and because the duplicate
// lands in skippedMalformed (its NULL field decodes to ""), the arithmetic
// still balances against count(n): len(corpus)+skippedMalformed == expected,
// so errOracleCorpusSizeMismatch never fires. The corpus is short exactly
// one real subject and nothing catches it on size alone.
//
// Two independent subtests cover this, one per guarded property -- a row
// with a NULL canonical_id (valid kind) only needs the canonical_id guard to
// be excluded, and a row with a NULL kind (valid canonical_id) only needs
// the kind guard; each subtest's fixture is blind to the OTHER guard, so
// removing either guard alone is caught by exactly one subtest's behavioral
// check. A THIRD, cheap static check (assertBothNullGuardsPresent) runs
// after every query this test issues and independently requires BOTH
// guards to be textually present in every cypher string, regardless of
// which subtest issued it -- catching a single-guard removal even in the
// subtest whose own fixture would not otherwise react to it.
func TestFetchEmbedderFenceCorpusExcludesNullKeyedRowsFromBothQueries(t *testing.T) {
	t.Run("canonical_id NULL, kind present", func(t *testing.T) {
		malformed := row{"n": &node{Properties: map[string]interface{}{
			propKind: "project", propEmbedding: []interface{}{1.0, 0.0}, // canonical_id absent -- NULL server-side.
		}}}
		testFetchEmbedderFenceCorpusExcludesOneNullGuardedRowType(t, propCanonicalID, malformed)
	})
	t.Run("kind NULL, canonical_id present", func(t *testing.T) {
		malformed := row{"n": &node{Properties: map[string]interface{}{
			propCanonicalID: "null-kind-row", propEmbedding: []interface{}{1.0, 0.0}, // kind absent -- NULL server-side.
		}}}
		testFetchEmbedderFenceCorpusExcludesOneNullGuardedRowType(t, propKind, malformed)
	})
}

// testFetchEmbedderFenceCorpusExcludesOneNullGuardedRowType runs one
// guarded-property scenario for
// TestFetchEmbedderFenceCorpusExcludesNullKeyedRowsFromBothQueries.
// guardedProp is the ONE property (propKind or propCanonicalID) whose
// IS NOT NULL clause is what excludes malformed from the predicate -- the
// fake's "is this cypher guarded" check below deliberately looks at
// guardedProp ALONE, so this reproduces the exact evasion the round-4 review
// finding described: a cypher missing only the OTHER guard still reads as
// "guarded" to THIS fixture and is answered honestly, which is why
// assertBothNullGuardsPresent (a static, fixture-independent check) also
// runs against every captured cypher before this returns.
func testFetchEmbedderFenceCorpusExcludesOneNullGuardedRowType(t *testing.T, guardedProp string, malformed row) {
	t.Helper()
	const wellFormedTotal = oracleFetchBatchSize // exactly one full page, forcing a second (short) page every time.

	wellFormed := func(i int) row {
		return row{"n": &node{Properties: map[string]interface{}{
			propKind: "project", propCanonicalID: fmt.Sprintf("r%05d", i),
			propEmbedding: []interface{}{1.0, 0.0},
		}}}
	}
	guardedQuery := func(cypher string) bool {
		return strings.Contains(cypher, guardedProp+" IS NOT NULL")
	}

	var capturedCyphers []string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		capturedCyphers = append(capturedCyphers, cypher)
		guarded := guardedQuery(cypher)
		if strings.Contains(cypher, "count(") {
			if guarded {
				return []row{{"total": int64(wellFormedTotal)}}, nil // NULL-keyed row excluded by the predicate.
			}
			return []row{{"total": int64(wellFormedTotal + 1)}}, nil // includes the one NULL-keyed row.
		}
		skip, _ := params["skip"].(int)
		if guarded {
			// Real-predicate-equivalent paging: the NULL-keyed row was never
			// a candidate, so there is nothing unstable to order.
			if skip >= wellFormedTotal {
				return nil, nil
			}
			end := skip + oracleFetchBatchSize
			if end > wellFormedTotal {
				end = wellFormedTotal
			}
			rows := make([]row, 0, end-skip)
			for i := skip; i < end; i++ {
				rows = append(rows, wellFormed(i))
			}
			return rows, nil
		}
		// UNGUARDED corruption: the NULL-keyed row's position under
		// ORDER BY n.kind, n.canonical_id is unspecified -- simulated here as
		// landing on BOTH page boundaries (duplicated), while the well-formed
		// row it displaced (the last index) is never returned by either page.
		switch skip {
		case 0:
			rows := make([]row, 0, oracleFetchBatchSize)
			for i := 0; i < wellFormedTotal-1; i++ {
				rows = append(rows, wellFormed(i))
			}
			rows = append(rows, malformed) // fills the page to exactly oracleFetchBatchSize, so pagination continues.
			return rows, nil
		case oracleFetchBatchSize:
			return []row{malformed}, nil // short page: ends pagination.
		default:
			return nil, nil
		}
	}}

	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0}}, 0.5)

	corpus, err := adapter.fetchEmbedderFenceCorpus(context.Background(), "key", "org-1")
	if err != nil {
		t.Fatalf("fetchEmbedderFenceCorpus: %v", err)
	}
	missingID := fmt.Sprintf("r%05d", wellFormedTotal-1)
	found := false
	for _, v := range corpus {
		if v.CanonicalID == missingID {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s is missing from the corpus (%d entries) -- a NULL-keyed row's non-total ordering across a SKIP/LIMIT page boundary silently dropped a real subject while count(*) still balanced, which the size-only closure check cannot catch on its own; the %s IS NOT NULL predicate must be present on both the count and page queries", missingID, len(corpus), guardedProp)
	}
	if len(corpus) != wellFormedTotal {
		t.Fatalf("got %d corpus entries, want exactly %d (the NULL-keyed row excluded by both queries, not merely skipped-and-recounted)", len(corpus), wellFormedTotal)
	}

	assertBothNullGuardsPresent(t, capturedCyphers)
}

// assertBothNullGuardsPresent is the round-4 review finding 2 static
// safety net: independent of whichever single guard a given subtest's
// fixture happens to exercise behaviorally, every cypher this package sends
// for the corpus fetch must carry BOTH the kind and canonical_id
// IS NOT NULL clauses -- catching a regression to only one of the two guards
// even from the subtest whose own fixture is blind to that specific
// omission (see testFetchEmbedderFenceCorpusExcludesOneNullGuardedRowType's
// doc comment).
func assertBothNullGuardsPresent(t *testing.T, cyphers []string) {
	t.Helper()
	if len(cyphers) == 0 {
		t.Fatal("no cypher was captured to check for the NULL guards")
	}
	for _, cypher := range cyphers {
		if !strings.Contains(cypher, propKind+" IS NOT NULL") {
			t.Fatalf("cypher %q is missing the %s IS NOT NULL guard", cypher, propKind)
		}
		if !strings.Contains(cypher, propCanonicalID+" IS NOT NULL") {
			t.Fatalf("cypher %q is missing the %s IS NOT NULL guard", cypher, propCanonicalID)
		}
	}
}
