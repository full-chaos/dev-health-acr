package graphrank

import (
	"context"
	"errors"
	"fmt"
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
	searchTruncated   bool
	traverse          func(ctx context.Context, term string, observation CandidateNode) (contextfabric.SubjectCandidate, ObservationTraversal)
	isInternal        func(contextfabric.SubjectRef) bool
	traversalDegraded []int
}

func (f *fakeGraphBackend) deps() ResolveDeps {
	isInternal := f.isInternal
	if isInternal == nil {
		isInternal = noInternalSubjects
	}
	traverse := f.traverse
	if traverse == nil {
		traverse = func(context.Context, string, CandidateNode) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationNoParent
		}
	}
	return ResolveDeps{
		ExactHint: func(ctx context.Context, subject contextfabric.SubjectRef) (CandidateNode, bool, error) {
			node, ok := f.exactHints[SubjectKey(subject)]
			return node, ok, nil
		},
		Search: func(ctx context.Context, term string, limit int) ([]CandidateNode, bool, error) {
			f.searchCalls = append(f.searchCalls, term)
			if f.searchErr != nil {
				return nil, false, f.searchErr
			}
			return f.searchResults[term], f.searchTruncated, nil
		},
		Traverse:   traverse,
		IsInternal: isInternal,
		TraversalDegraded: func(ctx context.Context, orgID string, count int) {
			f.traversalDegraded = append(f.traversalDegraded, count)
		},
	}
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
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
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
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
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
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}, request, testInterpreted(), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v, want a silent skip, not an error", err)
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
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
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
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Project B"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
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
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
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
			resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1", RepositoryScopes: tc.scopes}, testRequest(), testInterpreted("Ask Dev"), backend.deps())
			if err != nil {
				t.Fatalf("ResolveSubjects() error = %v", err)
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
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Nothing Matches This"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
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
		traverse: func(context.Context, string, CandidateNode) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationTraversalErrored
		},
	}
	if _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("readiness review"), backend.deps()); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(backend.traversalDegraded) != 1 || backend.traversalDegraded[0] != 1 {
		t.Fatalf("telemetry = %#v, want exactly one degradation report of count 1", backend.traversalDegraded)
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
		Search: func(context.Context, string, int) ([]CandidateNode, bool, error) { return nil, false, nil },
		Traverse: func(context.Context, string, CandidateNode) (contextfabric.SubjectCandidate, ObservationTraversal) {
			return contextfabric.SubjectCandidate{}, ObservationNoParent
		},
		IsInternal: noInternalSubjects,
	}
	request := testRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "workbench"}}
	if _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted(), deps); err == nil {
		t.Fatal("ResolveSubjects() error = nil, want the ExactHint backend failure propagated")
	}
}
