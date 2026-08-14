package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// CHAOS-3781 (AC-3781-2) temporal-label bounds. The label is the only
// structured statement of what time an answer speaks for, so every way it
// could lie -- claiming a wider window than was asked for, disagreeing
// with the interpretation it belongs to, or being absent from a
// historical answer entirely -- has to be refused at the contract
// boundary.

func temporalTestTimes() (asOf, earlier, later time.Time) {
	asOf = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	return asOf, asOf.Add(-24 * time.Hour), asOf.Add(24 * time.Hour)
}

func validTemporalLabel() ContextFabricTemporalLabel {
	asOf, earlier, _ := temporalTestTimes()
	return ContextFabricTemporalLabel{
		Requested:        ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &asOf},
		Effective:        ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &earlier},
		Grain:            ContextFabricGrainDay,
		CoverageComplete: false,
	}
}

func TestTemporalLabelAcceptsANarrowedEffectiveTime(t *testing.T) {
	t.Parallel()

	if err := validTemporalLabel().Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a day-grain label", err)
	}

	// The exact-instant case: a source that answers at instant grain
	// makes effective equal requested, which is narrowing by zero and
	// must stay legal.
	asOf, _, _ := temporalTestTimes()
	exact := ContextFabricTemporalLabel{
		Requested:        ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &asOf},
		Effective:        ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &asOf},
		Grain:            ContextFabricGrainInstant,
		CoverageComplete: true,
	}
	if err := exact.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil when effective equals requested", err)
	}
}

func TestTemporalLabelRejectsAWiderEffectiveTime(t *testing.T) {
	t.Parallel()

	asOf, _, later := temporalTestTimes()
	start := asOf.Add(-72 * time.Hour)

	tests := []struct {
		name  string
		label ContextFabricTemporalLabel
	}{
		{
			// The core lie this guards against: answering with data
			// from AFTER the time that was asked about, then labeling
			// it as the answer for that time.
			name: "point-in-time effective after requested",
			label: ContextFabricTemporalLabel{
				Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &asOf},
				Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &later},
				Grain:     ContextFabricGrainDay,
			},
		},
		{
			name: "range effective starts before requested",
			label: ContextFabricTemporalLabel{
				Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &start, End: &asOf},
				Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: timePtr(start.Add(-time.Hour)), End: &asOf},
				Grain:     ContextFabricGrainDay,
			},
		},
		{
			name: "range effective ends after requested",
			label: ContextFabricTemporalLabel{
				Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &start, End: &asOf},
				Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &start, End: &later},
				Grain:     ContextFabricGrainDay,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.label.Validate(); err == nil {
				t.Fatal("Validate() accepted an effective time wider than the requested time")
			}
		})
	}
}

func TestTemporalLabelRejectsMalformedShapes(t *testing.T) {
	t.Parallel()

	asOf, earlier, _ := temporalTestTimes()

	tests := []struct {
		name  string
		label ContextFabricTemporalLabel
	}{
		{
			// A label on a current-axis answer would assert that a
			// present-tense answer speaks for some other time.
			name: "current axis carries no label",
			label: ContextFabricTemporalLabel{
				Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
				Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent},
				Grain:     ContextFabricGrainNone,
			},
		},
		{
			name: "axes disagree",
			label: ContextFabricTemporalLabel{
				Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &asOf},
				Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalObservedTime, AsOf: &earlier},
				Grain:     ContextFabricGrainDay,
			},
		},
		{
			name: "grain outside the closed vocabulary",
			label: ContextFabricTemporalLabel{
				Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &asOf},
				Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &earlier},
				Grain:     ContextFabricTemporalGrain("hourly"),
			},
		},
		{
			name: "requested violates its own axis shape",
			label: ContextFabricTemporalLabel{
				Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime},
				Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &earlier},
				Grain:     ContextFabricGrainDay,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.label.Validate(); err == nil {
				t.Fatalf("Validate() accepted a malformed label: %+v", test.label)
			}
		})
	}
}

// TestResultRequiresATemporalLabelOnAHistoricalAxis is the AC-3781-2
// invariant that matters most: an unlabeled historical answer is exactly
// the false historical answer the H6 refusal used to prevent, so the
// result contract itself must refuse to carry one.
func TestResultRequiresATemporalLabelOnAHistoricalAxis(t *testing.T) {
	t.Parallel()

	asOf, _, _ := temporalTestTimes()
	result := contextFabricHistoricalResult(t)

	result.Temporal = nil
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() accepted a historical result with no temporal label")
	} else if !strings.Contains(err.Error(), "temporal") {
		t.Fatalf("Validate() error = %v, want it to name the temporal label", err)
	}

	// And the converse: the label must agree with the interpretation it
	// belongs to, or a result could claim to speak for a time the reads
	// were never bounded by.
	result = contextFabricHistoricalResult(t)
	mismatched := asOf.Add(-365 * 24 * time.Hour)
	result.Temporal.Requested.AsOf = &mismatched
	result.Temporal.Effective.AsOf = &mismatched
	if err := result.Validate(); err == nil {
		t.Fatal("Validate() accepted a label disagreeing with the interpreted time context")
	}
}

