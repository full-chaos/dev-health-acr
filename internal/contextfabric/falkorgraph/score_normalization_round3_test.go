package falkorgraph

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// limitFromFulltextCypher extracts the trailing "LIMIT <n>" integer from a
// fulltextSearchNodes-shaped cypher string. queries.go inlines this value
// as a literal (never a Cypher param -- see queries.go's doc comment on
// why untrusted values never take that path; this one is always the
// adapter's own bounded int), so a fake conn that wants to simulate a REAL
// server-side LIMIT's truncation behavior (Codex R2-1) has to parse it out
// of the cypher text itself, the same way a real FalkorDB server would
// apply it.
func limitFromFulltextCypher(cypher string) int {
	idx := strings.LastIndex(cypher, "LIMIT ")
	if idx == -1 {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(cypher[idx+len("LIMIT "):]))
	return n
}

// truncatingFulltextConn returns a fakeConn that behaves like a real
// FalkorDB server's own Cypher LIMIT clause would: it always has the FULL
// pool of matching rows available, and returns only the first N of them,
// where N is whatever LIMIT value the query under test actually requested
// -- so a fix that asks for more rows than the caller's own budget (to
// detect truncation) observably gets more rows back, exactly like a real
// server would.
func truncatingFulltextConn(pool []row) *fakeConn {
	return &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		n := limitFromFulltextCypher(cypher)
		if n <= 0 || n > len(pool) {
			n = len(pool)
		}
		return pool[:n], nil
	}}
}

// TestResolveSubjectsTruncatedFullCoveragePairDoesNotAutoCommit is the
// Codex R2-1 probe: the truncation trap's survivor. Two candidates both
// have FULL coverage of a 2-term query (equally strong, genuinely
// ambiguous), but MaxSubjectCandidates=1 means the server-side Cypher LIMIT
// can only ever return ONE of them to fulltextSearchNodes. Before this fix,
// fulltextSearchNodes has no way to tell "the only candidate I was ever
// shown" apart from "the only candidate that exists" -- the survivor scores
// a full-coverage 0.75 and auto-commits as an unopposed lone match, when
// the truth is an ambiguous pair that should fall to clarification.
func TestResolveSubjectsTruncatedFullCoveragePairDoesNotAutoCommit(t *testing.T) {
	candidateA := fulltextRow("incident", "candidate_a", "Payment Outage A", "Payment Outage", nil)
	candidateB := fulltextRow("incident", "candidate_b", "Payment Outage B", "Payment Outage", nil)
	fake := truncatingFulltextConn([]row{candidateA, candidateB})
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.InvestigationRequest{
		Question: "payment outage",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 1, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"payment outage"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("ResolveSubjects(nil) committed %#v from a LIMIT-truncated survivor of two equally-strong full-coverage candidates -- want no auto-commit (Codex R2-1: a truncated 'lone' result must not read as unopposed)", resolution.Committed)
	}
}

// TestResolveSubjectsUntruncatedFullCoverageLoneHitStillAutoCommits is the
// companion negative case: when the corpus genuinely has only ONE matching
// candidate (the query's own LIMIT+1 overfetch comes back with exactly one
// row, not more), that candidate must still auto-commit at full coverage --
// the truncation fix must not punish a real, unopposed lone hit.
func TestResolveSubjectsUntruncatedFullCoverageLoneHitStillAutoCommits(t *testing.T) {
	candidateA := fulltextRow("incident", "candidate_a", "Payment Outage A", "Payment Outage", nil)
	fake := truncatingFulltextConn([]row{candidateA})
	adapter := newFakeAdapter(t, fake)
	request := contextfabric.InvestigationRequest{
		Question: "payment outage",
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 1, MaxCohortMembers: 10, MaxRelationshipPaths: 10,
			MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144, AllowClarification: true,
		},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeOpen, RequestedJudgment: "status",
		SubjectTerms: []string{"payment outage"},
		TimeContext:  contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}

	resolution, _, _, err := adapter.ResolveSubjects(context.Background(), storage.Principal{OrgID: "org-1"}, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "candidate_a" {
		t.Fatalf("ResolveSubjects(nil) committed %#v, want the genuinely-unopposed, full-coverage lone hit auto-committed (truncation detection must not false-positive when nothing was actually truncated)", resolution.Committed)
	}
}

// TestFulltextMatchedTermCountTokenizerParity is the Codex R2-2 probe: a
// query term must be found inside a candidate's search_text whenever the
// two literally share that word, regardless of what punctuation happens to
// sit next to it on either side. Before the fix, fulltextMatchedTermCount
// tokenized with tokenizeForFulltext, which strips only RediSearch's own
// query-syntax punctuation ("|%@\"'*-():") -- an underscore or period left
// a compound token glued together on whichever side had one, silently
// UNDER-counting a real match (never over-counting: see
// fulltextMatchedTermCount's doc comment for why promotion cannot happen).
func TestFulltextMatchedTermCountTokenizerParity(t *testing.T) {
	cases := []struct {
		name string
		text string
		term string
		want int
	}{
		{"hyphen-compound splits on both sides (already worked pre-fix)", "payment-gateway status", "gateway", 1},
		{"underscore-compound splits (Codex R2-2: was 0 pre-fix)", "user_id lookup", "id", 1},
		{"period-compound splits (Codex R2-2: was 0 pre-fix)", "payment.gateway timeout", "gateway", 1},
		{"unicode letter stays one word, is not treated as punctuation", "café status page", "café", 1},
		{"unicode punctuation still separates (ideographic full stop)", "incident。outage report", "outage", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fulltextMatchedTermCount(tc.text, []string{tc.term}); got != tc.want {
				t.Fatalf("fulltextMatchedTermCount(%q, [%q]) = %d, want %d", tc.text, tc.term, got, tc.want)
			}
		})
	}
}
