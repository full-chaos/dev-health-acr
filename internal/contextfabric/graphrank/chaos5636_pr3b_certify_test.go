package graphrank

// CHAOS-5636: certifies the 13-event slate this ticket registers --
// KindOffer, ConfirmedKindScope,
// LowPopulationKindScope(+Summary), IdentityGate(+Summary), EvidenceRound,
// EvidenceProbe, EvidenceCensusCommit, EvidenceSourceNative(+Probe),
// SliceBSurvivorVerdict(+Summary) -- through REAL production entry points
// (ResolveSubjects/ResolveSubjectsWithCommitBasis, or a direct call to the
// unexported producer itself when that producer already has its own
// unit-level fixture convention in this package), a REAL
// NewSlogResolutionTracer + slog.JSONHandler, and certify.Certify/
// CertifyAbsent/CertifyBoundedManyCount against the real emitted JSON --
// never a hand-built ResolutionTraceEvent fixture. Mirrors
// chaos5515_eventspec_certify_test.go/chaos5517_search_and_kind_offer_withheld_test.go's
// own established shape.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCertifyKindOfferFiresUnconditionallyOnTheOrdinaryPath drives an
// ordinary two-team search resolution (the SAME shape
// TestSearchCertifiesTheSelfCarriedBoundAcrossMultipleTerms already proves
// for eventspec.Search) and certifies eventspec.KindOffer fires exactly
// once -- kindOfferMaterial/candidateOfferMaterial/handleOfferMaterial run
// on every resolution, unconditionally.
func TestCertifyKindOfferFiresUnconditionallyOnTheOrdinaryPath(t *testing.T) {
	t.Parallel()
	teamA := candidateNode(contextfabric.SubjectTeam, "team:alpha", "Alpha", 0.6, "*")
	teamB := candidateNode(contextfabric.SubjectTeam, "team:beta", "Beta", 0.6, "*")
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"alpha": {teamA}, "beta": {teamB}},
	}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

	req := testRequest()
	if _, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
		storage.Principal{OrgID: "org_1"}, req, testInterpreted("alpha", "beta"), deps,
		nil, nil, nil, ""); err != nil {
		t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
	}

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.KindOffer,
		Want:  map[string]any{"request_id": req.RequestID},
	}); err != nil {
		t.Errorf("certify KindOffer: %v", err)
	}
}

// TestCertifyKindOfferFoldFiresEvenOnAnUpstreamErrorExit is the CHAOS-5636
// class proof for kindOfferFold (resolve.go): kindOfferMaterial's own call
// site sits deep inside resolveSubjects, downstream of the confirmed-kind
// scoped-snapshot's own error return -- forcing that error here (a genuine
// SearchKind backend fault) means resolveSubjects returns BEFORE ever
// reaching kindOfferMaterial's real call site, yet eventspec.KindOffer must
// still certify exactly once (the synthesized fallback line): a declared
// multiplicity is a property of EVERY exit path of its producer, the same
// guarantee exactlyOnceRequestFold already gives anchor_offer/
// kind_coverage_floor (see that type's own doc comment).
func TestCertifyKindOfferFoldFiresEvenOnAnUpstreamErrorExit(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchTruncated:  true,
		searchKindErr:    errors.New("boom"),
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	req := testRequest()
	_, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, req, testInterpreted(term), deps, confirmed, nil)
	if err == nil {
		t.Fatalf("ResolveSubjects() error = nil, want the forced SearchKind backend fault to propagate -- this test's whole claim depends on resolveSubjects returning BEFORE kindOfferMaterial's own call site")
	}

	log, perr := certify.Parse(buf.Bytes())
	if perr != nil {
		t.Fatalf("certify.Parse() error = %v", perr)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.KindOffer,
		Want:  map[string]any{"request_id": req.RequestID},
	}); err != nil {
		t.Errorf("certify KindOffer on an upstream error exit: %v", err)
	}
}

