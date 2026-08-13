package falkorgraph

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit is the Codex P1
// probe (fix round 2 of D11 / AC-3778-0): a lone fulltext hit that only
// matches ONE of several query terms must not auto-commit as if it were an
// unambiguous, high-confidence match.
//
// Before this fix: any singleton (or all-tied) result set from
// fulltextSearchNodes normalized straight to the confidence ceiling (0.75),
// regardless of how weak that lone hit's own match actually was --
// indistinguishable from a genuinely strong, fully-matching lone hit. Worse,
// this triggers even when the "lone" hit is an artifact of
// MaxSubjectCandidates truncating the result set to size 1 server-side
// (queries.go's Cypher LIMIT, applied BEFORE normalization ever runs) --
// stronger competing candidates could have existed and simply never made it
// back. 0.75 clears the >= 0.72 lone-candidate auto-commit gate in
// graphrank.ResolveFromMergedCandidates, so a weak or truncated lone hit
// committed a subject on its own.
//
// The query here has 4 OR-tokenized terms ("incident outage payment
// gateway"); the fake fulltext row matches only one of them, with a raw
// score (2.0) matching the live-observed per-matched-term contribution
// (score_normalization_live_test.go: 2 points per matched term). After the
// fix, this must normalize to a confidence well below the lone-candidate
// gate, leaving the subject uncommitted (ambiguous/no-match), not
// auto-committed.
func TestResolveSubjectsWeakLoneFulltextHitDoesNotAutoCommit(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return []row{
			{"node": &node{Properties: map[string]interface{}{propKind: "incident", propCanonicalID: "weak_hit", propLabel: "Unrelated Incident"}}, "score": 2.0},
		}, nil
	}}
	adapter := newFakeAdapter(t, fake)

	request := contextfabric.InvestigationRequest{
		Question: "incident outage payment gateway",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"incident outage payment gateway"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects() committed %#v from a lone hit matching only 1 of 4 query terms -- want no auto-commit (drift D11/P1: a weak or truncated lone hit must not read as an unambiguous match)", resolution.Committed)
	}
}
