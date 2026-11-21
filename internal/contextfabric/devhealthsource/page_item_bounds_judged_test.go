package devhealthsource_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

type judgedPageClient struct{ fakeClient }

func (c *judgedPageClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	rows, err := c.fakeClient.Query(ctx, statement, bindings)
	if scan, ok := rows.(*fakeScanner); ok {
		for _, binding := range bindings {
			if binding.Name == "row_limit" {
				scan.rows = scan.rows[:min(len(scan.rows), int(binding.Value.(uint32)))]
				break
			}
		}
	}
	return rows, err
}

var judgedPageStamp = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

const judgedPageNoRepo = "00000000-0000-0000-0000-000000000000"

func judgedPageRepo(n int) string { return fmt.Sprintf("6c2f9a10-4b7e-4d21-9f3a-%012d", n) }
func judgedPageSlug(n int) string { return fmt.Sprintf("acme/r%05d", n) }

func judgedPageIncidentID(n int) string { return fmt.Sprintf("8e4b1d70-2c9a-4f63-b815-%012d", n) }

func judgedPageIncident(n, active, ended int) []any {
	activeRepos := make([]string, active)
	for i := range activeRepos {
		activeRepos[i] = judgedPageRepo(i) + "|" + judgedPageSlug(i)
	}
	endedRepos := make([]string, ended)
	for i := range endedRepos {
		endedRepos[i] = judgedPageRepo(6000 + i)
	}
	return []any{judgedPageIncidentID(n), activeRepos, endedRepos, "mapping", "incident", "open", "low",
		judgedPageStamp, uint8(0), uint8(0), zeroTime, uint8(0), zeroTime, "", judgedPageStamp, judgedPageStamp}
}

func judgedPageDependency(source, target, spelling string, resolved, ended bool, at time.Time) []any {
	var hasTarget, linkEnded uint8
	if resolved {
		hasTarget = 1
	}
	if ended {
		linkEnded = 1
	}
	return []any{source, target, spelling, judgedPageNoRepo, "", at, judgedPageStamp.Add(-time.Hour), uint8(0), zeroTime,
		hasTarget, judgedPageStamp.Add(-time.Hour), uint8(0), zeroTime, judgedPageNoRepo, linkEnded, at, at}
}

func judgedPageCorpus(mode, limiter string) []fakeTable {
	early, late := judgedPageStamp.Add(-time.Second), judgedPageStamp.Add(time.Second)
	deps := [][]any{judgedPageDependency("ISSUE-A", "ISSUE-B", "blocks", true, false, early)}
	n := 7
	switch mode {
	case "relationship_identity":
		deps = append(deps, judgedPageDependency("ISSUE-B", "ISSUE-A", "blocked_by", true, false, late))
	case "entity_identity":
		n = 5
		deps = append(deps,
			judgedPageDependency("ISSUE-B", "ISSUE-A", "blocked_by", true, false, early),
			judgedPageDependency("ISSUE-C", "ghpr:acme/widget#42", "blocks", false, false, early),
			judgedPageDependency("ISSUE-D", "ghpr:acme/widget#42", "blocks", false, false, late))
	case "producer_node_tombstone":
		n = 5
		deps = append(deps,
			judgedPageDependency("ISSUE-B", "ISSUE-A", "blocked_by", true, false, early),
			judgedPageDependency("ISSUE-C", "ISSUE-TARGET", "blocks", true, false, early),
			judgedPageDependency("ISSUE-D", "ISSUE-TARGET", "blocks", true, false, late))
	case "tombstone_identity":
		deps = append(deps,
			judgedPageDependency("ISSUE-C", "ISSUE-D", "blocks", true, true, early),
			judgedPageDependency("ISSUE-B", "ISSUE-A", "blocked_by", true, false, late),
			judgedPageDependency("ISSUE-D", "ISSUE-C", "blocked_by", true, true, late.Add(time.Millisecond)))
	case "rejected_endpoint":
		deps = nil
	}
	var incidents [][]any
	if limiter == "tombstone_limit" {
		rawDependencyTombstones := map[string]int{
			"relationship_identity":   4,
			"entity_identity":         4,
			"producer_node_tombstone": 7,
			"tombstone_identity":      10,
		}[mode]
		// Two removed inverse links add six raw tombstones, one of which the
		// base deduplicates. The deleted incident adds one more tombstone
		// between the bulk mapping reread and the final dependency.
		deps = append(deps,
			judgedPageDependency("ISSUE-E", "ISSUE-F", "blocks", true, true, early),
			judgedPageDependency("ISSUE-F", "ISSUE-E", "blocked_by", true, true, early))
		for i := 0; i < 191; i++ {
			incidents = append(incidents, judgedPageIncident(i, 1, 26))
		}
		incidents = append(incidents, judgedPageIncident(191, 1, 5001-191*26-rawDependencyTombstones-6-1))
		deleted := judgedPageIncident(192, 1, 0)
		deleted[7], deleted[8] = judgedPageStamp.Add(time.Millisecond), uint8(1)
		deleted[14], deleted[15] = deleted[7], deleted[7]
		incidents = append(incidents, deleted)
	} else if mode == "rejected_endpoint" {
		incidents = append(incidents, judgedPageIncident(0, 5001, 0))
	} else {
		for i := 0; i < 192; i++ {
			incidents = append(incidents, judgedPageIncident(i, 26, 0))
		}
		incidents = append(incidents, judgedPageIncident(len(incidents), n, 0))
	}

	tables := []fakeTable{{match: incidentsPageMatch, rows: incidents, cursorOf: func(r []any) (time.Time, string) {
		return r[15].(time.Time), r[0].(string)
	}}}
	if mode == "rejected_endpoint" {
		tables = append(tables, fakeTable{
			match: "FROM work_graph_deployment_incident_edges AS e",
			rows: [][]any{{"edge-1", "deploy-1", judgedPageIncidentID(0), judgedPageSlug(0), late, uint8(0), zeroTime,
				uint8(0), zeroTime, uint8(0), zeroTime, uint8(0), zeroTime, judgedPageRepo(0), "native", late}},
			cursorOf: func(r []any) (time.Time, string) {
				return r[15].(time.Time), fmt.Sprintf("%s:%d:%s:%s", r[13], len(r[1].(string)), r[1], r[2])
			},
		})
	} else {
		tables = append(tables, fakeTable{match: "FROM work_item_dependencies AS d", rows: deps, cursorOf: func(r []any) (time.Time, string) {
			return r[15].(time.Time), identity.JoinSegments(r[3].(string), r[0].(string), r[1].(string), r[2].(string))
		}})
	}
	return tables
}

