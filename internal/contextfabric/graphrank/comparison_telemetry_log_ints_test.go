package graphrank

// THE NUMERIC LOG BARRIER ON THE FOUR COMPARISON LINES.
//
// Every attribute is routed by its TYPE, not by an argument about where its
// value comes from: a string through contextfabric.SanitizeLogAttr, an integer
// through contextfabric.SanitizeLogInt, an integer slice element by element
// through sanitizedLogInts, and a bool bare. The field set is read from the
// event types by reflection, so a field added later is covered without editing
// this file.
//
// The wiring is invisible to behaviour -- a wired and an unwired integer log
// identically, and code scanning reads the call, not the output -- so the
// shape half reads the emitter bodies. The behavioural half proves the barrier
// changes no emitted value.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
)

type comparisonLogEmitter struct {
	name   string
	method string
	event  any
	keys   map[string]string
	emit   func(sink SlogOperandResolutionSink, ctx context.Context, event any)
}

func comparisonLogEmitters() []comparisonLogEmitter {
	return []comparisonLogEmitter{
		{"ComparisonPolicyEvent", "RecordComparisonPolicy", ComparisonPolicyEvent{}, comparisonPolicyLogKeys,
			func(sink SlogOperandResolutionSink, ctx context.Context, event any) {
				sink.RecordComparisonPolicy(ctx, event.(ComparisonPolicyEvent))
			}},
		{"OperandSlotEvent", "RecordOperandSlot", OperandSlotEvent{}, operandSlotLogKeys,
			func(sink SlogOperandResolutionSink, ctx context.Context, event any) {
				sink.RecordOperandSlot(ctx, event.(OperandSlotEvent))
			}},
		{"ComparisonReceiptBindingEvent", "RecordComparisonReceiptBinding", ComparisonReceiptBindingEvent{}, comparisonReceiptBindingLogKeys,
			func(sink SlogOperandResolutionSink, ctx context.Context, event any) {
				sink.RecordComparisonReceiptBinding(ctx, event.(ComparisonReceiptBindingEvent))
			}},
		{"ComparisonDecisionEvent", "RecordComparisonDecision", ComparisonDecisionEvent{}, comparisonDecisionLogKeys,
			func(sink SlogOperandResolutionSink, ctx context.Context, event any) {
				sink.RecordComparisonDecision(ctx, event.(ComparisonDecisionEvent))
			}},
	}
}

// emitterBody returns the source of one sink method, from its signature to its
// closing brace, so text elsewhere in the file cannot satisfy an assertion.
func emitterBody(t *testing.T, source, method string) string {
	t.Helper()
	marker := "func (s SlogOperandResolutionSink) " + method + "("
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("could not find %q in comparison_telemetry.go", marker)
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("could not find the end of %s", method)
	}
	return source[start : start+end]
}

