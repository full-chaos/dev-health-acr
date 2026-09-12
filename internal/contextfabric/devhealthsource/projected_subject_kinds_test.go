package devhealthsource_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ProjectedSubjectKinds is read by the seam that decides which member kinds
// can be served as a cohort, so a declaration that says a kind is projected
// when nothing projects it admits a cohort that can only be empty, and a
// declaration that omits a kind the projection does emit keeps a question
// refused that the graph could answer.
//
// Neither direction is visible from the declaration alone. These pins run the
// producers and compare what they EMIT against what the registry DECLARES.

// theOrganizationScopeSubject is emitted by the ClickHouse source itself,
// once per batch, rather than by any table in the registry -- so it appears
// in executed output with no producer to declare it, and it is subtracted
// here rather than being given a fictional row.
//
// It is also the one kind that could never be a cohort MEMBER: an
// organization is the scope a cohort is discovered WITHIN. Declaring it
// projected would offer the seam a kind whose cohort has no meaning.
const theOrganizationScopeSubject = contractsv1.ContextFabricSubjectOrganization

func emittedEntityKinds(t *testing.T, batch contextfabric.ProjectionBatch) map[contractsv1.ContextFabricSubjectKind]bool {
	t.Helper()
	kinds := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, entity := range batch.Entities {
		if entity.Subject.Kind == theOrganizationScopeSubject {
			continue
		}
		kinds[entity.Subject.Kind] = true
	}
	return kinds
}

func sortedKinds(kinds map[contractsv1.ContextFabricSubjectKind]bool) []string {
	out := make([]string, 0, len(kinds))
	for kind := range kinds {
		out = append(out, string(kind))
	}
	sort.Strings(out)
	return out
}

// TestProjectedSubjectKindsMatchesWhatTheClickHouseProducersEmit executes
// every entity table in the registry against seeded rows and compares the
// kinds that come out with the kinds the registry declares.
//
// Both directions are asserted from the same executed batch: a declared kind
// nothing emitted is a claim the seam would act on, and an emitted kind
// nothing declared is a population the seam cannot see.
func TestProjectedSubjectKindsMatchesWhatTheClickHouseProducersEmit(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := append(baseTables(at),
		fakeTable{match: "FROM git_pull_request_reviews AS r", rows: [][]any{{"review-1", "repo-1", uint32(1042), "approved", at, "example-org/widget-service", at, uint8(0), zeroTime, "Typed session tokens"}}},
		fakeTable{match: "FROM ci_pipeline_runs AS c", rows: [][]any{{"run-1", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at, "fullstack-acceptance"}}},
	)
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available; with no batch this pin compares two empty sets and passes for the wrong reason")
	}
	emitted := emittedEntityKinds(t, batch)
	if len(emitted) == 0 {
		t.Fatal("the seeded batch emitted no entity subjects at all")
	}

	declaredByTable := devhealthsource.EntityTableSubjectKindsForTest()
	declared := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, name := range devhealthsource.EntityTableNamesForTest() {
		for _, kind := range declaredByTable[name] {
			declared[kind] = true
		}
	}
	for kind := range emitted {
		if !declared[kind] {
			t.Errorf("the ClickHouse producers emit %q but no entityTables row declares it, so ProjectedSubjectKinds() hides a population the seam decides on; emitted=%v declared=%v", kind, sortedKinds(emitted), sortedKinds(declared))
		}
	}
	for kind := range declared {
		if !emitted[kind] {
			t.Errorf("entityTables declares %q but no producer emitted it from seeded rows, so the seam may admit a cohort kind with no population; emitted=%v declared=%v", kind, sortedKinds(emitted), sortedKinds(declared))
		}
	}
}

// TestProjectedSubjectKindsMatchesWhatTheTeamsProjectsProducersEmit is the
// same pin for the second registry. It is separate because the two sources
// are wired and enabled independently, and a single test over both would go
// green on one source's kinds while the other emitted nothing.
func TestProjectedSubjectKindsMatchesWhatTheTeamsProjectsProducersEmit(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	emitted := emittedEntityKinds(t, batch)
	if len(emitted) == 0 {
		t.Fatal("the live-shaped teams/projects batch emitted no entity subjects at all")
	}

	declaredByTable := devhealthsource.EntityTableSubjectKindsForTest()
	declared := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, name := range devhealthsource.TeamsProjectsTableNamesForTest() {
		for _, kind := range declaredByTable[name] {
			declared[kind] = true
		}
	}
	for kind := range emitted {
		if !declared[kind] {
			t.Errorf("the teams/projects producers emit %q but no teamsProjectsTables row declares it; emitted=%v declared=%v", kind, sortedKinds(emitted), sortedKinds(declared))
		}
	}
	for kind := range declared {
		if !emitted[kind] {
			t.Errorf("teamsProjectsTables declares %q but no producer emitted it from live-shaped rows; emitted=%v declared=%v", kind, sortedKinds(emitted), sortedKinds(declared))
		}
	}
}

// TestProjectedSubjectKindsIsTheUnionOfBothRegistries pins the accessor
// itself against the per-table declarations it is built from -- the half
// neither pin above can see, since both quantify over one registry.
func TestProjectedSubjectKindsIsTheUnionOfBothRegistries(t *testing.T) {
	t.Parallel()
	union := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, kinds := range devhealthsource.EntityTableSubjectKindsForTest() {
		for _, kind := range kinds {
			union[kind] = true
		}
	}
	reported := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, kind := range devhealthsource.ProjectedSubjectKinds() {
		if reported[kind] {
			t.Fatalf("ProjectedSubjectKinds() reports %q twice; a caller counting kinds would over-count", kind)
		}
		reported[kind] = true
	}
	if len(reported) != len(union) {
		t.Fatalf("ProjectedSubjectKinds() = %v, want the union of both registries' declarations %v", sortedKinds(reported), sortedKinds(union))
	}
	for kind := range union {
		if !reported[kind] {
			t.Errorf("ProjectedSubjectKinds() omits %q, which a producer registry declares", kind)
		}
	}
}
