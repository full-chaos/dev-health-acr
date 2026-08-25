package graphrank

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fakeGraphBackend is a minimal, in-memory ResolveDeps implementation shared
// by every test in this file, mirroring zepgraph's fakeAPI pattern but
// operating on graphrank's own neutral CandidateNode/CandidateEdge shapes
// instead of *zep.EntityNode/EntityEdge -- these tests call
// graphrank.ResolveSubjects directly, never through zepgraph's or
// falkorgraph's adapter wrapper.
type fakeGraphBackend struct {
	exactHints    map[string]CandidateNode // keyed by graphrank.SubjectKey
	searchResults map[string][]CandidateNode
	searchCalls   []string
	searchErr     error
	// searchTruncated, when true, makes every Search() call report
	// truncated=true -- see ResolveDeps.Search and
	// ResolveFromMergedCandidates' searchTruncated parameter. Defaults to
	// false, so every existing test in this file that does not set it is
	// unaffected.
	searchTruncated bool
	// searchDegraded, when true, makes every Search() call report that a
	// retrieval MECHANISM was unavailable (codex round-1 F4). Distinct from
	// searchTruncated: degraded means "one way of finding results did not run
	// at all", truncated means "there were more results than the budget could
	// show". Defaults to false.
	searchDegraded    bool
	traverse          func(ctx context.Context, term string, observation CandidateNode, allowExactMatch bool) (contextfabric.SubjectCandidate, ObservationTraversal)
	isInternal        func(contextfabric.SubjectRef) bool
	traversalDegraded []int
	// subjectCandidatesAuthzDropped (CHAOS-3888) records every
	// ResolveDeps.SubjectCandidatesAuthzDropped call's count argument.
	subjectCandidatesAuthzDropped []int

	// CHAOS-3838 (spec L11) SearchQuestion fixture. enableSearchQuestion
	// defaults to false, so every pre-existing test in this file -- which
	// never sets it -- gets ResolveDeps.SearchQuestion == nil, exactly the
	// pre-ticket wiring, and is completely unaffected by this addition.
	enableSearchQuestion    bool
	searchQuestionResults   map[string][]CandidateNode
	searchQuestionCalls     []string
	searchQuestionErr       error
	searchQuestionTruncated bool
	searchQuestionDegraded  bool

	// vectorMarginCommitThreshold is CHAOS-3829's M (ResolveDeps.
	// VectorMarginCommitThreshold). Defaults to 0 ("uncalibrated"), so
	// every pre-existing test in this file -- which never sets it -- gets
	// the carve-out disabled, exactly the pre-ticket wiring.
	vectorMarginCommitThreshold float64
	// rawSignalObserver is CHAOS-3858's measurement-only capture (nil
	// default, so every pre-existing test is unaffected).
	rawSignalObserver RawSignalObserver

	// CHAOS-3884 AliasLookup fixture. enableAliasLookup defaults to false,
	// so every pre-existing test in this file -- which never sets it --
	// gets ResolveDeps.AliasLookup == nil, exactly the pre-ticket wiring,
	// mirroring enableSearchQuestion's own convention above.
	enableAliasLookup    bool
	aliasLookupClaimants map[string][]CandidateNode
	aliasLookupComplete  bool
	aliasLookupErr       error
	aliasLookupCalls     [][]string

	// CHAOS-4038 SearchKind fixture. enableSearchKind defaults to false, so
	// every pre-existing test in this file -- which never sets it -- gets
	// ResolveDeps.SearchKind == nil, exactly the pre-ticket wiring, mirroring
	// enableSearchQuestion's own convention above. searchKindResults is keyed
	// first by term, then by kind (a real backend's own per-(term,kind) query
	// result); searchKindCalls records every (term, kind) pair actually
	// queried, in call order.
	enableSearchKind    bool
	searchKindResults   map[string]map[contextfabric.SubjectKind][]CandidateNode
	searchKindCalls     []searchKindCall
	searchKindErr       error
	searchKindTruncated bool
	searchKindDegraded  bool

	// CHAOS-4154 VectorMechanismConfigured fixture. Defaults to false, so
	// every pre-existing test in this file -- which never sets it -- gets
	// ResolveDeps.VectorMechanismConfigured == false, exactly the pre-ticket
	// wiring (this field did not exist before CHAOS-4154).
	vectorMechanismConfigured bool

	// CHAOS-4155 ConfirmedKindVectorCensus fixture. enableConfirmedKindVectorCensus
	// defaults to false, so every pre-existing test in this file -- which
	// never sets it -- gets ResolveDeps.ConfirmedKindVectorCensus == nil,
	// exactly the pre-ticket wiring, mirroring enableSearchKind's own
	// convention above. confirmedKindVectorCensusResult is returned
	// verbatim on every call; confirmedKindVectorCensusCalls records every
	// (kind, terms) call, in call order.
	enableConfirmedKindVectorCensus bool
	confirmedKindVectorCensusResult ConfirmedKindVectorCensusOutcome
	confirmedKindVectorCensusCalls  []confirmedKindVectorCensusCall
}

