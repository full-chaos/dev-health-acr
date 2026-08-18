package devhealthsource_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// censusFakeClient is a dedicated, minimal ClickHouseQueryClient double for
// RunCensus's aggregate-first protocol (design brief v5 §1.3(2)) -- kept
// separate from clickhouse_test.go's own fakeClient (that one's org_id
// binding assertion and cursor-pagination machinery target the ordinary
// producer queries' shape, not the aggregate/row two-statement protocol
// this file exercises).
type censusFakeClient struct {
	// aggregateCount/aggregateReadAt answer the "SELECT count(), now64(),
	// min(<natural key>)" statement.
	aggregateCount  uint64
	aggregateReadAt time.Time
	// aggregateWitness answers the aggregate statement's min(<natural
	// key>) column (sol review correction: the identity-witness check).
	// Empty means "default to rowKeys[0] when exactly one row key is
	// configured" -- so every test written before the witness column
	// existed keeps working unchanged; a test exercising the
	// IDENTITY-SWAP race sets this explicitly to a value that DISAGREES
	// with rowKeys[0].
	aggregateWitness string
	// aggregateRowCount, when >1, makes the aggregate statement return
	// MORE THAN ONE row (simulates a backend contract violation the
	// EXACTLY-ONE-ROW assertion must catch). 0 or 1 means "exactly one
	// row", the normal case.
	aggregateRowCount int
	// rowKeys answers the row statement (LIMIT 2) -- however many entries
	// are here are returned, letting a test simulate 0/1/2-row races.
	rowKeys []string
	// calls records each statement issued, for statement-count assertions.
	calls []string
	// err, if set, is returned by every Query call (simulates census_error).
	err error
}

func (c *censusFakeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.calls = append(c.calls, statement)
	if c.err != nil {
		return nil, c.err
	}
	if strings.Contains(statement, "count(), now64()") {
		witness := c.aggregateWitness
		if witness == "" && len(c.rowKeys) == 1 {
			witness = c.rowKeys[0]
		}
		rowCount := c.aggregateRowCount
		if rowCount == 0 {
			rowCount = 1
		}
		rows := make([][]any, rowCount)
		for i := range rows {
			rows[i] = []any{c.aggregateCount, c.aggregateReadAt, witness}
		}
		return &censusFakeScanner{rows: rows}, nil
	}
	rows := make([][]any, 0, len(c.rowKeys))
	for _, key := range c.rowKeys {
		rows = append(rows, []any{key})
	}
	return &censusFakeScanner{rows: rows}, nil
}

type censusFakeScanner struct {
	rows [][]any
	row  int
}

func (s *censusFakeScanner) Next() bool { return s.row < len(s.rows) }
func (s *censusFakeScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	s.row++
	for i, d := range dest {
		switch target := d.(type) {
		case *uint64:
			*target = row[i].(uint64)
		case *time.Time:
			*target = row[i].(time.Time)
		case *string:
			*target = row[i].(string)
		}
	}
	return nil
}
func (s *censusFakeScanner) Err() error   { return nil }
func (s *censusFakeScanner) Close() error { return nil }

func pullRequestPredicate(t *testing.T) devhealthsource.CensusPredicate {
	t.Helper()
	predicate, err := devhealthsource.BuildCensusDiscriminator(contextfabric.SubjectPullRequest, "532", true, "", "", false)
	if err != nil {
		t.Fatalf("BuildCensusDiscriminator: %v", err)
	}
	return predicate
}

// TestRunCensus_EmptyCensus pins protocol (a): Count==0 still returns a
// non-zero CensusReadAt, and the row statement (b) never runs.
func TestRunCensus_EmptyCensus(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	client := &censusFakeClient{aggregateCount: 0, aggregateReadAt: readAt}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if result.Count != 0 || !result.CensusReadAt.Equal(readAt) {
		t.Fatalf("result = %#v, want Count=0 CensusReadAt=%v", result, readAt)
	}
	if result.ClosureMismatch {
		t.Fatalf("ClosureMismatch = true, want false for an empty census")
	}
	if result.StatementCount != 1 {
		t.Fatalf("StatementCount = %d, want 1 (row statement must never run at count==0)", result.StatementCount)
	}
	if len(client.calls) != 1 {
		t.Fatalf("issued %d statements, want exactly 1", len(client.calls))
	}
}

