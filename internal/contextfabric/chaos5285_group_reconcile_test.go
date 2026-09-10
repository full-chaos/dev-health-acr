package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupReconcileBundle is the group read's bundle, carrying every CARRIER a
// bundle can carry rather than facts alone -- versions, watermarks and a
// temporal grain. The point of the reconcile stage is that all of them survive
// composition, and a fixture that returned facts alone could not tell a merge
// that preserved them from one that dropped them.
func groupReconcileBundle(grain TemporalGrain, version, watermark string) CanonicalFactBundle {
	bundle := emptyFactBundle()
	bundle.Facts = []CanonicalFact{{
		Kind:        FactHealth,
		Subject:     SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("team_security"), Label: "Security"},
		Fields:      map[string]FactValue{"severity": StringFactValue("elevated")},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}}
	bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
	bundle.Versions = map[FactKind]string{FactHealth: version}
	bundle.Watermarks = map[FactKind]string{FactHealth: watermark}
	bundle.TemporalGrain = grain
	return bundle
}

// TestTheGroupReadsOwnCarriersSurviveComposition is CHAOS-5285 test-table
// row 13, the reconcile row.
//
// A second read contributes more than facts. Its provider versions, its
// watermarks and its temporal grain are the provenance of the evidence it
// brought, and evidence whose provenance did not survive composition is
// evidence with no provenance -- a failure that is SILENT, because the facts
// themselves look complete. The grain is the sharpest case: an answer is only
// as precise as its least precise source, so a day-grain group read must
// coarsen an instant-grain member read, and a merge that kept the first read's
// grain would overstate the precision of the very data the answer is built
// from.
func TestTheGroupReadsOwnCarriersSurviveComposition(t *testing.T) {
	t.Parallel()

	into := emptyFactBundle()
	into.Facts = groupReadMemberFacts()
	into.Versions = map[FactKind]string{FactMetrics: "metrics-v7"}
	into.Watermarks = map[FactKind]string{FactMetrics: "2026-09-09T00:00:00Z"}
	into.TemporalGrain = GrainInstant

	group := groupReconcileBundle(GrainDay, "health-v3", "2026-09-08T00:00:00Z")
	conflicted := mergeGroupBundle(&into, group, "org_1")
	t.Logf("conflicted=%v facts=%d versions=%v watermarks=%v grain=%q",
		conflicted, len(into.Facts), into.Versions, into.Watermarks, into.TemporalGrain)

	if conflicted {
		t.Fatalf("the two reads were reported as conflicting, but they name DIFFERENT fact kinds -- nothing disagrees")
	}
	if got := into.Versions[FactHealth]; got != "health-v3" {
		t.Errorf("the group read's version for %q = %q, want %q -- a group fact whose provider version never reached the turn is evidence with no provenance, and nothing downstream would say so",
			FactHealth, got, "health-v3")
	}
	if got := into.Watermarks[FactHealth]; got != "2026-09-08T00:00:00Z" {
		t.Errorf("the group read's watermark for %q = %q, want it preserved", FactHealth, got)
	}
	if got := into.Versions[FactMetrics]; got != "metrics-v7" {
		t.Errorf("the MEMBER read's version for %q = %q, want it untouched -- composition must not cost the first read its own provenance", FactMetrics, got)
	}
	if into.TemporalGrain != GrainDay {
		t.Errorf("composed grain = %q, want %q -- an answer is only as precise as its least precise source, and keeping the finer grain overstates the precision of the data the answer was built from",
			into.TemporalGrain, GrainDay)
	}
}

// TestTwoReadsThatDisagreeAboutOneKindsMetadataAreNotComposed is the other
// half of row 13: conflicting opaque metadata takes the refusal path rather
// than an invented ordering.
//
// Neither read is the authority on the other's version string. Picking one --
// newest, last writer, or the group read because it ran second -- would be an
// ordering no producer declared. The refusal must also leave the turn's bundle
// EXACTLY as it was: a half-merged bundle carrying some of the group read's
// facts under the member read's versions is worse than either outcome.
func TestTwoReadsThatDisagreeAboutOneKindsMetadataAreNotComposed(t *testing.T) {
	t.Parallel()

	into := emptyFactBundle()
	into.Facts = groupReadMemberFacts()
	into.Versions = map[FactKind]string{FactHealth: "health-v3"}
	factsBefore := len(into.Facts)

	group := groupReconcileBundle(GrainDay, "health-v4", "2026-09-08T00:00:00Z")
	conflicted := mergeGroupBundle(&into, group, "org_1")
	t.Logf("conflicted=%v facts=%d versions=%v", conflicted, len(into.Facts), into.Versions)

	if !conflicted {
		t.Fatalf("two reads reporting %q at versions %q and %q were composed anyway -- the turn now carries one kind's evidence under two provenances and says so nowhere",
			FactHealth, "health-v3", "health-v4")
	}
	if len(into.Facts) != factsBefore {
		t.Errorf("facts = %d after a refused composition, want %d -- a refusal must leave the bundle untouched, not half-merged", len(into.Facts), factsBefore)
	}
	if got := into.Versions[FactHealth]; got != "health-v3" {
		t.Errorf("version for %q = %q after a refused composition, want the turn's own %q", FactHealth, got, "health-v3")
	}
}

// TestTheGroupReadsScopeDecisionsReachTheOperator is row 13's telemetry half.
//
// Every scope-expansion decision a read makes is owed to the operator
// immediately -- whether it expanded, declined or failed. The group read
// resolves its scope over a DIFFERENT root population than the member read,
// so its decisions are not a subset of the first read's and cannot be inferred
// from them. Before this stage they had no representation anywhere.
func TestTheGroupReadsScopeDecisionsReachTheOperator(t *testing.T) {
	t.Parallel()

	scoped := func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		// A scope record on EVERY read, so the assertion below counts
		// emissions rather than the presence of a scope on one arm.
		bundle.Scope = &FactReadScope{Events: []FactScopeExpansionEvent{{RequirementKind: FactMetrics}}}
		return bundle
	}
	recorder := &groupReadRecorder{facts: scoped}
	telemetry := &recordingTelemetry{}
	engine, request := groupReadEngineFixture(t, telemetry, recorder)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	t.Logf("fact requests issued=%d scope expansions emitted=%d", len(recorder.requests), len(telemetry.factScopeExpansions))
	if len(recorder.requests) != 2 {
		t.Fatalf("fixture defect: %d fact requests issued, want 2 (the member read and the group read) -- this pin needs both to exist", len(recorder.requests))
	}
	if len(telemetry.factScopeExpansions) != len(recorder.requests) {
		t.Errorf("%d scope-expansion emissions for %d fact reads -- the group read resolves its scope over a different root population, so its decisions are not a subset of the member read's and are unrecoverable if they are never emitted",
			len(telemetry.factScopeExpansions), len(recorder.requests))
	}
}