func TestEveryComparisonLogAttributeIsRoutedThroughTheBarrierForItsType(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("comparison_telemetry.go")
	if err != nil {
		t.Fatalf("read comparison_telemetry.go: %v", err)
	}
	source := string(raw)

	routed := map[reflect.Kind]int{}
	for _, emitter := range comparisonLogEmitters() {
		body := emitterBody(t, source, emitter.method)
		structType := reflect.TypeOf(emitter.event)
		for index := 0; index < structType.NumField(); index++ {
			field := structType.Field(index)
			selector := "event." + field.Name
			if got := strings.Count(body, selector); got != 1 {
				t.Errorf("%s: %s appears %d times in %s, want exactly 1 -- a second use can reach the logger bare", emitter.name, selector, got, emitter.method)
				continue
			}
			var want []string
			switch {
			case field.Type.Kind() == reflect.String:
				want = []string{"contextfabric.SanitizeLogAttr(" + selector + ")", "contextfabric.SanitizeLogAttr(string(" + selector + "))"}
			case field.Type.Kind() == reflect.Int:
				want = []string{"contextfabric.SanitizeLogInt(int64(" + selector + "))"}
			case field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Int:
				want = []string{"sanitizedLogInts(" + selector + ")"}
			case field.Type.Kind() == reflect.Bool:
				want = []string{"LogKeys[\"" + field.Name + "\"], " + selector}
			default:
				t.Errorf("%s.%s has kind %s, which this pin has no barrier rule for -- decide one before logging it", emitter.name, field.Name, field.Type)
				continue
			}
			matched := false
			for _, candidate := range want {
				if strings.Contains(body, candidate) {
					matched = true
				}
			}
			if !matched {
				t.Errorf("%s.%s (%s) is not routed through its barrier in %s; want one of %q", emitter.name, field.Name, field.Type, emitter.method, want)
				continue
			}
			routed[field.Type.Kind()]++
		}
	}
	if routed[reflect.Int] != 12 || routed[reflect.Slice] != 1 {
		t.Errorf("routed %d int and %d int-slice attributes, want 12 and 1 -- a changed count means a field moved without this pin being read", routed[reflect.Int], routed[reflect.Slice])
	}
}

func TestSanitizedLogIntsBarrierIsInTheHelperBody(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("comparison_telemetry.go")
	if err != nil {
		t.Fatalf("read comparison_telemetry.go: %v", err)
	}
	source := string(raw)
	marker := "func sanitizedLogInts(values []int) []int64 {"
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("could not find %q", marker)
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of sanitizedLogInts")
	}
	body := source[start : start+end]
	if !strings.Contains(body, "contextfabric.SanitizeLogInt(int64(value))") {
		t.Error("sanitizedLogInts no longer calls contextfabric.SanitizeLogInt on each element -- the slice attribute would reach the logger unrecognised")
	}
}

func TestEveryComparisonIntegerAttributeIsEmittedUnchanged(t *testing.T) {
	t.Parallel()

	for _, emitter := range comparisonLogEmitters() {
		t.Run(emitter.name, func(t *testing.T) {
			t.Parallel()
			structType := reflect.TypeOf(emitter.event)
			event := reflect.New(structType).Elem()
			want := map[string]any{}
			for index := 0; index < structType.NumField(); index++ {
				field := structType.Field(index)
				switch {
				case field.Type.Kind() == reflect.Int:
					value := int64(1)<<40 + int64(index)*7 + 3
					event.Field(index).SetInt(value)
					want[emitter.keys[field.Name]] = float64(value)
				case field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Int:
					event.Field(index).Set(reflect.ValueOf([]int{0, 1, 1 << 33}))
					want[emitter.keys[field.Name]] = []any{float64(0), float64(1), float64(int64(1) << 33)}
				}
			}
			if len(want) == 0 {
				t.Skip("no integer attributes on this event")
			}

			records := telemetryCapture(t, slog.LevelInfo, func(sink SlogOperandResolutionSink, ctx context.Context) {
				emitter.emit(sink, ctx, event.Interface())
			})
			if len(records) != 1 {
				t.Fatalf("emitted %d records, want 1", len(records))
			}
			for key, value := range want {
				if got := fmt.Sprint(records[0][key]); got != fmt.Sprint(value) {
					t.Errorf("%s = %v, want %v -- the barrier must return every value unchanged", key, records[0][key], value)
				}
			}
		})
	}
}

func TestSanitizedLogIntsKeepsNilAndEmptyDistinct(t *testing.T) {
	t.Parallel()

	if got := sanitizedLogInts(nil); got != nil {
		t.Errorf("sanitizedLogInts(nil) = %#v, want nil -- a nil slice logs as null and must keep doing so", got)
	}
	if got := sanitizedLogInts([]int{}); got == nil || len(got) != 0 {
		t.Errorf("sanitizedLogInts([]int{}) = %#v, want an empty non-nil slice -- an empty slice logs as []", got)
	}
}
