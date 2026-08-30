package devhealthsource_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
)

// CHAOS-4565. Suppression is a decision NOT TO ASSERT. It is not a
// retraction, and incremental graph application does not delete an absent
// relationship -- so an OWNED_BY_TEAM edge projected BEFORE its ownership row
// became suppressed stayed live indefinitely, and only a full rebuild cleared
// it. The graph could not tell "we never asserted this" from "we can no
// longer substantiate it".
//
// These tests cover the SCAN half of the fix: the decision to retract, the
// id it retracts, the reason vocabulary, idempotency, and the invariant that a
// batch never asserts and retracts the same edge. The SQL half -- making an
// ambiguous key visible at all, and moving the cursor when a projects-side
// write is what suppressed the row -- cannot be tested here, because
// fakeClient returns canned rows without executing the statement. That half is
// TestOwnershipProducerAgainstRealClickHouse's
// "a projected edge is retracted ..." subtests, and it is the binding proof.

// suppressedProjectTeamRow is the aggregate row the producer's SQL emits for a
// group with NO asserting row: edge_suppressed = 1. conflictingIdentity says
// which of the two suppression paths produced it, which is exactly the
// distinction the retraction reason carries.
func suppressedProjectTeamRow(projectID, teamID, source string, at time.Time, conflictingIdentity uint8) []any {
	identities := []string{}
	if conflictingIdentity == 1 {
		identities = []string{"own-ref\x00OWN-KEY\x00" + teamID + "\x00" + source}
	}
	return []any{projectID, teamID, source, at, uint8(1), time.Unix(0, 0).UTC(), at, "github", uint8(1), identities, conflictingIdentity}
}

func retractionClient(rows ...[]any) *fakeClient {
	return &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership FINAL", rows: rows}}}
}

// retractionBatch runs ONE incremental pass. The cursor is non-empty on
// purpose: the from-scratch path is a full snapshot, and the whole defect
// lives in the INCREMENTAL path -- proving retraction through a snapshot would
// prove nothing, because a snapshot is the rebuild this ticket exists to stop
// requiring.
func retractionBatch(t *testing.T, client *fakeClient, logged *bytes.Buffer) contextfabric.ProjectionBatch {
	t.Helper()
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	if logged != nil {
		source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{
		OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName,
		Cursor: testCursor(t, time.Unix(0, 0).UTC(), ""),
	})
	if err != nil {
		t.Fatalf("NextProjectionBatch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch: a page of suppressed rows now CARRIES a payload -- the retraction itself -- so it must publish rather than be skipped as fully-omitted")
	}
	return batch
}

func tombstoneIDs(batch contextfabric.ProjectionBatch, kind string) []string {
	ids := []string{}
	for _, tombstone := range batch.Tombstones {
		if strings.EqualFold(tombstone.Kind, kind) {
			ids = append(ids, tombstone.CanonicalID)
		}
	}
	return ids
}

// The retraction must name the EXACT edge the same group would have asserted.
//
// This is the one failure this whole change reduces to. applyTombstone
// matches on relationship_id: a tombstone whose canonical id differs from the
// projected edge's by one byte deletes nothing, returns no error, and is
// counted as applied -- so every log, receipt and counter in the pipeline
// reports a successful retraction while the stale edge is still in the graph.
// A test that only asserted "a tombstone was emitted" would pass on that.
//
// So the id is not spelled out here as a literal. The SAME group is run twice
// -- once with a row that asserts, once with a row that is suppressed -- and
// the two ids must be equal. That compares the production assertion path
// against the production retraction path, which a hand-copied string cannot
// do: both spellings would have to be wrong in the same way to pass.
func TestChaos4565_TheRetractionNamesTheEdgeTheSameGroupWouldAssert(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC)

	asserted := retractionBatch(t, retractionClient(
		projectTeamRow("proj-a", "team-x", "native", at, 1, time.Unix(0, 0).UTC(), at)), nil)
	if len(asserted.Relationships) != 1 {
		t.Fatalf("control: want exactly one asserted edge, got %d -- the comparison below is meaningless without it", len(asserted.Relationships))
	}
	assertedID := asserted.Relationships[0].RelationshipID

	retracted := retractionBatch(t, retractionClient(
		suppressedProjectTeamRow("proj-a", "team-x", "native", at, 1)), nil)
	got := tombstoneIDs(retracted, "relationship")
	if len(got) != 1 {
		t.Fatalf("a suppressed group emitted %d relationship tombstones, want exactly 1 -- before CHAOS-4565 it emitted a bare progress candidate and the previously projected edge stayed live forever", len(got))
	}
	if got[0] != assertedID {
		t.Fatalf("retraction names %q but the same group asserts %q -- applyTombstone matches on relationship_id, so this deletes NOTHING while every counter reports it applied", got[0], assertedID)
	}
	if len(retracted.Relationships) != 0 {
		t.Fatalf("a suppressed group asserted %d edges as well as retracting one", len(retracted.Relationships))
	}
}

