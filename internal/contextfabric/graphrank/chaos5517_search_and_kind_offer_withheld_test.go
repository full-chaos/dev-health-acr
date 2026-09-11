package graphrank

// CHAOS-5517 (clauses 3+6, resolution seam): certifies the first two
// events registered under this ticket -- eventspec.Search (the first
// MultiplicityBoundedManyPerPass event, self-carried index/total bound) and
// eventspec.KindOfferWithheld (the first MultiplicityZeroOrOnePerRequest
// event) -- through the REAL production entry point
// (ResolveSubjectsWithCommitBasis), a REAL NewSlogResolutionTracer +
// slog.JSONHandler, and certify.Certify/CertifyAbsent against the real
// emitted JSON. Per-candidate/per-term fixtures here reuse the existing,
// already-proven CHAOS-5218 pool-membership shape
// (chaos5218_kind_offer_pool_membership_test.go's own
// TestResolveSubjects_PartialWithholdingCarriesItsCountsNonZero fixture) for
// the withheld-fires case, rather than inventing a second one.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestSearchCertifiesTheSelfCarriedBoundAcrossMultipleTerms drives a real
// resolution with TWO distinct search terms and certifies BOTH resulting
// search lines through eventspec.Search -- index=1/total=2 and index=2/
// total=2 -- plus CertifyBoundedManyCount's own count read (2), proving the
// bound is readable from the lines themselves, not merely consistent with
// each other.
func TestSearchCertifiesTheSelfCarriedBoundAcrossMultipleTerms(t *testing.T) {
	t.Parallel()
	teamA := candidateNode(contextfabric.SubjectTeam, "team:alpha", "Alpha", 0.6, "*")
	teamB := candidateNode(contextfabric.SubjectTeam, "team:beta", "Beta", 0.6, "*")
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{
			"alpha": {teamA},
			"beta":  {teamB},
		},
	}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	req := testRequest()
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha", "beta"), deps,
		nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}

	lines := log.LinesWithMsg(eventspec.Search.Msg)
	if len(lines) != 2 {
		t.Fatalf("captured %d search lines for a 2-term resolution, want exactly 2", len(lines))
	}

	count, err := certify.CertifyBoundedManyCount(log, eventspec.Search, map[string]any{"request_id": req.RequestID})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount() error = %v", err)
	}
	if count != 2 {
		t.Fatalf("CertifyBoundedManyCount() = %d, want 2", count)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.Search,
		Want:  map[string]any{"request_id": req.RequestID, "index": 1, "total": 2},
	}); err != nil {
		t.Fatalf("Certify(index=1) error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.Search,
		Want:  map[string]any{"request_id": req.RequestID, "index": 2, "total": 2},
	}); err != nil {
		t.Fatalf("Certify(index=2) error = %v", err)
	}
}

// TestSearchCertifiesZeroTermsAsALegitimateBoundedManyCount drives a real
// resolution with NO search terms at all (an interpreted question with an
// empty SubjectTerms list) and asserts CertifyBoundedManyCount reads back a
// legitimate count of 0 -- the "zero detail lines...must certify, not
// absent" shape (chris's engineering ruling, 2026-09-11): the caller already
// knows terms was empty from its own fixture, not from the trace, but the
// trace's own bounded-many mechanism must not refuse an honestly-empty
// scope the way ExactlyOnePerRequest/ExactlyOnePerPass refuse one.
func TestSearchCertifiesZeroTermsAsALegitimateBoundedManyCount(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	req := testRequest()
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted(), deps,
		nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}
	if lines := log.LinesWithMsg(eventspec.Search.Msg); len(lines) != 0 {
		t.Fatalf("captured %d search lines for a 0-term resolution, want exactly 0", len(lines))
	}

	count, err := certify.CertifyBoundedManyCount(log, eventspec.Search, map[string]any{"request_id": req.RequestID})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount() on a genuinely empty scope error = %v -- zero must certify, not refuse", err)
	}
	if count != 0 {
		t.Fatalf("CertifyBoundedManyCount() = %d, want 0", count)
	}
}

