package devhealthfacts_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const runIDStatementPrefix = "SELECT argMax(run_id, completed_at) FROM work_unit_membership_runs"

func isRunIDRead(statement string) bool {
	return strings.HasPrefix(strings.TrimSpace(statement), runIDStatementPrefix)
}

func countRunIDReads(client *fakeClient) int {
	n := 0
	for _, q := range client.queries {
		if isRunIDRead(q.statement) {
			n++
		}
	}
	return n
}

const scopeLoadStatement = "groupUniqArray(100001)(work_unit_id)"

func readThroughProduction(t *testing.T, client contextpacket.ClickHouseQueryClient, org string) []contextfabric.SourceObservation {
	t.Helper()
	registry, request := coldScopeRegistryProduction(t, client)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: org}, request)
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(bundle.Coverage.Sources) == 0 {
		t.Fatal("no coverage source: measurement did not happen")
	}
	return bundle.Coverage.Sources
}

// A scope that cannot be remembered (empty run, failed load, run over the
// remembered bound) is loaded once per read, by the prerequisite step; the
// statements of the read use it and never load it again inside the clock.
func TestUnrememberedScopeIsLoadedOncePerReadThroughTheProductionComposition(t *testing.T) {
	tooMany := make([]string, 100001)
	for i := range tooMany {
		tooMany[i] = "wu"
	}
	for name, mutate := range map[string]func(*fakeTable){
		"empty":    func(tb *fakeTable) { tb.rows = [][]any{} },
		"overflow": func(tb *fakeTable) { tb.rows = [][]any{{tooMany}} },
		"failed":   func(tb *fakeTable) { tb.err = context.DeadlineExceeded },
	} {
		t.Run(name, func(t *testing.T) {
			tables := coldScopeTables()
			for i := range tables {
				if tables[i].match == scopeUnitsMatch {
					mutate(&tables[i])
				}
			}
			client := &fakeClient{tables: tables}
			readThroughProduction(t, client, "org-handoff-"+name)
			if got := countStatements(client, scopeLoadStatement); got != 1 {
				t.Fatalf("scope loads in one read = %d, want 1", got)
			}
			if runs := countRunIDReads(client); runs != 1 {
				t.Fatalf("run-id reads in one read = %d, want 1 (pinned by the prerequisite step)", runs)
			}
			if len(phaseZeroStatements(client)) == 0 {
				t.Fatal("no phase-0 statement ran: measurement did not happen")
			}
		})
	}
}

// flipRunClient reports run-1 to the first run-id read and run-2 afterwards.
type flipRunClient struct {
	*fakeClient
	runReads int
}

func (c *flipRunClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if isRunIDRead(statement) {
		c.runReads++
		run := "run-1"
		if c.runReads > 1 {
			run = "run-2"
		}
		for i := range c.fakeClient.tables {
			if c.fakeClient.tables[i].match == scopeRunMatch {
				c.fakeClient.tables[i].rows = [][]any{{run}}
			}
		}
	}
	return c.fakeClient.Query(ctx, statement, bindings)
}

// A run that changes after the prerequisite step is not read inside the
// clock: the phases keep the carried scope, one request serves one run.
func TestRunChangeAfterThePrerequisiteStepIsNotReadInsideTheClock(t *testing.T) {
	client := &flipRunClient{fakeClient: &fakeClient{tables: coldScopeTables()}}
	readThroughProduction(t, client, "org-handoff-runchange")
	if got := countStatements(client.fakeClient, scopeLoadStatement); got != 1 {
		t.Fatalf("scope loads = %d, want 1 (the prerequisite step only)", got)
	}
	if client.runReads != 1 {
		t.Fatalf("run-id reads = %d, want 1: measurement did not happen or the phases re-read the run", client.runReads)
	}
}

// Every provider whose own constructor declares the prerequisite step is
// reached through the production composition (the decorator must not hide it).
func TestProductionCompositionReachesEveryPrerequisiteLoader(t *testing.T) {
	client := &fakeClient{}
	declaring := map[contextfabric.FactKind]bool{}
	for _, p := range devhealthfacts.NewProvidersWithOperations(client, nil) {
		if _, ok := p.(contextfabric.FactPrerequisiteLoader); ok {
			declaring[p.Capability().Kind] = true
		}
	}
	if len(declaring) == 0 {
		t.Fatal("no provider declares a prerequisite loader: measurement did not happen")
	}
	reached := map[contextfabric.FactKind]bool{}
	for _, p := range devhealthfacts.NewInstrumentedProvidersWithOperations(client, readers.NoopInstrumentation{}, nil) {
		if _, ok := p.(contextfabric.FactPrerequisiteLoader); ok {
			reached[p.Capability().Kind] = true
		}
	}
	for kind := range declaring {
		if !reached[kind] {
			t.Errorf("%s declares a prerequisite loader the production composition hides", kind)
		}
	}
}

func TestOverflowAndEmptyColdLoadsAreReportedOncePerOrganization(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	tooMany := make([]string, 100001)
	for i := range tooMany {
		tooMany[i] = "wu"
	}
	for name, rows := range map[string][][]any{"overflow": {{tooMany}}, "empty": {}} {
		var buf syncBuffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		tables := coldScopeTables()
		for i := range tables {
			if tables[i].match == scopeUnitsMatch {
				tables[i].rows = rows
			}
		}
		client := &fakeClient{tables: tables}
		registry, request := coldScopeRegistryProduction(t, client)
		for i := 0; i < 2; i++ {
			if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-report-" + name}, request); err != nil {
				t.Fatal(err)
			}
		}
		got := 0
		for _, line := range strings.Split(buf.String(), "\n") {
			if strings.Contains(line, "investment membership scope cold load") && strings.Contains(line, "outcome="+name) {
				got++
			}
		}
		if got != 1 {
			t.Errorf("%s: cold-load lines = %d over two reads, want 1\n%s", name, got, buf.String())
		}
	}
}
