package contextpacket_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ingestTables names, per catalog source, the table fragment its sync-time
// statement reads; the repository row is the run stamp.
var ingestTables = map[string]string{
	"repository_freshness.v1":   "FROM repos FINAL WHERE org_id",
	"git_commits.v1":            "FROM git_commits AS c",
	"git_commit_files.v1":       "FROM git_commit_stats AS c",
	"pull_requests.v1":          "FROM git_pull_requests AS p",
	"pull_request_reviews.v1":   "FROM git_pull_request_reviews AS r",
	"ci_pipeline_runs.v1":       "FROM ci_pipeline_runs AS c",
	"deployments.v1":            "FROM deployments AS d",
	"work_items.v1":             "FROM work_items FINAL",
	"work_item_dependencies.v1": "FROM work_item_dependencies AS d",
	"work_graph.v1":             "FROM work_graph_edges FINAL",
	"incidents.v1":              "FROM operational_incidents AS i",
}

// syncStoreClient models a repository that is fully synced but quiet: every
// source's rows carry an old event time (commit, deploy, update) while the
// stored sync times are as scripted per source.
type syncStoreClient struct {
	event      time.Time
	ingest     map[string]time.Time // statement table fragment -> sync time; absent = no rows
	ingestFail map[string]bool
	ingestSeen []string
}

func (c *syncStoreClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "FROM system.columns") {
		return &rowScanner{rows: [][]any{{uint64(1)}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT max(observed_at)") {
		return &rowScanner{rows: [][]any{{c.event, uint64(3)}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT max(") {
		for source, fragment := range ingestTables {
			if !strings.Contains(statement, fragment) {
				continue
			}
			c.ingestSeen = append(c.ingestSeen, source)
			if c.ingestFail[source] {
				return nil, errors.New("sync time boom")
			}
			when, ok := c.ingest[source]
			if !ok {
				return &rowScanner{rows: [][]any{{time.Time{}, uint64(0)}}}, nil
			}
			return &rowScanner{rows: [][]any{{when, uint64(2)}}}, nil
		}
		return nil, errors.New("unexpected sync time statement")
	}
	for _, query := range contextpacket.SourceQueryCatalogV1 {
		if strings.Contains(statement, query.Statement) {
			return &rowScanner{rows: [][]any{{"acr:v1:test:1", "dev_health", "test", "1", query.ID, "", "native", 1.0, "citation", c.event}}}, nil
		}
	}
	return nil, errors.New("unexpected query")
}

func ingestPlan(t *testing.T, requestID string) contextpacket.ReadPlan {
	t.Helper()
	plan, err := contextpacket.BuildReadPlanV1(fixturePrincipal(), fixtureRequest(requestID, "main", "commit-1"))
	if err != nil {
		t.Fatal(err)
	}
	plan.RepoID = "00000000-0000-0000-0000-000000000001"
	return plan
}

func watermarkOf(t *testing.T, values []contractsv1.SourceWatermark, source string) contractsv1.SourceWatermark {
	t.Helper()
	for _, value := range values {
		if value.Source == source {
			return value
		}
	}
	t.Fatalf("no %s watermark in %+v", source, values)
	return contractsv1.SourceWatermark{}
}

func allSynced(when time.Time) map[string]time.Time {
	out := map[string]time.Time{}
	for source := range ingestTables {
		out[source] = when
	}
	return out
}

func TestQuietButSyncedRepositoryReadsFreshOnEverySyncStampedSource(t *testing.T) {
	now := time.Now().UTC()
	synced := now.Add(-time.Hour)
	client := &syncStoreClient{event: now.Add(-9 * 24 * time.Hour), ingest: allSynced(synced)}
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), ingestPlan(t, "quiet-synced"))
	if err != nil {
		t.Fatal(err)
	}
	for source := range ingestTables {
		w := watermarkOf(t, result.Watermarks, source)
		if w.Status != contractsv1.SourceWatermarkStatusFresh || w.LastIngestedAt == nil || !w.LastIngestedAt.Equal(synced) {
			t.Errorf("%s = %+v, want fresh at the sync time %v although the newest event is 9 days old", source, w, synced)
		}
		if w.LastEventAt == nil || !w.LastEventAt.Equal(client.event) {
			t.Errorf("%s last_event_at = %v, want the event time %v", source, w.LastEventAt, client.event)
		}
	}
}

func TestOldRowWriteTimeIsUnknownAndOnlyTheRepositoryRunStampGoesStale(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-3 * 24 * time.Hour)
	// The repository row was rewritten an hour ago, but it is another dataset's
	// unit: it must not lift any other source, and an old row write must not
	// be called stale (a quiet repository rewrites nothing).
	ingest := allSynced(old)
	ingest["repository_freshness.v1"] = now.Add(-time.Hour)
	client := &syncStoreClient{event: old, ingest: ingest}
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), ingestPlan(t, "old-write"))
	if err != nil {
		t.Fatal(err)
	}
	for source := range ingestTables {
		w := watermarkOf(t, result.Watermarks, source)
		if source == "repository_freshness.v1" {
			if w.Status != "fresh" {
				t.Errorf("%s = %+v, want fresh on its run stamp", source, w)
			}
			continue
		}
		if w.Status != "unknown" || w.LastIngestedAt == nil || !w.LastIngestedAt.Equal(old) {
			t.Errorf("%s = %+v, want unknown at its old write time %v, never stale and never lifted by the repository row", source, w, old)
		}
	}
}

