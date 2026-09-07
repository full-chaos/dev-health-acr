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
		OfferPoolEmptiedByExclusion: false,
	})
	// FALSE is emitted, not omitted. This key separates two empties that are
	// identical on every other key of the line, so a build that printed it
	// only when true would be indistinguishable from one that never prints
	// it -- and the false case is the ordinary one.
	if got, ok := rec["offer_pool_emptied_by_exclusion"].(bool); !ok || got {
		t.Errorf("offer_pool_emptied_by_exclusion = %v (present=%t), want an explicit false", got, ok)
	}
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

// THE WITHHELD-POOL LINE. The rig arm's one regression was invisible until
// this key existed: a resolution that found candidates and offered none of
// them printed a decision summary identical to one from an empty graph, and
// the turn it cost could only be diagnosed by replaying the whole chain.
func TestTheDeployedDecisionSummarySaysWhenThePoolWasEmptiedByTheExclusion(t *testing.T) {
	rec := emitFrameGateDecisionSummary(t, graphrank.ResolutionTraceEvent{
		RequestID: "request_offer_pool_emptied", Stage: "decision_summary",
		DecisionEventCount: 1, DecisionAmbiguousCount: 1,
		DecisionCommittedIDs: []string{}, DecisionCommitGates: []string{}, DecisionCommitBases: []string{},
		DecisionFrameGate: "passed", DecisionRefuseBasis: "none",
		OfferPoolVectorOnlyExcluded: 3, OfferPoolEmptiedByExclusion: true,
	})
	if got, ok := rec["offer_pool_emptied_by_exclusion"].(bool); !ok || !got {
		t.Fatalf("offer_pool_emptied_by_exclusion = %v (present=%t), want true -- without it a withheld pool and an empty graph print the same line", got, ok)
	}
	// The counters must corroborate it on the same line: a true flag beside
	// zero withheld candidates would be unreadable, and is the shape a
	// hardcoded `true` would produce.
	if got, _ := rec["offer_pool_vector_only_excluded"].(float64); got != 3 {
		t.Errorf("offer_pool_vector_only_excluded = %v, want 3 standing beside the flag", got)
	}
	if got, _ := rec["ambiguous_count"].(float64); got != 1 {
		t.Errorf("ambiguous_count = %v, want 1 -- the flag only means anything on an ambiguous resolution", got)
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

// THE ANCHOR-POOL SCOPE, READ BACK OUT OF THE DEPLOYED SINK (CHAOS-5393).
//
// Same argument as the four above: the seam's behaviour is an ADMISSION that
// did or did not happen, and a resolution that filtered its own scope anchor
// out of the pool prints a decision summary identical to one whose graph
// simply held nothing -- same zero committed_count, same empty ids, same
// gates. `anchor_pool_kind_scope` beside `member_kind_confirmed` is the only
// thing at Info that separates them.
func TestTheDeployedDecisionSummaryNamesTheAnchorPoolKindScope(t *testing.T) {
	rec := emitFrameGateDecisionSummary(t, graphrank.ResolutionTraceEvent{
		RequestID: "request_anchor_scope", Stage: "decision_summary",
		DecisionEventCount: 1, DecisionCommittedCount: 1,
		DecisionCommittedIDs: []string{"team.v2:github:chaos"},
		DecisionCommitGates:  []string{"exact_index"},
		DecisionCommitBases:  []string{"statistical"},
		DecisionFrameGate:    "passed", DecisionRefuseBasis: "none",
		DecisionAnchorPoolKindScope:       "team",
		DecisionAnchorPoolKindScopeSource: "receipt",
		DecisionMemberKindConfirmed:       "project",
	})
	for key, want := range map[string]string{
		"anchor_pool_kind_scope":        "team",
		"anchor_pool_kind_scope_source": "receipt",
		"member_kind_confirmed":         "project",
	} {
		got, ok := rec[key].(string)
		if !ok {
			t.Errorf("the emitted line carries no %q; without it an operator cannot tell a pool that admitted the anchor from one that filtered it out -- line: %v", key, rec)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// The two kinds on a scope-anchored line are never equal -- invariant
	// I11 -- and a build where they ARE equal is one that scoped the anchor
	// search to the member kind. Asserted here because that equality is the
	// defect's signature, readable with no other context.
	if rec["anchor_pool_kind_scope"] == rec["member_kind_confirmed"] {
		t.Errorf("anchor_pool_kind_scope == member_kind_confirmed (%v): the resolved anchor's kind is never the member kind", rec["anchor_pool_kind_scope"])
	}
}

// THE ORDINARY LINE still carries all three keys, with explicit `none`
// tokens. Without this arm the keys could appear only on scope-anchored
// resolutions, and their absence elsewhere would be indistinguishable from a
// build that stopped emitting them.
func TestTheDeployedDecisionSummaryCarriesExplicitNoneForTheAnchorScope(t *testing.T) {
	rec := emitFrameGateDecisionSummary(t, graphrank.ResolutionTraceEvent{
		RequestID: "request_no_anchor_scope", Stage: "decision_summary",
		DecisionCommittedIDs: []string{}, DecisionCommitGates: []string{}, DecisionCommitBases: []string{},
		DecisionFrameGate: "passed", DecisionRefuseBasis: "none",
		DecisionAnchorPoolKindScope:       "none",
		DecisionAnchorPoolKindScopeSource: "none",
		DecisionMemberKindConfirmed:       "none",
	})
	for _, key := range []string{"anchor_pool_kind_scope", "anchor_pool_kind_scope_source", "member_kind_confirmed"} {
		if got, _ := rec[key].(string); got != "none" {
			t.Errorf("%s = %q, want the explicit token \"none\", never an empty value or an absent key", key, got)
		}
	}
}

// THE ANCHOR-POOL STAGE LINE reaches the production log level on its own.
// It is once per resolution, so unlike offer_pool there is no per-candidate
// volume split to make: if this line is Debug the scope is invisible in
// production on exactly the turns that need it.
func TestTheDeployedAnchorPoolSummaryReachesTheProductionLogLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	defaultResolutionTracer(nil, logger).Trace(graphrank.ResolutionTraceEvent{
		RequestID: "request_anchor_pool_stage", Stage: "anchor_pool", AnchorPoolSummary: true,
		DecisionAnchorPoolKindScope:       "team",
		DecisionAnchorPoolKindScopeSource: "confirmed_anchor",
		DecisionMemberKindConfirmed:       "project",
	})
	if buf.Len() == 0 {
		t.Fatal("the anchor_pool summary emitted nothing at the production log level")
	}
	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &rec); err != nil {
		t.Fatalf("captured line is not JSON: %v -- line: %s", err, buf.String())
	}
	if level, _ := rec["level"].(string); level != "INFO" {
		t.Fatalf("level = %q, want INFO", level)
	}
	if got, _ := rec["anchor_pool_kind_scope_source"].(string); got != "confirmed_anchor" {
		t.Errorf("anchor_pool_kind_scope_source = %q, want \"confirmed_anchor\" -- the fallback source must be distinguishable from the receipt in production", got)
	}
}
