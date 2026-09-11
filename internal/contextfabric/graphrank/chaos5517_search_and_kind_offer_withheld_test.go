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
