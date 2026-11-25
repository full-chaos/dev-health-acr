package devhealthfacts_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func budgetStop() error {
	return &devhealthfacts.BudgetExceededError{Code: 307, CapBytes: 1 << 20}
}

// servedFactReadReason runs one provider through the registry and returns the
// reason the answer carries for the kind: the sentence a caller reads.
func servedFactReadReason(t *testing.T, provider contextfabric.FactProvider, principal storage.Principal, kind contextfabric.FactKind, subjects ...contextfabric.SubjectRef) string {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := registry.ReadFacts(context.Background(), principal, contextfabric.CanonicalFactRequest{
		Subjects:     subjects,
		Question:     contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		Requirements: []contextfabric.FactRequirement{{Kind: kind}},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	for _, source := range bundle.Coverage.Sources {
		if source.Source == "canonical_fact:"+string(kind) {
			return source.Reason
		}
	}
	t.Fatalf("no coverage source for %s: %+v", kind, bundle.Coverage.Sources)
	return ""
}

func genericFailedRead(kind contextfabric.FactKind) string {
	return "The " + contractsv1.ContextFabricFactKindLabel(contractsv1.ContextFabricFactKind(kind)) + " read failed; the facts for it are not shown."
}

// assertServedEndsWith: a team answer can carry a scope-expansion note ahead of
// the kind's own sentence, so the sentence is the end of the reason.
func assertServedEndsWith(t *testing.T, got, want string) {
	t.Helper()
	if !strings.HasSuffix(got, want) {
		t.Fatalf("reason %q does not end with %q", got, want)
	}
}

const budgetAdvice = "narrow the window or the subject"

// A read stopped at the budget tells the caller to narrow the window or the
// subject only when the statement that stopped depends on them. A statement
// that reads the whole organization (or the caller's whole grant) does not, so
// it serves the generic sentence. One cell per such statement.

// ReadTeamBlockerUnlinkedItems counts the organization's blocked items with no
// link: no window, no requested subject.
func TestAnOrganizationWideBlockerStatementStoppedAtTheBudgetGivesNoNarrowingAdvice(t *testing.T) {
	tables := teamBlockerTables()
	for i := range tables {
		if tables[i].match == "SELECT toInt64(uniqExact(if(" {
			tables[i].err = budgetStop()
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactBlockers)
	got := servedFactReadReason(t, provider, storage.Principal{OrgID: "org-1"}, contextfabric.FactBlockers, teamSubject("CHAOS"))
	assertServedEndsWith(t, got, genericFailedRead(contextfabric.FactBlockers))
}

// ReadBlockerAuthorizedIDs reads the caller's whole grant: no window, no
// requested subject.
func TestTheCallersGrantStatementStoppedAtTheBudgetGivesNoNarrowingAdvice(t *testing.T) {
	tables := append([]fakeTable{{match: "ORDER BY work_item_id LIMIT", err: budgetStop()}}, teamBlockerTables()...)
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactBlockers)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"acme/repo"}}
	got := servedFactReadReason(t, provider, principal, contextfabric.FactBlockers, teamSubject("CHAOS"))
	assertServedEndsWith(t, got, genericFailedRead(contextfabric.FactBlockers))
}

// ReadProjectMixInputs digests the whole organization's source tables to prove
// the read did not move: no window, no requested subject.
func TestTheProjectMixInputsDigestStoppedAtTheBudgetGivesNoNarrowingAdvice(t *testing.T) {
	tables := append([]fakeTable{{match: "sum(cityHash64(", err: budgetStop()}}, nativePhasedTables(nativeMixUnits("a", 1))...)
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactInvestment)
	got := servedFactReadReason(t, provider, storage.Principal{OrgID: "org-1"}, contextfabric.FactInvestment, projectSubject("linear", "a"))
	assertServedEndsWith(t, got, genericFailedRead(contextfabric.FactInvestment))
}

// Control: a statement bounded by the requested subject still tells the caller
// to narrow it (the advice is true there).
func TestASubjectBoundStatementStoppedAtTheBudgetStillSaysToNarrow(t *testing.T) {
	tables := teamBlockerTables()
	for i := range tables {
		if tables[i].match == "GROUP BY team_id, repo_key" {
			tables[i].err = budgetStop()
		}
	}
	provider := findProvider(t, devhealthfacts.NewProviders(&fakeClient{tables: tables}), contextfabric.FactBlockers)
	got := servedFactReadReason(t, provider, storage.Principal{OrgID: "org-1"}, contextfabric.FactBlockers, teamSubject("CHAOS"))
	want := "The blockers read needed more data than one answer may read; " + budgetAdvice + "; the facts for it are not shown."
	assertServedEndsWith(t, got, want)
}

// ReadProjectMixChunkBounds lists the organization's work unit ids to cut the
// project mix into chunks: it binds the organization and two numeric caps, no
// requested subject and no window. The cell asserts the injected stop fired.
func TestTheProjectMixChunkBoundsStatementStoppedAtTheBudgetGivesNoNarrowingAdvice(t *testing.T) {
	const marker = "AS chunk_bounds"
	tables := append([]fakeTable{{match: marker, err: budgetStop()}}, nativePhasedTables(nativeMixUnits("a", 1))...)
	client := &fakeClient{tables: tables}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	got := servedFactReadReason(t, provider, storage.Principal{OrgID: "org-1"}, contextfabric.FactInvestment, projectSubject("linear", "a"))
	fired := 0
	for _, query := range client.queries {
		if strings.Contains(query.statement, marker) {
			fired++
		}
	}
	if fired == 0 {
		t.Fatalf("the chunk bounds statement never ran, so the injected stop did not fire; the cell proves nothing")
	}
	assertServedEndsWith(t, got, genericFailedRead(contextfabric.FactInvestment))
}