// TestRunCensus_SingletonAgrees pins protocol (b)'s success path: count==1
// and the row statement returns exactly the one attested identity.
func TestRunCensus_SingletonAgrees(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	client := &censusFakeClient{aggregateCount: 1, aggregateReadAt: readAt, rowKeys: []string{"repo-1:532"}}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if result.Count != 1 || result.ClosureMismatch {
		t.Fatalf("result = %#v, want Count=1 ClosureMismatch=false", result)
	}
	if result.SatisfierNaturalKey != "repo-1:532" {
		t.Fatalf("SatisfierNaturalKey = %q, want %q", result.SatisfierNaturalKey, "repo-1:532")
	}
	if result.StatementCount != 2 || result.RowsRead != 1 {
		t.Fatalf("StatementCount=%d RowsRead=%d, want 2 and 1", result.StatementCount, result.RowsRead)
	}
}

// TestRunCensus_SingletonVanishedBetweenStatements pins the RACE case:
// aggregate said count==1 but the row statement returned zero rows -- a
// row that vanished between (a) and (b). Brief §1.3(2): "can only DEMOTE a
// decisive outcome to clarify, never mint one."
func TestRunCensus_SingletonVanishedBetweenStatements(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: nil}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if !result.ClosureMismatch {
		t.Fatalf("ClosureMismatch = false, want true (aggregate said 1, row statement returned 0)")
	}
	if result.SatisfierNaturalKey != "" {
		t.Fatalf("SatisfierNaturalKey = %q, want empty on mismatch", result.SatisfierNaturalKey)
	}
}

// TestRunCensus_RowLandedBetweenStatements pins the OTHER race direction:
// aggregate said count==1 but LIMIT 2 caught two rows -- a row landed
// between (a) and (b).
func TestRunCensus_RowLandedBetweenStatements(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"repo-1:532", "repo-1:9999"}}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if !result.ClosureMismatch {
		t.Fatalf("ClosureMismatch = false, want true (aggregate said 1, row statement returned 2)")
	}
	if result.RowsRead != 2 {
		t.Fatalf("RowsRead = %d, want 2", result.RowsRead)
	}
}

// TestRunCensus_IdentityPreservingSwapDemotes is the sol review
// correction's own named race scenario: the aggregate statement (a) and
// the row statement (b) BOTH report count==1/exactly-one-row, but for a
// DIFFERENT row -- e.g. a mutable FK moved satisfier W1 out of D's
// population and a different satisfier W2 in, between the two statements.
// Before the identity-witness fix, this passed every existing check
// (count==1 twice, one row read) and would have committed W2 stamped with
// a receipt that was actually read against W1's population. It must
// demote to ClosureMismatch, never mint a commit.
func TestRunCensus_IdentityPreservingSwapDemotes(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{
		aggregateCount: 1, aggregateReadAt: time.Now().UTC(),
		aggregateWitness: "repo-1:532",            // statement (a)'s own row: W1
		rowKeys:          []string{"repo-1:9999"}, // statement (b)'s row: W2 -- count-preserving, but a DIFFERENT satisfier
	}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if !result.ClosureMismatch {
		t.Fatalf("ClosureMismatch = false, want true -- (a) attested W1 (repo-1:532), (b) returned a DIFFERENT single row W2 (repo-1:9999); count agreeing twice must not be enough to commit")
	}
	if result.SatisfierNaturalKey != "" {
		t.Fatalf("SatisfierNaturalKey = %q, want empty -- an identity-preserving-count swap must never mint a commit", result.SatisfierNaturalKey)
	}
}

// TestRunCensus_AggregateMoreThanOneRowIsAContractViolation pins the
// EXACTLY-ONE-ROW assertion (sol review correction, setting pin): the
// aggregate statement must return exactly one row; a backend returning
// more must fail loudly rather than silently reading only the first.
func TestRunCensus_AggregateMoreThanOneRowIsAContractViolation(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), aggregateRowCount: 2, rowKeys: []string{"repo-1:532"}}
	if _, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t)); err == nil {
		t.Fatalf("RunCensus: want an error when the aggregate statement returns more than one row, got nil")
	}
}

