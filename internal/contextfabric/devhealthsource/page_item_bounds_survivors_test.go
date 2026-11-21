package devhealthsource_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

func fanOut2Incidents(rows [][]any) fakeTable {
	return fakeTable{match: incidentsPageMatch, rows: rows, cursorOf: func(r []any) (time.Time, string) {
		return r[15].(time.Time), r[0].(string)
	}}
}

func fanOut2Dependencies(rows [][]any) fakeTable {
	return fakeTable{match: "FROM work_item_dependencies AS d", rows: rows, cursorOf: func(r []any) (time.Time, string) {
		return r[15].(time.Time), identity.JoinSegments(r[3].(string), r[0].(string), r[1].(string), r[2].(string))
	}}
}

func fanOut2Correlations(rows [][]any) fakeTable {
	return fakeTable{match: "FROM work_graph_deployment_incident_edges AS e", rows: rows, cursorOf: func(r []any) (time.Time, string) {
		return r[15].(time.Time), fmt.Sprintf("%s:%d:%s:%s", r[13], len(r[1].(string)), r[1], r[2])
	}}
}

func fanOut2Repos(at time.Time) fakeTable {
	return repoRow(judgedPageRepo(0), judgedPageSlug(0), "github", at)
}

func fanOut2Source(t *testing.T, path string) (*devhealthsource.ClickHouseProjectionSource, *judgedPageClient, contextfabric.ProjectionCheckpoint, *bytes.Buffer) {
	t.Helper()
	db := &judgedPageClient{}
	logs := new(bytes.Buffer)
	source, err := devhealthsource.NewClickHouseProjectionSource(db)
	if err != nil {
		t.Fatal(err)
	}
	source.SetClockForTest(func() time.Time { return judgedPageStamp.Add(time.Minute) })
	source.WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "91000000-0000-4000-8000-000000000001", Source: devhealthsource.SourceName}
	if path != "from_zero" {
		at := judgedPageStamp.Add(time.Minute)
		if path == "incremental" {
			at = judgedPageStamp.Add(-time.Minute)
		}
		db.tables = []fakeTable{fanOut2Repos(at)}
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil || !available {
			t.Fatalf("frontier: available=%v err=%v", available, err)
		}
		checkpoint.Cursor = batch.NextCursor
		logs.Reset()
	}
	return source, db, checkpoint, logs
}

func fanOut2Walk(t *testing.T, source *devhealthsource.ClickHouseProjectionSource, checkpoint contextfabric.ProjectionCheckpoint) []contextfabric.ProjectionBatch {
	t.Helper()
	var batches []contextfabric.ProjectionBatch
	for i, idle := 0, 0; i < 12 && idle < 3; i++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		if !available {
			idle++
			continue
		}
		idle = 0
		if batch.NextCursor == checkpoint.Cursor {
			t.Fatal("cursor did not advance")
		}
		if err := batch.Validate(); err != nil {
			t.Fatal(err)
		}
		checkpoint.Cursor = batch.NextCursor
		batches = append(batches, batch)
	}
	return batches
}

// A page whose items fit after deduplication publishes every item: the
// overflow cap counts what the batch carries, not the raw candidates.
func TestOverflowCapCountsOnlyTheItemsABatchCarries(t *testing.T) {
	for _, kind := range []string{"relationship", "tombstone"} {
		t.Run(kind, func(t *testing.T) {
			for _, path := range []string{"from_zero", "incremental", "overlap"} {
				t.Run(path, func(t *testing.T) {
					source, db, checkpoint, logs := fanOut2Source(t, path)
					at := judgedPageStamp.Add(-time.Second)
					isTombstone := kind == "tombstone"
					deps := [][]any{
						judgedPageDependency("ISSUE-A", "ISSUE-B", "blocks", true, isTombstone, at),
						judgedPageDependency("ISSUE-B", "ISSUE-A", "blocked_by", true, isTombstone, at),
					}
					var incidents [][]any
					for i := 0; i < 192; i++ {
						if isTombstone {
							incidents = append(incidents, judgedPageIncident(i, 1, 26))
						} else {
							incidents = append(incidents, judgedPageIncident(i, 26, 0))
						}
					}
					var repoID string
					if isTombstone {
						incidents = append(incidents, judgedPageIncident(192, 1, 3))
						repoID = judgedPageRepo(6002)
					} else {
						incidents = append(incidents, judgedPageIncident(192, 7, 0))
						repoID = judgedPageRepo(6)
					}
					lastID := identity.DeriveRelationship(identity.RelationshipFamilyIncidentRepository,
						"incident:"+judgedPageIncidentID(192), contextfabric.RepositoryCanonicalID(repoID), "BELONGS_TO_REPOSITORY")
					db.tables = append(db.tables, fanOut2Incidents(incidents), fanOut2Dependencies(deps))
					batches := fanOut2Walk(t, source, checkpoint)
					count, last := 0, 0
					for _, batch := range batches {
						if isTombstone {
							count += len(batch.Tombstones)
							for _, item := range batch.Tombstones {
								if item.CanonicalID == lastID {
									last++
								}
							}
						} else {
							count += len(batch.Relationships)
							for _, item := range batch.Relationships {
								if item.RelationshipID == lastID {
									last++
								}
							}
						}
					}
					drops := strings.Count(logs.String(), "quarantine_reason=contract_bound_violation item_kind="+kind)
					t.Logf("batches=%d %ss=%d want=5000 last_incident_item=%d want=1 Info-visible_bound_quarantines=%d", len(batches), kind, count, last, drops)
					if count != 5000 || last != 1 || drops != 0 {
						t.Fail()
					}
				})
			}
		})
	}
}

