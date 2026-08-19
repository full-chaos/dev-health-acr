package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// TestCensusSatisfierSetBudgetMatchesGraphrank pins
// devhealthsource.CensusBudget against graphrank.CensusSatisfierSetBudget
// (chaos3896_slice_b_presentation.go's own doc comment): graphrank cannot
// import devhealthsource, so the 999 row-budget bound is necessarily
// duplicated across the package boundary -- this test lives on the side
// that CAN import both, so a future edit to either constant that forgets
// its mirror fails loudly instead of silently drifting apart.
func TestCensusSatisfierSetBudgetMatchesGraphrank(t *testing.T) {
	t.Parallel()
	if devhealthsource.CensusBudget != graphrank.CensusSatisfierSetBudget {
		t.Fatalf("devhealthsource.CensusBudget = %d, graphrank.CensusSatisfierSetBudget = %d -- these must stay equal", devhealthsource.CensusBudget, graphrank.CensusSatisfierSetBudget)
	}
}

// censusSetFakeClient answers the aggregate-first protocol's two
// statements PLUS CHAOS-3896 Slice B's new non-decisive satisfier-SET
// fetch (issued only for 2<=count<=CensusBudget). Deliberately separate
// from chaos3899_census_test.go's own censusFakeClient (that one has no
// notion of a third statement).
type censusSetFakeClient struct {
	aggregateCount   uint64
	aggregateWitness string
	setRowKeys       []string // answers the satisfier-SET LIMIT B+1 fetch
}

func (c *censusSetFakeClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "count(), now64()") {
		return &censusSetFakeScanner{rows: [][]any{{c.aggregateCount, time.Now().UTC(), c.aggregateWitness}}}, nil
	}
	rows := make([][]any, 0, len(c.setRowKeys))
	for _, key := range c.setRowKeys {
		rows = append(rows, []any{key})
	}
	return &censusSetFakeScanner{rows: rows}, nil
}

type censusSetFakeScanner struct {
	rows [][]any
	row  int
}

func (s *censusSetFakeScanner) Next() bool { return s.row < len(s.rows) }
func (s *censusSetFakeScanner) Scan(dest ...any) error {
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
func (s *censusSetFakeScanner) Err() error   { return nil }
func (s *censusSetFakeScanner) Close() error { return nil }

func pullRequestPredicateForSetTest(t *testing.T) devhealthsource.CensusPredicate {
	t.Helper()
	predicate, err := devhealthsource.BuildCensusDiscriminator(contextfabric.SubjectPullRequest, "532", true, "", "", false)
	if err != nil {
		t.Fatalf("BuildCensusDiscriminator: %v", err)
	}
	return predicate
}

// TestRunCensus_SatisfierSetFetchedWhenCountInEnrichmentRange pins the
// happy path: 2<=count<=CensusBudget, the fetch's own row count agrees
// with the aggregate's -- SatisfierNaturalKeys populated, no closure
// mismatch.
func TestRunCensus_SatisfierSetFetchedWhenCountInEnrichmentRange(t *testing.T) {
	t.Parallel()
	client := &censusSetFakeClient{aggregateCount: 3, setRowKeys: []string{"org-1:repo-1:1", "org-1:repo-1:2", "org-1:repo-1:3"}}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicateForSetTest(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if result.Count != 3 {
		t.Fatalf("Count = %d, want 3", result.Count)
	}
	if result.SatisfierSetClosureMismatch {
		t.Fatal("SatisfierSetClosureMismatch = true, want false (the fetch agreed with the aggregate)")
	}
	if len(result.SatisfierNaturalKeys) != 3 {
		t.Fatalf("SatisfierNaturalKeys = %#v, want 3 entries", result.SatisfierNaturalKeys)
	}
}

// TestRunCensus_SatisfierSetClosureMismatchWhenRowCountDisagrees pins
// chris's own ruling: the enrichment fetch must be attested against the
// SAME count the aggregate witnessed -- a race that changes the row count
// between the two statements demotes to SatisfierSetClosureMismatch, never
// a (possibly wrong) partial set.
func TestRunCensus_SatisfierSetClosureMismatchWhenRowCountDisagrees(t *testing.T) {
	t.Parallel()
	// Aggregate says 3, but only 2 rows come back on the fetch (a row
	// vanished between statements -- the same race class the decisive
	// Count==1 path already guards against).
	client := &censusSetFakeClient{aggregateCount: 3, setRowKeys: []string{"org-1:repo-1:1", "org-1:repo-1:2"}}
	result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicateForSetTest(t))
	if err != nil {
		t.Fatalf("RunCensus: %v", err)
	}
	if !result.SatisfierSetClosureMismatch {
		t.Fatal("SatisfierSetClosureMismatch = false, want true (row count disagreed with the aggregate)")
	}
	if len(result.SatisfierNaturalKeys) != 0 {
		t.Fatalf("SatisfierNaturalKeys = %#v, want empty on a closure mismatch", result.SatisfierNaturalKeys)
	}
}

// TestRunCensus_NoSatisfierSetFetchOutsideEnrichmentRange pins that the
// non-decisive fetch is NEVER issued for Count==0 or Count>CensusBudget --
// only the SQL statement count differs; Count==1's own decisive path is
// pinned separately in chaos3899_census_test.go and untouched by this
// slice.
func TestRunCensus_NoSatisfierSetFetchOutsideEnrichmentRange(t *testing.T) {
	t.Parallel()
	t.Run("count==0", func(t *testing.T) {
		t.Parallel()
		client := &censusSetFakeClient{aggregateCount: 0}
		result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicateForSetTest(t))
		if err != nil {
			t.Fatalf("RunCensus: %v", err)
		}
		if result.StatementCount != 1 {
			t.Fatalf("StatementCount = %d, want 1 (aggregate only, no enrichment fetch for an empty census)", result.StatementCount)
		}
	})
	t.Run("count>CensusBudget", func(t *testing.T) {
		t.Parallel()
		client := &censusSetFakeClient{aggregateCount: devhealthsource.CensusBudget + 1}
		result, err := devhealthsource.RunCensus(context.Background(), client, "org-1", contextfabric.SubjectPullRequest, pullRequestPredicateForSetTest(t))
		if err != nil {
			t.Fatalf("RunCensus: %v", err)
		}
		if result.StatementCount != 1 {
			t.Fatalf("StatementCount = %d, want 1 (over budget -- no enrichment fetch, design brief's own cost-contract discipline)", result.StatementCount)
		}
	})
}

