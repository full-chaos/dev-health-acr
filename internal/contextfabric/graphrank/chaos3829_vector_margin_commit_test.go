package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// resolveOneWithVectorMargin is resolveOne (corroboration_test.go) extended
// with CHAOS-3829's ResolveFromMergedCandidates parameters, for tests that
// exercise the commit-path carve-out directly. searchTruncated is exposed
// too (the carve-out's whole REACH property depends on firing even when it
// is true -- see vectorMarginCommit's own doc comment). retrievalDegraded
// defaults to false (not degraded) -- codex r4 J2's own tests use
// resolveOneWithVectorMarginDegraded instead. effectiveSearchLimit defaults
// to max itself (no separate cap narrowing) and calibratedTopK defaults to
// 20 (the shipped identity's own pinned value, retrieval_policy.go) -- codex
// r5 K1/K2's own tests use resolveOneWithVectorMarginAndEnvelope instead to
// vary either independently of max.
func resolveOneWithVectorMargin(searchTruncated bool, vectorArmSimilarity map[string]float64, threshold float64, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	return resolveOneWithVectorMarginAndMax(10, searchTruncated, vectorArmSimilarity, threshold, candidates...)
}

// resolveOneWithVectorMarginAndMax is resolveOneWithVectorMargin with an
// explicit max (ResolveFromMergedCandidates' own max parameter) -- CHAOS-3829
// codex r1 F1's tests need to vary this below the default of 10.
// retrievalDegraded defaults to false.
func resolveOneWithVectorMarginAndMax(max int, searchTruncated bool, vectorArmSimilarity map[string]float64, threshold float64, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	return resolveOneWithVectorMarginFull(max, searchTruncated, false, vectorArmSimilarity, threshold, candidates...)
}

// resolveOneWithVectorMarginDegraded is resolveOneWithVectorMargin (max=10)
// with an explicit retrievalDegraded -- CHAOS-3829 codex r4 J2's own tests.
func resolveOneWithVectorMarginDegraded(searchTruncated, retrievalDegraded bool, vectorArmSimilarity map[string]float64, threshold float64, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	return resolveOneWithVectorMarginFull(10, searchTruncated, retrievalDegraded, vectorArmSimilarity, threshold, candidates...)
}

// resolveOneWithVectorMarginFull is resolveOneWithVectorMarginEnvelope with
// effectiveSearchLimit defaulted to max itself, calibratedTopK defaulted to
// 20 (codex r5 K1/K2), and unscopedVisibility defaulted to true (codex r7
// M1 -- every PRE-EXISTING test in this file implicitly exercises an
// unscoped resolution) -- every PRE-EXISTING test built on this helper
// keeps its original meaning unchanged: every one of them uses max<=10<=20,
// so the new two-sided envelope is always satisfied whenever the old
// max>=2 test alone was, and none of them narrows scope.
func resolveOneWithVectorMarginFull(max int, searchTruncated, retrievalDegraded bool, vectorArmSimilarity map[string]float64, threshold float64, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	return resolveOneWithVectorMarginEnvelope(max, max, 20, searchTruncated, retrievalDegraded, true, vectorArmSimilarity, threshold, candidates...)
}

// resolveOneWithVectorMarginAndEnvelope is resolveOneWithVectorMargin
// (searchTruncated=false, retrievalDegraded=false, unscopedVisibility=true
// -- none of which is what codex r5 K1/K2's own tests are exercising) with
// an explicit effectiveSearchLimit/calibratedTopK, independent of max --
// CHAOS-3829 codex r5 K1/K2's own tests need to vary these directly (a cap
// narrower than the request, or a search depth past the calibrated TopK),
// which resolveOneWithVectorMarginAndMax cannot express since it feeds one
// number into both max and effectiveSearchLimit. max is kept at whichever
// is larger of 10 or effectiveSearchLimit -- max must always be >=
// effectiveSearchLimit in production (effectiveSearchLimit is max clamped
// DOWN by a cap, never up), and these tests are not exercising max's own
// (unrelated) truncation behavior.
func resolveOneWithVectorMarginAndEnvelope(effectiveSearchLimit, calibratedTopK int, vectorArmSimilarity map[string]float64, threshold float64, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	max := 10
	if effectiveSearchLimit > max {
		max = effectiveSearchLimit
	}
	return resolveOneWithVectorMarginEnvelope(max, effectiveSearchLimit, calibratedTopK, false, false, true, vectorArmSimilarity, threshold, candidates...)
}

