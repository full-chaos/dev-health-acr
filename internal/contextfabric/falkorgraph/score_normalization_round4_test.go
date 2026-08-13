package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// termIndexedTruncatingFulltextConn is like truncatingFulltextConn
// (score_normalization_round3_test.go) but keys the candidate pool by the
// exact `$query` param value too, not just the LIMIT -- needed to give two
// DIFFERENT Search() calls (one per SubjectTerms entry) their own
// independent pool, so one call can be truncated while another, for a
// different term, is not.
func termIndexedTruncatingFulltextConn(pools map[string][]row) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		pool := pools[q]
		n := limitFromFulltextCypher(cypher)
		if n <= 0 || n > len(pool) {
			n = len(pool)
		}
		return pool[:n], nil
	}}
}

// TestResolveSubjectsTruncatedExactLabelMatchDoesNotAutoCommit is the Codex
// round-3-review escape path (a) probe: graphrank.NodeCandidate forces
// Confidence to 1.0 whenever a hybrid-search hit's own label/name exactly
// (case-insensitively) equals the search term -- regardless of what
// Relevance the backend set. falkorgraph's truncation fix floor-caps
// Relevance on a truncated call (R2-1), but NodeCandidate's exact-match
// override reads that floor-capped value, sees it doesn't matter, and
// promotes to 1.0 anyway -- completely bypassing the cap.
//
// MaxSubjectCandidates=1: the query "Widget" has 2 real candidates
// (Widget, Other), so the combined query is truncated to the ONE survivor
// -- which happens to be named EXACTLY "Widget", the search term itself.
// Before the round-3-review fix, this committed at Confidence=1.0.
func TestResolveSubjectsTruncatedExactLabelMatchDoesNotAutoCommit(t *testing.T) {
	widget := fulltextRow("incident", "widget_subject", "Widget", "Widget", nil)
	other := fulltextRow("incident", "other_subject", "Other", "Other Widget", nil)
	fake := truncatingFulltextConn([]row{widget, other})
	adapter := newFakeAdapter(t, fake)
	request, interpreted := openQuestionRequest("Widget")
	request.Options.MaxSubjectCandidates = 1

	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects() committed %#v -- an exact label match surviving a TRUNCATED search must not auto-commit at Confidence=1.0 just because its name happened to equal the search term (Codex round-3 review, escape path a)", resolution.Committed)
	}
}

// TestResolveSubjectsTruncatedThenMergedCandidateDoesNotAutoCommit is the
// Codex round-3-review escape path (b) probe: candidatesBySubject's merge
// (resolve.go) keeps whichever of two same-subject entries has the HIGHER
// confidence, with no memory of whether either one came from a truncated
// call. Two DIFFERENT SubjectTerms entries both resolve to the SAME
// subject: "alpha" truncates (a real competing candidate existed and was
// dropped), keeping this subject at the floor (0.50); "alpha widget" does
// NOT truncate and finds the SAME subject again, this time at full 2-of-2
// coverage (0.75). The merge keeps only the higher-confidence 0.75 entry,
// silently erasing the fact that the "alpha" call was truncated at all.
// Before the round-3-review fix, this committed at Confidence=0.75.
func TestResolveSubjectsTruncatedThenMergedCandidateDoesNotAutoCommit(t *testing.T) {
	// Call 1 ("alpha"): 2 real candidates, MaxSubjectCandidates=1 truncates
	// to 1 survivor -- "merged_subject", floor-capped at 0.50.
	truncatedSurvivor := fulltextRow("incident", "merged_subject", "Alpha Project", "Alpha Project", nil)
	competitor := fulltextRow("incident", "other_subject", "Other", "Other alpha", nil)
	// Call 2 ("alpha widget"): the SAME subject ("merged_subject"), the
	// ONLY real candidate for this term -- not truncated, full 2-of-2
	// coverage (0.75).
	fullMatchSameSubject := fulltextRow("incident", "merged_subject", "Alpha Widget Project", "Alpha Widget Project", nil)

	fake := termIndexedTruncatingFulltextConn(map[string][]row{
		"alpha":        {truncatedSurvivor, competitor}, // 2 rows -> truncated to 1 at limit=1
		"alpha|widget": {fullMatchSameSubject},          // 1 row -> not truncated
	})
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.InvestigationRequest{
		Question: "alpha vs alpha widget",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 1, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"alpha", "alpha widget"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects() committed %#v -- a subject whose higher-confidence entry came from an UNtruncated call must not auto-commit when a DIFFERENT call in the SAME resolution was truncated (Codex round-3 review, escape path b: truncation is a property of the resolution, not of one candidate's score)", resolution.Committed)
	}
}
