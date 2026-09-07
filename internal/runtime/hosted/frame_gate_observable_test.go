package hosted

// THE ORDERING SEAM, READ BACK OUT OF THE SINK THE DEPLOYED CONSTRUCTION
// RETURNS.
//
// The seam's whole behaviour is a REFUSAL and an EXCLUSION -- things that do
// not happen. A regression in it therefore produces no error, no stack, no
// changed count: a laundered commit and a correct one carry identical
// decision_event_count, committed_ids, commit_gates and commit_bases. The
// only thing that can distinguish them at Info is a key that says what the
// resolution was ALLOWED to do before it began, which is what these four
// assert.
//
// Driven through defaultResolutionTracer(nil, logger) -- the exact call
// open.go makes for every real deployment -- rather than through a test
// adapter, because a test that hands its own sink to a double proves
// formatting and stays green the day the runtime stops installing one.

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// emitFrameGateDecisionSummary drives one decision_summary event through the
// deployed sink at the production log level and returns the decoded line.
func emitFrameGateDecisionSummary(t *testing.T, event graphrank.ResolutionTraceEvent) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	defaultResolutionTracer(nil, logger).Trace(event)
	if buf.Len() == 0 {
		t.Fatal("the deployed tracer emitted NOTHING at the production log level")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO -- the ordering verdict must survive the production log level", level)
	}
	return rec
}

// A PASSING resolution still carries every key, with explicit tokens and
// explicit zeros. This is the arm that makes the refusing arms mean
// something: if the keys appeared only when the gate refused, their absence
// on an ordinary line would be indistinguishable from a build that never
// emits them, and the regression this seam guards against is exactly a build
// that stopped enforcing.
func TestTheDeployedDecisionSummaryCarriesAPassingFrameGate(t *testing.T) {
	rec := emitFrameGateDecisionSummary(t, graphrank.ResolutionTraceEvent{
		RequestID: "request_frame_gate_passed", Stage: "decision_summary",
		DecisionEventCount: 1, DecisionCommittedCount: 1,
		DecisionCommittedIDs: []string{"team.v2:github:platform"},
		DecisionCommitGates:  []string{"exact_index"},
		DecisionCommitBases:  []string{"statistical"},
		DecisionFrameGate:    "passed", DecisionRefuseBasis: "none",
		OfferPoolVectorOnlyExcluded: 0, OfferPoolVectorOnlyDemoted: 0,
	})
	if got, _ := rec["frame_gate"].(string); got != "passed" {
		t.Errorf("frame_gate = %q, want \"passed\" -- without this key an operator cannot tell a gate that was consulted from one that was never consulted at all", got)
	}
	if got, _ := rec["refuse_basis"].(string); got != "none" {
		t.Errorf("refuse_basis = %q, want the explicit token \"none\", never an empty value", got)
	}
	for _, key := range []string{"offer_pool_vector_only_excluded", "offer_pool_vector_only_demoted"} {
		got, ok := rec[key].(float64)
		if !ok {
			t.Errorf("the emitted line carries no numeric %q; an absent count and a measured zero must never read alike -- line: %v", key, rec)
			continue
		}
		if got != 0 {
			t.Errorf("%s = %v, want 0 on a resolution that excluded nothing", key, got)
		}
	}
}

// THE REFUSING ARM, and the one an operator reads when the seam has been
// weakened: a resolution running under a refusing verdict is a gate bypass,
// and this line is what makes it visible beside a non-zero committed_count.
func TestTheDeployedDecisionSummaryCarriesARefusingFrameGate(t *testing.T) {
	rec := emitFrameGateDecisionSummary(t, graphrank.ResolutionTraceEvent{
		RequestID: "request_frame_gate_refused", Stage: "decision_summary",
		DecisionCommittedIDs: []string{}, DecisionCommitGates: []string{}, DecisionCommitBases: []string{},
		DecisionFrameGate:           "refused:member_kind_unservable",
		DecisionRefuseBasis:         "member_kind_unservable",
		OfferPoolVectorOnlyExcluded: 3, OfferPoolVectorOnlyDemoted: 1,
	})
	if got, _ := rec["frame_gate"].(string); got != "refused:member_kind_unservable" {
		t.Errorf("frame_gate = %q, want the refusing verdict WITH its basis -- the outcome alone does not say what refused it", got)
	}
	if got, _ := rec["refuse_basis"].(string); got != "member_kind_unservable" {
		t.Errorf("refuse_basis = %q, want \"member_kind_unservable\"", got)
	}
	if got, _ := rec["offer_pool_vector_only_excluded"].(float64); got != 3 {
		t.Errorf("offer_pool_vector_only_excluded = %v, want 3", got)
	}
	if got, _ := rec["offer_pool_vector_only_demoted"].(float64); got != 1 {
		t.Errorf("offer_pool_vector_only_demoted = %v, want 1", got)
	}
}

// The folded offer_pool summary is Info on its own stage line too. The
// per-candidate lines are retrieval-pool-sized -- 186 of 329 offered
// candidates were vector-only in one measured 36-question arm -- so they stay
// Debug, and this is the line an operator actually gets.
func TestTheDeployedOfferPoolSummaryReachesTheProductionLogLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	tracer := defaultResolutionTracer(nil, logger)
	tracer.Trace(graphrank.ResolutionTraceEvent{
		RequestID: "request_offer_pool_summary", Stage: "offer_pool", OfferPoolSummary: true,
		OfferPoolVectorOnlyExcluded: 2, OfferPoolVectorOnlyDemoted: 0,
	})
	if buf.Len() == 0 {
		t.Fatal("the offer-pool summary emitted nothing at the production log level")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO", level)
	}
	if got, _ := rec["vector_only_excluded"].(float64); got != 2 {
		t.Errorf("vector_only_excluded = %v, want 2", got)
	}
	// Explicit zero, not an omitted key: a run that demoted nothing must be
	// distinguishable from a build that stopped counting demotions.
	got, ok := rec["vector_only_demoted"].(float64)
	if !ok || got != 0 {
		t.Errorf("vector_only_demoted = %v (present=%t), want an explicit 0", got, ok)
	}

	// The PER-CANDIDATE line stays BELOW Info. Asserted here rather than
	// assumed, because promoting it would put one line per retrieval
	// candidate into production logs, which is the volume class this
	// package's other summaries exist to avoid.
	buf.Reset()
	tracer.Trace(graphrank.ResolutionTraceEvent{
		RequestID: "request_offer_pool_candidate", Stage: "offer_pool",
		OfferPoolDisposition: "vector_only_excluded",
	})
	if buf.Len() != 0 {
		t.Fatalf("the per-candidate offer_pool line reached the production log level: %s", buf.String())
	}
}