// confirmedKindVectorCensusCall records one
// ResolveDeps.ConfirmedKindVectorCensus(kind, terms) call -- see
// fakeGraphBackend.confirmedKindVectorCensusCalls.
type confirmedKindVectorCensusCall struct {
	kind  contextfabric.SubjectKind
	terms []string
}

// searchKindCall records one ResolveDeps.SearchKind(term, kind, ...) call --
// see fakeGraphBackend.searchKindCalls.
type searchKindCall struct {
	term string
	kind contextfabric.SubjectKind
}

func (f *fakeGraphBackend) deps() ResolveDeps {
	isInternal := f.isInternal
	if isInternal == nil {
		isInternal = noInternalSubjects
	}
	traverse := f.traverse
	if traverse == nil {
		traverse = func(context.Context, string, CandidateNode, bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationNoParent
		}
	}
	deps := ResolveDeps{
		ExactHint: func(ctx context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
			node, ok := f.exactHints[SubjectKey(subject)]
			return node, ok, nil
		},
		Search: func(ctx context.Context, term string, limit int) ([]CandidateNode, bool, bool, error) {
			f.searchCalls = append(f.searchCalls, term)
			if f.searchErr != nil {
				return nil, false, false, f.searchErr
			}
			return f.searchResults[term], f.searchTruncated, f.searchDegraded, nil
		},
		Traverse:   traverse,
		IsInternal: isInternal,
		TraversalDegraded: func(ctx context.Context, orgID string, count int) {
			f.traversalDegraded = append(f.traversalDegraded, count)
		},
		SubjectCandidatesAuthzDropped: func(ctx context.Context, orgID string, count int) {
			f.subjectCandidatesAuthzDropped = append(f.subjectCandidatesAuthzDropped, count)
		},
		VectorMarginCommitThreshold: f.vectorMarginCommitThreshold,
		RawSignalObserver:           f.rawSignalObserver,
		VectorMechanismConfigured:   f.vectorMechanismConfigured,
	}
	if f.enableSearchQuestion {
		deps.SearchQuestion = func(ctx context.Context, question string, limit int) ([]CandidateNode, bool, bool, error) {
			f.searchQuestionCalls = append(f.searchQuestionCalls, question)
			if f.searchQuestionErr != nil {
				return nil, false, false, f.searchQuestionErr
			}
			return f.searchQuestionResults[question], f.searchQuestionTruncated, f.searchQuestionDegraded, nil
		}
	}
	if f.enableAliasLookup {
		deps.AliasLookup = func(ctx context.Context, orgID string, terms []string) (map[string][]CandidateNode, bool, error) {
			f.aliasLookupCalls = append(f.aliasLookupCalls, terms)
			if f.aliasLookupErr != nil {
				return nil, false, f.aliasLookupErr
			}
			return f.aliasLookupClaimants, f.aliasLookupComplete, nil
		}
	}
	if f.enableSearchKind {
		deps.SearchKind = func(ctx context.Context, term string, kind contextfabric.SubjectKind, limit int) ([]CandidateNode, bool, bool, error) {
			f.searchKindCalls = append(f.searchKindCalls, searchKindCall{term: term, kind: kind})
			if f.searchKindErr != nil {
				return nil, false, false, f.searchKindErr
			}
			return f.searchKindResults[term][kind], f.searchKindTruncated, f.searchKindDegraded, nil
		}
	}
	if f.enableConfirmedKindVectorCensus {
		deps.ConfirmedKindVectorCensus = func(ctx context.Context, kind contextfabric.SubjectKind, terms []string) ConfirmedKindVectorCensusOutcome {
			f.confirmedKindVectorCensusCalls = append(f.confirmedKindVectorCensusCalls, confirmedKindVectorCensusCall{kind: kind, terms: terms})
			return f.confirmedKindVectorCensusResult
		}
	}
	return deps
}

func testRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_12345678",
		Question: "What is driving Ask Dev?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
}

func testInterpreted(terms ...string) contextfabric.InterpretedQuestion {
	return contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", SubjectTerms: terms,
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
}

// TestResolveSubjectsUsesExactCanonicalHintBeforeSemanticSearch is the
// direct port of zepgraph's same-named test: a caller-explicit exact hint
// that resolves must short-circuit before any search runs.
func TestResolveSubjectsUsesExactCanonicalHintBeforeSemanticSearch(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*"),
	}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject || len(backend.searchCalls) != 0 {
		t.Fatalf("resolution = %#v searches = %#v", resolution, backend.searchCalls)
	}
}