// TestKindOfferWithheldCertifiesWhenItFires reuses
// chaos5218_kind_offer_pool_membership_test.go's own proven partial-
// withholding fixture (a grouped frame declaring team, in the pool, AND
// project, not in the pool) and certifies the resulting single
// kind_offer_withheld line through eventspec.KindOfferWithheld.
func TestKindOfferWithheldCertifiesWhenItFires(t *testing.T) {
	t.Parallel()
	teamNode := candidateNode(contextfabric.SubjectTeam, "team:CHAOS", "CHAOS", 0.55, "*")
	prNode := candidateNode(contextfabric.SubjectPullRequest, "pr:1", "CHAOS pull request", 0.5, "*")
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"CHAOS": {teamNode, prNode}},
	}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	frame := &contextfabric.QuestionFrame{
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionGroupedMembers,
			Grouped: &contextfabric.GroupedSetExpression{
				GroupKind: contextfabric.SubjectTeam, MemberKind: contextfabric.SubjectProject,
			},
		},
	}

	req := testRequest()
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("CHAOS"), deps, nil, nil, frame, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}
	if lines := log.LinesWithMsg(eventspec.KindOfferWithheld.Msg); len(lines) != 1 {
		t.Fatalf("captured %d kind_offer_withheld lines, want exactly 1", len(lines))
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.KindOfferWithheld,
		Want: map[string]any{
			"request_id":                             req.RequestID,
			"withheld_count":                         1,
			"withheld_kinds":                         []string{string(contractsv1.ContextFabricSubjectProject)},
			"suppressed_by_unservable_declared_kind": false,
		},
	}); err != nil {
		t.Fatalf("Certify() error = %v", err)
	}
}

// TestKindOfferWithheldCertifiesAbsentOnTheOrdinaryPath drives an ordinary
// resolution with no grouped frame at all (the common case: most
// resolutions never reach CHAOS-5218's own trigger) and asserts
// CertifyAbsent -- the explicit "this line legitimately never fires"
// certificate a MultiplicityZeroOrOnePerRequest event owes, per clause 3's
// own "absence must never substitute for a measured zero" rule turned
// around: an event that TRULY never applies here must be asserted absent,
// not merely unchecked.
func TestKindOfferWithheldCertifiesAbsentOnTheOrdinaryPath(t *testing.T) {
	t.Parallel()
	teamNode := candidateNode(contextfabric.SubjectTeam, "team:CHAOS", "CHAOS", 0.6, "*")
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"CHAOS": {teamNode}},
	}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	req := testRequest()
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("CHAOS"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}
	if err := certify.CertifyAbsent(log, eventspec.KindOfferWithheld, map[string]any{"request_id": req.RequestID}); err != nil {
		t.Fatalf("CertifyAbsent() error = %v -- an ungrouped frame must never reach CHAOS-5218's own trigger", err)
	}
}

