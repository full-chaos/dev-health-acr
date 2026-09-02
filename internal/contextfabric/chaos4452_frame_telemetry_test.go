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
// There are no repair keys here: this slice REFUSES an invalid frame
// rather than repairing it, and a log key nothing can populate is a key an
// operator would wait forever to see. They land with the bounded repair,
// in the change that can actually emit them.
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
	"ProposedKind":           "proposed_kind",
	"ProposedGoals":          "proposed_goals",
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
	result := ValidateFrame(proposed, nil, "")
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
	result := ValidateFrame(proposed, nil, "")
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
	if records[0]["derived_obligation_count"] == nil {
		t.Error("derived_obligation_count is absent on a valid frame -- a present zero is countable, an absent key is not")
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
//
// STRENGTHENED after codex round 1 finding 2. The first version planted a
// canary ONLY in the anchor terms, asserted the record was clean, and
// passed -- while `proposed_goals` was leaking arbitrary model text
// through a different field. A leak test that covers one field is a test
// for the leak its author thought of, not for the leak class, and it reads
// as coverage either way. It now plants a canary in a GOAL as well, which
// is the field that actually leaked.
func TestFrameValidationTelemetryLeaksNoQuestionContent(t *testing.T) {
	const secretAnchor = "zzz-confidential-team-name"
	const secretGoal = "zzz-confidential-goal-text"
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState, InvestigationGoal(secretGoal)},
		SubjectExpression: SubjectExpression{
			Kind: SubjectExpressionChildrenOfScope,
			Scoped: &ScopedSetExpression{
				AnchorTerms: []string{secretAnchor},
				MemberKind:  contractsv1.ContextFabricSubjectProject,
			},
		},
		Emphasis: []AnswerEmphasis{EmphasisNegativeOutliers},
	}
	result := ValidateFrame(proposed, nil, "")
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
		if containsSecret(value, secretGoal) {
			t.Fatalf("record[%q] contains the unrecognized goal text -- this is the field that actually leaked before the vocabulary filter was added", key)
		}
	}
}

// TestUnrecognizedGoalIsRejectedAndNeverReachesTelemetry is codex round 1's
// finding 2, reproduced by this lane and closed.
//
// THE DEFECT, executed on the pre-fix tree: a frame carrying
// `Goals=[assess_state, "arbitrary model text"]` returned
// `outcome="valid"` and the sink logged
// `proposed_goals = [assess_state arbitrary model text zzz-leak]`.
//
// It was invisible in three places at once. The unknown goal missed
// table 1's map, so it contributed no obligation; it missed
// goalDischarge's map, so I16 could not see the axis it should have
// failed on; and it reached the event verbatim, putting free text into a
// log field. I15 checked only NON-EMPTINESS, which is what the design's
// prose says -- but the design's prose describes a frame that has already
// been through sanitization, and a validator cannot assume its caller ran
// it.
//
// WHY THE PACKAGE'S OWN LEAK TEST MISSED IT, recorded because it is the
// more useful half: TestFrameValidationTelemetryLeaksNoQuestionContent
// plants its canary in the scoped ANCHOR TERMS and asserts the record is
// clean. It passed throughout. It covered the leak the author thought of,
// not the leak CLASS -- which is the green-but-vacuous failure AGENTS.md
// names. That test now plants a canary in a GOAL as well.
func TestUnrecognizedGoalIsRejectedAndNeverReachesTelemetry(t *testing.T) {
	const junk = "arbitrary model text zzz-leak"
	proposed := QuestionFrame{
		Goals: []InvestigationGoal{GoalAssessState, InvestigationGoal(junk)},
		SubjectExpression: SubjectExpression{
			Kind:  SubjectExpressionNamed,
			Named: &NamedSubjectExpression{Terms: []string{"dev health ops"}},
		},
	}

	// HALF ONE -- the validator names it, in phase A1, as I15.
	failure, bad := ValidateFramePhaseA1(proposed)
	if !bad {
		t.Fatal("a goal outside the closed vocabulary must fail phase A1 -- silently ignoring it means the axis contributes no obligation AND no discharge, so nothing downstream can see it")
	}
	if failure.Invariant != FrameInvariantI15 {
		t.Fatalf("failed invariant = %q, want %q -- I15 is the goal-axis invariant, and I10 is the design's own precedent for pairing non-emptiness with vocabulary membership",
			failure.Invariant, FrameInvariantI15)
	}
	if failure.Detail != FrameFailureGoalOutsideVocabulary {
		t.Fatalf("failure detail = %q, want %q -- an out-of-vocabulary goal and an empty goal set are different operational states", failure.Detail, FrameFailureGoalOutsideVocabulary)
	}

	// HALF TWO -- the whole flow refuses, rather than returning usable.
	result := ValidateFrame(proposed, nil, "")
	if result.Outcome != FrameValidationOutcomeRefusedInvalid {
		t.Fatalf("outcome = %q, want %q", result.Outcome, FrameValidationOutcomeRefusedInvalid)
	}

	// HALF THREE -- and even so, nothing free-text reaches the log line.
	event := FrameValidationEventFrom(proposed, result, "")
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordFrameValidation(context.Background(), storage.Principal{OrgID: "org_sink_test"}, event)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	for key, value := range records[0] {
		if containsSecret(value, junk) {
			t.Fatalf("record[%q] carries the unrecognized goal text -- telemetry is the one place where being wrong is silent and permanent", key)
		}
	}
	if !containsSecret(records[0]["proposed_goals"], string(GoalAssessState)) {
		t.Errorf("proposed_goals = %v -- the RECOGNIZED goal must survive the filter; dropping the whole set would destroy the field's diagnostic value",
			records[0]["proposed_goals"])
	}
}

