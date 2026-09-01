package contextfabric

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4085 SINK DISCIPLINE applied to frame validation.
//
// The recorded lesson (chaos4085_telemetry_sink_test.go's own header): a
// field populated on a struct and never logged is NOT telemetry, it is a
// field. CommitAffirmationTelemetry was an optional interface, nothing in
// production implemented it, every event failed a type assertion, and the
// whole signal disappeared with tests passing throughout.
//
// frameValidationEventLogKeys is the field -> log-key map, declared
// EXPLICITLY rather than derived by snake-casing. That is deliberate: an
// explicit map plus the exhaustiveness check below means ADDING A FIELD TO
// THE EVENT BREAKS THIS TEST until the field is also logged. A derived
// mapping would silently accept a new field the logger never emits, which
// is the exact failure this test exists to prevent.
var frameValidationEventLogKeys = map[string]string{
	"Outcome":                "outcome",
	"FailedInvariant":        "failed_invariant",
	"FailedPhase":            "failed_phase",
	"FailureDetail":          "failure_detail",
	"RepairAttempted":        "repair_attempted",
	"RepairLatencyMS":        "repair_latency_ms",
	"RepairBoundViolation":   "repair_bound_violation",
	"ProposedKind":           "proposed_kind",
	"RepairedKind":           "repaired_kind",
	"ProposedGoals":          "proposed_goals",
	"RepairedGoals":          "repaired_goals",
	"DerivedObligationCount": "derived_obligation_count",
	"WidenedObligationCount": "widened_obligation_count",
	"ShapeDiverged":          "shape_diverged",
	"EmittedShape":           "emitted_shape",
	"DerivedShape":           "derived_shape",
	"FrameVersion":           "frame_version",
}

// TestEveryFrameValidationEventFieldReachesTheLogLine is the structural
// half: the event struct and the key map must agree exactly, in both
// directions.
func TestEveryFrameValidationEventFieldReachesTheLogLine(t *testing.T) {
	eventType := reflect.TypeOf(FrameValidationEvent{})
	seen := map[string]bool{}
	for i := 0; i < eventType.NumField(); i++ {
		name := eventType.Field(i).Name
		seen[name] = true
		if _, ok := frameValidationEventLogKeys[name]; !ok {
			t.Errorf("FrameValidationEvent.%s has no log key -- a field that is never logged is not telemetry, it is a field", name)
		}
	}
	for name := range frameValidationEventLogKeys {
		if !seen[name] {
			t.Errorf("log key map names %q, which is not a field on FrameValidationEvent", name)
		}
	}
}

// TestFrameValidationTelemetryEmitsEveryFieldOnARefusal is the runtime
// half: drive the production sink and assert every mapped key is actually
// present in the emitted record.
//
// A REFUSAL rather than a valid frame, because the refusal path is the one
// carrying the diagnostic fields an operator needs and therefore the one a
// regression would quietly empty.
func TestFrameValidationTelemetryEmitsEveryFieldOnARefusal(t *testing.T) {
	proposed := discoveredTeamsEmphasisFrame(GoalAssessState)
	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_sink_test"}, nil, proposed, nil, "", nil)
	if result.Outcome != FrameValidationOutcomeRefusedInvalid {
		t.Fatalf("precondition: outcome = %q, want refused_invalid", result.Outcome)
	}
	event := FrameValidationEventFrom(proposed, result, "")

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFrameValidation(context.Background(), storage.Principal{OrgID: "org_sink_test"}, event)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 -- validation fires once per frame", len(records))
	}
	for field, key := range frameValidationEventLogKeys {
		if _, ok := records[0][key]; !ok {
			t.Errorf("record is missing %q (FrameValidationEvent.%s) -- an operator greps for this key", key, field)
		}
	}
	if records[0]["failed_invariant"] != string(FrameInvariantI14) {
		t.Errorf("failed_invariant = %v, want %q", records[0]["failed_invariant"], FrameInvariantI14)
	}
	// failed_phase distinguishes two different investigations from one
	// invariant id: a model emitting something malformed (a1) versus the
	// server's own derivation leaving an axis undischarged (a2).
	if records[0]["failed_phase"] != string(FrameValidationPhaseA2) {
		t.Errorf("failed_phase = %v, want a2", records[0]["failed_phase"])
	}
	if records[0]["level"] != "INFO" {
		t.Errorf("level = %v, want INFO -- a refused frame is a designed outcome, not a fault", records[0]["level"])
	}
}