// TestCorroborationAndReservedKindAdmittedCertifyThroughTheReserveFixture
// reuses subject_anchor_kind_test.go's own proven
// TestReservedPrefix_AdmissionTraceMatchesTheReturnedCandidates fixture (6
// low-confidence CI-run candidates plus one higher-confidence team
// candidate, max=3, reservedKinds=[team]) -- known to fire the CHAOS-4038
// reserve -- but swaps its capture tracer for a REAL
// NewSlogResolutionTracer + slog.JSONHandler (a capture struct is not an
// emitted line), driving the exported
// ResolveFromMergedCandidatesWithGateAndBasis production entry point
// (pass=1 always, per its own doc comment) directly. Certifies all three
// CHAOS-5517 events this one fixture reaches: Corroboration (per-candidate,
// bounded by the 7-candidate pool), CorroborationSummary (the same pool's
// own count), and ReservedKindAdmitted (bounded by however many the reserve
// actually admitted here).
func TestCorroborationAndReservedKindAdmittedCertifyThroughTheReserveFixture(t *testing.T) {
	t.Parallel()
	pool := make(map[string]contextfabric.SubjectCandidate)
	for i := 0; i < 6; i++ {
		c := contextfabric.SubjectCandidate{
			Subject:    contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: fmt.Sprintf("ci_%d", i)},
			State:      contractsv1.ContextFabricResolutionAmbiguous,
			Confidence: 0.9,
		}
		pool[SubjectKey(c.Subject)] = c
	}
	team := contextfabric.SubjectCandidate{
		Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_1", Label: "Platform Team"},
		State:      contractsv1.ContextFabricResolutionAmbiguous,
		Confidence: 0.4,
	}
	pool[SubjectKey(team.Subject)] = team

	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	requestID := "request_5517_reserve"
	res, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(
		pool, map[string]string{}, map[string]bool{}, 3, true, false,
		nil, 0, false, 10, 20, true,
		DefaultCommitGatePolicy(), nil, nil, false, tracer, requestID, "", false, false,
		[]contextfabric.SubjectKind{contextfabric.SubjectTeam})

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}

	// Corroboration: one Debug line per candidate in the 7-member pool,
	// bounded by that pool's own size.
	corrobLines := log.LinesWithMsg(eventspec.Corroboration.Msg)
	if len(corrobLines) != 7 {
		t.Fatalf("captured %d corroboration lines, want exactly 7 (one per pool member)", len(corrobLines))
	}
	count, err := certify.CertifyBoundedManyCount(log, eventspec.Corroboration, map[string]any{"request_id": requestID, "pass": 1})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(Corroboration) error = %v", err)
	}
	if count != 7 {
		t.Fatalf("CertifyBoundedManyCount(Corroboration) = %d, want 7", count)
	}

	// CorroborationSummary: exactly one line, candidate_count agrees with
	// the detail count above.
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.CorroborationSummary,
		Want:  map[string]any{"request_id": requestID, "pass": 1, "candidate_count": 7},
	}); err != nil {
		t.Fatalf("Certify(CorroborationSummary) error = %v", err)
	}

	// ReservedKindAdmitted: at least one admission (the fixture's own proven
	// claim -- the reserve fires here), every admitted subject actually
	// present in the returned candidates.
	admittedLines := log.LinesWithMsg(eventspec.ReservedKindAdmitted.Msg)
	if len(admittedLines) == 0 {
		t.Fatal("captured 0 reserved_kind_admitted lines; the reserve did not fire, so this proves nothing (same precondition the reused fixture's own test asserts)")
	}
	admittedCount, err := certify.CertifyBoundedManyCount(log, eventspec.ReservedKindAdmitted, map[string]any{"request_id": requestID, "pass": 1})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(ReservedKindAdmitted) error = %v", err)
	}
	if admittedCount != len(admittedLines) {
		t.Fatalf("CertifyBoundedManyCount(ReservedKindAdmitted) = %d, want %d (matching the raw line count)", admittedCount, len(admittedLines))
	}
	// Keyed on (kind, canonical_id) only -- contextfabric.SubjectRef also
	// carries Label, which the trace line never emits (candidate identity
	// on the wire is kind+canonical_id, same as every other stage's own
	// subject_kind/subject_canonical_id pair), so comparing the FULL struct
	// would spuriously fail on Label alone.
	type subjectIdentity struct {
		kind contextfabric.SubjectKind
		id   string
	}
	returned := make(map[subjectIdentity]bool, len(res.Candidates))
	for _, c := range res.Candidates {
		returned[subjectIdentity{kind: c.Subject.Kind, id: c.Subject.CanonicalID}] = true
	}
	for i := 1; i <= admittedCount; i++ {
		result, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.ReservedKindAdmitted,
			Want:  map[string]any{"request_id": requestID, "pass": 1, "index": i, "total": admittedCount, "survived": true},
		})
		if err != nil {
			t.Fatalf("Certify(ReservedKindAdmitted, index=%d) error = %v", i, err)
		}
		subjectKind, _ := result.Line["subject_kind"].(string)
		subjectID, _ := result.Line["subject_canonical_id"].(string)
		if !returned[subjectIdentity{kind: contextfabric.SubjectKind(subjectKind), id: subjectID}] {
			t.Errorf("certified admission (kind=%s id=%s) is NOT in the returned candidates", subjectKind, subjectID)
		}
	}
}

