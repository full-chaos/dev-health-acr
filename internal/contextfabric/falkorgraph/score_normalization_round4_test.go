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

// TestResolveSubjectsTruncatedExactLabelMatchAutoCommits SUPERSEDES
// TestResolveSubjectsTruncatedExactLabelMatchDoesNotAutoCommit (Codex
// round-3-review escape path (a)), which asserted the exact opposite. The
// reversal is deliberate and is CHAOS-3810's blocker fix -- recorded here
// rather than by deleting the old test, so the change of ruling is visible
// to the next reader instead of looking like drift.
//
// What round 3 got right and this keeps: an inflated RELEVANCE SCORE must
// not auto-commit under truncation, because a score only ranks candidates
// against each other and truncation means an unseen competitor may have
// outranked the survivor. Escape path (b) (the merge overwrite) is
// untouched and still pinned by the sibling test below.
//
// What round 3 got wrong: it treated NodeCandidate's exact label/name match
// the same way. String equality is not a ranking -- the search term IS this
// subject's label, which no unseen row can outrank; only a row carrying the
// IDENTICAL label could genuinely compete, and uniqueness of the exact match
// is checked separately (see the duplicate-label test below, and
// graphrank.ResolveFromMergedCandidates' exactIndex comment).
//
// The cost of the round-3 reading was total: falkorgraph floor-caps every
// candidate on a truncated call, and a real 20k+ subject corpus with
// MaxSubjectCandidates=10 truncates on essentially every search, so NOTHING
// ever auto-committed on a real corpus -- every investigation reached the
// canonical fact read with zero committed subjects and 500'd.
//
// MaxSubjectCandidates=1: the query "Widget" has 2 real candidates
// (Widget, Other), so the combined query is truncated to the ONE survivor
// -- named EXACTLY "Widget", the search term itself.
func TestResolveSubjectsTruncatedExactLabelMatchAutoCommits(t *testing.T) {
	widget := fulltextRow("incident", "widget_subject", "Widget", "Widget", nil)
	other := fulltextRow("incident", "other_subject", "Other", "Other Widget", nil)
	fake := truncatingFulltextConn([]row{widget, other})
	adapter := newFakeAdapter(t, fake)
	request, interpreted := openQuestionRequest("Widget")
	request.Options.MaxSubjectCandidates = 1

	resolution, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "widget_subject" {
		t.Fatalf("ResolveSubjects(nil) committed %#v -- the unique exact label match must auto-commit even though the search truncated (CHAOS-3810)", resolution.Committed)
	}
	var widgetCandidate *contextfabric.SubjectCandidate
	for i := range resolution.Candidates {
		if resolution.Candidates[i].Subject.CanonicalID == "widget_subject" {
			widgetCandidate = &resolution.Candidates[i]
			break
		}
	}
	if widgetCandidate == nil {
		t.Fatalf("ResolveSubjects(nil) candidates = %#v, want the committed widget_subject present in the candidate list too", resolution.Candidates)
	}
	if widgetCandidate.Confidence != 1 {
		t.Fatalf("widget_subject confidence = %v, want exactly 1 (NodeCandidate's exact label-match override)", widgetCandidate.Confidence)
	}
	if widgetCandidate.State != contextfabric.ResolutionCommitted {
		t.Fatalf("widget_subject State = %v, want ResolutionCommitted", widgetCandidate.State)
	}
}

// TestResolveSubjectsTruncatedDuplicateExactLabelsStillClarify is the
// uniqueness half of the rule above: when the retained set holds TWO
// subjects whose labels both equal the term exactly, the term does not
// identify one subject and the resolution must still fall to clarification.
// Without this, "exact match commits" would degenerate into "whichever
// same-named subject the backend returned first commits".
func TestResolveSubjectsTruncatedDuplicateExactLabelsStillClarify(t *testing.T) {
	widgetA := fulltextRow("incident", "widget_subject_a", "Widget", "Widget", nil)
	widgetB := fulltextRow("incident", "widget_subject_b", "Widget", "Widget", nil)
	other := fulltextRow("incident", "other_subject", "Other", "Other Widget", nil)
	fake := truncatingFulltextConn([]row{widgetA, widgetB, other})
	adapter := newFakeAdapter(t, fake)
	request, interpreted := openQuestionRequest("Widget")
	// Both same-labelled subjects survive the LIMIT; the third row is what
	// makes the call report truncation.
	request.Options.MaxSubjectCandidates = 2

	resolution, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects(nil) committed %#v -- two subjects sharing the exact label must clarify, not commit", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("ResolveSubjects(nil) produced no clarification prompt for two identically-labelled subjects")
	}
	if len(resolution.Candidates) != 2 {
		t.Fatalf("ResolveSubjects(nil) candidates = %#v, want both identically-labelled subjects retained", resolution.Candidates)
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

	resolution, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects(nil) committed %#v -- a subject whose higher-confidence entry came from an UNtruncated call must not auto-commit when a DIFFERENT call in the SAME resolution was truncated (Codex round-3 review, escape path b: truncation is a property of the resolution, not of one candidate's score)", resolution.Committed)
	}
	// Positive assertion (Codex round-4 review): "nothing committed" alone
	// passes vacuously if the merge never actually happened (e.g. a fake
	// bug returning empty pools for both queries). merged_subject must
	// actually be PRESENT -- confirming the merge DID keep call 2's
	// higher-confidence (0.75, untruncated, full 2-of-2 coverage) entry,
	// which is exactly what makes escape path (b) a real bug to guard
	// against: this candidate's OWN confidence gives no hint that a
	// sibling call was truncated. other_subject (call 1's competitor,
	// dropped by the LIMIT trim) must NOT appear as a candidate at all --
	// confirming it was truly cut, not merely outranked.
	var mergedCandidate *contextfabric.SubjectCandidate
	for i := range resolution.Candidates {
		if resolution.Candidates[i].Subject.CanonicalID == "other_subject" {
			t.Fatalf("ResolveSubjects(nil) candidates = %#v, want other_subject (dropped by the LIMIT trim) absent, not merely un-committed", resolution.Candidates)
		}
		if resolution.Candidates[i].Subject.CanonicalID == "merged_subject" {
			mergedCandidate = &resolution.Candidates[i]
		}
	}
	if mergedCandidate == nil {
		t.Fatalf("ResolveSubjects(nil) candidates = %#v, want merged_subject present -- \"nothing committed\" must not pass vacuously because the merge produced no candidate at all", resolution.Candidates)
	}
	if mergedCandidate.Confidence != 0.75 {
		t.Fatalf("merged_subject confidence = %v, want exactly 0.75 (call 2's untruncated, full-coverage entry -- confirming the merge kept it over call 1's floor-capped 0.50 entry, which is the exact condition escape path (b) needs)", mergedCandidate.Confidence)
	}
	if mergedCandidate.State != contextfabric.ResolutionAmbiguous {
		t.Fatalf("merged_subject State = %v, want ResolutionAmbiguous", mergedCandidate.State)
	}
}