// Both suppression paths must retract, and they must stay distinguishable.
//
// Fixing only the conflicting-identity path leaves the OLDER ambiguity hole
// open -- it has been live since v7 and is invisible only because every
// intervening source-version bump happened to rebuild. And folding the two
// into one number would tell an operator that something was withdrawn without
// saying which kind of wrong the data is, which is the exact distinction
// logConflictingIdentities and the catalog ambiguity line already keep apart.
func TestChaos4565_BothSuppressionPathsRetractUnderTheirOwnReason(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC)

	for _, testCase := range []struct {
		name                string
		conflictingIdentity uint8
		wantCount           string
		wantNotCount        string
		wantReason          string
	}{
		{
			name: "ambiguous key", conflictingIdentity: 0,
			wantCount:    "ownership_edge_tombstones_ambiguous_key=1",
			wantNotCount: "ownership_edge_tombstones_conflicting_identity=1",
			wantReason:   "project_key names more than one project",
		},
		{
			name: "conflicting identity", conflictingIdentity: 1,
			wantCount:    "ownership_edge_tombstones_conflicting_identity=1",
			wantNotCount: "ownership_edge_tombstones_ambiguous_key=1",
			wantReason:   "project_id and project_key resolve to different projects",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			logged := &bytes.Buffer{}
			batch := retractionBatch(t, retractionClient(
				suppressedProjectTeamRow("proj-a", "team-x", "native", at, testCase.conflictingIdentity)), logged)
			if len(batch.Tombstones) != 1 {
				t.Fatalf("want one retraction, got %d", len(batch.Tombstones))
			}
			if !strings.Contains(batch.Tombstones[0].Reason, testCase.wantReason) {
				t.Errorf("tombstone reason = %q, want it to name %q -- the reason travels with the retraction into the graph backend's own record of why the edge went", batch.Tombstones[0].Reason, testCase.wantReason)
			}
			output := logged.String()
			if !strings.Contains(output, testCase.wantCount) {
				t.Errorf("telemetry does not report %s; an unlogged retraction is a graph mutation nobody can see:\n%s", testCase.wantCount, output)
			}
			if strings.Contains(output, testCase.wantNotCount) {
				t.Errorf("telemetry reported %s for a %s suppression -- the two reasons answer different operator questions and must not bleed into each other:\n%s", testCase.wantNotCount, testCase.name, output)
			}
		})
	}
}

// The control, and it is not decoration: the cheapest way to make every
// retraction assertion above pass is to tombstone everything.
func TestChaos4565_AnEdgeThatStillResolvesIsNeverRetracted(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC)
	batch := retractionBatch(t, retractionClient(
		projectTeamRow("proj-clean", "team-x", "native", at, 1, time.Unix(0, 0).UTC(), at)), nil)
	if ids := tombstoneIDs(batch, "relationship"); len(ids) != 0 {
		t.Fatalf("retracted %v for a group that still has an asserting row -- retraction must follow suppression, not accompany every pass", ids)
	}
	if len(batch.Relationships) != 1 {
		t.Fatalf("want the edge asserted, got %d relationships", len(batch.Relationships))
	}
}

// A batch must never both assert and retract one edge.
//
// falkorgraph applies tombstones AFTER relationships (projection.go), so a
// batch holding both for one relationship_id writes the edge and then deletes
// it -- a self-inflicted version of the exact defect this ticket removes.
// No single GROUP does both: edge_suppressed is a group property and a group
// with one asserting row is never suppressed. That is what this pins.
//
// It is NOT a proof about the whole batch, and the difference cost a review
// round. Extending it to the batch needs distinct groups to get distinct ids,
// and projectTeamRelationshipID is a colon concatenation over id spaces that
// contain colons, so two groups CAN collide (CHAOS-4635). The batch-level
// property is enforced where it belongs instead --
// contracts/v1's validateProjectionRelationshipTombstoneCollision, which
// rejects the pair outright. Read this test as "the producer does not build
// the pair on purpose", never as "the pair cannot occur". a group that holds a clean row beside a conflicting
// one keeps its edge and is not retracted, in the same batch as a genuinely
// suppressed group that is.
func TestChaos4565_NoBatchAssertsAndRetractsTheSameEdge(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC)
	// A shared group (a clean row beside a conflicting one: edge_suppressed
	// stays 0 and the conflict is still recorded), plus a group that really
	// has no asserting row.
	sharedGroup := []any{"proj-shared", "team-x", "native", at, uint8(1), time.Unix(0, 0).UTC(), at, "github",
		uint8(0), []string{"own-ref-conflicting\x00KEY-B"}, uint8(1)}
	batch := retractionBatch(t, retractionClient(
		sharedGroup,
		suppressedProjectTeamRow("proj-gone", "team-x", "native", at, 1),
	), nil)

	retracted := map[string]struct{}{}
	for _, id := range tombstoneIDs(batch, "relationship") {
		retracted[id] = struct{}{}
	}
	for _, relationship := range batch.Relationships {
		if _, both := retracted[relationship.RelationshipID]; both {
			t.Fatalf("batch asserts AND retracts %q; tombstones apply after relationships, so this writes the edge and immediately deletes it", relationship.RelationshipID)
		}
	}
	if len(retracted) != 1 {
		t.Fatalf("want exactly the one genuinely suppressed group retracted, got %d: %v", len(retracted), retracted)
	}
	if len(batch.Relationships) != 1 {
		t.Fatalf("the shared group lost its edge -- a clean row's assertion outranks a conflicting row in the same group; got %d relationships", len(batch.Relationships))
	}
}

