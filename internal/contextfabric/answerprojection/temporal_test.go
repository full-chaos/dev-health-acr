package answerprojection

import (
	"strconv"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// historicalAxes is every non-current axis the contract defines. The tests
// below run over all of them rather than picking one, because the axes have
// different SHAPES (a point-in-time axis carries as_of, a range carries
// start/end) and a copy that handles one shape is no evidence about the
// other.
var historicalAxes = []contractsv1.ContextFabricTemporalAxis{
	contractsv1.ContextFabricTemporalValidTime,
	contractsv1.ContextFabricTemporalObservedTime,
	contractsv1.ContextFabricTemporalRange,
}

// historicalResult builds a valid result on axis, whose effective time is
// deliberately NARROWER than the requested one -- the interesting case,
// because a projection that dropped the label or confused the two fields
// would still look right if they were equal.
func historicalResult(t *testing.T, axis contractsv1.ContextFabricTemporalAxis) contractsv1.ContextFabricInvestigationResult {
	t.Helper()
	requestedInstant := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	effectiveInstant := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	windowStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	effectiveEnd := time.Date(2026, 3, 13, 0, 0, 0, 0, time.UTC)

	requested := contractsv1.ContextFabricTimeContext{Axis: axis}
	effective := contractsv1.ContextFabricTimeContext{Axis: axis}
	switch axis {
	case contractsv1.ContextFabricTemporalRange:
		requested.Start, requested.End = &windowStart, &requestedInstant
		effective.Start, effective.End = &windowStart, &effectiveEnd
	default:
		requested.AsOf = &requestedInstant
		effective.AsOf = &effectiveInstant
	}

	result := richResult()
	result.Interpretation.TimeContext = requested
	result.Temporal = &contractsv1.ContextFabricTemporalLabel{
		Requested:        requested,
		Effective:        effective,
		Grain:            contractsv1.ContextFabricGrainDay,
		CoverageComplete: false,
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the %s fixture is not a valid result, so it proves nothing: %v", axis, err)
	}
	return result
}

// TestEveryHistoricalResultProjectsItsTemporalLabel is the structural half
// of the projection's temporal rule.
//
// ContextFabricInvestigationResult.Validate refuses a non-current axis with
// no label (AC-3781-2). The projection cannot restate that rule -- it
// carries no interpretation, so it cannot read the axis it would test
// against. What closes the gap instead is this: Project copies the label
// from an already-valid result, so the only way a historical answer can
// reach a bounded consumer unlabeled is if Project drops it.
//
// That is exactly what this asserts, over every historical axis. Mutation:
// returning nil from projectTemporal, or removing the Temporal assignment
// in Project, fails all three subtests.
func TestEveryHistoricalResultProjectsItsTemporalLabel(t *testing.T) {
	for _, axis := range historicalAxes {
		t.Run(string(axis), func(t *testing.T) {
			result := historicalResult(t, axis)

			projection := Project(result, Budget{})
			if err := projection.Validate(); err != nil {
				t.Fatalf("a valid historical result produced an invalid projection: %v", err)
			}
			if projection.Temporal == nil {
				t.Fatal("a historical answer reached a bounded consumer with no temporal label")
			}
			if got, want := projection.Temporal.Requested.Axis, result.Temporal.Requested.Axis; got != want {
				t.Errorf("projected requested axis = %q, want %q", got, want)
			}
			if got, want := projection.Temporal.Grain, result.Temporal.Grain; got != want {
				t.Errorf("projected grain = %q, want %q", got, want)
			}
			if projection.Temporal.CoverageComplete != result.Temporal.CoverageComplete {
				t.Errorf("projected coverage_complete = %v, want %v", projection.Temporal.CoverageComplete, result.Temporal.CoverageComplete)
			}
		})
	}
}

// TestProjectedTemporalLabelIsVerbatimAndNarrower proves the copy is a copy.
//
// The dangerous failure is not a missing label but a WRONG one: an
// effective time reported as the requested time reads as full coverage of a
// window the sources never covered, which is the H6 defect with a label
// attached. So both instants are compared field by field, and the narrowing
// relation is asserted to survive.
func TestProjectedTemporalLabelIsVerbatimAndNarrower(t *testing.T) {
	for _, axis := range historicalAxes {
		t.Run(string(axis), func(t *testing.T) {
			result := historicalResult(t, axis)
			projection := Project(result, Budget{})

			assertSameInstant(t, "requested.as_of", projection.Temporal.Requested.AsOf, result.Temporal.Requested.AsOf)
			assertSameInstant(t, "requested.start", projection.Temporal.Requested.Start, result.Temporal.Requested.Start)
			assertSameInstant(t, "requested.end", projection.Temporal.Requested.End, result.Temporal.Requested.End)
			assertSameInstant(t, "effective.as_of", projection.Temporal.Effective.AsOf, result.Temporal.Effective.AsOf)
			assertSameInstant(t, "effective.start", projection.Temporal.Effective.Start, result.Temporal.Effective.Start)
			assertSameInstant(t, "effective.end", projection.Temporal.Effective.End, result.Temporal.Effective.End)

			// The fixture is deliberately narrowed, so a projection that
			// substituted requested for effective would be caught here even
			// if every pointer above happened to be non-nil.
			if axis == contractsv1.ContextFabricTemporalRange {
				if !projection.Temporal.Effective.End.Before(*projection.Temporal.Requested.End) {
					t.Error("the projected effective window is no longer narrower than the requested one")
				}
				return
			}
			if !projection.Temporal.Effective.AsOf.Before(*projection.Temporal.Requested.AsOf) {
				t.Error("the projected effective instant is no longer earlier than the requested one")
			}
		})
	}
}

// TestProjectDoesNotAliasTheTemporalLabel closes the aliasing half of
// TestProjectDoesNotMutateTheCanonicalResult for the new pointer field.
//
// Project must not hand a consumer a view into the caller's own result.
// Sharing the *ContextFabricTemporalLabel, or its time pointers, would let
// a mutation of the projection silently rewrite what time the STORED
// answer claims to speak for.
func TestProjectDoesNotAliasTheTemporalLabel(t *testing.T) {
	result := historicalResult(t, contractsv1.ContextFabricTemporalValidTime)
	original := *result.Temporal.Effective.AsOf

	projection := Project(result, Budget{})
	if projection.Temporal == result.Temporal {
		t.Fatal("the projection shares the result's temporal label pointer")
	}
	moved := original.Add(-72 * time.Hour)
	*projection.Temporal.Effective.AsOf = moved
	if !result.Temporal.Effective.AsOf.Equal(original) {
		t.Errorf("mutating the projection moved the canonical effective time to %s", result.Temporal.Effective.AsOf)
	}
}

// TestCurrentAxisResultsCarryNoTemporalLabel is the converse. A current
// answer must stay exactly what it was before CHAOS-3781: nil, not an
// invented "current" label. A non-nil label on the current axis is
// rejected by the contract, so inventing one would break every current
// answer.
func TestCurrentAxisResultsCarryNoTemporalLabel(t *testing.T) {
	result := richResult()
	if result.Interpretation.TimeContext.Axis != contractsv1.ContextFabricTemporalCurrent {
		t.Fatalf("fixture drifted off the current axis (%q), so this proves nothing", result.Interpretation.TimeContext.Axis)
	}

	projection := Project(result, Budget{})
	if err := projection.Validate(); err != nil {
		t.Fatalf("a current-axis result produced an invalid projection: %v", err)
	}
	if projection.Temporal != nil {
		t.Errorf("a current answer carries a temporal label: %+v", projection.Temporal)
	}
}

func assertSameInstant(t *testing.T, field string, got, want *time.Time) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil:
		t.Errorf("%s is absent from the projection, want %s", field, want)
	case want == nil:
		t.Errorf("%s is %s in the projection, want absent", field, got)
	case !got.Equal(*want):
		t.Errorf("%s = %s, want the canonical %s", field, got, want)
	}
}

