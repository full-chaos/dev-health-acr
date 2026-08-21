package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// fulltextRow builds a fake fulltext-search result row shaped the way
// fulltextSearchNodes decodes it: a "node" key carrying a search_text
// property (the same property fulltextMatchedTermCount reads client-side to
// compute matched-term coverage -- see queries.go's doc comment for why
// coverage is computed from this property rather than via additional
// FalkorDB queries), plus an optional "score" key.
func fulltextRow(kind, canonicalID, label, searchText string, score *float64) row {
	r := row{"node": &node{Properties: map[string]interface{}{
		propKind: kind, propCanonicalID: canonicalID, propLabel: label, propSearchText: searchText,
	}}}
	if score != nil {
		r["score"] = *score
	}
	return r
}

// fixedRowsFulltextConn returns a fakeConn that answers every fulltext
// query (the combined query is now the ONLY query fulltextSearchNodes
// issues -- see queries.go's fix-round-2 doc comment) with the same fixed
// set of rows, regardless of the exact query text. Matched-term coverage is
// computed entirely client-side from each row's own search_text (see
// fulltextRow), so the fake conn itself no longer needs to distinguish
// between different query param values the way an earlier version of this
// fix's fakes did.
func fixedRowsFulltextConn(rows []row) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return rows, nil
	}}
}

func openQuestionRequest(question string) (contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion) {
	request := contextfabric.InvestigationRequest{
		Question: question,
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{question},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}
	return request, interpreted
}

// TestResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit is the Codex P1
// probe (fix round 2 of D11 / AC-3778-0): a lone fulltext hit whose own
// indexed text contains only ONE of several query terms must not
// auto-commit as if it were an unambiguous, high-confidence match.
//
// Before this fix: any singleton (or all-tied) result set from
// fulltextSearchNodes normalized straight to the confidence ceiling (0.75),
// regardless of how weak that lone hit's own match actually was --
// indistinguishable from a genuinely strong, fully-matching lone hit. Worse,
// this triggers even when the "lone" hit is an artifact of
// MaxSubjectCandidates truncating the result set to size 1 server-side
// (queries.go's Cypher LIMIT, applied BEFORE normalization ever runs) --
// stronger competing candidates could have existed and simply never made it
// back. 0.75 clears the lone-candidate auto-commit gate in
// graphrank.ResolveFromMergedCandidates, so a weak or truncated lone hit
// committed a subject on its own.
//
// The query here has 4 OR-tokenized terms ("incident outage payment
// gateway"); the fake candidate's search_text is "Unrelated outage Status"
// -- only "outage" overlaps (Codex R2-4: an earlier fixture's search_text
// also contained the literal word "Incident", so it actually proved a
// 2-of-4 match, not the 1-of-4 this test's name and comments claimed --
// fixed so the stated edge is the proven edge). After the fix, this must
// normalize to a confidence well below the lone-candidate gate, leaving
// the subject uncommitted (ambiguous/no-match), not auto-committed.
func TestResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit(t *testing.T) {
	weakHit := fulltextRow("incident", "weak_hit", "Unrelated Status", "Unrelated outage Status", nil)
	fake := fixedRowsFulltextConn([]row{weakHit})
	adapter := newFakeAdapter(t, fake)
	request, interpreted := openQuestionRequest("incident outage payment gateway")

	resolution, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects(nil) committed %#v from a lone hit matching only 1 of 4 query terms -- want no auto-commit (drift D11/P1: a weak or truncated lone hit must not read as an unambiguous match)", resolution.Committed)
	}
	// Codex R2-4: pin the exact confidence, not just "didn't commit" -- a
	// 2-of-4 match would also stay under the lone-candidate gate (0.625,
	// below both the pre- and post-CHAOS-3857 gate values), so "nothing
	// committed" alone cannot distinguish the claimed 1-of-4 edge from a
	// different, weaker claim.
	if len(resolution.Candidates) != 1 {
		t.Fatalf("ResolveSubjects(nil) candidates = %#v, want exactly 1", resolution.Candidates)
	}
	const want1of4 = 0.50 + 0.25*0.25 // fulltextRelevanceFloor + span*(1/4)
	if got := resolution.Candidates[0].Confidence; got != want1of4 {
		t.Fatalf("weak hit confidence = %v, want %v (exactly a 1-of-4-term match)", got, want1of4)
	}
}