// TestOfferPoolCertifiesBothDispositionsWithACombinedTotal reuses
// offer_pool_test.go's own proven "both dispositions in one call" fixture
// (one vector-only-excluded candidate, one vector-only-demoted candidate,
// one ordinary exact-match candidate) but swaps its capture tracer
// (offerPoolTracer) for a REAL NewSlogResolutionTracer + slog.JSONHandler,
// driving the same exported ResolveFromMergedCandidatesWithGateAndBasis
// entry point (pass=1) resolveOfferPoolTraced already uses. Certifies that
// the DETAIL event's own self-carried Total (2: one demoted + one excluded)
// agrees with OfferPoolSummary's own two separate counts for the SAME pass
// -- the cross-event agreement chris's ruling names as the preferred
// cross-check wherever a natural summary count exists.
func TestOfferPoolCertifiesBothDispositionsWithACombinedTotal(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	requestID := "req_offer_pool_identity_0000000000"
	resolveOfferPoolTraced(tracer,
		vectorOfferCandidate("team_vector_only", 0.5, contextfabric.ResolutionProposed, contextfabric.MatchVector),
		vectorOfferCandidate("team_receipt_named", 1, contextfabric.ResolutionCommitted, contextfabric.MatchVector),
		vectorOfferCandidate("team_exact", 1, contextfabric.ResolutionProposed, contextfabric.MatchExact),
	)

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.OfferPoolSummary,
		Want:  map[string]any{"request_id": requestID, "pass": 1, "vector_only_excluded": 1, "vector_only_demoted": 1},
	}); err != nil {
		t.Fatalf("Certify(OfferPoolSummary) error = %v", err)
	}

	count, err := certify.CertifyBoundedManyCount(log, eventspec.OfferPool, map[string]any{"request_id": requestID, "pass": 1})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(OfferPool) error = %v", err)
	}
	if count != 2 {
		t.Fatalf("CertifyBoundedManyCount(OfferPool) = %d, want 2 (the summary's own 1 excluded + 1 demoted)", count)
	}

	seenDispositions := map[string]bool{}
	for i := 1; i <= count; i++ {
		result, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.OfferPool,
			Want:  map[string]any{"request_id": requestID, "pass": 1, "index": i, "total": 2},
		})
		if err != nil {
			t.Fatalf("Certify(OfferPool, index=%d) error = %v", i, err)
		}
		disposition, _ := result.Line["disposition"].(string)
		seenDispositions[disposition] = true
	}
	if !seenDispositions["vector_only_demoted"] || !seenDispositions["vector_only_excluded"] {
		t.Fatalf("certified dispositions = %v, want both vector_only_demoted and vector_only_excluded present", seenDispositions)
	}
}