// legacyLimitations returns count distinct model-authored limitations with
// the retrieval-degradation disclosure LAST, which is where the engine puts
// it -- and therefore exactly where a prefix-keeping cut loses it.
func legacyLimitations(count int) []string {
	limitations := make([]string, 0, count)
	for i := 0; i < count-1; i++ {
		limitations = append(limitations, "Legacy model caveat number "+strconv.Itoa(i)+".")
	}
	return append(limitations, contractsv1.ContextFabricRetrievalDegradedLimitation)
}

// TestLegacyReadRetainsTheDegradationDisclosure is the read-side half of
// CHAOS-3746's limitation-retention decision.
//
// A stored row written when the canonical cap was 250 can carry 250
// limitations, with the disclosure last. The projection keeps a prefix of
// 100, so the disclosure was precisely the entry it dropped: a bounded
// consumer received a hundred model caveats and no statement that retrieval
// had been degraded, which reads as a cleaner answer than the investigation
// gave. Nothing looked wrong from the canonical view, which still carried
// it -- only the bounded consumer was misled.
//
// Both spellings are covered: a legacy row is exactly the row most likely
// to carry the legacy wording, so testing only the current one would miss
// the case this is about.
func TestLegacyReadRetainsTheDegradationDisclosure(t *testing.T) {
	for name, disclosure := range map[string]string{
		"current": contractsv1.ContextFabricRetrievalDegradedLimitation,
		"legacy":  contractsv1.ContextFabricRetrievalDegradedLimitationLegacy,
	} {
		t.Run(name, func(t *testing.T) {
			const stored = 250
			result := richResult()
			limitations := legacyLimitations(stored)
			limitations[len(limitations)-1] = disclosure
			result.Limitations = limitations
			if err := result.ValidateStored(); err != nil {
				t.Fatalf("a legacy row with %d limitations is unreadable, so this proves nothing: %v", stored, err)
			}

			projection := Project(result, Budget{})
			if err := projection.Validate(); err != nil {
				t.Fatalf("a legacy stored row produced an invalid projection: %v", err)
			}

			found := false
			for _, limitation := range projection.Limitations {
				if contractsv1.IsContextFabricRetrievalDegradedLimitation(limitation) {
					found = true
				}
			}
			if !found {
				t.Error("the degradation disclosure was cut, so a bounded consumer reads a degraded answer as a clean one")
			}
			if got, want := len(projection.Limitations), contractsv1.ContextFabricProjectedNarrativeMaxCount; got != want {
				t.Errorf("projected %d limitations, want the cap %d: retention must displace, never widen", got, want)
			}
			// The displaced entry is genuinely gone, so it is counted like
			// every other drop -- a shortened list that reported no
			// omission would be the silent truncation this contract
			// forbids.
			if want := stored - contractsv1.ContextFabricProjectedNarrativeMaxCount; projection.ProjectionBudget.LimitationsOmitted != want {
				t.Errorf("limitations_omitted = %d, want %d", projection.ProjectionBudget.LimitationsOmitted, want)
			}
			if !projection.ProjectionBudget.Truncated {
				t.Error("dropping limitations must set truncated")
			}
		})
	}
}