func TestRepositoryRunStampOlderThanADayIsStale(t *testing.T) {
	now := time.Now().UTC()
	ingest := allSynced(now.Add(-time.Hour))
	ingest["repository_freshness.v1"] = now.Add(-3 * 24 * time.Hour)
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(&syncStoreClient{event: now, ingest: ingest}), ingestPlan(t, "run-stamp-stale"))
	if err != nil {
		t.Fatal(err)
	}
	if w := watermarkOf(t, result.Watermarks, "repository_freshness.v1"); w.Status != "stale" {
		t.Fatalf("repository_freshness = %+v, want stale: its run stamp is three days old", w)
	}
	if w := watermarkOf(t, result.Watermarks, "git_commits.v1"); w.Status != "fresh" {
		t.Fatalf("git_commits = %+v, want fresh on its own recent write", w)
	}
}

func TestSourcesWithoutAStoredSyncTimeAreUnknownNeverStale(t *testing.T) {
	now := time.Now().UTC()
	client := &syncStoreClient{event: now.Add(-200 * 24 * time.Hour), ingest: allSynced(now)}
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), ingestPlan(t, "no-sync-time"))
	if err != nil {
		t.Fatal(err)
	}
	derived := []string{"ai_workflow_runs.v1", "ai_workflow_artifacts.v1", "ai_review_outcomes.v1", "deployment_incident_provenance.v1", "file_hotspots.v1", "file_complexity.v1"}
	for _, source := range derived {
		w := watermarkOf(t, result.Watermarks, source)
		if w.Status != contractsv1.SourceWatermarkStatusUnknown || w.LastIngestedAt != nil {
			t.Errorf("%s = %+v, want unknown with no last_ingested_at (it stores only a compute time)", source, w)
		}
		if w.LastEventAt == nil || !w.LastEventAt.Equal(client.event) {
			t.Errorf("%s last_event_at = %v, want %v", source, w.LastEventAt, client.event)
		}
		if slices.Contains(client.ingestSeen, source) {
			t.Errorf("%s: a sync-time statement ran for a source that stores none", source)
		}
	}
}

func TestUnreadableSyncTimeIsUnknownAndLoud(t *testing.T) {
	now := time.Now().UTC()
	client := &syncStoreClient{event: now.Add(-9 * 24 * time.Hour), ingest: allSynced(now.Add(-time.Hour)), ingestFail: map[string]bool{"work_items.v1": true}}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	plan := ingestPlan(t, "ingest-boom")
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), plan)
	if err != nil {
		t.Fatal(err)
	}
	w := watermarkOf(t, result.Watermarks, "work_items.v1")
	if w.Status != "unknown" || w.LastIngestedAt != nil || w.LastEventAt == nil {
		t.Fatalf("work_items = %+v, want unknown, no last_ingested_at, event time kept (never the event time under the ingest name)", w)
	}
	if !slices.Contains(result.Warnings, "ingest_time_unreadable:work_items.v1") {
		t.Fatalf("Warnings = %v, want ingest_time_unreadable:work_items.v1", result.Warnings)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "source=work_items.v1") || !strings.Contains(out, "sync time boom") || !strings.Contains(out, "request_id="+plan.RequestID) {
		t.Fatalf("unreadable sync time not logged loudly: %s", out)
	}
}

