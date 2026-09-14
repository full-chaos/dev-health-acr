package contextfabric_test

// The requirement outcome transition line, certified against its eventspec
// declaration from the bytes the production slog JSON handler wrote during real
// Investigate calls: identity, level, the bounded-many index/total, every
// declared field's presence, type and closed vocabulary, and the VALUES each
// scenario must carry. Every scenario differs from every other in at least one
// asserted value.

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// certifyTransitionLine certifies the one line of a scenario whose requirement
// is `requirement`, asserting `want` on it.
func certifyTransitionLine(t *testing.T, raw []byte, requestID, requirement string, want map[string]any) {
	t.Helper()
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse() on production slog output: %v", err)
	}
	var index any
	for _, line := range log.LinesWithMsg(eventspec.RequirementOutcomeTransition.Msg) {
		if line["requirement"] == requirement {
			index = line["index"]
		}
	}
	if index == nil {
		t.Fatalf("no transition line names %q", requirement)
	}
	assertion := map[string]any{"request_id": requestID, "index": index, "requirement": requirement}
	for key, value := range want {
		assertion[key] = value
	}
	result, err := certify.Certify(log, certify.Assertion{Event: eventspec.RequirementOutcomeTransition, Want: assertion})
	if err != nil {
		t.Fatalf("certify %s: %v", requirement, err)
	}
	t.Logf("CERTIFIED %s: %v", requirement, result.Line)
}

func TestTheRequirementOutcomeTransitionLineCertifiesAgainstItsSpecification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		scenario    string
		requirement string
		want        map[string]any
	}{
		{"class_b", "count/member/team", map[string]any{
			"org_id": "org_1", "obligation": "count", "role": "member", "subject_kind": "team",
			"predicted": "served", "predicted_reason": "none", "assembled_outcome": "unavailable",
			"cause": "computed_population_absent", "cause_coverage": "fact_pruned", "cause_overrun": "none", "cause_narrowing": "none",
			"served": 0, "declared": 0, "served_fact_count": 0, "member_set_resolved": false, "total": 1,
		}},
		{"narrowed_count", "count/member/team", map[string]any{
			"predicted": "served", "predicted_reason": "none", "assembled_outcome": "narrowed",
			"cause": "none", "served": 2, "declared": 5, "served_fact_count": 1, "member_set_resolved": true, "total": 1,
		}},
		{"state_subject_kind_unsupported", "state/subject/team", map[string]any{
			"obligation": "state", "role": "subject", "subject_kind": "team",
			"predicted": "unavailable", "predicted_reason": "subject_kind_unsupported", "assembled_outcome": "narrowed",
			"cause": "none", "cause_overrun": "items", "served": 13, "declared": 18, "served_fact_count": 0, "member_set_resolved": false,
		}},
		{"state_no_declaring_producer", "state/subject/team", map[string]any{
			"predicted": "unavailable", "predicted_reason": "no_declaring_producer", "assembled_outcome": "narrowed",
			"cause": "none", "cause_overrun": "items", "served": 13, "declared": 18,
		}},
		{"state_no_declaring_producer", "principal_drivers/subject/team", map[string]any{
			"obligation": "principal_drivers", "predicted": "served", "predicted_reason": "none", "assembled_outcome": "unavailable",
			"cause": "none", "cause_coverage": "requirement_read_not_planned", "served_fact_count": 12,
		}},
	} {
		tc := tc
		t.Run(tc.scenario+"/"+tc.requirement, func(t *testing.T) {
			t.Parallel()
			raw, requestID := contextfabric.RunRequirementOutcomeTransitionScenarioForTest(t, tc.scenario)
			certifyTransitionLine(t, raw, requestID, tc.requirement, tc.want)
		})
	}
}

// The control scenario serves its count as predicted and writes no line.
func TestAServedCountWritesNoRequirementOutcomeTransitionLine(t *testing.T) {
	t.Parallel()
	raw, _ := contextfabric.RunRequirementOutcomeTransitionScenarioForTest(t, "served_count")
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	if lines := log.LinesWithMsg(eventspec.RequirementOutcomeTransition.Msg); len(lines) != 0 {
		t.Fatalf("got %d transition lines for a count served as predicted: %v", len(lines), lines)
	}
}

