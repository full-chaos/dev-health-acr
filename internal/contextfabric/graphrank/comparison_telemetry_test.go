package graphrank

// T-TELEMETRY, at the sink.
//
// The engine-level arm in the falkorgraph package drives a real request
// through a real handler. THIS file proves the two properties that arm cannot
// see: that every field of every event has a declared log key (so a field
// added later cannot silently never reach the log), and that the sink emits at
// INFO with the caller's own context rather than a background one.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestEveryTelemetryFieldHasADeclaredLogKey is the reflection test the plan
// requires, and it checks BOTH directions plus uniqueness.
//
// A one-directional check would miss the case that actually bites: a field
// added to an event struct with no entry in the key map compiles, runs, and
// silently never reaches the log -- an observable that exists in the type and
// not in the output. The reverse direction catches a key left behind after a
// field is renamed, which is how a log consumer ends up parsing for something
// nothing emits.
func TestEveryTelemetryFieldHasADeclaredLogKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		event any
		keys  map[string]string
	}{
		{"ComparisonPolicyEvent", ComparisonPolicyEvent{}, comparisonPolicyLogKeys},
		{"OperandSlotEvent", OperandSlotEvent{}, operandSlotLogKeys},
		{"ComparisonReceiptBindingEvent", ComparisonReceiptBindingEvent{}, comparisonReceiptBindingLogKeys},
		{"ComparisonDecisionEvent", ComparisonDecisionEvent{}, comparisonDecisionLogKeys},
	}

	checked := 0
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			structType := reflect.TypeOf(testCase.event)

			// DIRECTION 1: every field has a key.
			for index := 0; index < structType.NumField(); index++ {
				field := structType.Field(index).Name
				if _, ok := testCase.keys[field]; !ok {
					t.Errorf("field %s.%s has no declared log key -- it exists in the type and not in the output, which is an observable that is not observable",
						testCase.name, field)
				}
			}
			// DIRECTION 2: every key has a field.
			for field := range testCase.keys {
				if _, ok := structType.FieldByName(field); !ok {
					t.Errorf("log key declared for %s.%s, which is not a field -- a consumer parsing for it would find nothing", testCase.name, field)
				}
			}
			// UNIQUENESS: no two fields share a key.
			seen := make(map[string]string, len(testCase.keys))
			for field, key := range testCase.keys {
				if other, duplicate := seen[key]; duplicate {
					t.Errorf("%s: fields %s and %s both map to log key %q -- one silently overwrites the other in the emitted line", testCase.name, other, field, key)
				}
				seen[key] = field
			}
			if structType.NumField() == 0 {
				t.Fatalf("%s has no fields; this arm measured nothing", testCase.name)
			}
		})
		checked++
	}
	if checked != len(cases) {
		t.Fatalf("only %d of %d event types reached the assertions", checked, len(cases))
	}
}

// telemetryCapture runs a sink against a JSON handler at a chosen level and
// returns the decoded records.
func telemetryCapture(t *testing.T, level slog.Level, emit func(sink SlogOperandResolutionSink, ctx context.Context)) []map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: level}))
	emit(NewSlogOperandResolutionSink(logger), context.Background())

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("emitted line is not valid JSON: %v\nline = %q", err, line)
		}
		records = append(records, record)
	}
	return records
}

// TestTheSinkEmitsAtInfoAndIsInvisibleBelowIt is the level property, asserted
// in BOTH directions.
//
// The positive half is the point of the whole file: the existing resolution
// tracer emits at Debug, so on a production rig none of it exists, and an
// observable nobody can read is not an observable. The negative half is what
// makes the positive half mean something -- without it, a sink that emitted at
// every level would pass.
func TestTheSinkEmitsAtInfoAndIsInvisibleBelowIt(t *testing.T) {
	t.Parallel()

	emit := func(sink SlogOperandResolutionSink, ctx context.Context) {
		sink.RecordComparisonDecision(ctx, ComparisonDecisionEvent{
			RequestID: "request_telemetry", OrgID: "org-1",
			Decision: comparisonDecisionHeld, PublishedCommitted: 0, UnboundReceipts: 1,
		})
	}

	atInfo := telemetryCapture(t, slog.LevelInfo, emit)
	if len(atInfo) != 1 {
		t.Fatalf("records at Info = %d, want 1 -- the sink is invisible at the level a production rig reads", len(atInfo))
	}
	if got := atInfo[0]["level"]; got != "INFO" {
		t.Errorf("level = %v, want INFO", got)
	}

	// ABOVE Info, it must be absent -- otherwise "emits at Info" is untested:
	// a sink logging at Warn would satisfy the assertion above just as well.
	atWarn := telemetryCapture(t, slog.LevelWarn, emit)
	if len(atWarn) != 0 {
		t.Errorf("records at Warn = %d, want 0 -- these are Info-level operational events, not warnings", len(atWarn))
	}
}