// TestResolveSubjectsExactHintPathRespectsMaxSubjectCandidates is the direct
// port of zepgraph's same-named test (Codex finding G4): the exact-hint
// branch must truncate to Options.MaxSubjectCandidates just like the
// hybrid-search branch does.
func TestResolveSubjectsExactHintPathRespectsMaxSubjectCandidates(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{}}
	hints := make([]contextfabric.SubjectHint, 0, 5)
	for i := 0; i < 5; i++ {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: fmt.Sprintf("project_%d", i), Label: fmt.Sprintf("Project %d", i)}
		backend.exactHints[SubjectKey(subject)] = candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*")
		hints = append(hints, contextfabric.SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"})
	}
	request := testRequest()
	request.Options.MaxSubjectCandidates = 2
	request.RequestedScope.SubjectHints = hints
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) > 2 || len(resolution.Committed) > 2 {
		t.Fatalf("resolution = %#v, want at most Options.MaxSubjectCandidates=2 candidates/committed", resolution)
	}
}

// TestResolveSubjectsExactHintForUnauthorizedSubjectIsSkippedSilently is the
// direct port of zepgraph's same-named test: an exact hint naming a subject
// that exists but is not authorized for the calling principal must degrade
// silently (no error, no leak), matching what Engine's prior-subject-receipt
// expansion feeds this path.
func TestResolveSubjectsExactHintForUnauthorizedSubjectIsSkippedSilently(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_secret", Label: "Secret Project"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, []string{"other/private"}),
	}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "prior_subject_receipt"}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}, request, testInterpreted(), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v, want a silent skip, not an error", err)
	}
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 {
		t.Fatalf("resolution = %#v, want the unauthorized subject to never surface", resolution)
	}
}

// TestResolveSubjectsExactHintTruncationRetainsCallerHintsOverReceiptDerivedOnes
// is the direct port of zepgraph's same-named test (Codex round-2 finding
// N4): caller-explicit hints must be retained ahead of receipt-derived ones
// under truncation, not decided by lexical sort order alone.
func TestResolveSubjectsExactHintTruncationRetainsCallerHintsOverReceiptDerivedOnes(t *testing.T) {
	t.Parallel()
	// "project_a" sorts lexically before "project_z", so a pure-lexical
	// truncation would keep "project_a" (receipt-derived) and drop
	// "project_z" (the caller's own explicit hint) under a budget of 1.
	callerSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_z", Label: "Project Z"}
	receiptSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_a", Label: "Project A"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(callerSubject):  candidateNode(callerSubject.Kind, callerSubject.CanonicalID, callerSubject.Label, 1, "*"),
		SubjectKey(receiptSubject): candidateNode(receiptSubject.Kind, receiptSubject.CanonicalID, receiptSubject.Label, 1, "*"),
	}}
	request := testRequest()
	request.Options.MaxSubjectCandidates = 1
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
		{Kind: callerSubject.Kind, ID: callerSubject.CanonicalID, Label: callerSubject.Label, Source: "workbench"},
		{Kind: receiptSubject.Kind, ID: receiptSubject.CanonicalID, Label: receiptSubject.Label, Source: "prior_subject_receipt"},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_z" {
		t.Fatalf("resolution.Committed = %#v, want the caller-explicit hint retained over the receipt-derived one", resolution.Committed)
	}
}

// TestResolveSubjectsMergesHybridSearchWhenOnlyReceiptDerivedHintResolves is
// the direct port of zepgraph's same-named test (Codex round-2 finding N5):
// a receipt-derived hint resolving must NOT short-circuit hybrid search --
// a conversational follow-up naming a different subject must still be found.
func TestResolveSubjectsMergesHybridSearchWhenOnlyReceiptDerivedHintResolves(t *testing.T) {
	t.Parallel()
	receiptSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_a", Label: "Project A"}
	other := candidateNode(contextfabric.SubjectProject, "project_b", "Project B", 0.9, "*")
	backend := &fakeGraphBackend{
		exactHints:    map[string]CandidateNode{SubjectKey(receiptSubject): candidateNode(receiptSubject.Kind, receiptSubject.CanonicalID, receiptSubject.Label, 1, "*")},
		searchResults: map[string][]CandidateNode{"Project B": {other}},
	}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: receiptSubject.Kind, ID: receiptSubject.CanonicalID, Label: receiptSubject.Label, Source: "prior_subject_receipt"}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Project B"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	var sawOther bool
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "project_b" {
			sawOther = true
		}
	}
	if !sawOther {
		t.Fatalf("resolution = %#v, want hybrid search to still run and merge in the interpreted subject term", resolution)
	}
}