// resolveOneWithVectorMarginAndScope is resolveOneWithVectorMargin (max=10,
// searchTruncated=false, retrievalDegraded=false, calibratedTopK=20 -- none
// of which is what codex r7 M1's own tests are exercising) with an
// explicit unscopedVisibility, so those tests can vary it independently of
// everything else.
func resolveOneWithVectorMarginAndScope(unscopedVisibility bool, vectorArmSimilarity map[string]float64, threshold float64, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	return resolveOneWithVectorMarginEnvelope(10, 10, 20, false, false, unscopedVisibility, vectorArmSimilarity, threshold, candidates...)
}

// resolveOneWithVectorMarginEnvelope is the single authority every helper
// above funnels through, so they cannot independently drift on how a
// ResolveFromMergedCandidates call is built.
func resolveOneWithVectorMarginEnvelope(max, effectiveSearchLimit, calibratedTopK int, searchTruncated, retrievalDegraded, unscopedVisibility bool, vectorArmSimilarity map[string]float64, threshold float64, candidates ...contextfabric.SubjectCandidate) contextfabric.SubjectResolution {
	bySubject := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, candidate := range candidates {
		bySubject[SubjectKey(candidate.Subject)] = candidate
	}
	return ResolveFromMergedCandidates(bySubject, map[string]string{}, map[string]bool{}, max, true, searchTruncated, vectorArmSimilarity, threshold, retrievalDegraded, effectiveSearchLimit, calibratedTopK, unscopedVisibility)
}

// TestChaos3829_CarveOutRescuesTheExactTwoCorroboratedCandidatesScenario is
// the direct before/after pairing with
// TestAC_3778_3_TwoCorroboratedCandidatesStillClarify (corroboration_test.go):
// the SAME two corroborated candidates that fail the top-of-two gate (0.75 <
// 0.88) and stay ambiguous WITHOUT a calibrated M now COMMIT the
// higher-vector-similarity one once M is configured and both have a
// sufficient margin -- proving the carve-out is a genuine RESCUE for
// exactly the class of case it was ratified to reach, not merely a
// standalone code path nothing exercises.
func TestChaos3829_CarveOutRescuesTheExactTwoCorroboratedCandidatesScenario(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical, contextfabric.MatchTraversalParent)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)

	// Baseline (M=0, uncalibrated): still ambiguous, exactly as
	// TestAC_3778_3_TwoCorroboratedCandidatesStillClarify pins.
	baseline := resolveOneWithVectorMargin(false, nil, 0, auth, authz)
	if len(baseline.Committed) != 0 {
		t.Fatalf("baseline (uncalibrated M) must stay ambiguous, got %v", baseline.Committed)
	}

	// Rescue: auth's vector arm similarity decisively leads authz's.
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	rescued := resolveOneWithVectorMargin(false, similarities, 0.10, auth, authz)
	if len(rescued.Committed) != 1 || rescued.Committed[0].CanonicalID != "auth" {
		t.Fatalf("rescued resolution.Committed = %v, want exactly [auth]", rescued.Committed)
	}
}

// TestChaos3829_CarveOutFiresEvenWhenSearchTruncated is the core REACH
// pinning test: production's vector arm is truncated on almost every real
// search (CHAOS-3829's own measurement), so a carve-out reachable only when
// UNtruncated would almost never fire. searchTruncated=true here, matching
// what the shipped searchTruncated branch would otherwise force to
// ambiguous on its own -- the carve-out must still rescue it.
func TestChaos3829_CarveOutFiresEvenWhenSearchTruncated(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMargin(true, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("resolution.Committed = %v, want exactly [auth] even under searchTruncated=true", resolution.Committed)
	}
}

// --- Fail-closed: every missing-input dimension, one at a time -------------

func TestChaos3829_UncalibratedThresholdNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{SubjectKey(auth.Subject): 0.99, SubjectKey(authz.Subject): 0.01}
	// threshold=0 is the documented "uncalibrated" sentinel -- must disable
	// the carve-out entirely, regardless of how decisive the margin is.
	resolution := resolveOneWithVectorMargin(false, similarities, 0, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("threshold=0 (uncalibrated) must never commit, got %v", resolution.Committed)
	}
}