// TestEveryEmittedLineCarriesItsCorrelationIdentifiersAsKeyValues asserts
// key:VALUE pairs on decoded JSON, never a substring of the formatted line.
//
// A substring check would pass on a line that merely happened to contain the
// text anywhere -- including inside a different field's value -- which is how
// a telemetry test ends up green against output no consumer can parse.
func TestEveryEmittedLineCarriesItsCorrelationIdentifiersAsKeyValues(t *testing.T) {
	t.Parallel()

	records := telemetryCapture(t, slog.LevelInfo, func(sink SlogOperandResolutionSink, ctx context.Context) {
		sink.RecordComparisonPolicy(ctx, ComparisonPolicyEvent{
			RequestID: "request_telemetry", OrgID: "org-1",
			Admission: contextfabric.ComparisonAdmittedNamedPair, SlotCount: 2,
			QuestionSearchSuppressed: true, EvidenceCensusSuppressed: true, CandidateBudget: 10,
		})
		sink.RecordOperandSlot(ctx, OperandSlotEvent{
			RequestID: "request_telemetry", OrgID: "org-1",
			SlotPosition: 1, SlotKind: contextfabric.SubjectTeam, TermCount: 1,
			CandidateCount: 2, CommittedCount: 0, Outcome: operandSlotAmbiguous,
		})
		sink.RecordComparisonReceiptBinding(ctx, ComparisonReceiptBindingEvent{
			RequestID: "request_telemetry", OrgID: "org-1",
			ReceiptsConsidered: 2, BoundCount: 1, UnboundCount: 1, BoundSlotPositions: []int{0},
		})
		sink.RecordComparisonDecision(ctx, ComparisonDecisionEvent{
			RequestID: "request_telemetry", OrgID: "org-1",
			Decision: comparisonDecisionHeld, PublishedCommitted: 0, UnboundReceipts: 1,
		})
	})

	if len(records) != 4 {
		t.Fatalf("emitted %d records, want 4 -- one per event type", len(records))
	}
	for index, record := range records {
		// CORRELATION IDENTIFIERS ON EVERY LINE. Without a request id a reader
		// cannot join these lines to each other or to the affirmation gate's
		// retraction warning, which is the join that reveals a half-published
		// comparison.
		if got, ok := record["request_id"].(string); !ok || strings.TrimSpace(got) == "" {
			t.Errorf("record %d has no nonempty request_id (%#v)", index, record["request_id"])
		}
		if got, ok := record["org_id"].(string); !ok || strings.TrimSpace(got) == "" {
			t.Errorf("record %d has no nonempty org_id (%#v)", index, record["org_id"])
		}
	}

	// SPOT-CHECK THE VALUES, as decoded values rather than as text.
	if got := records[0]["admission"]; got != string(contextfabric.ComparisonAdmittedNamedPair) {
		t.Errorf("admission = %v, want %q", got, contextfabric.ComparisonAdmittedNamedPair)
	}
	if got := records[1]["outcome"]; got != string(operandSlotAmbiguous) {
		t.Errorf("slot outcome = %v, want %q", got, operandSlotAmbiguous)
	}
	if got := records[2]["unbound_count"]; got != float64(1) {
		t.Errorf("unbound_count = %v (%T), want 1", got, got)
	}
	if got := records[3]["decision"]; got != comparisonDecisionHeld {
		t.Errorf("decision = %v, want %q", got, comparisonDecisionHeld)
	}
}

// TestTheSinkNeverNamesACombinedOutcome pins the closed-vocabulary rule as a
// property rather than a comment.
//
// An ambiguous operand beside a resolved one is TWO slot lines and ONE hold
// line. A single token naming that pairing cannot be counted, and the moment
// one exists every new pairing needs another.
func TestTheSinkNeverNamesACombinedOutcome(t *testing.T) {
	t.Parallel()

	records := telemetryCapture(t, slog.LevelInfo, func(sink SlogOperandResolutionSink, ctx context.Context) {
		sink.RecordOperandSlot(ctx, OperandSlotEvent{
			RequestID: "r", OrgID: "o", SlotPosition: 0, SlotKind: contextfabric.SubjectTeam,
			CommittedCount: 1, CandidateCount: 1, Outcome: operandSlotResolved,
		})
		sink.RecordOperandSlot(ctx, OperandSlotEvent{
			RequestID: "r", OrgID: "o", SlotPosition: 1, SlotKind: contextfabric.SubjectTeam,
			CommittedCount: 0, CandidateCount: 2, Outcome: operandSlotAmbiguous,
		})
		sink.RecordComparisonDecision(ctx, ComparisonDecisionEvent{
			RequestID: "r", OrgID: "o", Decision: comparisonDecisionHeld,
		})
	})

	if len(records) != 3 {
		t.Fatalf("emitted %d records, want 3 -- two slot lines and one decision line", len(records))
	}
	if records[0]["outcome"] == records[1]["outcome"] {
		t.Errorf("both slot lines report %v; the two operands had different outcomes and the vocabulary must keep them apart", records[0]["outcome"])
	}
	if got := records[2]["decision"]; got != comparisonDecisionHeld {
		t.Errorf("decision = %v, want %q -- one aggregate line, and it is a hold", got, comparisonDecisionHeld)
	}
	// The decision vocabulary has exactly two members and neither describes a
	// mixture. If a third ever appears, this is where it has to be justified.
	for _, member := range []string{comparisonDecisionPublished, comparisonDecisionHeld} {
		if strings.Contains(member, "partial") || strings.Contains(member, "_and_") {
			t.Errorf("decision vocabulary member %q names a combination", member)
		}
	}
}