func fanOut2IncidentAt(row []any, at time.Time) []any {
	row[15] = at
	return row
}

func fanOut2GoodIncidents() [][]any {
	var incidents [][]any
	for i := 0; i < 192; i++ {
		incidents = append(incidents, judgedPageIncident(i, 26, 0))
	}
	return append(incidents, judgedPageIncident(192, 8, 0))
}

func fanOut2BadEndpoint(at time.Time) []any {
	return fanOut2IncidentAt(judgedPageIncident(300, 5001, 0), at)
}

func fanOut2Correlation(at time.Time) []any {
	return []any{"edge-1", "deploy-1", judgedPageIncidentID(300), judgedPageSlug(0), at, uint8(0), zeroTime,
		uint8(0), zeroTime, uint8(1), judgedPageStamp.Add(-time.Minute), uint8(0), zeroTime, judgedPageRepo(0), "native", at}
}

// A quarantined endpoint late on a page removes an earlier edge to it, so a
// shorter prefix can carry more items than the whole page. A page whose
// judged items fit is published whole, as main publishes it.
func TestAPageWhoseJudgedItemsFitIsNeverCut(t *testing.T) {
	for _, path := range []string{"from_zero", "incremental", "overlap"} {
		t.Run(path, func(t *testing.T) {
			source, db, checkpoint, logs := fanOut2Source(t, path)
			incidents := fanOut2GoodIncidents()
			for i := 193; i <= 195; i++ {
				at := judgedPageStamp.Add(time.Duration(i-192) * time.Millisecond)
				deleted := fanOut2IncidentAt(judgedPageIncident(i, 1, 0), at)
				deleted[7], deleted[8], deleted[14] = at, uint8(1), at
				incidents = append(incidents, deleted)
			}
			incidents = append(incidents, fanOut2BadEndpoint(judgedPageStamp.Add(time.Second)))
			db.tables = append(db.tables, fanOut2Incidents(incidents), fanOut2Correlations([][]any{fanOut2Correlation(judgedPageStamp.Add(-time.Second))}))
			batches := fanOut2Walk(t, source, checkpoint)
			edgeCounts, correlations := []int{}, 0
			for _, batch := range batches {
				edgeCounts = append(edgeCounts, len(batch.Relationships))
				for _, edge := range batch.Relationships {
					if edge.Type == "CORRELATED_WITH_INCIDENT" {
						correlations++
					}
				}
			}
			t.Logf("relationships_per_batch=%v want=[5000] invalid_endpoint_correlations=%d want=0 Info-visible_endpoint_quarantines=%d",
				edgeCounts, correlations, strings.Count(logs.String(), "quarantine_reason=endpoint_entity_quarantined item_kind=relationship relationship_type=CORRELATED_WITH_INCIDENT"))
			if len(edgeCounts) != 1 || edgeCounts[0] != 5000 || correlations != 0 {
				t.Fail()
			}
		})
	}
}

// The overlap re-read publishes only unseen rows inside its window edge, and
// measures exactly those.
func TestOverlapMeasuresTheRowsItPublishes(t *testing.T) {
	t.Run("already_seen_endpoint", fanOut2AlreadySeenEndpoint)
	t.Run("endpoint_beyond_window_edge", fanOut2EndpointBeyondWindowEdge)
}

