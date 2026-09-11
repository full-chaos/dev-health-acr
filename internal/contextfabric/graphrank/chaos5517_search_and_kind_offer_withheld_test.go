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