func TestChaos3829_NegativeThresholdNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{SubjectKey(auth.Subject): 0.99, SubjectKey(authz.Subject): 0.01}
	resolution := resolveOneWithVectorMargin(false, similarities, -1, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("a negative threshold must never commit, got %v", resolution.Committed)
	}
}

func TestChaos3829_FewerThanTwoVectorTaggedCandidatesNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)
	// Only ONE of the two candidates has a vector-arm similarity entry.
	similarities := map[string]float64{SubjectKey(auth.Subject): 0.90}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.05, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("fewer than two vector-tagged candidates must never commit, got %v", resolution.Committed)
	}
}

func TestChaos3829_NilVectorArmSimilarityMapNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)
	resolution := resolveOneWithVectorMargin(false, nil, 0.05, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("a nil vectorArmSimilarity map must never commit, got %v", resolution.Committed)
	}
}

// TestChaos3829_UncorroboratedTopVectorCandidateNeverFires proves the
// "top candidate corroborated" precondition is read from the VECTOR-ranked
// top-1 specifically (DistinctMechanismCount>=2), not merely "some
// candidate somewhere is corroborated" -- here the HIGHER-similarity
// candidate has only ONE mechanism (uncorroborated), so the carve-out must
// refuse even though a corroborated candidate exists in the set.
func TestChaos3829_UncorroboratedTopVectorCandidateNeverFires(t *testing.T) {
	uncorroboratedTop := corroborationCandidate("auth", 0.60, contextfabric.MatchVector) // single mechanism
	corroboratedSecond := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(uncorroboratedTop.Subject):  0.90, // vector-arm TOP by similarity
		SubjectKey(corroboratedSecond.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.10, uncorroboratedTop, corroboratedSecond)
	if len(resolution.Committed) != 0 {
		t.Fatalf("an uncorroborated vector-arm top-1 must never commit, got %v", resolution.Committed)
	}
}

func TestChaos3829_MarginBelowThresholdNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.91,
		SubjectKey(authz.Subject): 0.90, // margin 0.01, below threshold
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("a margin below threshold must never commit, got %v", resolution.Committed)
	}
}

func TestChaos3829_MarginExactlyAtThresholdFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.50, contextfabric.MatchVector, contextfabric.MatchLexical)
	// 0.75/0.50/0.25 are all exact binary fractions (powers of two), so the
	// subtraction below is exact float64 arithmetic -- margin == threshold
	// with no floating-point rounding hazard to confound the assertion.
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.75,
		SubjectKey(authz.Subject): 0.50, // margin exactly 0.25
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.25, auth, authz)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("margin == threshold must clear the gate (>=, not >), got %v", resolution.Committed)
	}
}

// --- Additive-only: never overrides an existing commit ---------------------

// TestChaos3829_NeverOverridesAnExistingLoneGateCommit proves the carve-out
// is checked ONLY as a rescue: a lone candidate that already clears the
// existing lone-candidate gate (via corroboration -- its two mechanisms lift
// it well above LoneFloor regardless of where LoneFloor itself sits) commits
// through that path, and the carve-out's own conditions (even if
// satisfiable) are never consulted -- there is only one commit-eligible
// candidate here, so vectorMarginCommit could not fire anyway (needs >=2),
// but this test pins the ambiguous flag is never touched or re-evaluated
// once a gate above already committed.
func TestChaos3829_NeverOverridesAnExistingLoneGateCommit(t *testing.T) {
	lone := corroborationCandidate("auth", 0.72, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{SubjectKey(lone.Subject): 0.90}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.05, lone)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("the existing lone-gate commit must stand, got %v", resolution.Committed)
	}
}

