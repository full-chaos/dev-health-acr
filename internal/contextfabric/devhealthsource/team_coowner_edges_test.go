package devhealthsource_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type statementRecorder struct {
	inner      *fakeClient
	statements []string
}

func (r *statementRecorder) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	r.statements = append(r.statements, statement)
	return r.inner.Query(ctx, statement, bindings)
}

// The work item -> team statement reads the primary and the co-owner row and
// pages on a key that names the team, so the two rows of one work item (one
// computed_at) are two positions.
func TestWorkItemTeamStatementReadsCoOwnerRowsAndPagesOnTheTeam(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	recorder := &statementRecorder{inner: &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_team_attributions AS a FINAL", rows: [][]any{
			workItemTeamRow("wi-1", "T-A", "native_team", "high", zeroRepositoryUUID, "", at),
			workItemTeamRow("wi-1", "T-B", "project_ownership", "medium", zeroRepositoryUUID, "", at),
		}},
	}}}
	source, err := devhealthsource.NewTeamsProjectsSource(recorder, true)
	if err != nil {
		t.Fatal(err)
	}
	batch, ok, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil || !ok {
		t.Fatalf("NextProjectionBatch: ok=%v err=%v", ok, err)
	}
	var statement string
	for _, s := range recorder.statements {
		if strings.Contains(s, "FROM work_item_team_attributions AS a FINAL") {
			statement = s
		}
	}
	if !strings.Contains(statement, "a.is_primary IN (1, 2)") || strings.Contains(statement, "is_primary = 1") {
		t.Errorf("statement does not read primary and co-owner rows:\n%s", statement)
	}
	if order := statement[strings.LastIndex(statement, "ORDER BY"):]; !strings.Contains(order, "a.team_id") {
		t.Errorf("ORDER BY does not carry the team id: %s", order)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(batch.NextCursor, "="))
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(batch.NextCursor)
	}
	if err != nil {
		t.Fatalf("cursor %q: %v", batch.NextCursor, err)
	}
	var state struct{ After string }
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("cursor %s: %v", raw, err)
	}
	if !strings.Contains(state.After, "T-B") {
		t.Errorf("cursor position %q does not name the team of the last row (T-B)", state.After)
	}
}