func TestSourceWithNoRowsIsMissingNotFresh(t *testing.T) {
	now := time.Now().UTC()
	// The sync-time statements say the repository synced; the source holds no
	// rows, so the evidence read is empty and the source stays missing.
	client := &noRowsClient{syncStoreClient{event: now, ingest: allSynced(now)}}
	result, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), ingestPlan(t, "no-rows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range result.Watermarks {
		if w.Status != "missing" || w.LastIngestedAt != nil || w.LastEventAt != nil {
			t.Errorf("%+v, want missing with no times", w)
		}
	}
}

type noRowsClient struct{ syncStoreClient }

func (c *noRowsClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "FROM system.columns") {
		return &rowScanner{rows: [][]any{{uint64(1)}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT max(observed_at)") {
		return &rowScanner{rows: [][]any{{time.Time{}, uint64(0)}}}, nil
	}
	if strings.HasPrefix(statement, "SELECT max(") {
		return c.syncStoreClient.Query(ctx, statement, bindings)
	}
	return &rowScanner{}, nil
}

// bareExecutor implements only the evidence read.
type bareExecutor struct{ event time.Time }

func (e bareExecutor) QueryEvidence(_ context.Context, query contextpacket.SourceQuery, _ []contextpacket.ClickHouseBinding) ([]contractsv1.EvidenceRef, error) {
	if query.ID != "git_commits.v1" {
		return nil, nil
	}
	return []contractsv1.EvidenceRef{{ObservedAt: e.event}}, nil
}

func TestExecutorWithoutSyncTimeReadsServesUnknownNotAFreshnessVerdict(t *testing.T) {
	result, err := contextpacket.ExecuteCatalog(context.Background(), bareExecutor{event: time.Now().UTC().Add(-90 * 24 * time.Hour)}, ingestPlan(t, "bare"))
	if err != nil {
		t.Fatal(err)
	}
	w := watermarkOf(t, result.Watermarks, "git_commits.v1")
	if w.Status != "unknown" || w.LastIngestedAt != nil || w.LastEventAt == nil {
		t.Fatalf("git_commits = %+v, want unknown with only an event time", w)
	}
}

func TestSyncTimeStatementsCarryTheRequestScopeAndTimeTravel(t *testing.T) {
	var statements []string
	var bindingNames [][]string
	client := ingestRecordingClient{inner: &syncStoreClient{event: time.Now(), ingest: allSynced(time.Now())}, record: func(statement string, bindings []contextpacket.ClickHouseBinding) {
		if strings.HasPrefix(statement, "SELECT max(") && !strings.HasPrefix(statement, "SELECT max(observed_at)") {
			statements = append(statements, statement)
			names := make([]string, 0, len(bindings))
			for _, b := range bindings {
				names = append(names, b.Name)
			}
			bindingNames = append(bindingNames, names)
		}
	}}
	if _, err := contextpacket.ExecuteCatalog(context.Background(), contextpacket.NewClickHouseSourceExecutor(client), ingestPlan(t, "scope")); err != nil {
		t.Fatal(err)
	}
	if len(statements) < len(ingestTables) {
		t.Fatalf("ran %d sync-time statements, want at least one per sync-stamped source (%d)", len(statements), len(ingestTables))
	}
	for index, statement := range statements {
		for _, want := range []string{"{org_id:String}", "{as_of:Nullable(DateTime64(3, 'UTC'))} IS NULL OR", "FINAL"} {
			if !strings.Contains(statement, want) {
				t.Errorf("sync-time statement lacks %s: %s", want, statement)
			}
		}
		if !strings.Contains(statement, "{repo_id:UUID}") {
			t.Errorf("sync-time statement is not repository scoped: %s", statement)
		}
		if !slices.Contains(bindingNames[index], "org_id") || !slices.Contains(bindingNames[index], "as_of") {
			t.Errorf("sync-time statement ran without org_id/as_of bindings: %v", bindingNames[index])
		}
	}
}

type ingestRecordingClient struct {
	inner  *syncStoreClient
	record func(string, []contextpacket.ClickHouseBinding)
}

func (c ingestRecordingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.record(statement, bindings)
	return c.inner.Query(ctx, statement, bindings)
}