// TestResolveSubjectsFullyMatchingLoneFulltextHitStillAutoCommits is the
// companion positive case: a lone hit whose own search_text contains EVERY
// OR-tokenized query term (the strongest possible lexical signal) must
// still clear the lone-candidate gate and auto-commit -- the P1 fix
// narrows eligibility for the confidence ceiling, it does not remove the
// ceiling's reachability entirely for a genuinely unambiguous single match.
func TestResolveSubjectsFullyMatchingLoneFulltextHitStillAutoCommits(t *testing.T) {
	strongHit := fulltextRow("incident", "strong_hit", "Payment Gateway Outage Incident", "Payment Gateway Outage Incident", nil)
	fake := fixedRowsFulltextConn([]row{strongHit})
	adapter := newFakeAdapter(t, fake)
	request, interpreted := openQuestionRequest("payment gateway outage")

	resolution, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "strong_hit" {
		t.Fatalf("ResolveSubjects(nil) committed %#v, want the lone, fully-term-matching hit auto-committed", resolution.Committed)
	}
}

// TestResolveSubjectsComparesConfidenceAcrossTermsOnOneScale is the Codex P3
// probe: ResolveSubjects issues one fulltextSearchNodes call PER entry in
// SubjectTerms (resolve.go's loop), and merges/sorts the resulting
// SubjectCandidates together (resolve.go:118, resolution.go:36) as if their
// Confidence values shared one scale. Two DIFFERENT search terms here each
// produce their own independent fulltextSearchNodes call/result set: "alpha"
// (1 term, full match -- nothing else in THAT call's own result set to rank
// against) and "beta gamma delta" (3 terms, only 1 matched by its lone hit).
// Under the earlier per-call-RELATIVE normalization, "beta"'s lone,
// 1-of-3-term hit would ALSO read as that call's own ceiling (nothing else
// in ITS result set to rank below), tying it with "alpha"'s genuinely full
// match in resolution.Candidates -- an apples-to-oranges comparison that
// would show up as a tie where a strict ranking belongs. The fix's
// absolute, coverage-based scale must rank the full match strictly ABOVE
// the partial one in the merged, sorted candidate list even though they
// came from two separate, independently-executed Search() calls (and, in
// this design, independent candidates with different search_text values).
//
// This intentionally checks resolution.Candidates' order/confidence, not
// resolution.Committed: a fulltext-only ceiling (0.75) can never itself
// clear the top-of-two gate (needs >= 0.88, Codex P2 -- accepted as
// intended), so neither candidate auto-commits regardless of whether P3 is
// fixed. The auto-commit outcome is identical either way; only the ORDER
// and relative magnitude of the two confidences reveals whether the
// cross-call comparison is sound.
func TestResolveSubjectsComparesConfidenceAcrossTermsOnOneScale(t *testing.T) {
	fullMatch := fulltextRow("incident", "full_match", "Alpha Widget", "Alpha Widget", nil)
	partialMatch := fulltextRow("incident", "partial_match", "Unrelated", "Unrelated beta", nil)
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		switch q {
		case "alpha":
			return []row{fullMatch}, nil
		case "beta|gamma|delta":
			return []row{partialMatch}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.InvestigationRequest{
		Question: "alpha vs beta gamma delta",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"alpha", "beta gamma delta"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Candidates) != 2 {
		t.Fatalf("ResolveSubjects(nil) candidates = %#v, want 2 (one per independent Search() call)", resolution.Candidates)
	}
	byID := make(map[string]float64, 2)
	for _, c := range resolution.Candidates {
		byID[c.Subject.CanonicalID] = c.Confidence
	}
	full, fullOK := byID["full_match"]
	partial, partialOK := byID["partial_match"]
	if !fullOK || !partialOK {
		t.Fatalf("ResolveSubjects(nil) candidates = %#v, want both full_match and partial_match present", resolution.Candidates)
	}
	if full <= partial {
		t.Fatalf("full_match confidence (%v, from an independent 1-term query's own call) did not rank above partial_match confidence (%v, a 1-of-3-term hit from a DIFFERENT call) -- confidence is not comparable across independent Search() calls (Codex P3)", full, partial)
	}
	if resolution.Candidates[0].Subject.CanonicalID != "full_match" {
		t.Fatalf("ResolveSubjects(nil) candidates = %#v, want full_match sorted first (higher confidence)", resolution.Candidates)
	}
}

// TestResolveSubjectsNonExactFulltextPairDoesNotAutoCommit is the Codex P2
// coverage test: two competing, non-exact fulltext hits -- both fully
// matching their own respective terms, so both land at the confidence
// ceiling (0.75) -- must NOT auto-commit via the top-of-two gate (needs
// >= 0.88 with a >= 0.12 gap, both unreachable at a shared ceiling of 0.75).
// Codex ruled this a correction, not a regression: two competing lexical
// hits are genuinely ambiguous, and falling to clarification matches the
// product model (docs/design/context-fabric-falkordb-adapter.md §6.2).
func TestResolveSubjectsNonExactFulltextPairDoesNotAutoCommit(t *testing.T) {
	candidateA := fulltextRow("incident", "candidate_a", "Payment Outage Incident", "Payment Outage Incident", nil)
	candidateB := fulltextRow("incident", "candidate_b", "Gateway Failure Incident", "Gateway Failure Incident", nil)
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		q, _ := params["query"].(string)
		switch q {
		case "payment|outage":
			return []row{candidateA}, nil
		case "gateway|failure":
			return []row{candidateB}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.InvestigationRequest{
		Question: "payment outage or gateway failure",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"payment outage", "gateway failure"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects(nil) committed %#v for two equally-strong, competing non-exact fulltext hits -- want no auto-commit, want clarification (Codex P2: two competing lexical hits are genuinely ambiguous)", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("ResolveSubjects(nil) produced no ClarificationPrompt for an ambiguous two-candidate fulltext resolution")
	}
}

// TestFulltextMatchedTermCountCountsWholeCaseInsensitiveWords directly
// unit-tests the client-side coverage helper: it must count whole,
// case-insensitive word matches (not substrings), tokenizing text the same
// way the query terms themselves were tokenized.
func TestFulltextMatchedTermCountCountsWholeCaseInsensitiveWords(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"all terms present, same case", "incident outage payment", 3},
		{"all terms present, different case", "INCIDENT Outage PAYMENT", 3},
		{"one term missing", "incident outage", 2},
		{"substring is not a match", "incidental outages payments", 0},
		{"empty text", "", 0},
	}
	terms := []string{"incident", "outage", "payment"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fulltextMatchedTermCount(tc.text, terms)
			if got != tc.want {
				t.Fatalf("fulltextMatchedTermCount(%q, %v) = %d, want %d", tc.text, terms, got, tc.want)
			}
		})
	}
	// The "Dev Agent" case from the live regression
	// (TestLiveRelationshipProjectionPreservesPriorCanonicalEntityMetadata):
	// both "Dev" and "Agent" must be found even though the property value
	// is newline-joined ("Ask Dev\nAskDev\nDev Agent") and "Dev" also
	// appears as a substring of the compound word "AskDev".
	if got := fulltextMatchedTermCount("Ask Dev\nAskDev\nDev Agent", []string{"Dev", "Agent"}); got != 2 {
		t.Fatalf("fulltextMatchedTermCount(%q, %v) = %d, want 2", "Ask Dev\nAskDev\nDev Agent", []string{"Dev", "Agent"}, got)
	}
}