// TestRunCensus_AggregateStatementPinsEmptyResultSetting pins the sol
// review correction's setting pin: every aggregate statement carries
// SETTINGS empty_result_for_aggregation_by_empty_set = 0, so ClickHouse
// cannot silently return ZERO rows for an aggregate over an empty input --
// the guarantee the empty-census receipt (Count==0 always carries a
// non-zero CensusReadAt) structurally depends on.
func TestRunCensus_AggregateStatementPinsEmptyResultSetting(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{aggregateCount: 0, aggregateReadAt: time.Now().UTC()}
	if _, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t)); err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	found := false
	for _, statement := range client.calls {
		if strings.Contains(statement, "count(), now64()") {
			found = true
			if !strings.Contains(statement, "SETTINGS empty_result_for_aggregation_by_empty_set = 0") {
				t.Fatalf("aggregate statement missing the empty_result_for_aggregation_by_empty_set=0 setting pin: %s", statement)
			}
		}
	}
	if !found {
		t.Fatalf("no aggregate statement was issued")
	}
}

// TestRunCensus_MultiSatisfier_NoRowStatement pins that count>1 never
// issues the row statement at all (brief §1.3(2): "count>1 -> clarify with
// no row fetch").
func TestRunCensus_MultiSatisfier_NoRowStatement(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{aggregateCount: 3, aggregateReadAt: time.Now().UTC()}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if result.Count != 3 || result.ClosureMismatch {
		t.Fatalf("result = %#v, want Count=3 ClosureMismatch=false", result)
	}
	if result.StatementCount != 1 {
		t.Fatalf("StatementCount = %d, want 1 (no row statement at count>1)", result.StatementCount)
	}
	if len(client.calls) != 1 {
		t.Fatalf("issued %d statements, want exactly 1", len(client.calls))
	}
}

// TestRunCensus_ErrorPoisonsTheKind pins census_error: a backend error on
// either statement propagates rather than being swallowed into a false
// empty/singleton result.
func TestRunCensus_ErrorPoisonsTheKind(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{err: errors.New("clickhouse: connection reset")}
	if _, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t)); err == nil {
		t.Fatalf("RunCensus: want error, got nil")
	}
}

// TestRunCensus_NoJoin pins design brief v5 §1.3(1): neither statement this
// function builds ever contains the word JOIN.
func TestRunCensus_NoJoin(t *testing.T) {
	t.Parallel()
	client := &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"repo-1:532"}}
	if _, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicate(t)); err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	for _, statement := range client.calls {
		if strings.Contains(strings.ToUpper(statement), "JOIN") {
			t.Fatalf("census statement contains JOIN (brief §1.3(1) base-table-only): %s", statement)
		}
		if !strings.Contains(statement, "FINAL") {
			t.Fatalf("census statement missing FINAL: %s", statement)
		}
	}
}

// TestVerifyExactlyOneSourceNaturalKey is the Slice-A setup-invariant
// checker's own unit pin (brief §6 "Setup oracle").
func TestVerifyExactlyOneSourceNaturalKey(t *testing.T) {
	t.Parallel()
	t.Run("unique", func(t *testing.T) {
		client := &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"repo-1:532"}}
		ok, count, readAt, err := devhealthsource.VerifyExactlyOneSourceNaturalKey(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, "532", true, "", "", false)
		if err != nil {
			t.Fatalf("VerifyExactlyOneSourceNaturalKey: %v", err)
		}
		if !ok || count != 1 || readAt.IsZero() {
			t.Fatalf("ok=%v count=%d readAt=%v, want ok=true count=1 non-zero readAt", ok, count, readAt)
		}
	})
	t.Run("absent_fails_setup", func(t *testing.T) {
		client := &censusFakeClient{aggregateCount: 0, aggregateReadAt: time.Now().UTC()}
		ok, count, _, err := devhealthsource.VerifyExactlyOneSourceNaturalKey(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, "532", true, "", "", false)
		if err != nil {
			t.Fatalf("VerifyExactlyOneSourceNaturalKey: %v", err)
		}
		if ok || count != 0 {
			t.Fatalf("ok=%v count=%d, want ok=false count=0 for a scored referent missing from the source", ok, count)
		}
	})
	t.Run("duplicate_fails_setup", func(t *testing.T) {
		client := &censusFakeClient{aggregateCount: 2, aggregateReadAt: time.Now().UTC()}
		ok, count, _, err := devhealthsource.VerifyExactlyOneSourceNaturalKey(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, "532", true, "", "", false)
		if err != nil {
			t.Fatalf("VerifyExactlyOneSourceNaturalKey: %v", err)
		}
		if ok || count != 2 {
			t.Fatalf("ok=%v count=%d, want ok=false count=2 for a non-unique natural key", ok, count)
		}
	})
}