// TestResultTemporalLabelSurvivesAJSONRoundTrip proves the label is
// carried by the wire format, not just the Go struct -- and that a
// current-axis result still serializes with no "temporal" key at all, so
// every pre-CHAOS-3781 result stays byte-identical.
func TestResultTemporalLabelSurvivesAJSONRoundTrip(t *testing.T) {
	t.Parallel()

	result := contextFabricHistoricalResult(t)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded); err != nil {
		t.Fatalf("re-serialized historical result failed schema validation: %v", err)
	}
	var decoded ContextFabricInvestigationResult
	if err := decodeContextFabricStrict(encoded, &decoded); err != nil {
		t.Fatalf("decode round trip: %v", err)
	}
	if decoded.Temporal == nil {
		t.Fatal("temporal label was lost in a JSON round trip")
	}
	if got, want := decoded.Temporal.Grain, result.Temporal.Grain; got != want {
		t.Fatalf("grain = %q, want %q", got, want)
	}
	if !decoded.Temporal.Effective.AsOf.Equal(*result.Temporal.Effective.AsOf) {
		t.Fatalf("effective as_of = %v, want %v", decoded.Temporal.Effective.AsOf, result.Temporal.Effective.AsOf)
	}

	currentAxis := contextFabricGoldenResult(t, "context_fabric_investigation_result.v1.json")
	encodedCurrent, err := json.Marshal(currentAxis)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedCurrent), "temporal") {
		t.Fatal("a current-axis result must not emit a temporal key")
	}
}

func contextFabricHistoricalResult(t *testing.T) ContextFabricInvestigationResult {
	t.Helper()
	return contextFabricGoldenResult(t, "context_fabric_investigation_result_historical.v1.json")
}

func contextFabricGoldenResult(t *testing.T, name string) ContextFabricInvestigationResult {
	t.Helper()
	var result ContextFabricInvestigationResult
	if err := decodeContextFabricStrict(contextFabricGolden(t, name), &result); err != nil {
		t.Fatalf("decode golden %s: %v", name, err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("golden %s does not validate: %v", name, err)
	}
	return result
}

func timePtr(value time.Time) *time.Time { return &value }

// TestR4_4_InstantsOutsideTheRepresentableRangeAreRefused is CHAOS-3781
// round-4 R4-4, red→green.
//
// Any nonzero past timestamp used to pass validation and then flow into
// UnixNano(), which is undefined outside roughly 1677-09-21..2262-04-11.
// Year 1 does not saturate there — it WRAPS, to a plausible-looking modern
// instant. That is worse than an obvious error: it would admit graph
// elements at the wrong time and let two different requests collide on one
// reuse key, both silently.
//
// Refused, never clamped: clamping answers a different question than the
// one asked, which is the defect class this axis exists to remove.
func TestR4_4_InstantsOutsideTheRepresentableRangeAreRefused(t *testing.T) {
	t.Parallel()
	farPast := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
	farFuture := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	ok := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	for _, testCase := range []struct {
		name        string
		timeContext ContextFabricTimeContext
	}{
		{"as_of in year 1", ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &farPast}},
		{"as_of in year 9999", ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &farFuture}},
		{"range start out of range", ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &farPast, End: &ok}},
		{"range end out of range", ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &ok, End: &farFuture}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if err := testCase.timeContext.Validate(); err == nil {
				t.Fatal("an instant outside the epoch-nanosecond range was accepted; it would wrap rather than fail on conversion")
			}
		})
	}

	// The wrap this prevents, demonstrated: year 1 does not saturate.
	if farPast.UnixNano() >= 0 {
		t.Logf("year 1 UnixNano() = %d -- wraps to a non-negative value, i.e. a plausible modern instant", farPast.UnixNano())
	}
}

// TestR4_4_OrdinaryInstantsStillValidate is the over-blocking guard: the
// bound must reject only what genuinely cannot be represented.
func TestR4_4_OrdinaryInstantsStillValidate(t *testing.T) {
	t.Parallel()
	for _, instant := range []time.Time{
		time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		at := instant
		context := ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &at}
		if err := context.Validate(); err != nil {
			t.Fatalf("%s is representable but was refused: %v", instant.Format("2006-01-02"), err)
		}
	}
}