// TestFulltextRelevanceFromMatchedTermsEdgeCases directly unit-tests the
// normalization helper (Codex P4's requirement, adapted to this fix round's
// design: the helper takes exact integer match/term counts rather than a
// raw float score, so its edge cases are the integer-domain equivalents of
// what P4 asked of the earlier float-based version -- singleton/full match,
// zero/negative counts, and an impossible over-count -- rather than
// NaN/Inf, which cannot occur on an int).
func TestFulltextRelevanceFromMatchedTermsEdgeCases(t *testing.T) {
	const epsilon = 1e-9
	cases := []struct {
		name             string
		matchedTermCount int
		termCount        int
		want             float64
	}{
		{"full singleton match (1 of 1)", 1, 1, fulltextRelevanceCeiling},
		{"full multi-term match (4 of 4)", 4, 4, fulltextRelevanceCeiling},
		{"partial match (1 of 4)", 1, 4, 0.50 + 0.25*0.25},
		{"partial match (2 of 3)", 2, 3, 0.50 + 0.25*(2.0/3.0)},
		{"zero termCount is defensive floor, not a divide-by-zero", 1, 0, fulltextRelevanceFloor},
		{"zero matchedTermCount is floor (no coverage)", 0, 4, fulltextRelevanceFloor},
		{"negative matchedTermCount clamps to floor, not below it", -3, 4, fulltextRelevanceFloor},
		{"over-count clamps to ceiling, not above it", 9, 4, fulltextRelevanceCeiling},
		{"negative termCount is defensive floor", 1, -1, fulltextRelevanceFloor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fulltextRelevanceFromMatchedTerms(tc.matchedTermCount, tc.termCount)
			if got < fulltextRelevanceFloor-epsilon || got > fulltextRelevanceCeiling+epsilon {
				t.Fatalf("fulltextRelevanceFromMatchedTerms(%d, %d) = %v, outside documented band [%v, %v]",
					tc.matchedTermCount, tc.termCount, got, fulltextRelevanceFloor, fulltextRelevanceCeiling)
			}
			if diff := got - tc.want; diff > epsilon || diff < -epsilon {
				t.Fatalf("fulltextRelevanceFromMatchedTerms(%d, %d) = %v, want %v", tc.matchedTermCount, tc.termCount, got, tc.want)
			}
		})
	}
}