// TestNewCensusFunc_BridgesSatisfierSetToCanonicalIDs pins the adapter
// closure's own new job: devhealthsource is the only side of the
// graphrank/devhealthsource boundary that can call
// BridgeSatisfierToCanonicalID, so CensusOutcome must arrive at graphrank
// ALREADY bridged.
func TestNewCensusFunc_BridgesSatisfierSetToCanonicalIDs(t *testing.T) {
	t.Parallel()
	client := &censusSetFakeClient{aggregateCount: 2, setRowKeys: []string{"org-1:repo-1:1", "org-1:repo-1:2"}}
	censusFunc := devhealthsource.NewCensusFunc(client)
	outcome, err := censusFunc(context.Background(), "org-1", contextfabric.SubjectPullRequest, "532", true, "", "", false)
	if err != nil {
		t.Fatalf("censusFunc: %v", err)
	}
	if len(outcome.SatisfierCanonicalIDs) != 2 {
		t.Fatalf("SatisfierCanonicalIDs = %#v, want 2 bridged ids", outcome.SatisfierCanonicalIDs)
	}
	want := []string{"pull_request:repo-1:1", "pull_request:repo-1:2"}
	for i, id := range want {
		if outcome.SatisfierCanonicalIDs[i] != id {
			t.Fatalf("SatisfierCanonicalIDs[%d] = %q, want %q", i, outcome.SatisfierCanonicalIDs[i], id)
		}
	}
}

// TestNewCensusFunc_BridgesSingleWitnessToCanonicalID pins the Count==1
// decisive-path bridging (SatisfierCanonicalID, singular) the SAME adapter
// closure now also performs.
func TestNewCensusFunc_BridgesSingleWitnessToCanonicalID(t *testing.T) {
	t.Parallel()
	client := &censusSetFakeClient{aggregateCount: 1, aggregateWitness: "org-1:repo-1:532"}
	// The Count==1 decisive path's own row statement (LIMIT 2) must also
	// answer with the same single row for the witness check to pass --
	// reuse setRowKeys since censusSetFakeClient answers every non-aggregate
	// Query call from the same source.
	client.setRowKeys = []string{"org-1:repo-1:532"}
	censusFunc := devhealthsource.NewCensusFunc(client)
	outcome, err := censusFunc(context.Background(), "org-1", contextfabric.SubjectPullRequest, "532", true, "", "", false)
	if err != nil {
		t.Fatalf("censusFunc: %v", err)
	}
	if outcome.SatisfierCanonicalID != "pull_request:repo-1:532" {
		t.Fatalf("SatisfierCanonicalID = %q, want %q", outcome.SatisfierCanonicalID, "pull_request:repo-1:532")
	}
}