// TestUnrecognizedEmphasisAndDimensionsAreSanitizedByNormalization is the
// other half of the same class. Neither reaches a log field, so neither is
// a leak -- but left in place both are INVISIBLE, missing their derivation
// map and their discharge map, so the axis contributes nothing while
// looking decided.
//
// These are DROPPED rather than failed, which is §13.2.1 read literally:
// "an unknown string is DROPPED from the set, never an error". Goals are
// the deliberate exception (I15), because an empty goal set is a failure
// and because goals are the one axis whose values are logged.
func TestUnrecognizedEmphasisAndDimensionsAreSanitizedByNormalization(t *testing.T) {
	frame := QuestionFrame{
		Goals: []InvestigationGoal{GoalRankOrSurvey},
		SubjectExpression: SubjectExpression{
			Kind:       SubjectExpressionDiscoveredKind,
			Discovered: &DiscoveredSetExpression{MemberKind: contractsv1.ContextFabricSubjectTeam},
		},
		Emphasis:   []AnswerEmphasis{EmphasisNegativeOutliers, AnswerEmphasis("sideways_outliers")},
		Dimensions: []HealthDimension{HealthDimensionDeliveryFlow, HealthDimension("vibes")},
		Temporal:   TemporalIntent("whenever"),
	}
	normalized := NormalizeFrame(frame)

	if len(normalized.Emphasis) != 1 || normalized.Emphasis[0] != EmphasisNegativeOutliers {
		t.Errorf("emphasis = %v, want only the recognized member", normalized.Emphasis)
	}
	if len(normalized.Dimensions) != 1 || normalized.Dimensions[0] != HealthDimensionDeliveryFlow {
		t.Errorf("dimensions = %v, want only the recognized member", normalized.Dimensions)
	}
	if normalized.Temporal != TemporalIntentCurrent {
		t.Errorf("temporal = %q, want %q -- an out-of-vocabulary temporal misses table 2 AND temporalDischarge, so leaving it in place makes the axis mean silently nothing",
			normalized.Temporal, TemporalIntentCurrent)
	}
	// And the frame is then usable, because none of these is an error.
	if failure, bad := ValidateFramePhaseA2(DeriveFrameObligations(normalized, nil), ""); bad {
		t.Fatalf("normalized frame fails A2 on %q -- dropping an unknown member must never be a way to fail a sound interpretation", failure.Invariant)
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