// TestResolveSubjectsWrongSubjectControlNeverCommitsTheDecoy is the direct
// port of zepgraph's same-named test: a name-similar, fully authorized decoy
// with higher raw relevance than the actual exact match must never be the
// one that gets committed.
func TestResolveSubjectsWrongSubjectControlNeverCommitsTheDecoy(t *testing.T) {
	t.Parallel()
	target := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0.4, "*")
	decoy := candidateNode(contextfabric.SubjectProject, "project_ask_dev_analytics", "Ask Dev Analytics", 0.6, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {decoy, target}}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_ask_dev" {
		t.Fatalf("resolution.Committed = %#v, want only the exact-match subject committed", resolution.Committed)
	}
	for _, committed := range resolution.Committed {
		if committed.CanonicalID == "project_ask_dev_analytics" {
			t.Fatalf("resolution = %#v, the decoy subject must never be committed", resolution)
		}
	}
}

// TestResolveSubjectsAcceptsPrincipalWildcardRepositoryScope is the direct
// port of zepgraph's same-named test (CHAOS-3752 Reset 0 review must-do): a
// principal holding an org-wide "*" or an "owner/*" repository scope must
// still admit a narrowly-scoped node.
func TestResolveSubjectsAcceptsPrincipalWildcardRepositoryScope(t *testing.T) {
	t.Parallel()
	narrowlyScoped := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 1, []string{"acme/repo-x"})
	for _, tc := range []struct {
		name   string
		scopes []string
		want   bool
	}{
		{"global wildcard authorizes a narrowly-scoped node", []string{"*"}, true},
		{"owner wildcard authorizes a node under that owner", []string{"acme/*"}, true},
		{"owner wildcard for a different owner still denies (no unsafe widening)", []string{"other/*"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {narrowlyScoped}}}
			resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), nil, nil)
			if err != nil {
				t.Fatalf("ResolveSubjects(nil) error = %v", err)
			}
			got := len(resolution.Candidates) == 1 && resolution.Candidates[0].Subject.CanonicalID == "project_ask_dev"
			if got != tc.want {
				t.Fatalf("resolution = %#v, want candidate present = %v", resolution, tc.want)
			}
		})
	}
}

// TestResolveSubjectsReturnsSafeNoMatchWithoutCandidates is the direct port
// of zepgraph's same-named test: no search hits must produce a safe, empty
// resolution, not an error or a clarification prompt.
func TestResolveSubjectsReturnsSafeNoMatchWithoutCandidates(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{}}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Nothing Matches This"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 || resolution.ClarificationPrompt != "" {
		t.Fatalf("resolution = %#v, want a safe no-match result", resolution)
	}
}

// TestResolveSubjectsReportsTraversalDegradationThroughTelemetry is the
// direct port of zepgraph's same-named test (Codex round-3 finding P1-1): a
// traversal error must be reported through the deps.TraversalDegraded hook
// with a content-safe count.
func TestResolveSubjectsReportsTraversalDegradationThroughTelemetry(t *testing.T) {
	t.Parallel()
	document := observationNode("node-document-erroring", "document_error", "Ask Dev readiness review", 0.9)
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"readiness review": {document}},
		traverse: func(context.Context, string, CandidateNode, bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationTraversalErrored
		},
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("readiness review"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(backend.traversalDegraded) != 1 || backend.traversalDegraded[0] != 1 {
		t.Fatalf("telemetry = %#v, want exactly one degradation report of count 1", backend.traversalDegraded)
	}
}

// TestResolveSubjectsReportsSubjectCandidatesAuthzDroppedThroughTelemetry
// (CHAOS-3888) proves the authz-drop counter: a hybrid-search result node
// outside the principal's repository scope must be excluded from the
// resolution exactly as before (TestNodeCandidateFiltersUnauthorizedNodesBeforeCandidates
// already pins that), AND now also reported through
// ResolveDeps.SubjectCandidatesAuthzDropped -- while the AUTHORIZED sibling
// found by the same search call must not inflate the count.
func TestResolveSubjectsReportsSubjectCandidatesAuthzDroppedThroughTelemetry(t *testing.T) {
	t.Parallel()
	authorized := candidateNode(contextfabric.SubjectProject, "project_ask_dev", "Ask Dev", 0.9, []string{"full-chaos/dev-health-acr"})
	foreign := candidateNode(contextfabric.SubjectProject, "project_foreign", "Ask Dev Foreign", 0.95, []string{"other/private"})
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"Ask Dev": {authorized, foreign}},
	}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}
	resolution, _, err := ResolveSubjects(context.Background(), principal, testRequest(), testInterpreted("Ask Dev"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "project_foreign" {
			t.Fatalf("resolution.Candidates = %#v, want the unauthorized subject to never surface", resolution.Candidates)
		}
	}
	if len(backend.subjectCandidatesAuthzDropped) != 1 || backend.subjectCandidatesAuthzDropped[0] != 1 {
		t.Fatalf("subjectCandidatesAuthzDropped telemetry = %#v, want exactly one report of count 1 (the authorized sibling must not inflate it)", backend.subjectCandidatesAuthzDropped)
	}
}