// TestDecisionCertifiesAllThreeOutcomeCardinalities reuses three existing,
// independently proven fixtures (chaos4096's own multi-subject-commit
// shape for "committed", N=2; chaos4117's own single-candidate no-commit
// and two-tied-candidates ambiguous shapes) with a REAL
// NewSlogResolutionTracer, driving the SAME exported
// ResolveFromMergedCandidatesWithGateAndBasis entry point (pass=1) each of
// those fixtures already uses. Certifies eventspec.Decision's own bound:
// Total=2 with two distinct indices for the multi-commit case, Total=1/
// Index=1 for the two single-line outcomes.
func TestDecisionCertifiesAllThreeOutcomeCardinalities(t *testing.T) {
	t.Parallel()

	t.Run("committed_N=2", func(t *testing.T) {
		t.Parallel()
		first := corroborationCandidate("multi_first", 1, contextfabric.MatchExact)
		first.State = contextfabric.ResolutionCommitted
		second := corroborationCandidate("multi_second", 1, contextfabric.MatchAlias)
		second.State = contextfabric.ResolutionCommitted
		bySubject := map[string]contextfabric.SubjectCandidate{
			SubjectKey(first.Subject):  first,
			SubjectKey(second.Subject): second,
		}
		var buf bytes.Buffer
		tracer := NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		resolution, _, _ := ResolveFromMergedCandidatesWithGateAndBasis(
			bySubject, map[string]string{}, map[string]bool{}, 10, true, false,
			nil, 0, false, 10, 20, true,
			DefaultCommitGatePolicy(), nil, nil, false, tracer, "req-multi", "", false, false, nil)
		if len(resolution.Committed) != 2 {
			t.Fatalf("resolution.Committed = %v, want 2", resolution.Committed)
		}

		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		count, err := certify.CertifyBoundedManyCount(log, eventspec.Decision, map[string]any{"request_id": "req-multi", "pass": 1})
		if err != nil {
			t.Fatalf("CertifyBoundedManyCount(Decision) error = %v", err)
		}
		if count != 2 {
			t.Fatalf("CertifyBoundedManyCount(Decision) = %d, want 2", count)
		}
		for i := 1; i <= 2; i++ {
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.Decision,
				Want:  map[string]any{"request_id": "req-multi", "pass": 1, "index": i, "total": 2, "outcome": "committed"},
			}); err != nil {
				t.Fatalf("Certify(Decision, index=%d) error = %v", i, err)
			}
		}
	})

	t.Run("no_commit_empty_pool", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		tracer := NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		resolution := resolveWithTracer(tracer, 10, true)
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %v, want none", resolution.Committed)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.Decision,
			Want:  map[string]any{"request_id": "request_1", "pass": 1, "index": 1, "total": 1, "outcome": "no_commit"},
		}); err != nil {
			t.Fatalf("Certify(Decision, no_commit) error = %v", err)
		}
	})

	t.Run("ambiguous_tied", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		tracer := NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		resolution := resolveWithTracer(tracer, 7, false,
			corroborationCandidate("a", 0.30, contextfabric.MatchLexical),
			corroborationCandidate("b", 0.30, contextfabric.MatchLexical))
		if len(resolution.Committed) != 0 {
			t.Fatalf("resolution.Committed = %v, want none (ambiguous)", resolution.Committed)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.Decision,
			Want:  map[string]any{"request_id": "request_1", "pass": 1, "index": 1, "total": 1, "outcome": "ambiguous"},
		}); err != nil {
			t.Fatalf("Certify(Decision, ambiguous) error = %v", err)
		}
	})
}

// TestAnchorPoolAndKindCoverageFloorCertifyOnAnOrdinaryResolution drives an
// ordinary single-term resolution and certifies AnchorPool and
// KindCoverageFloor -- both unconditional, once per resolveSubjects call.
func TestAnchorPoolAndKindCoverageFloorCertifyOnAnOrdinaryResolution(t *testing.T) {
	t.Parallel()
	teamA := candidateNode(contextfabric.SubjectTeam, "team:alpha", "Alpha", 0.9, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": {teamA}}}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := testRequest()
	_, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha"), deps, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() on real production output error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.AnchorPool,
		Want:  map[string]any{"request_id": req.RequestID, "anchor_pool_kind_scope": "none", "anchor_pool_kind_scope_source": "none", "member_kind_confirmed": "none"},
	}); err != nil {
		t.Fatalf("Certify(AnchorPool) error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.KindCoverageFloor,
		Want:  map[string]any{"request_id": req.RequestID},
	}); err != nil {
		t.Fatalf("Certify(KindCoverageFloor) error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.AnchorOffer,
		Want:  map[string]any{"request_id": req.RequestID, "labels_normalized_count": 0},
	}); err != nil {
		t.Fatalf("Certify(AnchorOffer) error = %v", err)
	}
}

