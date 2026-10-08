package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// A project the source marks inactive carries a closed validity window, yet
// the team still owns it: the ownership edge is open and the project is a
// member of the team's projects.
func TestDiscoverContextTeamAnchorServesOwnedProjectsWithClosedNodeWindow(t *testing.T) {
	closed := map[string]bool{"project:p-00": true, "project:p-01": true}
	base := budgetTeamFakeWith(nil, 0).queryFunc
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		rows, err := base(ctx, graphKey, cypher, params, readOnly)
		if err != nil || strings.Contains(cypher, "UNION") || strings.Contains(cypher, "fulltext") {
			return rows, err
		}
		id, _ := params["id"].(string)
		if closed[id] {
			for _, r := range rows {
				r["n"].(*node).Properties[propValidToNs] = int64(1)
				r["n"].(*node).Properties[propPropertyPrefix+"state"] = "completed"
				r["n"].(*node).Properties[propPropertyPrefix+"is_active"] = false
			}
		}
		return rows, nil
	}}
	frame := projectsOfAnchorFrame("Platform")
	cohort := discoverBudgetTeam(t, fake, frame).Cohort
	requireWholeCohort(t, cohort, "project", 14)
	for _, m := range cohort.Members {
		id := m.Subject.CanonicalID
		want := 1
		if closed[id] {
			want = 2
		}
		if len(m.InclusionReasons) != want {
			t.Fatalf("%s reasons = %v, want %d", id, m.InclusionReasons, want)
		}
		if closed[id] && m.InclusionReasons[1] != "Project state: completed; archived." {
			t.Fatalf("%s state reason = %q", id, m.InclusionReasons[1])
		}
	}
}

func TestWalkStepCypherOwnershipStepCarriesNoNodeWindow(t *testing.T) {
	temporal := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &time.Time{}})
	step := walkStep{fromKind: "team", toKind: "project", relation: "OWNED_BY_TEAM", direction: walkIn}
	if cypher := walkStepCypher(step, temporal); !strings.Contains(cypher, "b."+propValidToNs) || !strings.Contains(cypher, "r."+propValidToNs) {
		t.Fatalf("plain step must window edge and node: %s", cypher)
	}
	step.edgeWindowOnly = true
	cypher := walkStepCypher(step, temporal)
	if strings.Contains(cypher, "b."+propValidToNs) || strings.Contains(cypher, "b."+propValidFromNs) || !strings.Contains(cypher, "r."+propValidToNs) {
		t.Fatalf("edge-window-only step must window the edge alone: %s", cypher)
	}
}

// A question about an instant keeps the node window: a project not yet
// existing, or already ended, at the instant is not a member then.
func TestDiscoverContextTeamAnchorHistoricalWindowKeepsNodeWindow(t *testing.T) {
	at := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	base := budgetTeamFakeWith(nil, 0).queryFunc
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		rows, err := base(ctx, graphKey, cypher, params, readOnly)
		if err != nil || strings.Contains(cypher, "UNION") || strings.Contains(cypher, "fulltext") {
			return rows, err
		}
		if id, _ := params["id"].(string); id == "repository:r-00" {
			for _, r := range rows {
				r["n"].(*node).Properties[propValidToNs] = at.Add(-time.Hour).UnixNano()
			}
		}
		return rows, nil
	}}
	ids := cohortIDs(discoverBudgetTeamAt(t, fake, at))
	if ids["repository:r-00"] || len(ids) != 9 {
		t.Fatalf("members = %v, want the 9 repositories existing at the instant", ids)
	}
}