func fanOut2AlreadySeenEndpoint(t *testing.T) {
	source, db, checkpoint, logs := fanOut2Source(t, "from_zero")
	badEndpoint := fanOut2BadEndpoint(judgedPageStamp.Add(-time.Second))
	db.tables = []fakeTable{fanOut2Incidents([][]any{badEndpoint}), fanOut2Repos(judgedPageStamp.Add(time.Minute))}
	seeded := false
	for i := 0; i < 6; i++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil {
			t.Fatalf("seed frontier and seen incident: %v", err)
		}
		if available {
			checkpoint.Cursor = batch.NextCursor
			if strings.Contains(logs.String(), "quarantine_reason=authorization_repositories_exceeded item_kind=entity") {
				seeded = true
				break
			}
		}
	}
	if !seeded {
		t.Fatal("the initial walk did not judge the incident and publish a frontier")
	}
	logs.Reset()

	db.tables[0] = fanOut2Incidents(append([][]any{badEndpoint}, fanOut2GoodIncidents()...))
	db.tables = append(db.tables, fanOut2Correlations([][]any{fanOut2Correlation(judgedPageStamp.Add(-2 * time.Second))}))
	fanOut2RequireGoodEdges(t, fanOut2Walk(t, source, checkpoint), logs)
}

type fanOut2ArrivalClient struct {
	judgedPageClient
	frontier time.Time
	arriving [][]any
}

func (c *fanOut2ArrivalClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if len(c.arriving) > 0 && strings.Contains(statement, incidentsPageMatch) {
		for _, binding := range bindings {
			if binding.Name == "since_us" && binding.Value.(int64) < c.frontier.UnixMicro() {
				// The paged query already read at the frontier. The new rows
				// become visible to the overlap query from behind it.
				c.tables[1].rows = append(c.tables[1].rows, c.arriving...)
				c.arriving = nil
				break
			}
		}
	}
	return c.judgedPageClient.Query(ctx, statement, bindings)
}

func fanOut2EndpointBeyondWindowEdge(t *testing.T) {
	frontier := judgedPageStamp.Add(time.Minute)
	db := &fanOut2ArrivalClient{frontier: frontier}
	db.tables = []fakeTable{fanOut2Repos(frontier)}
	source, err := devhealthsource.NewClickHouseProjectionSource(db)
	if err != nil {
		t.Fatal(err)
	}
	source.SetClockForTest(func() time.Time { return frontier.Add(3 * time.Second) })
	logs := new(bytes.Buffer)
	source.WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "91000000-0000-4000-8000-000000000001", Source: devhealthsource.SourceName}
	batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil || !available {
		t.Fatalf("frontier: available=%v err=%v", available, err)
	}
	checkpoint.Cursor = batch.NextCursor
	logs.Reset()

	db.tables = append(db.tables, fanOut2Incidents(fanOut2GoodIncidents()),
		fanOut2Correlations([][]any{fanOut2Correlation(judgedPageStamp.Add(-time.Second))}))
	db.arriving = [][]any{fanOut2BadEndpoint(frontier.Add(time.Second))}
	for i := 301; i <= 302; i++ {
		at := frontier.Add(2 * time.Second)
		deleted := fanOut2IncidentAt(judgedPageIncident(i, 1, 0), at)
		deleted[7], deleted[8], deleted[14] = at, uint8(1), at
		db.arriving = append(db.arriving, deleted)
	}
	batches := fanOut2Walk(t, source, checkpoint)
	if len(db.arriving) != 0 {
		t.Fatal("did not reach the overlap incident read")
	}
	fanOut2RequireGoodEdges(t, batches, logs)
}

func fanOut2RequireGoodEdges(t *testing.T, batches []contextfabric.ProjectionBatch, logs *bytes.Buffer) {
	t.Helper()
	lastID := identity.DeriveRelationship(identity.RelationshipFamilyIncidentRepository,
		"incident:"+judgedPageIncidentID(192), contextfabric.RepositoryCanonicalID(judgedPageRepo(7)), "BELONGS_TO_REPOSITORY")
	good, last := 0, 0
	for _, batch := range batches {
		for _, edge := range batch.Relationships {
			if edge.Type == "BELONGS_TO_REPOSITORY" {
				good++
			}
			if edge.RelationshipID == lastID {
				last++
			}
		}
	}
	drops := strings.Count(logs.String(), "quarantine_reason=contract_bound_violation item_kind=relationship")
	t.Logf("batches=%d incident_repository_edges=%d want=5000 last_incident_edge=%d want=1 Info-visible_bound_quarantines=%d",
		len(batches), good, last, drops)
	if good != 5000 || last != 1 || drops != 0 {
		t.Fail()
	}
}