// TestFrameValidationTelemetryFiresOnValidFramesToo. §13.6: "Fired on
// EVERY frame reaching validation including valid ones, so the denominator
// is countable."
//
// An event that fires only on failure makes "the validator never rejects
// anything" and "the validator never ran" the same observation -- the
// lesson lane-4579 wrote up and §4.3 already applies to family resolution.
func TestFrameValidationTelemetryFiresOnValidFramesToo(t *testing.T) {
	proposed := namedFrame(GoalAssessState)
	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_sink_test"}, nil, proposed, nil, "", nil)
	event := FrameValidationEventFrom(proposed, result, "")

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFrameValidation(context.Background(), storage.Principal{OrgID: "org_sink_test"}, event)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0]["outcome"] != string(FrameValidationOutcomeValid) {
		t.Errorf("outcome = %v, want valid", records[0]["outcome"])
	}
	if records[0]["failed_invariant"] != "" {
		t.Errorf("failed_invariant = %v on a valid frame, want empty", records[0]["failed_invariant"])
	}
	if records[0]["repair_attempted"] != false {
		t.Errorf("repair_attempted = %v, want false -- a present false is countable, an absent key is not", records[0]["repair_attempted"])
	}
}

// TestFrameValidationTelemetryLeaksNoQuestionContent is the content-safety
// line every method on this sink holds.
//
// AN ALLOW-LIST, not a denylist: a denylist only catches the leaks someone
// thought of. The frame carries FREE STRINGS -- a named subject's terms and
// a scoped set's anchor terms -- which are durably captured on the receipt
// for scoring and must NEVER reach a log field. This test builds a frame
// whose terms are distinctive and asserts both that the key set is closed
// and that the term text appears nowhere in the record.
func TestFrameValidationTelemetryLeaksNoQuestionContent(t *testing.T) {
	const secretAnchor = "zzz-confidential-team-name"
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{secretAnchor},
				MemberKind:  contractsv1.ContextFabricSubjectProject,
			},
		},
		Emphasis: []AnswerEmphasis{EmphasisNegativeOutliers},
	}
	result := ValidateAndRepairFrame(context.Background(), storage.Principal{OrgID: "org_sink_test"}, nil, proposed, nil, "", nil)
	event := FrameValidationEventFrom(proposed, result, "")

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFrameValidation(context.Background(), storage.Principal{OrgID: "org_sink_test"}, event)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}

	allowed := map[string]bool{"time": true, "level": true, "msg": true, "org_id": true, "request_id": true}
	for _, key := range frameValidationEventLogKeys {
		allowed[key] = true
	}
	for key := range records[0] {
		if !allowed[key] {
			t.Errorf("frame validation record carries unexpected key %q -- this event is closed enums, counts and an org id only", key)
		}
	}

	for key, value := range records[0] {
		if containsSecret(value, secretAnchor) {
			t.Fatalf("record[%q] contains the anchor term -- retrieval pointers are captured on the RECEIPT for scoring and must never reach a log field", key)
		}
	}
}

// containsSecret walks a decoded JSON value looking for a substring. The
// walk exists because a leak could arrive nested inside the goal arrays
// rather than as a top-level string, and a top-level-only check would read
// as coverage while missing it.
func containsSecret(value any, secret string) bool {
	switch typed := value.(type) {
	case string:
		return typed == secret || (len(typed) >= len(secret) && contains(typed, secret))
	case []any:
		for _, element := range typed {
			if containsSecret(element, secret) {
				return true
			}
		}
	case map[string]any:
		for _, element := range typed {
			if containsSecret(element, secret) {
				return true
			}
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