// Re-running the pass over the same data must change nothing.
//
// Idempotency here is not an optimisation, it is what lets the retraction be
// emitted UNCONDITIONALLY. This producer is backend-neutral and cannot ask the
// graph whether the edge is present, so it never knows whether a retraction is
// "necessary"; applyTombstone's DELETE matching zero rows is what makes the
// never-projected case and the re-run case the same no-op. The observable
// consequence is that two passes over identical data produce identical
// batches -- same batch id, same tombstone set -- which is also what makes a
// replayed batch safe.
func TestChaos4565_ReRunningThePassProducesTheIdenticalRetraction(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC)
	rows := []any{"proj-a", "team-x", "native", at, uint8(1), time.Unix(0, 0).UTC(), at, "github", uint8(1), []string{}, uint8(0)}

	first := retractionBatch(t, retractionClient(rows), nil)
	second := retractionBatch(t, retractionClient(rows), nil)

	if first.BatchID != second.BatchID {
		t.Fatalf("batch id changed across an identical re-run (%q vs %q); ApplyProjectionBatch's replay idempotency is keyed on it", first.BatchID, second.BatchID)
	}
	if first.NextCursor != second.NextCursor {
		t.Fatalf("cursor changed across an identical re-run (%q vs %q)", first.NextCursor, second.NextCursor)
	}
	firstIDs, secondIDs := tombstoneIDs(first, "relationship"), tombstoneIDs(second, "relationship")
	if len(firstIDs) != 1 || len(secondIDs) != 1 || firstIDs[0] != secondIDs[0] {
		t.Fatalf("retraction is not stable across a re-run: %v then %v", firstIDs, secondIDs)
	}
	if !first.Tombstones[0].EffectiveAt.Equal(second.Tombstones[0].EffectiveAt) {
		t.Fatalf("effective_at moved across an identical re-run (%v vs %v); it orders the tombstone against the edge it retires, so a drifting value makes the retraction's own staleness guard non-deterministic",
			first.Tombstones[0].EffectiveAt, second.Tombstones[0].EffectiveAt)
	}
}

// A retraction is ordered against the edge it retires by EffectiveAt, and it
// must be able to remove an edge whose own ObservedAt equals it.
//
// This is not a hypothetical boundary, it is the COMMON case for the
// ambiguity path. Ambiguity usually arrives because a new project starts
// sharing an existing key -- the ownership row itself is untouched -- so the
// group's watermark, and therefore the tombstone's EffectiveAt, can be exactly
// the value the live edge already carries. applyTombstone's guard is
// `observed_at_ns IS NULL OR observed_at_ns <= effectiveNs`; a strict `<`
// there would silently refuse every retraction of that shape, so the tombstone
// must not be stamped earlier than the edge the same data produces.
func TestChaos4565_TheRetractionIsNotStampedBeforeTheEdgeItRetires(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 13, 19, 0, 0, 0, time.UTC)

	asserted := retractionBatch(t, retractionClient(
		projectTeamRow("proj-a", "team-x", "native", at, 1, time.Unix(0, 0).UTC(), at)), nil)
	retracted := retractionBatch(t, retractionClient(
		suppressedProjectTeamRow("proj-a", "team-x", "native", at, 1)), nil)

	edgeObservedAt := asserted.Relationships[0].ObservedAt
	if retracted.Tombstones[0].EffectiveAt.Before(edgeObservedAt) {
		t.Fatalf("tombstone effective_at %v is BEFORE the edge's observed_at %v for identical source data -- applyTombstone would treat the retraction as stale and match zero rows",
			retracted.Tombstones[0].EffectiveAt, edgeObservedAt)
	}
}
