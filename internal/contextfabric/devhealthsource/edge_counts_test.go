package devhealthsource

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type edgeCountRows struct {
	rows [][]any
	pos  int
	err  error
}

func (r *edgeCountRows) Next() bool { r.pos++; return r.pos <= len(r.rows) }
func (r *edgeCountRows) Err() error { return r.err }
func (r *edgeCountRows) Close() error {
	return nil
}
func (r *edgeCountRows) Scan(dest ...any) error {
	row := r.rows[r.pos-1]
	if len(row) != len(dest) {
		return fmt.Errorf("fake row has %d columns, scan wants %d", len(row), len(dest))
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = row[i].(string)
		case *uint64:
			*d = row[i].(uint64)
		default:
			return fmt.Errorf("unsupported scan target %T", dest[i])
		}
	}
	return nil
}

type edgeCountClient struct {
	statements []string
	bindings   [][]contextpacket.ClickHouseBinding
	// byProbe answers a statement by substring; the first match wins.
	rows map[string][][]any
	err  error
}

func (c *edgeCountClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	c.bindings = append(c.bindings, bindings)
	if c.err != nil {
		return nil, c.err
	}
	if strings.Contains(statement, "information_schema") || strings.Contains(statement, "system.columns") {
		return &edgeCountRows{rows: [][]any{{uint64(0)}}}, nil
	}
	for probe, rows := range c.rows {
		if strings.Contains(statement, probe) {
			return &edgeCountRows{rows: rows}, nil
		}
	}
	return &edgeCountRows{}, nil
}

func TestMembershipEdgeCountSQLReadsTheProducersOwnRowSource(t *testing.T) {
	t.Parallel()
	for _, ingest := range []bool{false, true} {
		statement := membershipEdgeCountSQL(ingest)
		producer := membershipStatement(cursorState{}, ingest, false)
		rows := membershipRows(cursorState{}, ingest, false)
		if !strings.HasPrefix(producer, rows) {
			t.Fatalf("ingest=%v: the producer statement must be built from membershipRows", ingest)
		}
		if !strings.Contains(statement, "FROM ("+rows+")") {
			t.Fatalf("ingest=%v: the edge count must read the producer's own row source, got:\n%s", ingest, statement)
		}
		for _, want := range []string{
			"uniqExact(subject_kind, repo_id_str, subject_id, provider, resolved_project_id, event_id)",
			"is_malformed = 0", "is_duplicate_add = 0", "key_resolution_count = 1", "source != 'column_superseded'", "GROUP BY source",
		} {
			if !strings.Contains(statement, want) {
				t.Fatalf("ingest=%v: edge count statement lacks %q:\n%s", ingest, want, statement)
			}
		}
		for _, banned := range []string{"ORDER BY observed_at", "LIMIT {row_limit", "{since_us"} {
			if strings.Contains(statement, banned) {
				t.Fatalf("ingest=%v: a count has no page or cursor (%q):\n%s", ingest, banned, statement)
			}
		}
	}
}

func TestMembershipStatementIsUnchangedByTheRowSourceSplit(t *testing.T) {
	t.Parallel()
	for _, ingest := range []bool{false, true} {
		cursorColumn := "observed_at"
		if ingest {
			cursorColumn = "ingest_at"
		}
		for _, cursor := range []cursorState{{}, {After: "k"}} {
			got := membershipStatement(cursor, ingest, true)
			want := membershipRows(cursor, ingest, true) + sincePredicate(cursor, cursorColumn, membershipRowKey()) + orderBy(cursorColumn, membershipRowKey())
			if got != want {
				t.Fatalf("ingest=%v cursor=%+v: statement drifted from rows+keyset+order", ingest, cursor)
			}
		}
		if !strings.HasSuffix(membershipStatement(cursorState{}, ingest, true), "LIMIT {row_limit:UInt32}") {
			t.Fatalf("an empty cursor adds no SETTINGS")
		}
	}
}

func TestTeamsProjectsEdgeCountsReportEveryArmAndZeroFillsTheMissingOne(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{"uniqExact(subject_kind": {{"transition", uint64(6756)}}}}
	source, err := NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	transition := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmTransition}
	column := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmWorkItemColumn}
	if len(counts) != 2 || counts[transition] != 6756 {
		t.Fatalf("both arms must be present: %+v", counts)
	}
	if v, ok := counts[column]; !ok || v != 0 {
		t.Fatalf("an arm with no rows is a measured zero, not a missing key: %+v", counts)
	}
	last := client.bindings[len(client.bindings)-1]
	if len(last) != 1 || last[0].Name != "org_id" || last[0].Value != "org-a" {
		t.Fatalf("the organization is bound, never interpolated: %+v", last)
	}
}