// TestR5_3_ProjectionIngestRejectsUnrepresentableTimestamps is round-5
// R5-3, red→green.
//
// R4-4 bounded REQUEST timestamps. Projection INGEST was still unbounded:
// validateTimeRange rejected only zero and reversed windows, so a
// year-9999 valid_to passed validation and then wrapped through UnixNano
// on its way into the graph — corrupting historical admission for that
// element, and reordering tombstones against the rows they retire.
//
// Rejected, never clamped: an out-of-range producer timestamp is data
// corruption, and clamping would write a value the source never asserted
// while leaving no trace anything was wrong.
func TestR5_3_ProjectionIngestRejectsUnrepresentableTimestamps(t *testing.T) {
	t.Parallel()
	farFuture := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	farPast := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	subject := ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_item:WI-1", Label: "WI-1"}
	scope := ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/svc"}}

	entity := func(validFrom, validTo *time.Time) ContextFabricEntityProjection {
		return ContextFabricEntityProjection{
			Subject: subject, Authorization: scope, EvidenceRefIDs: []string{"acr:v1:work-item:WI-1"},
			ObservedAt: observed, ValidFrom: validFrom, ValidTo: validTo, SourceVersion: "v1",
		}
	}

	if err := entity(nil, &farFuture).Validate(); err == nil {
		t.Fatal("a year-9999 valid_to was accepted; it wraps rather than fails on conversion to epoch nanoseconds")
	}
	if err := entity(&farPast, nil).Validate(); err == nil {
		t.Fatal("a year-1 valid_from was accepted")
	}
	// A tombstone's effective_at orders it against the rows it retires.
	tombstone := ContextFabricProjectionTombstone{
		Kind: "incident", CanonicalID: "incident:i-1", Reason: "source_deleted",
		EffectiveAt: farFuture, SourceVersion: "v1",
	}
	if err := tombstone.Validate(); err == nil {
		t.Fatal("a year-9999 tombstone effective_at was accepted; a wrapped value could sort before the data it removes")
	}

	// Over-blocking guard: ordinary windows still validate.
	if err := entity(&observed, nil).Validate(); err != nil {
		t.Fatalf("an ordinary validity window was refused: %v", err)
	}
}

// TestR6_1_EveryProjectionTimestampIsBounded is round-6 R6-1, red→green.
//
// R5-3 above bounded entities and tombstones by HAND, and the hand list
// missed contents and episodes -- so an episode ending in year 9999 still
// wrapped through UnixNano, which is the same enumerate-by-inspection miss
// this branch has now made in five places.
//
// The fix derives the enumeration instead of writing it, so this test
// asserts the DERIVATION rather than a longer list: the batch validator
// must reject an unrepresentable instant wherever it sits, including in
// the collections nobody remembered.
func TestR6_1_EveryProjectionTimestampIsBounded(t *testing.T) {
	t.Parallel()
	farFuture := time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	farPast := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
	observed := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	subject := ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_item:WI-1", Label: "WI-1"}
	scope := ContextFabricAuthorizationScope{RepositorySlugs: []string{"acme/svc"}}

	// The baseline must be VALID, or every case below would pass for the
	// wrong reason -- reporting an unrelated field as proof of the bound.
	if err := validContextFabricProjectionBatch().Validate(); err != nil {
		t.Fatalf("the baseline batch is invalid, so no case below would prove anything: %v", err)
	}

	cases := []struct {
		name    string
		corrupt func(*ContextFabricProjectionBatch)
	}{
		{"episode ended_at", func(batch *ContextFabricProjectionBatch) {
			batch.Episodes = []ContextFabricEpisodeProjection{{
				EpisodeID: "episode_12345678", Subject: subject, Goal: "ship", Outcome: "done", Summary: "s",
				Authorization: scope, EvidenceRefIDs: []string{"evidence_12345678"},
				StartedAt: observed, EndedAt: farFuture, SourceVersion: "source-v1",
			}}
		}},
		{"episode started_at", func(batch *ContextFabricProjectionBatch) {
			batch.Episodes = []ContextFabricEpisodeProjection{{
				EpisodeID: "episode_12345678", Subject: subject, Goal: "ship", Outcome: "done", Summary: "s",
				Authorization: scope, EvidenceRefIDs: []string{"evidence_12345678"},
				StartedAt: farPast, EndedAt: observed, SourceVersion: "source-v1",
			}}
		}},
		{"content observed_at", func(batch *ContextFabricProjectionBatch) {
			batch.Contents = []ContextFabricContentProjection{{
				ContentID: "content_12345678", Subject: subject, Title: "t", Body: "b", ContentDigest: "sha256:abc",
				Authorization: scope, EvidenceRefIDs: []string{"evidence_12345678"},
				// Untrusted must be TRUE or Validate refuses the content for
				// the untrusted-content rule instead, and this case would go
				// green with the bound removed -- which the red proof caught.
				ObservedAt: farFuture, SourceVersion: "source-v1", Untrusted: true,
			}}
		}},
		{"batch generated_at", func(batch *ContextFabricProjectionBatch) {
			batch.GeneratedAt = farFuture
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			batch := validContextFabricProjectionBatch()
			test.corrupt(&batch)
			if err := batch.Validate(); err == nil {
				t.Fatalf("%s outside the representable range was accepted; it wraps rather than fails on conversion to epoch nanoseconds", test.name)
			}
		})
	}
}
