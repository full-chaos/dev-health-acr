package devhealthsource

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestIdentityUniverseCoversExactlyTheAliasLookupScopedKinds cross-checks
// devhealthsource's OWN source-table coverage (identityUniverseKinds)
// against graphrank's counting-scope registry (isAliasLookupScopedKind, via
// its exported mirror) -- the two registries live in different packages by
// design and must never silently drift apart. Covers the count AND that
// every kind actually queried is in scope.
func TestIdentityUniverseCoversExactlyTheAliasLookupScopedKinds(t *testing.T) {
	t.Parallel()
	want := []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectRepository,
		contractsv1.ContextFabricSubjectProject,
		contractsv1.ContextFabricSubjectTeam,
	}
	for _, kind := range want {
		if !graphrank.IsAliasLookupScopedKind(kind) {
			t.Errorf("graphrank.IsAliasLookupScopedKind(%q) = false, want true -- devhealthsource's IdentityUniverse queries this kind", kind)
		}
	}
	if len(identityUniverseKinds) != len(want) {
		t.Fatalf("identityUniverseKinds has %d entries, want %d (one per graphrank-scoped kind) -- a registry drifted", len(identityUniverseKinds), len(want))
	}
	// work_item is DELIBERATELY out of scope for slice 1 (decision 2) --
	// the negative half of the parity check: devhealthsource must not
	// silently widen back to a kind graphrank does not (yet) count.
	if graphrank.IsAliasLookupScopedKind(contractsv1.ContextFabricSubjectWorkItem) {
		t.Error("graphrank.IsAliasLookupScopedKind(work_item) = true, want false -- slice-1 decision 2 excludes it; widening it back is a deliberate, separate decision")
	}
}

// fakeIdentityQuery is an entityTable.query double: it ignores client and
// limit entirely, returning n synthetic single-alias candidates for kind on
// the FIRST call (cursor.After == "") and nothing (truncated=false) on any
// subsequent call, so fetchIdentityKind's drain loop terminates after one
// page regardless of n.
func fakeIdentityQuery(kind contractsv1.ContextFabricSubjectKind, n int) func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	return func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		if cursor.After != "" {
			return nil, false, nil
		}
		observedAt := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
		rows := make([]candidate, 0, n)
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s-%d", orgID, i)
			entity := &contractsv1.ContextFabricEntityProjection{
				Subject:    contractsv1.ContextFabricSubjectRef{Kind: kind, CanonicalID: string(kind) + ":" + id, Label: id},
				Aliases:    []string{id},
				ObservedAt: observedAt,
			}
			rows = append(rows, candidate{observedAt: observedAt, sortKey: id, entity: entity})
		}
		return rows, false, nil
	}
}

// TestIdentityUniverse_AnyShortBranchPoisonsCompletenessGlobally pins
// decision 2's second half (team-lead amendment, 2026-08-17, settled): the
// row budget is evaluated PER BRANCH (fetchIdentityKind's own local `rows`,
// one independent count per registered kind), but a hit on ANY ONE branch
// must poison complete=false for the WHOLE call -- never just that one
// kind's own contribution. Swaps identityUniverseKinds for two fakes: one
// kind that exceeds identityUniverseRowBudget on its own, one that stays
// well under it, and asserts both that complete is false AND that the
// under-budget kind's real rows still made it into the result (a short
// branch poisons the PROOF, not the DATA).
func TestIdentityUniverse_AnyShortBranchPoisonsCompletenessGlobally(t *testing.T) {
	original := identityUniverseKinds
	t.Cleanup(func() { identityUniverseKinds = original })
	identityUniverseKinds = []entityTable{
		{name: "over-budget", query: fakeIdentityQuery(contractsv1.ContextFabricSubjectRepository, identityUniverseRowBudget+1)},
		{name: "under-budget", query: fakeIdentityQuery(contractsv1.ContextFabricSubjectProject, 3)},
	}

	rows, _, complete, err := IdentityUniverse(context.Background(), nil, "org-budget-test")
	if err != nil {
		t.Fatalf("IdentityUniverse() error = %v", err)
	}
	if complete {
		t.Fatal("IdentityUniverse() complete = true, want false -- the over-budget branch alone must poison the whole call")
	}
	projectRows := 0
	for _, row := range rows {
		if row.Kind == contractsv1.ContextFabricSubjectProject {
			projectRows++
		}
	}
	if projectRows != 3 {
		t.Fatalf("project rows returned = %d, want 3 -- the under-budget branch's real rows must survive a sibling branch's incompleteness", projectRows)
	}
}
