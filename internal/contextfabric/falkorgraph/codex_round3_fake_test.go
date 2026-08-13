package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDiscoverContextResolvesPastALongRunOfFilteredCandidatesToReachAdmissibleWinners
// is the Codex P2a round-3 probe: the round-2 fix's own
// rankAndBoundCandidateEdges additionally capped the ranked candidate list
// to collectLimit*4 BEFORE resolution -- reasoning that a filtered
// candidate "doesn't consume admission budget, so it can't starve a real
// contender" was true of the ADMISSION budget but false of the PREFIX the
// cap itself imposed. Twelve candidates that will all resolve as
// edgeFiltered (their endpoint was never projected) sort ahead of three
// genuinely admissible candidates purely by UUID tie-break; with
// MaxResults=3 the old cap was 3*4=12, so exactly these 12 losers filled
// the entire pre-resolution prefix and the three winners were discarded
// before resolution ever got a chance to attempt them. The fix removes the
// prefix cap entirely: resolution now walks the complete ranked list until
// the admission budget fills or the list is exhausted, so a long run of
// filtered candidates can never itself become the starvation mechanism.
func TestDiscoverContextResolvesPastALongRunOfFilteredCandidatesToReachAdmissibleWinners(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}

	const loserCount = 12
	loserRelIDs := make([]string, loserCount)
	for i := range loserRelIDs {
		// "rel_loser_*" sorts strictly before "rel_winner_*" (l < w), so
		// every loser ranks ahead of every winner under
		// graphrank.SortEdgesByRelevance's ascending-UUID tie-break.
		loserRelIDs[i] = fmt.Sprintf("rel_loser_%02d", i)
	}
	winnerRelIDs := []string{"rel_winner_a", "rel_winner_b", "rel_winner_c"}
	winnerTargets := map[string]string{
		"rel_winner_a": "work_winner_a", "rel_winner_b": "work_winner_b", "rel_winner_c": "work_winner_c",
	}

	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			rows := make([]row, 0, loserCount+len(winnerRelIDs))
			for i, relID := range loserRelIDs {
				// The loser targets are deliberately never stubbed in the
				// nodeByKindID branch below -- resolveEdge's own `to`-node
				// lookup finds nothing (a legitimate, not-an-error miss)
				// and classifies the edge as edgeFiltered.
				rows = append(rows, row{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: relID, propEvidenceRefs: []string{"evidence_loser"}}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": fmt.Sprintf("work_loser_%02d", i),
				})
			}
			for _, relID := range winnerRelIDs {
				rows = append(rows, row{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: relID, propEvidenceRefs: []string{"evidence_" + relID}}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": winnerTargets[relID],
				})
			}
			return rows, nil
		default: // nodeByKindID
			if params["id"] == "p1" {
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			}
			for relID, target := range winnerTargets {
				if params["id"] == target {
					return []row{fakeSubjectNodeRow("work_item", target, relID)}, nil
				}
			}
			// Every work_loser_NN target is intentionally unstubbed: falls
			// through to nil, nil (not found, not an error).
			return nil, nil
		}
	}}
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 3, PoolSize: 1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	principal := storage.Principal{OrgID: "org-1"}

	result, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 3))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(result.Paths) != 3 {
		t.Fatalf("len(Paths) = %d, want exactly 3 -- a long run of filtered candidates ranked ahead of the real winners must not itself discard those winners: %#v", len(result.Paths), result.Paths)
	}
	admitted := make(map[string]bool, 3)
	for _, p := range result.Paths {
		for _, e := range p.Edges {
			admitted[e.To.CanonicalID] = true
		}
	}
	for _, target := range []string{"work_winner_a", "work_winner_b", "work_winner_c"} {
		if !admitted[target] {
			t.Fatalf("admitted targets = %v, want all three winners -- the pre-resolution prefix cap (removed) would have discarded them behind 12 filtered candidates", admitted)
		}
	}
}
