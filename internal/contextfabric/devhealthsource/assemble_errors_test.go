package devhealthsource_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// rawDriverError stands in for what a ClickHouse driver or Scan failure
// actually carries: column types, the failing statement, server internals.
// github.com/full-chaos/dev-health-go/clickhouse's own operationError is already bounded
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
		{match: "FROM teams AS tm FINAL", err: rawDriverError},
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

// budgetExceededError stands in for the real ClickHouse driver exception
// CHAOS-3848 fixes classification for: TOO_MANY_BYTES (Code 307), the
// exception clickhouse-go's native driver returns when a query reads more
// bytes than max_bytes_to_read allows. Its Message deliberately carries
// server-side detail (limits, byte counts) the same way a real one would, so
// the leak assertions below exercise the real risk, not a sanitized stand-in.
var budgetExceededError = &clickhousedriver.Exception{
	Code:    307,
	Name:    "DB::Exception",
	Message: "Limit for read exceeded: 17987654 bytes read, maximum 16777216 bytes",
}

// TestQueryBudgetExceededClassifiesDistinctlyFromUnavailable is CHAOS-3848's
// part-2 closure test. Pre-fix, tableReadError had exactly one
// classification path: every cause -- a connection drop, a malformed
// statement, or a permanent per-query budget exception -- wrapped
// ErrUnavailable, so budgetExceededError() (a real TOO_MANY_BYTES exception)
// classified identically to a transient dependency outage. This asserts the
// budget-specific sentinel instead, and that ErrUnavailable is no longer
// also satisfied -- a single error should not answer to two different retry
// stories.
func TestQueryBudgetExceededClassifiesDistinctlyFromUnavailable(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM repos", err: budgetExceededError}}}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("NewClickHouseProjectionSource: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.SourceName})
	if err == nil {
		t.Fatal("a query-budget failure must surface as an error")
	}

	if !errors.Is(err, contextfabric.ErrQueryBudgetExceeded) {
		t.Errorf("a TOO_MANY_BYTES exception must classify as ErrQueryBudgetExceeded, got: %v", err)
	}
	if errors.Is(err, contextfabric.ErrUnavailable) {
		t.Errorf("a permanent budget exception must NOT also classify as ErrUnavailable (that reads as transient), got: %v", err)
	}
	if !errors.Is(err, budgetExceededError) {
		t.Error("the underlying cause must stay inspectable via errors.Is")
	}

	message := err.Error()
	if !strings.Contains(message, "307") {
		t.Errorf("error string should name the bounded ClickHouse exception code, got: %s", message)
	}
	for _, leaked := range []string{"17987654", "16777216", "Limit for read exceeded"} {
		if strings.Contains(message, leaked) {
			t.Errorf("error string leaks raw driver exception text %q: %s", leaked, message)
		}
	}
}

// failingEpisodeRows is an EpisodeRows implementation whose read fails with
// raw driver text. The point is the BOUNDARY, not today's adapter: the
// Postgres EpisodeStore happens to sanitize its errors, so this leak was
// invisible in production while remaining available to any alternate
// provider.
type failingEpisodeRows struct{}

func (failingEpisodeRows) ListSince(context.Context, string, time.Time, string, int) ([]storage.EpisodeProjectionRecord, error) {
	return nil, rawDriverError
}

// TestEpisodesSourceBoundsItsReadFailuresToo is codex round-2 F4. My first
// error sweep was scoped to the files this branch had CHANGED rather than to
// the ProjectionSource boundary as a class, which is exactly how this one
// escaped -- a diff-shaped sweep cannot find a leak in a file the diff never
// touched.
func TestEpisodesSourceBoundsItsReadFailuresToo(t *testing.T) {
	t.Parallel()
	source, err := devhealthsource.NewEpisodesProjectionSource(failingEpisodeRows{})
	if err != nil {
		t.Fatalf("NewEpisodesProjectionSource: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.EpisodesSourceName})
	if err == nil {
		t.Fatal("a failing episode read must surface as an error")
	}
	for _, leaked := range []string{"converting UInt32", "SELECT", "org_id = 'acme'", "code: 184"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("episodes error string leaks raw driver text %q: %s", leaked, err.Error())
		}
	}
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Errorf("episodes error must stay classified as ErrUnavailable, got: %v", err)
	}
	if !errors.Is(err, rawDriverError) {
		t.Error("episodes error must keep its cause inspectable via errors.Is")
	}
}