// TestCertifyConfirmedKindScopeCertifiesTheCompleteStateOnCase57 reuses
// TestResolveSubjects_ConfirmedKindScope_Case57ShapeClearsStaleGlobalTruncation's
// own fixture (chaos4154_confirmed_kind_scope_test.go) -- the exhaustive
// SearchKind pass succeeds untruncated/non-degraded with no live vector
// mechanism, so state=="complete" -- and certifies through the real
// production JSON.
func TestCertifyConfirmedKindScopeCertifiesTheCompleteStateOnCase57(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectWorkItem
	term := "widget rollout"
	subject := contextfabric.SubjectRef{Kind: kind, CanonicalID: "wi_1", Label: "Widget Rollout Backend Task"}
	node := candidateNode(kind, subject.CanonicalID, subject.Label, 0.9, "*")
	rival := candidateNode(kind, "wi_rival", "Something Else Entirely", 0.85, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {rival}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			term: {kind: {node}},
		},
		searchTruncated: true,
	}
	confirmed := &contextfabric.ConfirmedExpectedKind{Kind: kind}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := testRequest()
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, req, testInterpreted(term), deps, confirmed, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the isolated kind-scoped candidate (this test's own fixture is proven elsewhere; a change here breaks the premise)", resolution.Committed)
	}

	log, perr := certify.Parse(buf.Bytes())
	if perr != nil {
		t.Fatalf("certify.Parse() error = %v", perr)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.ConfirmedKindScope,
		Want: map[string]any{
			"request_id":      req.RequestID,
			"state":           eventspec.ConfirmedKindScopeComplete,
			"candidate_count": 1,
		},
	}); err != nil {
		t.Errorf("certify ConfirmedKindScope: %v", err)
	}
}

// TestCertifyConfirmedKindScopeCertifiesAbsentWhenConfirmedKindIsNil reuses
// TestResolveSubjects_ConfirmedKindScope_NilConfirmedKindNeverTriggers's own
// premise (CHAOS-4039 non-interference: confirmedKind==nil must leave this
// mechanism structurally unreachable) and certifies ABSENT through the real
// production JSON -- the reason ConfirmedKindScope is declared
// MultiplicityZeroOrOnePerRequest, not folded unconditional the way
// KindOffer is.
func TestCertifyConfirmedKindScopeCertifiesAbsentWhenConfirmedKindIsNil(t *testing.T) {
	t.Parallel()
	kind := contextfabric.SubjectProject
	term := "widget rollout"
	node := candidateNode(kind, "project_1", "Something Entirely Different", 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{term: {node}},
		searchTruncated:  true,
	}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := testRequest()
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, req, testInterpreted(term), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	log, perr := certify.Parse(buf.Bytes())
	if perr != nil {
		t.Fatalf("certify.Parse() error = %v", perr)
	}
	if err := certify.CertifyAbsent(log, eventspec.ConfirmedKindScope, map[string]any{"request_id": req.RequestID}); err != nil {
		t.Errorf("certify.CertifyAbsent(ConfirmedKindScope): %v", err)
	}
}