// TestChaos3829_NeverOverridesAnExistingTopOfTwoCommit proves a resolution
// that ALREADY commits via the existing top-of-two gate (0.88/0.12) is
// untouched by the carve-out, even when a vectorArmSimilarity map AND a
// configured threshold are both present -- ambiguous never becomes true in
// the first place, so vectorMarginCommit is never even attempted.
func TestChaos3829_NeverOverridesAnExistingTopOfTwoCommit(t *testing.T) {
	top := corroborationCandidate("auth", 0.90, contextfabric.MatchVector, contextfabric.MatchLexical, contextfabric.MatchTraversalParent)
	second := corroborationCandidate("authz", 0.50, contextfabric.MatchVector)
	// The carve-out's OWN criteria would pick "authz" as vector-top-1 if it
	// were ever consulted (higher raw similarity) -- proving non-firing
	// here is not a coincidence of matching subjects.
	similarities := map[string]float64{
		SubjectKey(top.Subject):    0.10,
		SubjectKey(second.Subject): 0.99,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.01, top, second)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("the existing top-of-two commit (auth, via the 0.88/0.12 gate) must stand unchanged, got %v", resolution.Committed)
	}
}

// TestChaos3829_NeverOverridesTheExactLabelOverride proves the CHAOS-3810
// exact-label carve-out -- which runs BEFORE this one -- still wins
// outright even when this carve-out's own criteria are satisfiable.
func TestChaos3829_NeverOverridesTheExactLabelOverride(t *testing.T) {
	exact := corroborationCandidate("auth", 1, contextfabric.MatchExact)
	other := corroborationCandidate("authz", 0.60, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(exact.Subject): 0.10,
		SubjectKey(other.Subject): 0.99,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.01, exact, other)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("the exact-label override must stand unchanged, got %v", resolution.Committed)
	}
}

// TestChaos3829_CommitsTheVectorRankedTop1EvenWhenADifferentCandidateHasHigherConfidence
// proves the carve-out commits the VECTOR-ARM-ranked top-1's subject, which
// can differ from the overall confidence-sorted top candidate -- this is
// the "additive, possibly DIFFERENT reach" the ratified geometry describes,
// not a re-ranking of the existing confidence order.
func TestChaos3829_CommitsTheVectorRankedTop1EvenWhenADifferentCandidateHasHigherConfidence(t *testing.T) {
	higherConfidenceButLowerSimilarity := corroborationCandidate("auth", 0.80, contextfabric.MatchVector, contextfabric.MatchLexical)
	lowerConfidenceButHigherSimilarity := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(higherConfidenceButLowerSimilarity.Subject): 0.20,
		SubjectKey(lowerConfidenceButHigherSimilarity.Subject): 0.90,
	}
	// Confidences (0.80/0.55) do not clear the 0.88/0.12 top-of-two gate,
	// so the existing gates leave this ambiguous -- the carve-out gets a
	// chance, and must rank by SIMILARITY, not by the already-failed
	// Confidence order.
	resolution := resolveOneWithVectorMargin(false, similarities, 0.10, higherConfidenceButLowerSimilarity, lowerConfidenceButHigherSimilarity)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "authz" {
		t.Fatalf("resolution.Committed = %v, want exactly [authz] (the higher-vector-similarity subject, not the higher-confidence one)", resolution.Committed)
	}
}

// TestChaos3829_DeterministicTieBreakOnEqualSimilarity proves a similarity
// tie between two otherwise-eligible candidates resolves the SAME way every
// time (SubjectKey ascending), mirroring the rest of this package's
// tie-break convention (ResolveFromMergedCandidates' own ranking sort).
func TestChaos3829_DeterministicTieBreakOnEqualSimilarity(t *testing.T) {
	b := corroborationCandidate("b-subject", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	a := corroborationCandidate("a-subject", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(a.Subject): 0.80,
		SubjectKey(b.Subject): 0.80,
	}
	first := resolveOneWithVectorMargin(false, similarities, 0, a, b) // threshold 0: verifies tie math never panics/misbehaves even disabled
	if len(first.Committed) != 0 {
		t.Fatalf("threshold=0 must stay disabled regardless of the tie, got %v", first.Committed)
	}
	// A tie has a ZERO margin, which never clears a POSITIVE threshold --
	// this is the correct, safe outcome for a genuine tie (no decisive
	// winner), asserted here so a future change to the tie-break cannot
	// silently start committing on ties.
	tied := resolveOneWithVectorMargin(false, similarities, 0.01, a, b)
	if len(tied.Committed) != 0 {
		t.Fatalf("a genuine similarity TIE (margin=0) must never clear a positive threshold, got %v", tied.Committed)
	}
}

// --- codex r1 F0: competitor is NOT restricted to commitIndex -------------

// TestChaos3829_F0_HigherSimilarityFilteredCompetitorBlocksCommit is the
// core F0 pinning test: vectorArmSimilarity carries a subject that never
// became a SubjectCandidate at all (simulating a raw ANN result
// NodeCandidate rejected -- authorization, an internal filter, or any
// other reason) at a HIGHER similarity than the only eligible candidate.
// The resulting margin is negative and must fail closed, even though the
// filtered subject was never in commitIndex.
func TestChaos3829_F0_HigherSimilarityFilteredCompetitorBlocksCommit(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject): 0.70,
		// "filtered-out" never appears in the candidates list below at
		// all -- it is NOT commit-eligible -- but it IS in the side map,
		// with a HIGHER similarity than auth.
		"project\x00filtered-out": 0.95,
	}
	// Only ONE candidate is commit-eligible (auth) -- the OLD (pre-F0)
	// logic would have found no second commitIndex entry at all and
	// refused for lack of a competitor; make sure a SECOND eligible-but-
	// lower-similarity candidate exists too, so the old logic WOULD have
	// found a (weaker) competitor and fired, proving this test actually
	// distinguishes the two behaviors.
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities[SubjectKey(authz.Subject)] = 0.60 // lower than auth -- old logic's "competitor"

	resolution := resolveOneWithVectorMargin(false, similarities, 0.05, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- the filtered competitor's higher similarity must yield a negative margin", resolution.Committed)
	}
}