func TestTeamsProjectsEdgeCountsFailLoudly(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		client  *edgeCountClient
		org     string
		wantErr string
	}{
		"unknown arm": {client: &edgeCountClient{rows: map[string][][]any{"uniqExact(subject_kind": {{"mystery", uint64(1)}}}}, org: "org-a", wantErr: `unknown arm "mystery"`},
		"query error": {client: &edgeCountClient{rows: map[string][][]any{}, err: errors.New("clickhouse down")}, org: "org-a", wantErr: "clickhouse down"},
		"blank org":   {client: &edgeCountClient{}, org: "  ", wantErr: "organization is required"},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source, err := NewTeamsProjectsSource(tc.client, true)
			if err != nil {
				t.Fatal(err)
			}
			counts, err := source.ProjectionSourceEdgeCounts(context.Background(), tc.org)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) || counts != nil {
				t.Fatalf("want error %q and no counts, got %+v, %v", tc.wantErr, counts, err)
			}
		})
	}
}

func TestDisabledTeamsProjectsSourceMeasuresAndDefersNothing(t *testing.T) {
	t.Parallel()
	source, err := NewTeamsProjectsSource(&edgeCountClient{}, false)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err != nil || len(counts) != 0 || len(source.DeferredEdgeRelations()) != 0 {
		t.Fatalf("a disabled source projects nothing: %+v %v %v", counts, err, source.DeferredEdgeRelations())
	}
}

func TestClickHouseSourceEdgeCountsAreTheReviewCount(t *testing.T) {
	t.Parallel()
	client := &edgeCountClient{rows: map[string][][]any{"git_pull_request_reviews": {{uint64(412)}}}}
	source, err := NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatal(err)
	}
	counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
	if err != nil {
		t.Fatal(err)
	}
	want := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest}
	if len(counts) != 1 || counts[want] != 412 {
		t.Fatalf("one measured population, the distinct reviews: %+v", counts)
	}
	if got := client.statements[len(client.statements)-1]; got != pullRequestReviewCountSQL {
		t.Fatalf("the edge count must share the review node count statement, got:\n%s", got)
	}
	var kindStatement string
	for _, st := range clickHouseKindCountStatements {
		if st.kind == contractsv1.ContextFabricSubjectPullRequestReview {
			kindStatement = st.sql
		}
	}
	if kindStatement != pullRequestReviewCountSQL {
		t.Fatal("the review node count and the edge count must be one statement")
	}
}

// Every relation type a source produces is either measured by its edge count or
// named in DeferredEdgeRelations: a new producer cannot ship unmeasured by omission.
func TestEveryProducedRelationTypeIsMeasuredOrNamedDeferred(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		produced []contractsv1.ContextFabricRelationshipType
		source   contextfabric.ProjectionSourceEdgeCounts
		counts   map[string][][]any
	}{
		"clickhouse": {
			produced: ProducedRelationshipTypes(),
			counts:   map[string][][]any{"git_pull_request_reviews": {{uint64(1)}}},
		},
		"teams_projects": {
			produced: TeamsProjectsRelationshipTypes(),
			counts:   map[string][][]any{"uniqExact(subject_kind": {{"transition", uint64(1)}}},
		},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := &edgeCountClient{rows: tc.counts}
			var source contextfabric.ProjectionSourceEdgeCounts
			var err error
			if name == "clickhouse" {
				source, err = NewClickHouseProjectionSource(client)
			} else {
				source, err = NewTeamsProjectsSource(client, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			counts, err := source.ProjectionSourceEdgeCounts(context.Background(), "org-a")
			if err != nil {
				t.Fatal(err)
			}
			covered := map[contractsv1.ContextFabricRelationshipType]string{}
			for key := range counts {
				covered[key.Relation] = "measured"
			}
			for _, relation := range source.DeferredEdgeRelations() {
				if covered[relation] != "" {
					t.Fatalf("%s is both measured and deferred", relation)
				}
				covered[relation] = "deferred"
			}
			var missing, extra []string
			for _, relation := range tc.produced {
				if covered[relation] == "" {
					missing = append(missing, string(relation))
				}
				delete(covered, relation)
			}
			for relation := range covered {
				extra = append(extra, string(relation))
			}
			sort.Strings(missing)
			sort.Strings(extra)
			if len(missing) != 0 || len(extra) != 0 {
				t.Fatalf("produced relation types not measured or named deferred: %v; measured or deferred but not produced: %v", missing, extra)
			}
			if len(source.DeferredEdgeRelations()) == 0 {
				t.Fatalf("this leaf defers relation types by name; an empty list means a producer was dropped from the guard")
			}
		})
	}
}