// TestCertifyIdentityGateCertifiesTheSelfCarriedBoundThroughAliasLookup
// reuses TestResolveSubjects_AliasLookupWiredEndToEnd's own fixture
// (chaos3884_identity_resolution_test.go) -- ONE alias-lookup-scoped
// (repository) candidate reaches NodeCandidate's own identity_gate
// emission, so IdentityGate certifies index=1/total=1 and
// IdentityGateSummary certifies candidate_count=1/fired_count=1.
func TestCertifyIdentityGateCertifiesTheSelfCarriedBoundThroughAliasLookup(t *testing.T) {
	t.Parallel()
	repoNode := aliasCandidateNode(contextfabric.SubjectRepository, "r1", "owner/dev-health-acr", -1, []string{"dev-health-acr"}, nil, true)
	backend := &fakeGraphBackend{
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"dev-health-acr": {repoNode}},
		aliasLookupComplete:  true,
	}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	req := testRequest()
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, req, testInterpreted("dev-health-acr"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "r1" {
		t.Fatalf("resolution.Committed = %#v, want r1 committed via AliasLookup (this fixture's own proven premise)", resolution.Committed)
	}

	log, perr := certify.Parse(buf.Bytes())
	if perr != nil {
		t.Fatalf("certify.Parse() error = %v", perr)
	}
	count, err := certify.CertifyBoundedManyCount(log, eventspec.IdentityGate, map[string]any{"request_id": req.RequestID})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(IdentityGate) error = %v", err)
	}
	if count != 1 {
		t.Fatalf("CertifyBoundedManyCount(IdentityGate) = %d, want 1", count)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.IdentityGate,
		Want: map[string]any{
			"request_id": req.RequestID, "index": 1, "total": 1,
			"from_keyed_identity_lookup": true, "alias_matched": true, "gate_fired": true,
		},
	}); err != nil {
		t.Errorf("certify IdentityGate: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.IdentityGateSummary,
		Want:  map[string]any{"request_id": req.RequestID, "candidate_count": 1, "fired_count": 1},
	}); err != nil {
		t.Errorf("certify IdentityGateSummary: %v", err)
	}
}

// TestCertifyIdentityGateSummaryCertifiesAbsentWithNoAliasLookupScopedCandidate
// drives an ORDINARY (non-alias-scoped) resolution -- no repository/
// project/team candidate ever reaches NodeCandidate's own
// isAliasLookupScopedKind gate -- and certifies BOTH IdentityGate (via
// CertifyBoundedManyCount == 0, the legitimate empty-scope shape every
// BoundedManyPerPass event allows) and IdentityGateSummary absent
// ("silence means never reached").
func TestCertifyIdentityGateSummaryCertifiesAbsentWithNoAliasLookupScopedCandidate(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.5, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"pr": {target}}}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	req := testRequest()
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, req, testInterpreted("pr"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	log, perr := certify.Parse(buf.Bytes())
	if perr != nil {
		t.Fatalf("certify.Parse() error = %v", perr)
	}
	count, err := certify.CertifyBoundedManyCount(log, eventspec.IdentityGate, map[string]any{"request_id": req.RequestID})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(IdentityGate) error = %v", err)
	}
	if count != 0 {
		t.Fatalf("CertifyBoundedManyCount(IdentityGate) = %d, want 0 -- a pull_request candidate is never alias-lookup-scoped", count)
	}
	if err := certify.CertifyAbsent(log, eventspec.IdentityGateSummary, map[string]any{"request_id": req.RequestID}); err != nil {
		t.Errorf("certify.CertifyAbsent(IdentityGateSummary): %v", err)
	}
}

// evidenceCensusFixtureBackend builds the shared evidence-census-commit
// fixture (mirrors chaos5365_rig_visibility_test.go's own
// TestSlogResolutionTracer_EvidenceCensusStagesReachInfo -- CensusFunc is
// genuinely wired in production, open.go:466) -- ONE stalled,
// confirmedKind==nil, searchTruncated resolution whose exact-hint attested
// satisfier merges and re-decides to commit. This single scenario reaches
// SEVEN of this ticket's thirteen events in one real production pass:
// KindOffer, LowPopulationKindScope(+Summary) (confirmedKind==nil,
// searchTruncated, nothing committed -- the SAME precondition that also
// gates the evidence round), EvidenceRound, EvidenceProbe,
// EvidenceCensusCommit, EvidenceSourceNative, and SliceBSurvivorVerdict(+Summary)
// (SurvivorsFirstOrder runs inside the SAME CensusFunc-gated block, after
// the evidence round, over the re-decided candidate list).
func evidenceCensusFixtureBackend() *fakeGraphBackend {
	target := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-1:532", "PR #532", 0.50, "*")
	return &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"PR 532": {target}},
		searchTruncated: true,
		exactHints: map[string]CandidateNode{
			SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:repo-1:532"}): target,
		},
	}
}