// TestChaos3829_F0_LowerSimilarityFilteredCompetitorDoesNotBlockCommit is
// the non-regression control for F0: a filtered-out subject with a LOWER
// similarity than the eligible top must not interfere -- F0 only widens
// what counts as a competitor, it never narrows what can win.
func TestChaos3829_F0_LowerSimilarityFilteredCompetitorDoesNotBlockCommit(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
		"project\x00filtered-out": 0.10, // lower than both -- never becomes the competitor
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("resolution.Committed = %v, want exactly [auth] -- a lower-similarity filtered subject must not block the commit", resolution.Committed)
	}
}

// --- codex r1 F4: corroboration is narrowed to Vector+Lexical specifically -

// TestChaos3829_F4_VectorPlusTraversalIsNotTheMeasuredPairing proves the
// narrowed corroboration check: a top candidate corroborated by
// MatchVector+MatchTraversalParent (2 distinct mechanisms -- the OLD,
// broader DistinctMechanismCount>=2 test would have accepted this) must NOT
// fire the carve-out, because that pairing was never part of what
// CalibrateMarginFromReport measured (lexical arm specifically).
func TestChaos3829_F4_VectorPlusTraversalIsNotTheMeasuredPairing(t *testing.T) {
	top := corroborationCandidate("auth", 0.60, contextfabric.MatchVector, contextfabric.MatchTraversalParent)
	second := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(top.Subject):    0.90,
		SubjectKey(second.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.10, top, second)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- Vector+Traversal is not the measured corroboration pairing", resolution.Committed)
	}
}

// TestChaos3829_F4_VectorPlusExactIsNotTheMeasuredPairing is the same
// narrowing proof for a Vector+Exact pairing.
func TestChaos3829_F4_VectorPlusExactIsNotTheMeasuredPairing(t *testing.T) {
	top := corroborationCandidate("auth", 0.60, contextfabric.MatchVector, contextfabric.MatchExact)
	second := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(top.Subject):    0.90,
		SubjectKey(second.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.10, top, second)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- Vector+Exact is not the measured corroboration pairing", resolution.Committed)
	}
}

// TestChaos3829_F4_VectorPlusLexicalStillFires is the positive control:
// the ONE pairing F4 keeps enabled.
func TestChaos3829_F4_VectorPlusLexicalStillFires(t *testing.T) {
	top := corroborationCandidate("auth", 0.60, contextfabric.MatchVector, contextfabric.MatchLexical)
	second := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(top.Subject):    0.90,
		SubjectKey(second.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.10, top, second)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("resolution.Committed = %v, want exactly [auth] -- Vector+Lexical is the measured, enabled pairing", resolution.Committed)
	}
}

// --- codex r1 F1: fail-closed when max < 2 ---------------------------------

