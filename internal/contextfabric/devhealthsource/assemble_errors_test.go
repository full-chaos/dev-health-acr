package devhealthsource_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// rawDriverError stands in for what a ClickHouse driver or Scan failure
// actually carries: column types, the failing statement, server internals.
// internal/runtime/clickhouse's own operationError is already bounded
// ("ClickHouse query failed"), but it is not the only error that reaches the
// shared read path -- rows.Scan and rows.Err surface driver text verbatim,
// which is precisely how a SELECT list and a table's column types would end
// up in a coordinator log line.
var rawDriverError = errors.New("code: 184, message: converting UInt32 to *int64 is unsupported (query: SELECT id, name, ifNull(description, '') FROM teams FINAL WHERE org_id = 'acme')")

// TestTableReadFailuresDoNotLeakDriverTextIntoErrorStrings is codex round-1
// F5. The wave rule is that error strings and logs carry bounded failure
// classifications only; the projection coordinator logs this error verbatim.
// Both halves matter: the operator-visible string must name only the
// classification and the table, AND the cause must stay inspectable
// programmatically, so classification is not bought by discarding detail.
func TestTableReadFailuresDoNotLeakDriverTextIntoErrorStrings(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM teams FINAL\nWHERE", err: rawDriverError},
	}}
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err == nil {
		t.Fatal("a failing table read must surface as an error")
	}

	message := err.Error()
	for _, leaked := range []string{"converting UInt32", "SELECT", "org_id = 'acme'", "code: 184", "ifNull"} {
		if strings.Contains(message, leaked) {
			t.Errorf("error string leaks raw driver/query text %q: %s", leaked, message)
		}
	}
	if !strings.Contains(message, "teams") {
		t.Errorf("error string must still name the table that failed, got: %s", message)
	}
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Errorf("error must stay classified as ErrUnavailable for the coordinator's retry decision, got: %v", err)
	}
	if !errors.Is(err, rawDriverError) {
		t.Errorf("the underlying cause must stay inspectable via errors.Is -- classification must not discard it")
	}
}

// TestClickHouseSourceSharesTheSameBoundedClassification proves the fix
// covers the shared path rather than one source: the repository/work-item
// producers run through the same assemble.go read loop.
func TestClickHouseSourceSharesTheSameBoundedClassification(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repos", err: rawDriverError}}}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("NewClickHouseProjectionSource: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.SourceName})
	if err == nil {
		t.Fatal("a failing table read must surface as an error")
	}
	if strings.Contains(err.Error(), "converting UInt32") || strings.Contains(err.Error(), "SELECT") {
		t.Errorf("error string leaks raw driver/query text: %s", err.Error())
	}
	if !errors.Is(err, contextfabric.ErrUnavailable) || !errors.Is(err, rawDriverError) {
		t.Errorf("classification or cause lost: %v", err)
	}
}
