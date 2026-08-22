package falkorgraph

import (
	"context"
	"fmt"
	"testing"
)

// CHAOS-4117: the pre-fix production default (MaxSubjectCandidates=10) made
// EVERY resolution against a real, subject-rich graph read as truncated --
// the root-cause finding measured search_truncated=true on 90/90 decisive
// arms -- and graphrank's searchTruncated branch (resolution.go) sits ABOVE
// the lone_floor/top_of_two statistical commit gates, short-circuiting both
// to ambiguous before they ever ran. This test pins the calibration's own
// mechanism directly against fulltextSearchNodesForResolution -- the exact
// function whose limit+1 truncation sentinel (queries.go) produces that
// signal -- with the SAME 15-subject population searched at the pre-fix
// limit (10, truncates) and the post-fix one (20, does not): the calibrated
// default is not just "wider", it is wide enough to stop truncating a
// population this size, which is what actually reopens the statistical
// gates. 20 is also the documented ceiling (RetrievalPolicy.CalibratedTopK,
// retrieval_policy.go) -- a third case just past it (21) pins that this
// test is not merely asserting "bigger limit truncates less" in general.
func TestFulltextSearchNodesForResolution_CHAOS4117CalibratedLimitRelievesTruncation(t *testing.T) {
	const population = 15
	rows := make([]row, 0, population)
	for i := 0; i < population; i++ {
		rows = append(rows, fulltextRow("work_item", fmt.Sprintf("wi_%d", i), fmt.Sprintf("Ask Dev task %d", i), "ask dev task", nil))
	}
	fake := fixedRowsFulltextConn(rows)
	adapter := newFakeAdapter(t, fake)

	tests := []struct {
		name          string
		limit         int
		wantTruncated bool
		wantCount     int
	}{
		{name: "pre_chaos4117_default_10_truncates", limit: 10, wantTruncated: true, wantCount: 10},
		{name: "post_chaos4117_default_20_does_not_truncate", limit: 20, wantTruncated: false, wantCount: population},
		{name: "past_the_calibrated_top_k_ceiling_still_relieves", limit: 21, wantTruncated: false, wantCount: population},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates, truncated, err := adapter.fulltextSearchNodesForResolution(context.Background(), "test-key", "org-1", "Ask Dev task", tt.limit, temporalFilter{})
			if err != nil {
				t.Fatalf("fulltextSearchNodesForResolution() error = %v", err)
			}
			if truncated != tt.wantTruncated {
				t.Fatalf("truncated = %v, want %v for a %d-subject population at limit=%d", truncated, tt.wantTruncated, population, tt.limit)
			}
			if len(candidates) != tt.wantCount {
				t.Fatalf("len(candidates) = %d, want %d", len(candidates), tt.wantCount)
			}
		})
	}
}