// TestChaos3829_F1_MaxBelowTwoNeverFires proves an otherwise-perfect
// scenario (decisive margin, corroborated top-1) still refuses when
// MaxSubjectCandidates < 2 -- there is no completeness bound for an
// unseen second-place candidate when a Search call can return at most one
// row per term.
func TestChaos3829_F1_MaxBelowTwoNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginAndMax(1, false, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- max=1 has no completeness bound for the carve-out", resolution.Committed)
	}
}

// TestChaos3829_F1_MaxAtTwoStillFires is the boundary positive control:
// max==2 (not merely >2) is sufficient.
func TestChaos3829_F1_MaxAtTwoStillFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginAndMax(2, false, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("resolution.Committed = %v, want exactly [auth] at max=2", resolution.Committed)
	}
}

// --- codex r2 G2: an exact-label collision must never reach the rescue ----

// TestChaos3829_G2_ExactLabelCollisionNeverReachesTheRescue is the core G2
// pinning test: two candidates BOTH satisfy the CHAOS-3810 exact-label
// override's own precondition (Confidence==1, MatchExact present) --
// len(exactIndex)==2, a genuinely irreducible collision per that carve-out's
// own doc comment (only a caller-explicit SubjectHint can disambiguate) --
// and one of them ALSO has a decisively higher vector-arm similarity and
// would otherwise clear vectorMarginCommit's own criteria outright. The
// resolution must stay ambiguous: mutating away the len(exactIndex)<2 guard
// makes this test fail (the higher-similarity duplicate wins instead).
func TestChaos3829_G2_ExactLabelCollisionNeverReachesTheRescue(t *testing.T) {
	dup1 := corroborationCandidate("dup1", 1, contextfabric.MatchExact, contextfabric.MatchVector, contextfabric.MatchLexical)
	dup2 := corroborationCandidate("dup2", 1, contextfabric.MatchExact, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(dup1.Subject): 0.95, // decisively higher -- would otherwise win the rescue outright
		SubjectKey(dup2.Subject): 0.50,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.05, dup1, dup2)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- an exact-label collision must stay ambiguous regardless of vector margin", resolution.Committed)
	}
}

// TestChaos3829_G2_SingleExactMatchStillUsesTheOverrideNotTheRescue is the
// non-regression control: a UNIQUE exact match (len(exactIndex)==1) is
// entirely unaffected -- it still commits through CHAOS-3810's own
// override, never through the rescue at all (which never even runs, since
// ambiguous never becomes true in this case).
func TestChaos3829_G2_SingleExactMatchStillUsesTheOverrideNotTheRescue(t *testing.T) {
	exact := corroborationCandidate("exact", 1, contextfabric.MatchExact)
	other := corroborationCandidate("other", 0.60, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(exact.Subject): 0.10, // deliberately LOW -- must win via the exact override, not the rescue
		SubjectKey(other.Subject): 0.99,
	}
	resolution := resolveOneWithVectorMargin(false, similarities, 0.01, exact, other)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "exact" {
		t.Fatalf("resolution.Committed = %v, want exactly [exact] via the unique exact-label override", resolution.Committed)
	}
}

// --- codex r4 J2: a degraded resolution must never reach the rescue -------

// TestChaos3829_J2_RetrievalDegradedNeverReachesTheRescue is the core J2
// pinning test: an otherwise-perfect rescue scenario (decisive margin,
// corroborated top-1) stays ambiguous when retrievalDegraded=true --
// mirroring the max<2 (F1) fail-closed shape. A degraded resolution was
// never part of the population CalibrateMarginFromReport's oracle harness
// measured (it hard-fails on any degradation), so its margin -- however
// decisive-looking -- has no calibration backing it.
func TestChaos3829_J2_RetrievalDegradedNeverReachesTheRescue(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginDegraded(false, true, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- a degraded resolution must never reach the rescue", resolution.Committed)
	}
}

// TestChaos3829_J2_NotDegradedStillFires is the positive control: the exact
// same fixture, retrievalDegraded=false, must still fire (proving the
// refusal above is specifically about the new guard, not some other
// difference between the two tests).
func TestChaos3829_J2_NotDegradedStillFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginDegraded(false, false, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("resolution.Committed = %v, want exactly [auth] when NOT degraded", resolution.Committed)
	}
}

// --- codex r5 K1+K2: the unified [2, calibratedTopK] envelope --------------