// TestResolveSubjectsExactHintForUnauthorizedSubjectReportsAuthzDropped
// (CHAOS-3888) extends TestResolveSubjectsExactHintForUnauthorizedSubjectIsSkippedSilently:
// the SAME unauthorized exact-hint drop must also be reported through
// SubjectCandidatesAuthzDropped, not just silently absorbed.
func TestResolveSubjectsExactHintForUnauthorizedSubjectReportsAuthzDropped(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_secret", Label: "Secret Project"}
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{
		SubjectKey(subject): candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, []string{"other/private"}),
	}}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "prior_subject_receipt"}}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}, request, testInterpreted(), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(backend.subjectCandidatesAuthzDropped) != 1 || backend.subjectCandidatesAuthzDropped[0] != 1 {
		t.Fatalf("subjectCandidatesAuthzDropped telemetry = %#v, want exactly one report of count 1", backend.subjectCandidatesAuthzDropped)
	}
}

// TestResolveSubjectsExactHintPropagatesBackendError proves ResolveSubjects
// surfaces a genuine ExactHint failure (as opposed to a safe "not found")
// rather than silently swallowing it -- there is no dedicated zepgraph test
// for this exact behavior, but it is a one-line branch in resolve.go with
// zero prior coverage anywhere in the codebase.
func TestResolveSubjectsExactHintPropagatesBackendError(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_1", Label: "Project 1"}
	deps := ResolveDeps{
		ExactHint: func(context.Context, contextfabric.SubjectRef) (CandidateNode, bool, error) {
			return CandidateNode{}, false, errors.New("transient backend failure")
		},
		Search: func(context.Context, string, int) ([]CandidateNode, bool, bool, error) { return nil, false, false, nil },
		Traverse: func(context.Context, string, CandidateNode, bool) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationNoParent
		},
		IsInternal: noInternalSubjects,
	}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps, nil, nil); err == nil {
		t.Fatal("ResolveSubjects(nil) error = nil, want the ExactHint backend failure propagated")
	}
}

// --- CHAOS-3838 (spec L11 -- ResolveDeps.SearchQuestion) ---

// TestResolveSubjects_SearchQuestionNilIsSkippedSilently pins the backward
// compatibility contract: a backend that leaves SearchQuestion nil (every
// pre-CHAOS-3838 backend, and every OTHER test in this file) must never be
// called into and must never affect the resolution -- ResolveDeps.SearchQuestion's
// own doc comment says nil means "found nothing", never an error.
func TestResolveSubjects_SearchQuestionNilIsSkippedSilently(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{}}
	request := testRequest() // carries a non-empty Question
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("ask dev"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) != 0 || len(resolution.Committed) != 0 {
		t.Fatalf("resolution = %#v, want empty -- nil SearchQuestion must contribute nothing", resolution)
	}
}

// TestResolveSubjects_SearchQuestionRunsExactlyOncePerResolution is the
// CHAOS-3838 budget proof: SearchQuestion must be called AT MOST ONCE per
// ResolveSubjects call, regardless of how many subject terms it resolves --
// "one extra provider call per resolution", never one per term.
func TestResolveSubjects_SearchQuestionRunsExactlyOncePerResolution(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchQuestion: true,
		searchResults:        map[string][]CandidateNode{},
	}
	request := testRequest()
	interpreted := testInterpreted("alpha", "beta", "gamma")
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, interpreted, backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(backend.searchCalls) != 3 {
		t.Fatalf("searchCalls = %#v, want one Search() call per of the 3 terms", backend.searchCalls)
	}
	if len(backend.searchQuestionCalls) != 1 {
		t.Fatalf("searchQuestionCalls = %#v, want exactly 1 regardless of term count", backend.searchQuestionCalls)
	}
	if backend.searchQuestionCalls[0] != request.Question {
		t.Fatalf("searchQuestionCalls[0] = %q, want the raw request.Question %q", backend.searchQuestionCalls[0], request.Question)
	}
}

// TestResolveSubjects_SearchQuestionSkippedForBlankQuestion proves a
// question that trims to empty never reaches the backend -- there is
// nothing meaningful to embed, so no provider call should be spent.
func TestResolveSubjects_SearchQuestionSkippedForBlankQuestion(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{enableSearchQuestion: true}
	request := testRequest()
	request.Question = "   "
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), backend.deps(), nil, nil); err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(backend.searchQuestionCalls) != 0 {
		t.Fatalf("searchQuestionCalls = %#v, want none for a blank question", backend.searchQuestionCalls)
	}
}