// TestSearchQuestionCertifiesFiringAndAbsent proves eventspec.SearchQuestion
// both ways: firing (backend wires SearchQuestion) and CertifyAbsent (the
// ordinary path, no backend wiring -- production reality today).
func TestSearchQuestionCertifiesFiringAndAbsent(t *testing.T) {
	t.Parallel()
	req := testRequest()
	node := candidateNode(contextfabric.SubjectProject, "project_only_question", "Only Question", 0.9, "*")

	t.Run("fires", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{
			enableSearchQuestion:    true,
			searchResults:           map[string][]CandidateNode{"alpha": {}},
			searchQuestionResults:   map[string][]CandidateNode{req.Question: {node}},
			searchQuestionTruncated: true,
		}
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha"), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.SearchQuestion,
			Want:  map[string]any{"request_id": req.RequestID, "result_count": 1, "truncated": true},
		}); err != nil {
			t.Fatalf("Certify(SearchQuestion) error = %v", err)
		}
	})

	t.Run("absent on the ordinary path", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": {node}}}
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha"), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if err := certify.CertifyAbsent(log, eventspec.SearchQuestion, map[string]any{"request_id": req.RequestID}); err != nil {
			t.Fatalf("CertifyAbsent(SearchQuestion) error = %v -- no backend implements SearchQuestion here", err)
		}
	})
}

// TestAliasLookupCertifiesFiringAndAbsent proves eventspec.AliasLookup both
// ways: firing (backend wires AliasLookup) and CertifyAbsent (no production
// composition root sets AliasLookup today).
func TestAliasLookupCertifiesFiringAndAbsent(t *testing.T) {
	t.Parallel()
	req := testRequest()
	node := candidateNode(contextfabric.SubjectRepository, "repo:alpha", "Alpha", 0.9, "*")

	t.Run("fires", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{
			searchResults:        map[string][]CandidateNode{"alpha": {}},
			enableAliasLookup:    true,
			aliasLookupClaimants: map[string][]CandidateNode{"alpha": {node}},
			aliasLookupComplete:  true,
		}
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha"), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.AliasLookup,
			Want:  map[string]any{"request_id": req.RequestID, "complete": true, "matched_claimants": 1},
		}); err != nil {
			t.Fatalf("Certify(AliasLookup) error = %v", err)
		}
	})

	t.Run("absent on the ordinary path", func(t *testing.T) {
		t.Parallel()
		backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": {node}}}
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha"), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if err := certify.CertifyAbsent(log, eventspec.AliasLookup, map[string]any{"request_id": req.RequestID}); err != nil {
			t.Fatalf("CertifyAbsent(AliasLookup) error = %v -- no production composition root wires AliasLookup today", err)
		}
	})
}

// TestConfirmedKindRescueCertifiesFiringAndAbsent reuses
// chaos4132_confirmed_kind_rescue_test.go's own proven fixtures for both
// shapes.
func TestConfirmedKindRescueCertifiesFiringAndAbsent(t *testing.T) {
	t.Parallel()

	t.Run("fires", func(t *testing.T) {
		t.Parallel()
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "wi_1", Label: "Ask Dev"}
		node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
		backend := &fakeGraphBackend{
			enableSearchKind: true,
			searchResults:    map[string][]CandidateNode{"Ask Dev": {}},
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
				"Ask Dev": {contextfabric.SubjectWorkItem: {node}},
			},
		}
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		req := testRequest()
		confirmed := &contextfabric.ConfirmedExpectedKind{Kind: contextfabric.SubjectWorkItem}
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("Ask Dev"), deps, confirmed, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.ConfirmedKindRescue,
			Want:  map[string]any{"request_id": req.RequestID, "attempted": true, "fired": true, "result_count": 1},
		}); err != nil {
			t.Fatalf("Certify(ConfirmedKindRescue) error = %v", err)
		}
	})

	t.Run("absent when never attempted", func(t *testing.T) {
		t.Parallel()
		node := candidateNode(contextfabric.SubjectTeam, "team:alpha", "Alpha", 0.9, "*")
		backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"alpha": {node}}}
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		req := testRequest()
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha"), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		if err := certify.CertifyAbsent(log, eventspec.ConfirmedKindRescue, map[string]any{"request_id": req.RequestID}); err != nil {
			t.Fatalf("CertifyAbsent(ConfirmedKindRescue) error = %v -- no confirmed kind means the rescue never attempts", err)
		}
	})
}

