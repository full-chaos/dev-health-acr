package hosted

// THE INTERPRETED-TIME VERDICT, READ BACK OUT OF THE SINK THE DEPLOYED
// CONSTRUCTION RETURNS.
//
// CHAOS-5421's whole diagnosis had to be done by reading source, because
// the log said `failure_classification="invalid_time_bound"` and nothing
// else: the six rules that can refuse an interpreted bound differ ONLY in
// wrapped error text, and the failure classifier deliberately never logs
// error text at any level, by design and correctly. So an operator could
// see that a turn was refused on time bounds and could not see which rule
// refused it, nor whether the CALLER or this engine's own interpreter had
// produced the bound.
//
// These four assert the line that closes that, through
// contextFabricEngineTelemetry(Options{Logger: ...}) -- the exact call
// open.go makes for every real deployment -- rather than through a test
// adapter. A test that hands its own sink to a double proves formatting
// and stays green the day the runtime stops installing one.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// emitInterpretedTimeBound drives one decision through the DEPLOYED
// telemetry at the production log level, with a real request context, and
// returns the decoded line.
func emitInterpretedTimeBound(t *testing.T, decision contextfabric.InterpretedTimeBoundDecision) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	// A REAL request context, not context.Background(): the join attrs are
	// what let an operator put this line beside the investigation-failed
	// line for the same turn, and a test using a bare context cannot see
	// them go missing.
	ctx := observability.WithRequestID(context.Background(), "req_5421aaaabbbbccccddddeeeeffff0011")
	contextFabricEngineTelemetry(Options{Logger: logger}).
		RecordInterpretedTimeBound(ctx, storage.Principal{OrgID: "org_5421"}, decision)
	if buf.Len() == 0 {
		t.Fatal("the deployed engine telemetry emitted NOTHING at the production log level; a verdict readable only under a debug flag is not an observable")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO -- a verdict demoted below the production level disappears in prod exactly as it does here", level)
	}
	if msg, _ := rec["msg"].(string); msg != "context fabric interpreted time bound" {
		t.Fatalf("msg = %q, want the interpreted-time-bound line", msg)
	}
	if got, _ := rec["request_id"].(string); got != "req_5421aaaabbbbccccddddeeeeffff0011" {
		t.Errorf("request_id = %q, want the context's own id -- without the join attr this line cannot be put beside the turn it describes", got)
	}
	if got, _ := rec["org_id"].(string); got != "org_5421" {
		t.Errorf("org_id = %q, want the principal's own org", got)
	}
	return rec
}

// THE ORDINARY ARM, and the one that makes every other arm mean something.
// If the keys appeared only when a bound was clamped or refused, their
// absence on an ordinary turn would be indistinguishable from a build that
// never emits them -- and the regression worth catching is precisely a
// build that stopped deciding.
func TestTheDeployedInterpretedTimeBoundLineCarriesAnOrdinaryVerdict(t *testing.T) {
	rec := emitInterpretedTimeBound(t, contextfabric.InterpretedTimeBoundDecision{
		Axis:    contextfabric.TemporalCurrent,
		Outcome: contextfabric.InterpretedTimeBoundOK,
	})
	if got, _ := rec["outcome"].(string); got != string(contextfabric.InterpretedTimeBoundOK) {
		t.Errorf("outcome = %q, want %q", got, contextfabric.InterpretedTimeBoundOK)
	}
	if got, _ := rec["axis"].(string); got != string(contextfabric.TemporalCurrent) {
		t.Errorf("axis = %q, want the interpreted axis echoed", got)
	}
	// FALSE is emitted, not omitted. An operator asking "how often does the
	// interpreter overshoot now?" needs the denominator, and a key printed
	// only when true supplies a numerator with none.
	if got, ok := rec["clamp_applied"].(bool); !ok || got {
		t.Errorf("clamp_applied = %v (present=%t), want an explicit false on a turn that clamped nothing", got, ok)
	}
	// And an explicit 0, never an omission: "not a range" and "we did not
	// measure the range" must never read alike.
	got, ok := rec["range_days"].(float64)
	if !ok {
		t.Fatalf("the emitted line carries no numeric range_days; an absent count and a measured zero must never read alike -- line: %v", rec)
	}
	if got != 0 {
		t.Errorf("range_days = %v, want an explicit 0 off the range axis", got)
	}
}

