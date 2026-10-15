package devhealthsource_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

func TestWorkItemTeamStatementCollapsesToOneRowPerPairAndLogsTheFold(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	row := workItemTeamRow("wi-1", "T-A", "native_team", "high", zeroRepositoryUUID, "", at)
	row[len(row)-1] = uint64(3)
	recorder := &statementRecorder{inner: &fakeClient{tables: []fakeTable{
		{match: "FROM work_item_team_attributions AS a FINAL", rows: [][]any{row}},
	}}}
	source, err := devhealthsource.NewTeamsProjectsSource(recorder, true)
	if err != nil {
		t.Fatal(err)
	}
	logged := &bytes.Buffer{}
	source.WithLogger(slog.New(slog.NewTextHandler(logged, nil)))
	if _, ok, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.TeamsProjectsSourceName}); err != nil || !ok {
		t.Fatalf("NextProjectionBatch: ok=%v err=%v", ok, err)
	}
	var statement string
	for _, s := range recorder.statements {
		if strings.Contains(s, "FROM work_item_team_attributions AS a FINAL") {
			statement = s
		}
	}
	for _, want := range []string{"GROUP BY toString(w.repo_id), ifNull(r.repo, ''), a.work_item_id, ifNull(a.team_id, '')", "max(a.computed_at)", "argMinIf(tuple(toString(a.source)"} {
		if !strings.Contains(statement, want) {
			t.Errorf("statement lacks %q:\n%s", want, statement)
		}
	}
	if !strings.Contains(logged.String(), "work_item_team_rows_collapsed=2") {
		t.Errorf("no collapsed-row count in the log:\n%s", logged.String())
	}
}