// TestKindHintSearchAndExactNameSearchCertifyPerCallBounds reuses
// chaos4348_reachability_test.go's own two proven fixtures (a kind-hinted
// project ordinary Search cannot find, and an exact-name-matched project
// with no hint at all) with a real slog tracer, certifying each event's own
// per-(request_id, term_hash) bound.
func TestKindHintSearchAndExactNameSearchCertifyPerCallBounds(t *testing.T) {
	t.Parallel()

	t.Run("kind_hint_search", func(t *testing.T) {
		t.Parallel()
		target := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:chaos-ops", Label: "chaos-ops"}
		targetNode := candidateNode(target.Kind, target.CanonicalID, target.Label, 1.0, "*")
		backend := &fakeGraphBackend{
			searchResults:     map[string][]CandidateNode{"chaos-ops": nil},
			enableSearchKind:  true,
			searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{"chaos-ops": {contextfabric.SubjectProject: {targetNode}}},
		}
		req := testRequest()
		req.ExpectedKinds = []contextfabric.SubjectKind{contextfabric.SubjectProject}
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos-ops"), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		termHash := traceTermHash("chaos-ops")
		count, err := certify.CertifyBoundedManyCount(log, eventspec.KindHintSearch, map[string]any{"request_id": req.RequestID, "term_hash": termHash})
		if err != nil {
			t.Fatalf("CertifyBoundedManyCount(KindHintSearch) error = %v", err)
		}
		if count != 1 {
			t.Fatalf("CertifyBoundedManyCount(KindHintSearch) = %d, want 1", count)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.KindHintSearch,
			Want:  map[string]any{"request_id": req.RequestID, "term_hash": termHash, "index": 1, "total": 1, "subject_canonical_id": target.CanonicalID},
		}); err != nil {
			t.Fatalf("Certify(KindHintSearch) error = %v", err)
		}
	})

	t.Run("exact_name_search", func(t *testing.T) {
		t.Parallel()
		target := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:gitlab:chaos-ops", Label: "chaos-ops"}
		targetNode := candidateNode(target.Kind, target.CanonicalID, target.Label, 0, "*")
		backend := &fakeGraphBackend{
			searchResults:             map[string][]CandidateNode{"chaos-ops": nil},
			enableExactNameCandidates: true,
			exactNameCandidates:       []CandidateNode{targetNode},
		}
		req := testRequest()
		var buf bytes.Buffer
		deps := backend.deps()
		deps.ResolutionTracer = NewSlogResolutionTracer(
			slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
			storage.Principal{OrgID: "org_1"}, req, testInterpreted("chaos-ops"), deps, nil, nil, nil, ""); err != nil {
			t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
		}
		log, err := certify.Parse(buf.Bytes())
		if err != nil {
			t.Fatalf("certify.Parse() on real production output error = %v", err)
		}
		termHash := traceTermHash("chaos-ops")
		count, err := certify.CertifyBoundedManyCount(log, eventspec.ExactNameSearch, map[string]any{"request_id": req.RequestID, "term_hash": termHash})
		if err != nil {
			t.Fatalf("CertifyBoundedManyCount(ExactNameSearch) error = %v", err)
		}
		if count != 1 {
			t.Fatalf("CertifyBoundedManyCount(ExactNameSearch) = %d, want 1", count)
		}
		if _, err := certify.Certify(log, certify.Assertion{
			Event: eventspec.ExactNameSearch,
			Want:  map[string]any{"request_id": req.RequestID, "term_hash": termHash, "index": 1, "total": 1, "subject_canonical_id": target.CanonicalID},
		}); err != nil {
			t.Fatalf("Certify(ExactNameSearch) error = %v", err)
		}
	})
}