func TestCertifyEvidenceCensusFamilyAndItsSiblings(t *testing.T) {
	t.Parallel()
	backend := evidenceCensusFixtureBackend()
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	deps.CensusFunc = func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{Count: 1, SatisfierCanonicalID: "pull_request:repo-1:532"}, nil
	}
	req := testRequest()
	req.Question = "why did PR 532 fail?"
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, req, testInterpreted("PR 532"), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want exactly one committed subject via the evidence-census re-decision (this fixture's own proven premise)", resolution.Committed)
	}

	log, perr := certify.Parse(buf.Bytes())
	if perr != nil {
		t.Fatalf("certify.Parse() error = %v", perr)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.KindOffer,
		Want:  map[string]any{"request_id": req.RequestID},
	}); err != nil {
		t.Errorf("certify KindOffer: %v", err)
	}

	lpCount, err := certify.CertifyBoundedManyCount(log, eventspec.LowPopulationKindScope, map[string]any{"request_id": req.RequestID})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(LowPopulationKindScope) error = %v", err)
	}
	if lpCount != 3 {
		t.Fatalf("CertifyBoundedManyCount(LowPopulationKindScope) = %d, want 3 (repository/project/team -- chaos4417LowPopulationScopedKinds)", lpCount)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.LowPopulationKindScope,
		Want:  map[string]any{"request_id": req.RequestID, "index": 1, "total": 3, "state": eventspec.ConfirmedKindScopeNotAttempted},
	}); err != nil {
		t.Errorf("certify LowPopulationKindScope: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.LowPopulationKindScopeSummary,
		Want:  map[string]any{"request_id": req.RequestID, "outcome": "no_low_pop_candidates"},
	}); err != nil {
		t.Errorf("certify LowPopulationKindScopeSummary: %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.EvidenceRound,
		Want:  map[string]any{"request_id": req.RequestID, "shadow_outcome": "would_commit", "shadow_kinds_censused": 1},
	}); err != nil {
		t.Errorf("certify EvidenceRound: %v", err)
	}

	epCount, err := certify.CertifyBoundedManyCount(log, eventspec.EvidenceProbe, map[string]any{"request_id": req.RequestID})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(EvidenceProbe) error = %v", err)
	}
	if epCount != 1 {
		t.Fatalf("CertifyBoundedManyCount(EvidenceProbe) = %d, want 1", epCount)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.EvidenceProbe,
		Want:  map[string]any{"request_id": req.RequestID, "index": 1, "total": 1, "census_kind": string(contextfabric.SubjectPullRequest)},
	}); err != nil {
		t.Errorf("certify EvidenceProbe: %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.EvidenceCensusCommit,
		Want:  map[string]any{"request_id": req.RequestID, "outcome": "merged", "graph_existence_ok": true},
	}); err != nil {
		t.Errorf("certify EvidenceCensusCommit: %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.EvidenceSourceNative,
		Want:  map[string]any{"request_id": req.RequestID, "source_native_match_count": 0, "source_native_any_resolved": false},
	}); err != nil {
		t.Errorf("certify EvidenceSourceNative: %v", err)
	}

	sbCount, err := certify.CertifyBoundedManyCount(log, eventspec.SliceBSurvivorVerdict, map[string]any{"request_id": req.RequestID})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(SliceBSurvivorVerdict) error = %v", err)
	}
	if sbCount != 1 {
		t.Fatalf("CertifyBoundedManyCount(SliceBSurvivorVerdict) = %d, want 1", sbCount)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.SliceBSurvivorVerdict,
		Want:  map[string]any{"request_id": req.RequestID, "index": 1, "total": 1, "survivor_verdict": "neutral"},
	}); err != nil {
		t.Errorf("certify SliceBSurvivorVerdict: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.SliceBSurvivorVerdictSummary,
		Want:  map[string]any{"request_id": req.RequestID, "candidate_count": 1, "neutral_count": 1, "eliminated_count": 0},
	}); err != nil {
		t.Errorf("certify SliceBSurvivorVerdictSummary: %v", err)
	}
}

