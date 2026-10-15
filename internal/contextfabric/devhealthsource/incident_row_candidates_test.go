package devhealthsource

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestIncidentRowCandidatesProjectWhatAFromZeroReadProjects(t *testing.T) {
	observed := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	changed := observed.Add(48 * time.Hour)
	const (
		incidentID = "8e4b1d70-2c9a-4f63-b815-000000000001"
		repoA      = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a01"
		repoB      = "6c2f9a10-4b7e-4d21-9f3a-8e5d1c7b0a02"
	)
	base := incidentRow{id: incidentID, repoID: repoA, repoSlug: "acme/checkout", title: "checkout timeouts", observedAt: observed, cursorAt: changed, projected: true}
	run := func(row incidentRow) ([]candidate, incidentPageStats) {
		t.Helper()
		seen := map[string]bool{}
		var stats incidentPageStats
		tombstone := func(kind, id, reason string, effectiveAt, observedAt time.Time, sortKey string) []candidate {
			if seen[kind+id] {
				return []candidate{progressCandidate(observedAt, sortKey)}
			}
			seen[kind+id] = true
			return []candidate{{observedAt: observedAt, sortKey: sortKey, tombstone: &contractsv1.ContextFabricProjectionTombstone{Kind: kind, CanonicalID: id, Reason: reason, EffectiveAt: effectiveAt}}}
		}
		out, err := incidentRowCandidates(row, tombstone, &stats)
		if err != nil {
			t.Fatal(err)
		}
		return out, stats
	}

	active := base
	active.active, active.repoActive = true, true
	out, _ := run(active)
	if len(out) != 2 || out[0].entity == nil || out[1].relationship == nil {
		t.Fatalf("an active mapping with a known repository: %+v, want the entity and its edge", out)
	}
	edgeA := out[1].relationship.RelationshipID
	if edgeA != incidentRepositoryRelationshipID("incident:"+incidentID, repoA) || out[1].relationship.To.CanonicalID != "repository:"+repoA {
		t.Fatalf("edge %q to %q, want the per-repository id to repository:%s", edgeA, out[1].relationship.To.CanonicalID, repoA)
	}
	if other := incidentRepositoryRelationshipID("incident:"+incidentID, repoB); other == edgeA {
		t.Fatal("two repositories of one incident share an edge id")
	}

	ended := base
	out, stats := run(ended)
	if len(out) != 1 || out[0].tombstone == nil || out[0].tombstone.Kind != "relationship" || out[0].tombstone.CanonicalID != edgeA || !out[0].tombstone.EffectiveAt.Equal(changed) {
		t.Fatalf("an ended mapping no active mapping repeats: %+v, want the edge to repository:%s retracted at the change", out, repoA)
	}
	if stats.edgesRetracted != 1 {
		t.Fatalf("edges retracted = %d, want 1", stats.edgesRetracted)
	}

	repeated := base
	repeated.repoActive = true
	if out, _ := run(repeated); len(out) != 1 || out[0].tombstone != nil || out[0].entity != nil {
		t.Fatalf("an ended mapping whose repository another active mapping names: %+v, want only progress", out)
	}

	unmapped := base
	unmapped.projected = false
	out, stats = run(unmapped)
	if len(out) != 1 || out[0].tombstone == nil || out[0].tombstone.Kind != "incident" || out[0].tombstone.CanonicalID != "incident:"+incidentID || out[0].tombstone.Reason != "unmapped" {
		t.Fatalf("no active mapping names a known repository: %+v, want the incident retracted as unmapped", out)
	}
	if stats.unmapped != 1 {
		t.Fatalf("unmapped = %d, want 1", stats.unmapped)
	}

	unknownRepo := base
	unknownRepo.active, unknownRepo.repoActive, unknownRepo.repoSlug = true, true, ""
	if out, _ := run(unknownRepo); len(out) != 1 || out[0].entity != nil || out[0].tombstone != nil {
		t.Fatalf("an active mapping to a repository the organization does not know: %+v, want only progress", out)
	}

	deleted := base
	deleted.deleted, deleted.active = true, true
	if out, _ := run(deleted); len(out) != 1 || out[0].tombstone == nil || out[0].tombstone.Reason != "source_deleted" {
		t.Fatalf("a deleted incident: %+v, want the source_deleted tombstone", out)
	}
}