// THE CLAMP ARM. This is the one the corpus needed and could not read: the
// turn is SERVED, so there is no failure line at all to carry the fact that
// the model reached past now and was pulled back.
func TestTheDeployedInterpretedTimeBoundLineReportsAClamp(t *testing.T) {
	rec := emitInterpretedTimeBound(t, contextfabric.InterpretedTimeBoundDecision{
		Axis:         contextfabric.TemporalRange,
		Outcome:      contextfabric.InterpretedTimeBoundFutureEnd,
		ClampApplied: true,
		RangeDays:    30,
	})
	if got, _ := rec["outcome"].(string); got != string(contextfabric.InterpretedTimeBoundFutureEnd) {
		t.Errorf("outcome = %q, want %q", got, contextfabric.InterpretedTimeBoundFutureEnd)
	}
	if got, ok := rec["clamp_applied"].(bool); !ok || !got {
		t.Errorf("clamp_applied = %v (present=%t), want true -- a served answer whose bound was silently moved is the regression that would be invisible at Info without this key", got, ok)
	}
	if got, _ := rec["range_days"].(float64); got != 30 {
		t.Errorf("range_days = %v, want the CLAMPED width 30, so the line describes the window that was actually read", got)
	}
}

// THE REFUSING ARMS. Each member is asserted through the deployed sink, so
// the vocabulary an operator reads is the vocabulary the code decides over
// -- not a second copy that can drift. Clamp and outcome are asserted
// together on range_too_wide, which is the shape that proves they are
// INDEPENDENT arms: a range can be pulled back to now and still be too
// wide, and both facts are true of that turn at once.
func TestTheDeployedInterpretedTimeBoundLineNamesEveryRefusingRule(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		decision  contextfabric.InterpretedTimeBoundDecision
		wantClamp bool
		wantDays  float64
	}{
		{
			name: "range too wide, after a clamp",
			decision: contextfabric.InterpretedTimeBoundDecision{
				Axis: contextfabric.TemporalRange, Outcome: contextfabric.InterpretedTimeBoundRangeTooWide,
				ClampApplied: true, RangeDays: 3000,
			},
			wantClamp: true, wantDays: 3000,
		},
		{
			name: "an instant that is absent or zero",
			decision: contextfabric.InterpretedTimeBoundDecision{
				Axis: contextfabric.TemporalValidTime, Outcome: contextfabric.InterpretedTimeBoundAbsentOrZero,
			},
		},
		{
			name: "an axis outside the closed vocabulary",
			decision: contextfabric.InterpretedTimeBoundDecision{
				Axis: contextfabric.TemporalAxis("sideways"), Outcome: contextfabric.InterpretedTimeBoundUnknownAxis,
			},
		},
		{
			name: "a range whose end precedes its start",
			decision: contextfabric.InterpretedTimeBoundDecision{
				Axis: contextfabric.TemporalRange, Outcome: contextfabric.InterpretedTimeBoundMalformedRange,
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rec := emitInterpretedTimeBound(t, testCase.decision)
			if got, _ := rec["outcome"].(string); got != string(testCase.decision.Outcome) {
				t.Errorf("outcome = %q, want %q -- the rule that refused is the one thing the pre-existing failure line could never say", got, testCase.decision.Outcome)
			}
			// The axis is echoed VERBATIM, including one outside the
			// vocabulary: an operator diagnosing an interpreter that
			// emitted a bad axis needs to see what it emitted.
			if got, _ := rec["axis"].(string); got != string(testCase.decision.Axis) {
				t.Errorf("axis = %q, want %q echoed verbatim", got, testCase.decision.Axis)
			}
			if got, ok := rec["clamp_applied"].(bool); !ok || got != testCase.wantClamp {
				t.Errorf("clamp_applied = %v (present=%t), want %v", got, ok, testCase.wantClamp)
			}
			if got, ok := rec["range_days"].(float64); !ok || got != testCase.wantDays {
				t.Errorf("range_days = %v (present=%t), want %v", got, ok, testCase.wantDays)
			}
		})
	}
}

// EVERY MEMBER OF THE VOCABULARY REACHES THE SINK. The three tests above
// name their members by hand, so a member added later would be emitted by
// the code and asserted by nothing. This one derives the set from the
// vocabulary itself and fails when the two disagree.
func TestEveryInterpretedTimeBoundOutcomeSurvivesTheDeployedSink(t *testing.T) {
	for _, outcome := range contextfabric.InterpretedTimeBoundOutcomeVocabulary() {
		rec := emitInterpretedTimeBound(t, contextfabric.InterpretedTimeBoundDecision{
			Axis: contextfabric.TemporalRange, Outcome: outcome, RangeDays: 7,
		})
		got, _ := rec["outcome"].(string)
		if got != string(outcome) {
			t.Errorf("outcome = %q, want %q -- a member the code can decide but the sink cannot carry is a member an operator can never read", got, outcome)
		}
		if !contextfabric.ValidInterpretedTimeBoundOutcome(contextfabric.InterpretedTimeBoundOutcome(got)) {
			t.Errorf("the sink emitted %q, which is not a member of the closed vocabulary", got)
		}
	}
}
