package graphrank

// CHAOS-5516 -- team-lead's addition after B8 surfaced the class in
// internal/runtime/hosted's own test fixtures: the same silent-zero shape
// those fixtures hit is still reachable from PRODUCTION code, not just
// tests, because the OLD individual Decision*/OfferPool* fields stay on the
// shared ResolutionTraceEvent struct (brief-pr2.md's own design: "an
// ADDITIONAL field for the one stage migrating, not a replacement"). A
// caller who hand-assembles a decision_summary event using only those old
// fields now gets a silently wrong (all-zero) summary instead of an error,
// because tracer.go's "decision_summary" case reads ONLY
// event.DecisionSummaryFields. This pin proves the closed form instead:
// SlogResolutionTracer.Trace REFUSES (an Error-level line, no summary
// emitted) rather than printing the zeroed line.
//
// Sibling-stage sweep (team-lead's rule: "names the sibling set swept"):
// grep of tracer.go's own switch shows anchor_pool and offer_pool read
// their OLD individual fields (event.DecisionAnchorPoolKindScope,
// event.OfferPoolVectorOnlyExcluded, ...) DIRECTLY -- there is no
// AnchorPoolFields/OfferPoolFields typed shadow on the shared struct for
// either stage that a construction path could leave zero while the old
// fields are set. RankedCutSummaryFields/AnchorSlotDisplacedFields exist
// (generated uniformly per design ruling A) but tracer.go's "ranked_cut"
// and "anchor_slot_displaced" cases do not read them at all -- unmigrated,
// per brief-pr2.md's OUT OF SCOPE. The silent-zero shape exists ONLY for
// decision_summary, the one stage this PR actually migrated the emission
// path for; the sibling set swept is empty.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

// TestDecisionSummaryConstructionRefusesAHandAssembledOldFieldEvent drives
// the exact repro class internal/runtime/hosted's own fixtures hit before
// their fix: a caller builds ResolutionTraceEvent{Stage: "decision_summary",
// ...} using only the OLD individual fields, DecisionSummaryFields left at
// its Go zero value.
func TestDecisionSummaryConstructionRefusesAHandAssembledOldFieldEvent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tracer := NewSlogResolutionTracer(logger)

	tracer.Trace(ResolutionTraceEvent{
		RequestID: "request_hand_assembled_old_fields", Stage: "decision_summary",
		// OLD individual fields, exactly the shape the ticket names
		// ("callers still assemble that event's field list") -- ALL that a
		// caller who has not been told about DecisionSummaryFields would
		// naturally set.
		DecisionEventCount: 2, DecisionCommittedCount: 1, DecisionNoCommitCount: 1,
		DecisionCommittedIDs: []string{"team.v2:github:platform"},
		DecisionCommitGates:  []string{"exact_index"},
		DecisionCommitBases:  []string{"statistical"},
		DecisionFrameGate:    "passed", DecisionRefuseBasis: "none",
		// DecisionSummaryFields: intentionally omitted -- its Go zero value.
	})

	if buf.Len() == 0 {
		t.Fatal("the tracer emitted NOTHING -- want a refusal line at Error, not silence")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "ERROR" {
		t.Fatalf("level = %q, want ERROR -- a silently zeroed decision_summary is the regression this guard exists to make loud", level)
	}
	if msg, _ := rec["msg"].(string); msg != "context fabric resolution trace: decision summary construction refused" {
		t.Errorf("msg = %q, want the named refusal message", msg)
	}
	if got, _ := rec["stage"].(string); got != "decision_summary" {
		t.Errorf("stage = %q, want \"decision_summary\"", got)
	}
	if got, _ := rec["request_id"].(string); got != "request_hand_assembled_old_fields" {
		t.Errorf("request_id = %q, want the caller's own request id -- an operator must be able to find which call this was", got)
	}
	// NO zeroed decision_summary line: the refusal must REPLACE the emission,
	// never accompany it.
	if _, ok := rec["decision_event_count"]; ok {
		t.Errorf("a decision_event_count key reached the log -- the refusal must replace the emission, not merely precede a still-emitted zeroed line: %v", rec)
	}
}

// TestDecisionSummaryConstructionAcceptsTheRealTypedPath is the positive
// control: a real decisionSummaryBuffer.flush()-shaped construction (every
// open-vocabulary field explicitly set, including the "none" tokens a
// genuine zero-decision resolution carries) must NOT be refused -- the
// explicit-zero contract (clause 3) and this new refusal must never
// collide.
func TestDecisionSummaryConstructionAcceptsTheRealTypedPath(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tracer := NewSlogResolutionTracer(logger)

	tracer.Trace(ResolutionTraceEvent{
		RequestID: "request_real_typed_zero", Stage: "decision_summary",
		// The same shape decisionSummaryBuffer.flush() builds for a genuine
		// zero-decision resolution: every count/bool at its explicit zero,
		// every open-vocabulary field an explicit "none" token (never "").
		DecisionSummaryFields: eventspec.NewDecisionSummaryFields(
			"request_real_typed_zero", 0, 0, 0, 0,
			[]string{}, []string{}, []string{},
			false, "none", "none",
			0, 0, false, 0, "none", "none", []string{}, 0,
			"none", "none", "none", []string{}, []string{},
		),
	})

	if buf.Len() == 0 {
		t.Fatal("the tracer emitted NOTHING for a real, correctly-constructed zero-decision summary -- want the explicit-zero line, not a refusal")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO -- a genuine zero-decision resolution must never be misread as an unconstructed event", level)
	}
	if got, _ := rec["decision_event_count"].(float64); got != 0 {
		t.Errorf("decision_event_count = %v, want the explicit 0", rec["decision_event_count"])
	}
}