// A page whose raw items pass a batch bound while the items the batch carries
// (after normalization, quarantine and deduplication) fit is not cut: an
// identity two rows share is still published once, and an edge to an incident
// quarantined on the page is still dropped with it.
func TestPageCutKeepsTheBatchJudgementOfAPageInsideTheBounds(t *testing.T) {
	for _, mode := range []string{"relationship_identity", "entity_identity", "producer_node_tombstone", "tombstone_identity", "rejected_endpoint"} {
		t.Run(mode, func(t *testing.T) {
			limiters := []string{"relationship_limit", "tombstone_limit"}
			if mode == "rejected_endpoint" {
				limiters = limiters[:1]
			}
			for _, limiter := range limiters {
				t.Run(limiter, func(t *testing.T) {
					for _, path := range []string{"from_zero", "incremental", "overlap"} {
						t.Run(path, func(t *testing.T) {
							ctx := context.Background()
							db := &judgedPageClient{}
							source, err := devhealthsource.NewClickHouseProjectionSource(db)
							if err != nil {
								t.Fatal(err)
							}
							source.SetClockForTest(func() time.Time { return judgedPageStamp.Add(time.Minute) })
							source.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
							checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "91000000-0000-4000-8000-000000000001", Source: devhealthsource.SourceName}
							if path != "from_zero" {
								at := judgedPageStamp.Add(time.Minute)
								if path == "incremental" {
									at = judgedPageStamp.Add(-time.Minute)
								}
								db.tables = []fakeTable{repoRow(judgedPageRepo(0), judgedPageSlug(0), "github", at)}
								batch, available, err := source.NextProjectionBatch(ctx, checkpoint)
								if err != nil || !available {
									t.Fatalf("frontier: available=%v err=%v", available, err)
								}
								checkpoint.Cursor = batch.NextCursor
							}
							db.tables = append(db.tables, judgedPageCorpus(mode, limiter)...)
							seen := map[string]int{}
							var batchSizes []string
							for i, idle := 0, 0; i < 12 && idle < 3; i++ {
								batch, available, err := source.NextProjectionBatch(ctx, checkpoint)
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
								batchSizes = append(batchSizes, fmt.Sprintf("e%d/r%d/t%d", len(batch.Entities), len(batch.Relationships), len(batch.Tombstones)))
								for _, entity := range batch.Entities {
									if mode == "entity_identity" && string(entity.Subject.Kind) == "work_item_ref" {
										seen[entity.Subject.CanonicalID]++
									}
								}
								for _, edge := range batch.Relationships {
									if mode == "relationship_identity" && string(edge.Type) == "BLOCKS" ||
										mode == "rejected_endpoint" && string(edge.Type) == "CORRELATED_WITH_INCIDENT" {
										seen[edge.RelationshipID]++
									}
								}
								for _, tomb := range batch.Tombstones {
									if mode == "producer_node_tombstone" && tomb.Kind == "work_item_ref" && strings.Contains(tomb.CanonicalID, "ISSUE-TARGET") ||
										mode == "tombstone_identity" && tomb.Kind == "relationship" && strings.Contains(tomb.Reason, "provider no longer reports") {
										seen[tomb.CanonicalID]++
									}
								}
							}
							maxEmissions := 0
							for _, count := range seen {
								maxEmissions = max(maxEmissions, count)
							}
							want := 1
							if mode == "rejected_endpoint" {
								want = 0
							}
							t.Logf("batches=%v tracked_identities=%d max_emissions=%d want=%d", batchSizes, len(seen), maxEmissions, want)
							if maxEmissions != want {
								t.Fail()
							}
						})
					}
				})
			}
		})
	}
}