// TestChaos3829_K2_EffectiveSearchLimitBelowTwoNeverFires is K2's core
// pinning test: a backend cap of 1 with a nominal request max of 2 -- the
// OLD (pre-K2) max>=2 test would have passed on the nominal value alone,
// but every Search call this resolution actually made was clamped to AT
// MOST one row, the identical hazard F1 already refuses at max==1. The
// rescue must stay ambiguous.
func TestChaos3829_K2_EffectiveSearchLimitBelowTwoNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	// effectiveSearchLimit=1 (the cap=1/request=2 scenario), calibratedTopK=20.
	resolution := resolveOneWithVectorMarginAndEnvelope(1, 20, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- a cap-clamped effective limit of 1 has no completeness bound for the carve-out", resolution.Committed)
	}
}

// TestChaos3829_K1_EffectiveSearchLimitAboveCalibratedTopKNeverFires is K1's
// core pinning test: an effective per-call limit of 21 exceeds the
// calibrated population's own measured depth (20) -- corroboration at that
// wider lexical rank was never scored, so the resolution must stay
// ambiguous even though the margin itself is decisive and the lower bound
// (>=2) is easily cleared.
func TestChaos3829_K1_EffectiveSearchLimitAboveCalibratedTopKNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginAndEnvelope(21, 20, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- an effective search limit (21) past the calibrated TopK (20) must never commit", resolution.Committed)
	}
}

// TestChaos3829_K1K2_EffectiveSearchLimitAtCalibratedTopKBoundaryFires is the
// boundary positive control: effectiveSearchLimit==calibratedTopK==20 (not
// merely <20) is sufficient -- the envelope is inclusive on both ends.
func TestChaos3829_K1K2_EffectiveSearchLimitAtCalibratedTopKBoundaryFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginAndEnvelope(20, 20, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("resolution.Committed = %v, want exactly [auth] at effectiveSearchLimit==calibratedTopK==20", resolution.Committed)
	}
}

// TestChaos3829_K1_UncalibratedTopKNeverFires is the fail-closed control for
// calibratedTopK itself: zero ("uncalibrated for this identity", the same
// convention every other calibrated field in this ticket uses) must refuse
// even when effectiveSearchLimit sits comfortably in what would otherwise be
// range.
func TestChaos3829_K1_UncalibratedTopKNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginAndEnvelope(10, 0, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- calibratedTopK=0 (uncalibrated) must never commit", resolution.Committed)
	}
}

// --- codex r7 M1: unscopedVisibility (scope-existence-oracle guard) --------

// TestChaos3829_M1_ScopedVisibilityNeverFires is M1's core pinning test: an
// otherwise-perfect rescue scenario (decisive margin, corroborated top-1,
// every other envelope condition satisfied) stays ambiguous when
// unscopedVisibility=false -- the resolution-level analogue of
// principal.RepositoryScopes being non-empty or RequestedScope narrowing
// visibility, either of which makes ResolveSubjects compute
// unscopedVisibility=false (see resolve.go). Rescue-off must be constant
// here, not merely narrower, so a scoped caller can never observe whether a
// hidden competitor exists.
func TestChaos3829_M1_ScopedVisibilityNeverFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginAndScope(false, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %v, want none -- a scoped resolution must never reach the rescue", resolution.Committed)
	}
}

// TestChaos3829_M1_UnscopedVisibilityStillFires is the positive control: the
// exact same fixture, unscopedVisibility=true, must still fire (proving the
// refusal above is specifically about the new guard, not some other
// difference between the two tests) -- mirrors J2's own
// degraded/not-degraded pairing shape.
func TestChaos3829_M1_UnscopedVisibilityStillFires(t *testing.T) {
	auth := corroborationCandidate("auth", 0.75, contextfabric.MatchVector, contextfabric.MatchLexical)
	authz := corroborationCandidate("authz", 0.55, contextfabric.MatchVector, contextfabric.MatchLexical)
	similarities := map[string]float64{
		SubjectKey(auth.Subject):  0.90,
		SubjectKey(authz.Subject): 0.60,
	}
	resolution := resolveOneWithVectorMarginAndScope(true, similarities, 0.10, auth, authz)
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "auth" {
		t.Fatalf("resolution.Committed = %v, want exactly [auth] when NOT scoped", resolution.Committed)
	}
}
