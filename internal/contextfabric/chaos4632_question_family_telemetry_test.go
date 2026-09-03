package contextfabric

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4632 §4.3 telemetry tests, asserted at the PRODUCTION SINK.
//
// These assert against the real slog handler's output bytes -- the log line
// an operator would actually receive -- and NOT against any struct field.
// That is not stylistic. chaos4085_telemetry_sink_test.go's header records
// what the alternative cost: CHAOS-4085's fields were defined, populated at
// every emission site, and covered by tests, and none of it reached
// production, because the production sink did not log them and no test
// looked at the sink. Every test involved read the value through an
// in-memory double, which is exactly the kind of test that cannot observe
// "nothing downstream consumes this".
//
// So: if you add a field to QuestionFamilyResolutionEvent, add its
// assertion here too.

// TestQuestionFamilyResolutionReachesTheProductionSink is the wiring pin.
// Every scalar field on the event must appear as a key in the emitted
// record.
func TestQuestionFamilyResolutionReachesTheProductionSink(t *testing.T) {
	samples := []FamilySample{
		{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectTeam},
		{Shape: ShapeDiscoveredCohort, GroupKind: contractsv1.ContextFabricSubjectProject},
		{Shape: ShapeSingleSubject, ModelFamily: QuestionFamilyTrend},
	}
	outcome := ResolveQuestionFamily(samples)
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordQuestionFamilyResolution(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			QuestionFamilyResolutionEventFrom(outcome, samples))
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(records))
	}
	record := records[0]

	for key, want := range map[string]any{
		"org_id":                     "org_sink_test",
		"family":                     string(QuestionFamilyGroupedCohortStatus),
		"source":                     string(QuestionFamilySourceModelConsensus),
		"family_version":             QuestionFamilyTableVersion,
		"ensemble_size":              float64(3),
		"downgraded_count":           float64(1),
		"consensus_field_divergence": float64(1),
	} {
		got, ok := record[key]
		if !ok {
			t.Errorf("record has no %q key -- an operator greps for this; a field on the struct that never reaches the sink is not telemetry", key)
			continue
		}
		if got != want {
			t.Errorf("record[%q] = %v, want %v", key, got, want)
		}
	}

	// The per-sample rows are the round-2 finding: singular fields cannot
	// represent two samples failing DIFFERENT precedence rows with
	// DIFFERENT attempted families. Every row's every field must be
	// present.
	for i := range samples {
		prefix := "sample_" + strconv.Itoa(i) + "_"
		for _, suffix := range []string{
			"shape", "attempted_family", "resolved_family", "row",
			"incompatibility_reason", "group_kind_set", "scope_anchor_set",
		} {
			if _, ok := record[prefix+suffix]; !ok {
				t.Errorf("record has no %q key -- a split consensus cannot be diagnosed without every per-sample field", prefix+suffix)
			}
		}
	}
	// Sample 2 is the downgraded one, and its reason must be nameable
	// from the line alone -- that is the whole diagnosis-from-artifacts
	// bar AGENTS.md sets.
	if record["sample_2_incompatibility_reason"] != string(FamilyIncompatibilityUnreachable) {
		t.Errorf("sample_2_incompatibility_reason = %v, want %q", record["sample_2_incompatibility_reason"], FamilyIncompatibilityUnreachable)
	}
	if record["sample_2_attempted_family"] != string(QuestionFamilyTrend) {
		t.Errorf("sample_2_attempted_family = %v, want %q", record["sample_2_attempted_family"], QuestionFamilyTrend)
	}
}

// TestQuestionFamilyResolutionFiresOnUnclassifiedToo pins the DENOMINATOR.
//
// An event that fires only on a successful classification makes "the
// resolver never classifies anything" and "the resolver never ran" the
// same observation. lane-4579 wrote this up in its §4 and codex confirmed
// it by mutation in its finding 5.
func TestQuestionFamilyResolutionFiresOnUnclassifiedToo(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		samples []FamilySample
		source  QuestionFamilySource
	}{
		{"no samples at all", nil, QuestionFamilySourceNone},
		{"a rejected 1-1 tie", []FamilySample{{Shape: ShapeDiscoveredCohort}, {Shape: ShapeSingleSubject}}, QuestionFamilySourcePluralityRejected},
		{"an unroutable shape", []FamilySample{{Shape: ShapeExplicitCohort}}, QuestionFamilySourceModel},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			outcome := ResolveQuestionFamily(testCase.samples)
			records := captureSlogJSON(t, func(logger *slog.Logger) {
				NewSlogEngineTelemetry(logger).RecordQuestionFamilyResolution(
					context.Background(), storage.Principal{OrgID: "org_sink_test"},
					QuestionFamilyResolutionEventFrom(outcome, testCase.samples))
			})
			if len(records) != 1 {
				t.Fatalf("got %d records, want 1 -- the event must fire even when nothing was classified", len(records))
			}
			if records[0]["family"] != string(QuestionFamilyUnclassified) {
				t.Errorf("family = %v, want unclassified", records[0]["family"])
			}
			if records[0]["source"] != string(testCase.source) {
				t.Errorf("source = %v, want %q -- a refused tie and a never-sampled resolution are DIFFERENT operational states", records[0]["source"], testCase.source)
			}
		})
	}
}