// Every key the production emitter writes is declared, and every declared key
// is written, compared on a real emitted line in both directions.
func TestTheRequirementOutcomeTransitionSpecificationDeclaresExactlyTheEmittedKeys(t *testing.T) {
	t.Parallel()
	raw, requestID := contextfabric.RunRequirementOutcomeTransitionScenarioForTest(t, "class_b")
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse(): %v", err)
	}
	lines := log.LinesWithMsg(eventspec.RequirementOutcomeTransition.Msg)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	line := lines[0]
	if line["request_id"] != requestID {
		t.Fatalf("request_id = %v, want %s", line["request_id"], requestID)
	}
	if eventspec.RequirementOutcomeTransition.Msg != contextfabric.RequirementOutcomeTransitionLogMessage {
		t.Fatalf("the declared msg %q is not the emitter's %q", eventspec.RequirementOutcomeTransition.Msg, contextfabric.RequirementOutcomeTransitionLogMessage)
	}
	declared := map[string]bool{}
	for _, field := range eventspec.RequirementOutcomeTransition.Fields {
		declared[field.Key] = true
		if _, ok := line[field.Key]; !ok {
			t.Errorf("declared key %q is not on the emitted line", field.Key)
		}
	}
	for key := range line {
		switch key {
		case "time", "level", "msg":
			continue
		}
		if !declared[key] {
			t.Errorf("emitted key %q is not declared on %s", key, eventspec.RequirementOutcomeTransition.ID)
		}
	}
}

// Every member of every closed vocabulary on the line certifies through the
// real production sink. Members no assembly writer produces today (for example
// an assembled `not_attempted`) are covered here, at the sink, and nowhere
// claimed as engine-driven.
func TestEveryClosedValueOnTheRequirementOutcomeTransitionLineCertifies(t *testing.T) {
	t.Parallel()
	base := contextfabric.RequirementOutcomeTransitionEvent{
		RequirementOutcomeTransition: contextfabric.RequirementOutcomeTransition{
			Requirement: "count/member/team", Obligation: "count", Role: "member", Subject: contextfabric.SubjectTeam,
			Predicted: contextfabric.RequirementPredictedServed, AssembledOutcome: contractsv1.ContextFabricRequirementUnavailable,
			AssemblyReason: contextfabric.RequirementAssemblyReasonNone, Served: 3, Declared: 7, ServedFactCount: 2,
		},
		Index: 1, Total: 1,
	}
	set := map[string]func(*contextfabric.RequirementOutcomeTransitionEvent, string){
		"predicted": func(e *contextfabric.RequirementOutcomeTransitionEvent, v string) {
			e.Predicted = contextfabric.RequirementPrediction(v)
		},
		"predicted_reason": func(e *contextfabric.RequirementOutcomeTransitionEvent, v string) {
			if v == "none" {
				v = ""
			}
			e.PredictedReason = contextfabric.RequirementUnavailableReason(v)
		},
		"assembled_outcome": func(e *contextfabric.RequirementOutcomeTransitionEvent, v string) {
			e.AssembledOutcome = contractsv1.ContextFabricPlanRequirementOutcome(v)
		},
		"cause": func(e *contextfabric.RequirementOutcomeTransitionEvent, v string) {
			e.AssemblyReason = contextfabric.RequirementAssemblyReason(v)
		},
	}
	closed := 0
	for _, field := range eventspec.RequirementOutcomeTransition.Fields {
		if len(field.ClosedVocabulary) == 0 {
			continue
		}
		closed++
		apply, ok := set[field.Key]
		if !ok {
			t.Fatalf("closed field %q has no setter in this sweep; add one", field.Key)
		}
		for _, member := range field.ClosedVocabulary {
			event := base
			apply(&event, member)
			requestID := "req_" + "0000000000000000000000000000abcd"
			ctx := observability.WithRequestID(context.Background(), requestID)
			var buf bytes.Buffer
			contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).
				RecordRequirementOutcomeTransition(ctx, storage.Principal{OrgID: "org_1"}, event)
			log, err := certify.Parse(buf.Bytes())
			if err != nil {
				t.Fatalf("certify.Parse(): %v", err)
			}
			if _, err := certify.Certify(log, certify.Assertion{
				Event: eventspec.RequirementOutcomeTransition,
				Want:  map[string]any{"request_id": requestID, "index": 1, field.Key: member, "served": 3, "declared": 7, "served_fact_count": 2},
			}); err != nil {
				t.Fatalf("%s=%q: the certifier refused a real production line: %v", field.Key, member, err)
			}
		}
	}
	if closed != len(set) {
		t.Fatalf("the specification declares %d closed fields, this sweep sets %d", closed, len(set))
	}
}
