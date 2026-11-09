package devhealthsource

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The declared vocabulary is the producer registry, not a second list.
func TestTableReadEventVocabularyIsTheProducerRegistry(t *testing.T) {
	if got, want := eventspec.DevhealthsourceTableReadTables(), NonMembershipTableNamesForTest(); !slices.Equal(got, want) {
		t.Fatalf("declared tables = %v, registry non-membership tables = %v", got, want)
	}
	if eventspec.DevhealthsourceTeamsProjectsSource != TeamsProjectsSourceName {
		t.Fatalf("declared source %q, source name %q", eventspec.DevhealthsourceTeamsProjectsSource, TeamsProjectsSourceName)
	}
}

// The lines the real paged walk and the real logger write are certified
// against their declarations: the page line of every table, the drain summary
// of every table, and the error line of a page that consumes rows not new.
func TestTableReadLinesAreCertifiedFromTheConfiguredJSONLogger(t *testing.T) {
	ledger := &presenceTelemetryLedger{}
	plan := sourcePlan{
		client: keysetRows{}, source: "page_cut_test", version: "v1",
		tables:      []entityTable{keysetTable("teams", numbered(pageCutStamp, "a", 0, 3)), keysetTable("projects", numbered(pageCutStamp, "b", 0, 2))},
		logger:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		observeRead: ledger.recordTableStatement,
		observePage: ledger.recordTableConsumed,
	}
	if _, available, err := plan.pagedBatch(context.Background(), "org", "", cursorState{}, false); err != nil || available {
		t.Fatalf("pagedBatch available=%v err=%v", available, err)
	}
	var logs bytes.Buffer
	logTableReadPages(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), "org", ledger)

	// A first read that consumes the same row twice on one page.
	at := time.Date(2026, 10, 1, 4, 37, 41, 0, time.UTC)
	dup := &presenceTelemetryLedger{}
	twice := []candidate{tableRow("team_repo_ownership", "d", at), tableRow("team_repo_ownership", "d", at)}
	dup.recordTableStatement("team_repo_ownership", twice, false)
	dup.recordTableConsumed(twice)
	logTableReadPages(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), "org", dup)

	// A statement that returned no row but says rows lie beyond its limit,
	// refused by the page bound of the real paged read.
	refusedLedger := &presenceTelemetryLedger{}
	refused := sourcePlan{
		client: keysetRows{}, source: "page_cut_test", version: "v1",
		tables: []entityTable{{name: "team_project_ownership", query: func(context.Context, contextpacket.ClickHouseQueryClient, string, cursorState, int) ([]candidate, bool, error) {
			return nil, true, nil
		}}},
		logger:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		observeRead: refusedLedger.recordTableStatement,
		observePage: refusedLedger.recordTableConsumed,
	}
	if _, _, err := refused.pagedBatch(context.Background(), "org", "", cursorState{}, false); err == nil {
		t.Fatalf("the page bound did not refuse the read")
	}
	logTableReadPages(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), "org", refusedLedger)

	parsed, err := certify.Parse(logs.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	org := redactOrg("org")
	// devhealthschema:not-a-production-replica these table names are fixture rows and expected log values; no column, type or engine is declared here.
	for _, a := range []certify.Assertion{
		{Event: eventspec.DevhealthsourceTableReadPage, Want: map[string]any{
			"org_id": org, "source": TeamsProjectsSourceName, "table": "teams", "run": 0, "page_n": 1,
			"statement_rows": 3, "statement_more": false, "consumed_rows": 3, "consumed_new": 3, "consumed_total": 3, "replay": false,
			"last_stamp": pageCutStamp.UTC().Format(time.RFC3339Nano), "last_key_digest": keyDigest("a002")}},
		{Event: eventspec.DevhealthsourceTableReadPage, Want: map[string]any{
			"org_id": org, "source": TeamsProjectsSourceName, "table": "projects", "run": 0, "page_n": 1,
			"statement_rows": 2, "statement_more": false, "consumed_rows": 2, "consumed_new": 2, "consumed_total": 2, "replay": false,
			"last_stamp": pageCutStamp.UTC().Format(time.RFC3339Nano), "last_key_digest": keyDigest("b001")}},
		{Event: eventspec.DevhealthsourceTableReadDrained, Want: map[string]any{
			"org_id": org, "source": TeamsProjectsSourceName, "table": "teams", "run": 0, "rows_total": 3, "pages": 1, "statements": 2}},
		{Event: eventspec.DevhealthsourceTableReadDrained, Want: map[string]any{
			"org_id": org, "source": TeamsProjectsSourceName, "table": "projects", "run": 0, "rows_total": 2, "pages": 1, "statements": 2}},
		{Event: eventspec.DevhealthsourceTableReadPage, Want: map[string]any{
			"org_id": org, "source": TeamsProjectsSourceName, "table": "team_project_ownership", "run": 0, "page_n": 1,
			"statement_rows": 0, "statement_more": true, "consumed_rows": 0, "consumed_new": 0, "consumed_total": 0, "replay": false,
			"last_stamp": "", "last_key_digest": ""}},
		{Event: eventspec.DevhealthsourceTableReadNotNew, Want: map[string]any{
			"org_id": org, "source": TeamsProjectsSourceName, "table": "team_repo_ownership", "run": 0, "page_n": 1,
			"consumed_rows": 2, "consumed_new": 1, "shared_position_rows": 1,
			"last_stamp": at.Format(time.RFC3339Nano), "last_key_digest": keyDigest("d")}},
	} {
		if _, err := certify.Certify(parsed, a); err != nil {
			t.Errorf("certify %s %v: %v", a.Event.ID, a.Want["table"], err)
		}
	}
}