// TestRetentionDoesNotDisturbAListThatFits proves the priority is inert
// below the cap. A rescue that reordered or displaced anything when there
// was room would be a behaviour change disguised as a safety net.
func TestRetentionDoesNotDisturbAListThatFits(t *testing.T) {
	result := richResult()
	result.Limitations = legacyLimitations(5)

	projection := Project(result, Budget{})

	if len(projection.Limitations) != 5 {
		t.Fatalf("projected %d limitations from 5", len(projection.Limitations))
	}
	for i, limitation := range projection.Limitations {
		if limitation != result.Limitations[i] {
			t.Errorf("limitation %d changed from %q to %q", i, result.Limitations[i], limitation)
		}
	}
	if projection.ProjectionBudget.LimitationsOmitted != 0 {
		t.Errorf("limitations_omitted = %d, want 0", projection.ProjectionBudget.LimitationsOmitted)
	}
}

// TestWarningsHaveNoRetentionPriority states the asymmetry deliberately.
// Warnings carry no service-authored disclosure, so nothing there outranks
// anything else and the plain prefix cut is correct for them.
func TestWarningsHaveNoRetentionPriority(t *testing.T) {
	result := richResult()
	warnings := make([]string, 0, 150)
	for i := 0; i < 149; i++ {
		warnings = append(warnings, "Warning number "+strconv.Itoa(i)+".")
	}
	// A string that WOULD be rescued if the predicate were wired here.
	warnings = append(warnings, contractsv1.ContextFabricRetrievalDegradedLimitation)
	result.Warnings = warnings
	if err := result.ValidateStored(); err != nil {
		t.Fatalf("the legacy warning fixture is unreadable: %v", err)
	}

	projection := Project(result, Budget{})

	for _, warning := range projection.Warnings {
		if contractsv1.IsContextFabricRetrievalDegradedLimitation(warning) {
			t.Error("a warning was rescued past the cut; retention priority belongs to limitations alone")
		}
	}
}
