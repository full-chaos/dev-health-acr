package devhealthsource_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// work_item_team_attributions keys on (repo_id, work_item_id, team_id, source),
// so one work item and team can hold several live rows: a primary row from one
// source and a co-owner row from another. The edge is one per (work item,
// team), so the producer must collapse them itself; leaving it to the batch
// pass drops the second row with a duplicate_within_batch quarantine WARN and
// loses its evidence ref.
func TestWorkItemTeamRowsOfOneTeamAreOneEdgeWithoutQuarantine(t *testing.T) {
	logged := &bytes.Buffer{}
	f := newIngestColumnsFixture(t, "94930000-0000-4000-8000-000000000001", nil,
		slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	f.team("T-A", f.old, f.old)
	at := f.hourAgo
	mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at, last_synced, ingested_at) VALUES ('linear:DUP-1', ?, ?, 'issue', 'open', '', '', 'linear', '', ?, ?, ?)`,
		zeroUUID, f.h.orgID, f.old, f.old, f.old)
	for _, row := range []struct {
		source  string
		primary uint8
	}{{"native_team", 1}, {"project_ownership", 2}} {
		mustExec(t, f.ctx, f.h.direct, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, 'linear:DUP-1', 'T-A', ?, ?, 'high', ?)`,
			f.h.orgID, zeroUUID, row.source, row.primary, at)
	}

	edges := 0
	winner := ""
	cursor := ""
	for page := 0; ; page++ {
		if page > 50 {
			t.Fatal("the drain did not converge")
		}
		b, ok, err := f.h.src.NextProjectionBatch(f.ctx, contextfabric.ProjectionCheckpoint{OrgID: f.h.orgID, Source: devhealthsource.TeamsProjectsSourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if !ok {
			break
		}
		cursor = b.NextCursor
		for _, r := range b.Relationships {
			if r.Type == contractsv1.ContextFabricRelationshipOwnedByTeam && r.From.Kind == contractsv1.ContextFabricSubjectWorkItem && r.To.Label == "T-A" {
				edges++
				if v := r.Properties["attribution_source"]; v.String != nil {
					winner = *v.String
				}
			}
		}
	}
	if edges != 1 {
		t.Fatalf("work item -> T-A edges = %d, want 1", edges)
	}
	if winner != "native_team" {
		t.Fatalf("edge attribution_source = %q, want the primary row's native_team", winner)
	}
	if strings.Contains(logged.String(), "duplicate_within_batch") {
		t.Fatalf("two attribution rows of one (work item, team) reached the batch pass as duplicates:\n%s", logged.String())
	}
}