// TestCertifyEvidenceCensusCommitCertifiesAbsentWhenTheRoundNeverRuns drives
// an ORDINARY, non-stalled resolution (an exact hint commits immediately,
// deps.CensusFunc stays nil) and certifies EvidenceRound/EvidenceCensusCommit/
// EvidenceSourceNative all absent together -- the shared gating
// EvidenceCensusCommit's own doc comment describes.
func TestCertifyEvidenceCensusCommitCertifiesAbsentWhenTheRoundNeverRuns(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	var buf bytes.Buffer
	deps := backend.deps()
	deps.ResolutionTracer = NewSlogResolutionTracer(
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	req := testRequest()
	req.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, req, testInterpreted(), deps, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 {
		t.Fatalf("resolution.Committed = %#v, want the exact hint to commit immediately (this fixture's own proven premise)", resolution.Committed)
	}

	log, perr := certify.Parse(buf.Bytes())
	if perr != nil {
		t.Fatalf("certify.Parse() error = %v", perr)
	}
	for _, ev := range []eventspec.Event{eventspec.EvidenceRound, eventspec.EvidenceCensusCommit, eventspec.EvidenceSourceNative} {
		if err := certify.CertifyAbsent(log, ev, map[string]any{"request_id": req.RequestID}); err != nil {
			t.Errorf("certify.CertifyAbsent(%s): %v", ev.ID, err)
		}
	}
}

// TestCertifyEvidenceSourceNativeProbeCertifiesTheSelfCarriedBound drives
// traceSourceNativeBinds (chaos3899_evidence_round.go) DIRECTLY -- the same
// "call the unexported producer itself" convention
// TestApplyLowPopulationKindOffers_* already uses in this package -- with
// two binds (one resolved, one not), proving EvidenceSourceNativeProbe's
// own self-carried index/total bound over a NON-trivial (>1 line) scope,
// which the shared evidence-census fixture above (0 binds) cannot exercise.
func TestCertifyEvidenceSourceNativeProbeCertifiesTheSelfCarriedBound(t *testing.T) {
	t.Parallel()
	binds := []SourceNativeBind{
		{Grammar: "commit_sha", Resolved: true, Kind: contextfabric.SubjectPullRequest},
		{Grammar: "repo_slug", Resolved: false},
	}
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	traceSourceNativeBinds(tracer, "req_pr3b_source_native", binds)

	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse() error = %v", err)
	}

	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.EvidenceSourceNative,
		Want:  map[string]any{"request_id": "req_pr3b_source_native", "source_native_match_count": 2, "source_native_any_resolved": true},
	}); err != nil {
		t.Errorf("certify EvidenceSourceNative: %v", err)
	}

	count, err := certify.CertifyBoundedManyCount(log, eventspec.EvidenceSourceNativeProbe, map[string]any{"request_id": "req_pr3b_source_native"})
	if err != nil {
		t.Fatalf("CertifyBoundedManyCount(EvidenceSourceNativeProbe) error = %v", err)
	}
	if count != 2 {
		t.Fatalf("CertifyBoundedManyCount(EvidenceSourceNativeProbe) = %d, want 2", count)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.EvidenceSourceNativeProbe,
		Want: map[string]any{
			"request_id": "req_pr3b_source_native", "index": 1, "total": 2,
			"source_native_grammar": "commit_sha", "source_native_resolved": true, "source_native_kind": string(contextfabric.SubjectPullRequest),
		},
	}); err != nil {
		t.Errorf("certify EvidenceSourceNativeProbe(index=1): %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{
		Event: eventspec.EvidenceSourceNativeProbe,
		Want: map[string]any{
			"request_id": "req_pr3b_source_native", "index": 2, "total": 2,
			"source_native_grammar": "repo_slug", "source_native_resolved": false, "source_native_kind": "",
		},
	}); err != nil {
		t.Errorf("certify EvidenceSourceNativeProbe(index=2): %v", err)
	}
}