// TestQuestionFamilyTelemetryLeaksNoContent is the content-safety line.
//
// ALLOW-LIST, not a denylist: a denylist only catches the leaks someone
// thought of. The scope anchor is the specific hazard -- it is free-form
// model text and must NEVER reach a log line, only the boolean saying it
// was set.
func TestQuestionFamilyTelemetryLeaksNoContent(t *testing.T) {
	secret := "a-very-distinctive-anchor-term"
	samples := []FamilySample{{
		Shape:           ShapeSingleSubject,
		ScopeAnchorTerm: secret,
		ScopeAnchorKind: contractsv1.ContextFabricSubjectTeam,
		RequestedKind:   contractsv1.ContextFabricSubjectProject,
		SubjectTerms:    []string{"another-distinctive-subject-term"},
	}}
	outcome := ResolveQuestionFamily(samples)
	var buffer strings.Builder
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordQuestionFamilyResolution(
			context.Background(), storage.Principal{OrgID: "org_sink_test"},
			QuestionFamilyResolutionEventFrom(outcome, samples))
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	for key, value := range records[0] {
		buffer.WriteString(key)
		buffer.WriteString("=")
		if text, ok := value.(string); ok {
			buffer.WriteString(text)
		}
		buffer.WriteString(" ")
	}
	rendered := buffer.String()
	if strings.Contains(rendered, secret) {
		t.Fatalf("the free-text scope anchor reached the log line: %s", rendered)
	}
	if strings.Contains(rendered, "another-distinctive-subject-term") {
		t.Fatalf("a subject term reached the log line: %s", rendered)
	}

	allowed := map[string]bool{
		"time": true, "level": true, "msg": true, "request_id": true,
		"org_id": true, "family": true, "source": true, "ensemble_size": true,
		"downgraded_count": true, "consensus_field_divergence": true,
		"family_version": true, "sample_families": true,
		"sample_0_shape": true, "sample_0_attempted_family": true,
		"sample_0_resolved_family": true, "sample_0_row": true,
		"sample_0_incompatibility_reason": true,
		"sample_0_group_kind_set":         true, "sample_0_scope_anchor_set": true,
		// The CHAOS-4452 shadow comparison. Every one of these is a
		// closed-vocabulary member or a boolean: two families, two rows,
		// one agreement class, one outcome, one server-authored version
		// constant, two booleans. No term, no anchor, no question text --
		// which is why the projection is allowed to reach a log line at
		// all while the frame's own subject terms never do.
		"shadow_frame_observed": true, "shadow_frame_outcome": true,
		"shadow_projection_version": true,
		"shadow_projected_family":   true, "shadow_projected_row": true,
		"shadow_precedence_family": true, "shadow_precedence_row": true,
		"shadow_agreement_class": true, "shadow_agreed": true,
		// SEAM 7's routing decision (CHAOS-4736), admitted under the same
		// rule and for the same reason: a closed-vocabulary source, a
		// closed-vocabulary class, a closed-vocabulary disposition and a
		// boolean. The decision is keyed on the PAIR OF ROWS the two
		// tables fired -- a structural property -- so nothing here can
		// carry a subject term, an anchor, or any part of the question.
		"family_source": true, "route_class": true,
		"route_disposition": true, "route_switched": true,
	}
	for key := range records[0] {
		if !allowed[key] {
			t.Errorf("unexpected key %q on the family resolution line -- every field must be an explicitly allowed closed-vocabulary value", key)
		}
	}
}

// TestSampleFamilyDistributionIsStablyOrdered pins that the distribution
// is sorted before it is logged.
//
// Go randomizes map iteration order, so logging the map directly would
// make two IDENTICAL resolutions emit different log lines -- which breaks
// grep-based operations and any log-diffing regression check, silently and
// intermittently.
func TestSampleFamilyDistributionIsStablyOrdered(t *testing.T) {
	samples := []FamilySample{
		{Shape: ShapeDiscoveredCohort}, {Shape: ShapeSingleSubject},
		{Shape: ShapeExplicitCohort}, {Shape: ShapeOpen},
		{Shape: ShapeSingleSubject, ComparisonTerms: []string{"a"}},
	}
	outcome := ResolveQuestionFamily(samples)
	var first string
	for attempt := 0; attempt < 25; attempt++ {
		records := captureSlogJSON(t, func(logger *slog.Logger) {
			NewSlogEngineTelemetry(logger).RecordQuestionFamilyResolution(
				context.Background(), storage.Principal{OrgID: "org_sink_test"},
				QuestionFamilyResolutionEventFrom(outcome, samples))
		})
		rendered := renderValue(records[0]["sample_families"])
		if attempt == 0 {
			first = rendered
			continue
		}
		if rendered != first {
			t.Fatalf("sample_families rendered differently across identical resolutions:\n  %s\n  %s", first, rendered)
		}
	}
}

// renderValue flattens the logged sample_families array to a comparable
// string.
func renderValue(value any) string {
	items, ok := value.([]any)
	if !ok {
		return "not-a-list"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprint(item))
	}
	return strings.Join(parts, "|")
}