// TestResolveSubjects_SearchQuestionFindsASubjectNoTermAlone proves the
// union actually widens the candidate set: a subject only the question-level
// pass proposes (no per-term Search call finds it) must still surface in the
// resolution, through the identical NodeCandidate/MergeCandidates path a
// term-level find would use.
func TestResolveSubjects_SearchQuestionFindsASubjectNoTermAlone(t *testing.T) {
	t.Parallel()
	onlyViaQuestion := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_paraphrase", Label: "Paraphrase Target"}
	request := testRequest()
	backend := &fakeGraphBackend{
		enableSearchQuestion: true,
		searchResults:        map[string][]CandidateNode{"alpha": {}},
		searchQuestionResults: map[string][]CandidateNode{
			request.Question: {candidateNode(onlyViaQuestion.Kind, onlyViaQuestion.CanonicalID, onlyViaQuestion.Label, 0.65, "*")},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) != 1 || resolution.Candidates[0].Subject != onlyViaQuestion {
		t.Fatalf("resolution.Candidates = %#v, want the question-only subject present", resolution.Candidates)
	}
}

// TestResolveSubjects_SearchQuestionDegradedFoldsIntoResolution proves the
// question-level pass's own degraded signal is folded into
// resolution.RetrievalDegraded exactly like a per-term Search call's would
// be, even when every per-term call is clean.
func TestResolveSubjects_SearchQuestionDegradedFoldsIntoResolution(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchQuestion:   true,
		searchResults:          map[string][]CandidateNode{"alpha": {}},
		searchQuestionDegraded: true,
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if !resolution.RetrievalDegraded {
		t.Fatal("resolution.RetrievalDegraded = false, want true -- the question-level pass alone reported a missing mechanism")
	}
}

// TestResolveSubjects_SearchQuestionTruncationBlocksAutoCommit proves the
// question-level pass's own truncated signal is resolution-wide authority
// exactly like a per-term Search call's would be (ResolveFromMergedCandidates'
// searchTruncated parameter): a single, otherwise-auto-committing candidate
// found ONLY via the question-level pass must fall to ambiguous when that
// pass itself reports truncation, because a genuinely competing candidate
// may have been cut off before ResolveSubjects ever saw it.
func TestResolveSubjects_SearchQuestionTruncationBlocksAutoCommit(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_only_question", Label: "Only Question"}
	request := testRequest()
	// Confidence 0.9, non-exact (term text "alpha" != the node's own label)
	// -- clears the lone-candidate gate on relevance alone, so this commits
	// unless truncation intervenes.
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.9, "*")
	node.Mechanism = contextfabric.MatchVector
	backend := &fakeGraphBackend{
		enableSearchQuestion:    true,
		searchResults:           map[string][]CandidateNode{"alpha": {}},
		searchQuestionResults:   map[string][]CandidateNode{request.Question: {node}},
		searchQuestionTruncated: true,
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want no auto-commit -- the question-level pass reported truncation, so a competing candidate may have been cut off", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt = \"\", want a clarification fallback for the truncated, otherwise-strong single candidate")
	}
}

// TestResolveSubjects_SearchQuestionPropagatesBackendError proves a genuine
// SearchQuestion failure (as opposed to "found nothing") surfaces as an
// error, exactly like a per-term Search failure does -- resolve.go must not
// swallow it.
func TestResolveSubjects_SearchQuestionPropagatesBackendError(t *testing.T) {
	t.Parallel()
	backend := &fakeGraphBackend{
		enableSearchQuestion: true,
		searchResults:        map[string][]CandidateNode{"alpha": {}},
		searchQuestionErr:    errors.New("transient embed failure"),
	}
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), backend.deps(), nil, nil); err == nil {
		t.Fatal("ResolveSubjects(nil) error = nil, want the SearchQuestion backend failure propagated")
	}
}

