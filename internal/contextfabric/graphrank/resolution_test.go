package graphrank

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjectsCommitsDocumentOnlyQuestionWithNoCanonicalParent is the
// direct port of zepgraph's same-named test (Codex round-2 finding N1/G1
// over-correction): a document with no traversable canonical parent must
// still be able to auto-commit -- excluding every observation-kind candidate
// unconditionally would make a question genuinely ABOUT a document
// unanswerable.
func TestResolveSubjectsCommitsDocumentOnlyQuestionWithNoCanonicalParent(t *testing.T) {
	t.Parallel()
	document := observationNode("node-standalone-document", "document_5678", "Platform Postmortem", 0.9)
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Platform Postmortem": {document}}}
	// traverse defaults to ObservationNoParent (fakeGraphBackend.deps()'s
	// zero value), matching "no incoming attribution edge exists at all".
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Platform Postmortem"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "document_5678" {
		t.Fatalf("resolution.Committed = %#v, want the standalone document committed since no canonical parent exists", resolution.Committed)
	}
}

// TestResolveSubjectsRetainsSharedParentAheadOfHigherScoringObservationsUnderTightBudget
// is the direct port of zepgraph's same-named test (Codex round-3 finding
// "2", truncate-before-decide class fix): two document candidates that both
// score higher than their shared canonical parent must not crowd the parent
// out of a tight candidate budget before the parent-aware commit decision
// ever runs.
func TestResolveSubjectsRetainsSharedParentAheadOfHigherScoringObservationsUnderTightBudget(t *testing.T) {
	t.Parallel()
	parentRef := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	parentCandidate := contextfabric.SubjectCandidate{
		ReceiptID: "receipt-parent", Subject: parentRef, State: contextfabric.ResolutionProposed,
		MatchReasons: []string{"Matched an associated document or episode that references this subject."}, Confidence: 0.9 * 0.85,
	}
	doc1 := observationNode("node-doc-1", "document_1", "Ask Dev Postmortem One", 0.95)
	doc2 := observationNode("node-doc-2", "document_2", "Ask Dev Postmortem Two", 0.92)
	backend := &fakeGraphBackend{
		searchResults: map[string][]CandidateNode{"Postmortem": {doc1, doc2}},
		traverse: func(ctx context.Context, term string, observation CandidateNode) (contextfabric.SubjectCandidate, ObservationTraversal) {
			// Both documents traverse to the same shared parent.
			return parentCandidate, ObservationParentFound
		},
	}
	request := testRequest()
	request.Options.MaxSubjectCandidates = 2
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Postmortem"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_ask_dev" {
		t.Fatalf("resolution.Committed = %#v, want the shared canonical parent committed, not a fake ambiguity", resolution.Committed)
	}
	var sawParent bool
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.CanonicalID == "project_ask_dev" {
			sawParent = true
		}
	}
	if !sawParent {
		t.Fatalf("resolution.Candidates = %#v, want the committed parent present in the truncated candidate list", resolution.Candidates)
	}
}

// TestResolveSubjectsCommitsReceiptSubjectOverHybridExactMatchUnderTightBudget
// is the direct port of zepgraph's same-named test (Codex round-3 finding
// "3"): a receipt-derived committed candidate (Confidence==1, State==Committed
// from the exact-hint loop) must win a tight truncation over an unrelated
// hybrid-exact match that also reaches Confidence==1, regardless of lexical
// tie-break order.
func TestResolveSubjectsCommitsReceiptSubjectOverHybridExactMatchUnderTightBudget(t *testing.T) {
	t.Parallel()
	receiptSubject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_z", Label: "Project Z"}
	// "project_a" sorts lexically before "project_z", so a naive lexical
	// tie-break truncation would keep this one instead.
	hybridExact := candidateNode(contextfabric.SubjectProject, "project_a", "Project A", 0.3, "*")
	backend := &fakeGraphBackend{
		exactHints:    map[string]CandidateNode{SubjectKey(receiptSubject): candidateNode(receiptSubject.Kind, receiptSubject.CanonicalID, receiptSubject.Label, 1, "*")},
		searchResults: map[string][]CandidateNode{"Project A": {hybridExact}},
	}
	request := testRequest()
	request.Options.MaxSubjectCandidates = 1
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: receiptSubject.Kind, ID: receiptSubject.CanonicalID, Label: receiptSubject.Label, Source: "prior_subject_receipt"}}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Project A"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "project_z" {
		t.Fatalf("resolution.Committed = %#v, want the receipt-derived subject committed under a tight budget", resolution.Committed)
	}
}

// TestResolveSubjectsMarksCloseCandidatesAmbiguousAndOffersClarification is
// the direct port of zepgraph's same-named test: two candidates within the
// ambiguity gap must both be marked ambiguous with a non-empty clarification
// prompt, and neither auto-commits.
func TestResolveSubjectsMarksCloseCandidatesAmbiguousAndOffersClarification(t *testing.T) {
	t.Parallel()
	alpha := candidateNode(contextfabric.SubjectProject, "project_alpha", "Widget Alpha", 0.75, "*")
	beta := candidateNode(contextfabric.SubjectProject, "project_beta", "Widget Beta", 0.70, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Which widget": {alpha, beta}}}
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Which widget"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none for ambiguous candidates", resolution.Committed)
	}
	if len(resolution.Candidates) != 2 {
		t.Fatalf("resolution.Candidates = %#v, want two ambiguous candidates", resolution.Candidates)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.State != contextfabric.ResolutionAmbiguous {
			t.Fatalf("candidate state = %q, want ambiguous", candidate.State)
		}
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty for an ambiguous, clarification-allowed request")
	}
}

// TestResolveSubjectsClarificationPromptOnlyNamesRetainedCandidates is the
// direct port of zepgraph's same-named test (Codex round-4 finding 1): the
// clarification prompt must be built from the RETAINED (post-truncation)
// candidate set, never naming a subject that truncation already dropped.
func TestResolveSubjectsClarificationPromptOnlyNamesRetainedCandidates(t *testing.T) {
	t.Parallel()
	alpha := candidateNode(contextfabric.SubjectProject, "project_alpha", "Widget Alpha", 0.75, "*")
	beta := candidateNode(contextfabric.SubjectProject, "project_beta", "Widget Beta", 0.70, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Which widget": {alpha, beta}}}
	request := testRequest()
	request.Options.MaxSubjectCandidates = 1
	resolution, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("Which widget"), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Candidates) != 1 {
		t.Fatalf("resolution.Candidates = %#v, want truncated to Options.MaxSubjectCandidates=1", resolution.Candidates)
	}
	retained := make(map[string]bool, len(resolution.Candidates))
	for _, candidate := range resolution.Candidates {
		retained[candidate.Subject.Label] = true
	}
	for _, label := range []string{"Widget Alpha", "Widget Beta"} {
		mentioned := strings.Contains(resolution.ClarificationPrompt, label)
		if mentioned && !retained[label] {
			t.Fatalf("ClarificationPrompt = %q, names %q which was truncated out of resolution.Candidates = %#v", resolution.ClarificationPrompt, label, resolution.Candidates)
		}
	}
}
