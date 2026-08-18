package devhealthsource_test

// CHAOS-3898 D-7: the deployment-incident edge query's LEFT JOIN to
// deployments used to key on (org_id, deployment_id) only, so two
// different repos' deployments sharing a deployment_id could join the
// WRONG repo's row -- a duplicate-RelationshipID wedge or a silently wrong
// deploy/finish window. This is the one defect in the CHAOS-3898 v4.1 §0
// inventory S1 actually fixes (design brief §6: "D-7 join fix"); every
// other defect (D-1..D-6) is RED-documented in
// internal/contextfabric/identity/chaos3898_known_defects_test.go instead.
//
// This test asserts the fix at the SQL-text level rather than through
// projected data: the fake ClickHouseQueryClient this package's test suite
// uses (clickhouse_test.go) resolves which canned rows to return by
// matching a substring of the FULL multi-join statement, and does not
// itself evaluate JOIN predicates -- so it cannot distinguish a correct
// join from a buggy one at the data level without a much larger rewrite of
// the fake. A statement-text regression test still catches exactly the
// failure mode this fix guards against: someone dropping the added
// predicate in a future edit.

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// statementRecordingClient wraps a fakeClient and records every SQL
// statement text it is asked to run, so a test can assert on the query
// shape without needing the fake to model JOIN semantics.
type statementRecordingClient struct {
	inner      *fakeClient
	statements []string
}

func (c *statementRecordingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.statements = append(c.statements, statement)
	return c.inner.Query(ctx, statement, bindings)
}

func TestD7_DeploymentIncidentEdgeJoinIsRepoQualified(t *testing.T) {
	t.Parallel()

	recorder := &statementRecordingClient{inner: &fakeClient{tables: baseTables(zeroTime)}}
	source, err := devhealthsource.NewClickHouseProjectionSource(recorder)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	if _, _, err := source.NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}

	var edgeStatement string
	for _, statement := range recorder.statements {
		if strings.Contains(statement, "FROM work_graph_deployment_incident_edges AS e") {
			edgeStatement = statement
			break
		}
	}
	if edgeStatement == "" {
		t.Fatal("no statement queried work_graph_deployment_incident_edges")
	}

	const wantPredicate = "d.deployment_id = e.deployment_id AND d.repo_id = e.repo_id"
	if !strings.Contains(edgeStatement, wantPredicate) {
		t.Errorf("D-7 regression: deployments join is missing the repo_id qualifier.\nwant substring: %s\ngot statement:\n%s", wantPredicate, edgeStatement)
	}
}