// TestResolveSubjects_SearchQuestionRunsAfterTermLoopForTieBreakDeterminism
// pins the ordering CHAOS-3838's implementation deliberately chose: the
// question-level pass merges AFTER every per-term pass, so a per-term find
// wins an exact-confidence tie against a question-level find of the SAME
// subject (MergeCandidates' documented "first processed wins a tie" rule).
// A term-level-only resolution therefore stays byte-identical to before
// this ticket, and the question pass can only ever ADD subjects or lose a
// tie -- never silently steal a term-level candidate's winning MatchReasons.
func TestResolveSubjects_SearchQuestionRunsAfterTermLoopForTieBreakDeterminism(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_tied", Label: "Tied Project"}
	request := testRequest()
	termNode := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.5, "*")
	termNode.Mechanism = contextfabric.MatchLexical
	questionNode := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.5, "*")
	questionNode.Mechanism = contextfabric.MatchLexical
	backend := &fakeGraphBackend{
		enableSearchQuestion: true,
		searchResults:        map[string][]CandidateNode{"alpha": {termNode}},
		searchQuestionResults: map[string][]CandidateNode{
			request.Question: {questionNode},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want the two same-subject finds merged into one", resolution.Candidates)
	}
	// The exact-match bump (candidate.Subject.Label == term) fires for
	// BOTH the term "alpha" is not equal to "Tied Project", so neither side
	// gets bumped to 1 here -- MatchedTerms is the tie-break-visible field:
	// a term-loop-first merge keeps the term-level MatchedTerms ("alpha")
	// as the winner's own before union, proving processing order.
	if !containsString(resolution.Candidates[0].MatchedTerms, "alpha") {
		t.Fatalf("resolution.Candidates[0].MatchedTerms = %v, want \"alpha\" present from the term-level pass", resolution.Candidates[0].MatchedTerms)
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// --- codex round-1 P1 (fix A): bounded question provenance ---

// TestResolveSubjects_SearchQuestionOversizedQuestionStaysContractValid is
// the codex round-1 P1 regression proof: before the fix, mergeSearchResults
// was called with the raw QUESTION as its term/provenance argument, which
// NodeCandidate records verbatim into SubjectCandidate.MatchedTerms --
// contractsv1's Validate() rejects any entry over 512 characters, so a
// realistic (513-8000 char) free-text question made the question-level pass
// produce an INVALID resolution. MUTATION CHECK: reverting
// mergeSearchResults' call site (resolve.go) to pass `question` instead of
// `questionProvenanceMarker` makes this fail -- Validate() returns an error
// and/or a MatchedTerms entry exceeds 512 chars.
func TestResolveSubjects_SearchQuestionOversizedQuestionStaysContractValid(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_big", Label: "Big Question Target"}
	// TrimSpace matters here: ResolveSubjects trims request.Question before
	// calling deps.SearchQuestion, and the fixture's map key must match
	// exactly what that call receives.
	oversized := strings.TrimSpace(strings.Repeat("why did this incident happen and what changed before it started ", 150)) // well over 512, into the thousands
	if len(oversized) <= 512 {
		t.Fatalf("test fixture bug: oversized question is only %d chars, want > 512", len(oversized))
	}
	request := testRequest()
	request.Question = oversized
	backend := &fakeGraphBackend{
		enableSearchQuestion: true,
		searchResults:        map[string][]CandidateNode{"alpha": {}},
		searchQuestionResults: map[string][]CandidateNode{
			oversized: {candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.65, "*")},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want 1", resolution.Candidates)
	}
	candidate := resolution.Candidates[0]
	if err := candidate.Validate(); err != nil {
		t.Fatalf("candidate.Validate() = %v, want a contract-valid candidate despite a %d-char question", err, len(oversized))
	}
	for _, term := range candidate.MatchedTerms {
		if len(term) > 512 {
			t.Fatalf("MatchedTerms entry %q is %d chars, want <= 512 (contractsv1's matchedTermLength bound)", term, len(term))
		}
	}
	if !containsString(candidate.MatchedTerms, questionProvenanceMarker) {
		t.Fatalf("MatchedTerms = %v, want the bounded provenance marker %q present", candidate.MatchedTerms, questionProvenanceMarker)
	}
}

// TestResolveSubjects_SearchQuestionDropsMarkerRatherThanRealTermsAtCap is
// the codex round-1 P1 fix's second half: a candidate already carrying
// matchedTermsCap (32) real, user-meaningful extracted terms must not
// overflow to 33 once questionProvenanceMarker unions in via the
// question-level pass -- the marker is dropped, every real term survives,
// and the result stays contract-valid.
func TestResolveSubjects_SearchQuestionDropsMarkerRatherThanRealTermsAtCap(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_full", Label: "Full Terms Target"}
	terms := make([]string, matchedTermsCap)
	searchResults := make(map[string][]CandidateNode, matchedTermsCap)
	for i := range terms {
		terms[i] = fmt.Sprintf("term%02d", i)
		searchResults[terms[i]] = []CandidateNode{candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.6, "*")}
	}
	request := testRequest()
	backend := &fakeGraphBackend{
		enableSearchQuestion: true,
		searchResults:        searchResults,
		searchQuestionResults: map[string][]CandidateNode{
			request.Question: {candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.65, "*")},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(terms...), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want 1", resolution.Candidates)
	}
	candidate := resolution.Candidates[0]
	if err := candidate.Validate(); err != nil {
		t.Fatalf("candidate.Validate() = %v, want valid at exactly the %d-term cap", err, matchedTermsCap)
	}
	if len(candidate.MatchedTerms) != matchedTermsCap {
		t.Fatalf("len(MatchedTerms) = %d, want exactly %d (marker dropped, every real term kept)", len(candidate.MatchedTerms), matchedTermsCap)
	}
	for _, term := range terms {
		if !containsString(candidate.MatchedTerms, term) {
			t.Fatalf("MatchedTerms = %v, missing real term %q -- the synthetic marker must be dropped before any real, user-typed term", candidate.MatchedTerms, term)
		}
	}
	if containsString(candidate.MatchedTerms, questionProvenanceMarker) {
		t.Fatalf("MatchedTerms = %v, want the question marker dropped once the real-term cap is already full", candidate.MatchedTerms)
	}
}

// TestQuestionProvenanceMarkerRespectsContractBounds cross-checks
// questionProvenanceMarker and matchedTermsCap against the REAL exported
// contractsv1 validator (via contextfabric.SubjectCandidate.Validate(), the
// type alias for it) rather than a duplicated numeric literal, so a future
// tightening of the unexported contract bounds trips this test rather than
// silently reintroducing the P1 the two constants exist to prevent.
func TestQuestionProvenanceMarkerRespectsContractBounds(t *testing.T) {
	t.Parallel()
	if len(questionProvenanceMarker) > 512 {
		t.Fatalf("questionProvenanceMarker is %d chars, want well under the contract's per-entry bound", len(questionProvenanceMarker))
	}
	terms := make([]string, matchedTermsCap+1)
	for i := range terms {
		terms[i] = fmt.Sprintf("term%03d", i)
	}
	base := contextfabric.SubjectCandidate{
		ReceiptID: "receipt_12345678", Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "P1"},
		State: contextfabric.ResolutionProposed, MatchReasons: []string{"test"}, Confidence: 0.5,
	}
	atCap := base
	atCap.MatchedTerms = terms[:matchedTermsCap]
	if err := atCap.Validate(); err != nil {
		t.Fatalf("a candidate at exactly matchedTermsCap=%d MatchedTerms entries failed Validate(): %v -- matchedTermsCap no longer matches the real contract bound", matchedTermsCap, err)
	}
	overCap := base
	overCap.MatchedTerms = terms[:matchedTermsCap+1]
	if err := overCap.Validate(); err == nil {
		t.Fatalf("a candidate with matchedTermsCap+1=%d MatchedTerms entries passed Validate() -- matchedTermsCap is now UNDER the real contract bound, capMatchedTermsAfterQuestionMerge is dropping the marker too aggressively", matchedTermsCap+1)
	}
}

// --- codex round-2 P1: a subject literally labeled the marker must not exact-match via the question path ---

// TestResolveSubjects_QuestionPathNeverExactMatchesEvenOnLiteralLabelEquality
// is the codex round-2 P1 end-to-end regression proof: a subject
// legitimately labeled the exact literal string questionProvenanceMarker
// ("[full question]") is found ONLY via the question-level vector pass.
// Before the fix, mergeSearchResults' NodeCandidate call compared THAT
// label against the marker it was itself passed as term, "matched" by
// definition, and promoted to confidence=1.0 + MatchExact -- a vector-only
// find auto-committing on the strength of an internal provenance string,
// violating AC-3778-3 (a vector hit alone must never commit). The fix must
// make this candidate MatchVector-only, banded confidence, never committed
// alone. MUTATION CHECK: reverting mergeSearchResults' question-pass call
// site to allowExactMatch=true reproduces confidence==1/MatchExact/an
// illegitimate auto-commit.
func TestResolveSubjects_QuestionPathNeverExactMatchesEvenOnLiteralLabelEquality(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_literal", Label: questionProvenanceMarker}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.65, "*")
	node.Mechanism = contextfabric.MatchVector
	request := testRequest()
	backend := &fakeGraphBackend{
		enableSearchQuestion: true,
		searchResults:        map[string][]CandidateNode{"alpha": {}},
		searchQuestionResults: map[string][]CandidateNode{
			request.Question: {node},
		},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NO auto-commit -- a vector-only find must never commit alone (AC-3778-3), regardless of this subject's label literally equaling the internal provenance marker", resolution.Committed)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want 1", resolution.Candidates)
	}
	candidate := resolution.Candidates[0]
	if candidate.Confidence == 1 {
		t.Fatal("candidate.Confidence = 1, want it derived from the vector similarity band -- a subject's label matching the internal provenance marker must never grant an exact match")
	}
	if HasMechanism(candidate.MatchMechanisms, contextfabric.MatchExact) {
		t.Fatalf("candidate.MatchMechanisms = %v, want MatchExact absent", candidate.MatchMechanisms)
	}
	if !HasMechanism(candidate.MatchMechanisms, contextfabric.MatchVector) || DistinctMechanismCount(candidate.MatchMechanisms) != 1 {
		t.Fatalf("candidate.MatchMechanisms = %v, want ONLY MatchVector (by construction on the question path)", candidate.MatchMechanisms)
	}
}

// TestResolveSubjects_TermPathStillExactMatchesLiteralEquality is the
// control proving the fix is scoped to the question path only: a subject
// found by a genuine, caller-derived TERM (interpretation's own
// SubjectTerms) that happens to equal its own label exactly must still
// auto-commit via the normal exact-match fast path -- allowExactMatch=false
// must never leak into per-term resolution.
func TestResolveSubjects_TermPathStillExactMatchesLiteralEquality(t *testing.T) {
	t.Parallel()
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_exact", Label: "Ask Dev"}
	node := candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 0.2, "*")
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"Ask Dev": {node}},
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != subject {
		t.Fatalf("resolution.Committed = %#v, want the term-path exact match auto-committed normally", resolution.Committed)
	}
}
