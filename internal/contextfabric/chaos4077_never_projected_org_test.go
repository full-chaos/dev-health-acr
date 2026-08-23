package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// neverProjectedGraphReader is a GraphReader stub that returns
// ErrGraphNotProjected-wrapped errors from ResolveSubjects, exactly the
// shape falkorgraph.Adapter.ResolveSubjects now returns for an org whose
// graph key has never been created (CHAOS-4077). DiscoverContext fails the
// test outright if called: the whole point of the engine.go short-circuit
// is that DiscoverContext must NEVER be reached once ResolveSubjects has
// already reported the graph does not exist -- it would query the
// identical nonexistent key and fail the same way, one call later.
type neverProjectedGraphReader struct {
	t *testing.T
}

func (g neverProjectedGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "never-projected-key", Epoch: 0}, nil
}

func (g neverProjectedGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	// %w, not a string-concatenated message: this must satisfy
	// errors.Is(err, ErrGraphNotProjected) exactly the way
	// falkorgraph.graphNotProjectedError's own real wrap does -- engine.go
	// checks the error CHAIN, not the message text.
	return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, fmt.Errorf("query context graph: %w", ErrGraphNotProjected)
}

func (g neverProjectedGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.t.Fatal("DiscoverContext must never be called once ResolveSubjects has already reported ErrGraphNotProjected -- it would query the identical nonexistent graph key and fail the same way")
	return GraphContext{}, nil
}

// TestEngineInvestigate_NeverProjectedOrgDegradesToCleanTerminal is the
// end-to-end proof CHAOS-4077 requires: a GraphReader reporting
// ErrGraphNotProjected from ResolveSubjects must produce the SAME clean,
// non-error terminal a legitimately-empty resolution already produces --
// never a propagated 5xx -- and must never call DiscoverContext at all.
func TestEngineInvestigate_NeverProjectedOrgDegradesToCleanTerminal(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := mustEngineForTerminalReasonTest(t, neverProjectedGraphReader{t: t}, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a clean terminal result, not a propagated error", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(result.SubjectResolution.Candidates) != 0 || len(result.SubjectResolution.Committed) != 0 {
		t.Fatalf("result.SubjectResolution = %+v, want empty candidates and committed", result.SubjectResolution)
	}
	if want := []string{"graph_not_projected"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("subjectlessTerminalReasons = %#v, want %#v -- a never-projected org must be distinguishable from an ordinary empty pool", telemetry.subjectlessTerminalReasons, want)
	}
	// codex xhigh review round 2 (confirmed real, LOW): the caller-facing
	// limitation must not claim a search ran "in this organization's
	// graph" -- it didn't, there was no graph to search.
	found := false
	for _, limitation := range result.Limitations {
		if limitation == noMatchLimitationGraphNotProjected {
			found = true
		}
		if limitation == noMatchLimitationUnproven {
			t.Fatalf("result.Limitations = %#v contains the unproven-search wording, which falsely claims a search ran", result.Limitations)
		}
	}
	if !found {
		t.Fatalf("result.Limitations = %#v, want it to contain the graph-not-projected wording", result.Limitations)
	}
}

// TestEngineInvestigate_GraphNotProjectedIsNotMistakenForOtherResolveErrors
// is the mutation-style negative control: an ORDINARY resolution error
// (not ErrGraphNotProjected) must still propagate as a stageError, never
// silently swallowed into the clean terminal path -- proving the
// errors.Is(err, ErrGraphNotProjected) branch in engine.go is load-bearing,
// not a change that accidentally caught every ResolveSubjects error.
func TestEngineInvestigate_GraphNotProjectedIsNotMistakenForOtherResolveErrors(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := mustEngineForTerminalReasonTest(t, ordinaryFailingGraphReader{}, telemetry)

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err == nil {
		t.Fatal("Investigate() error = nil, want a propagated error for a genuine (non-ErrGraphNotProjected) resolution failure")
	}
	if errors.Is(err, ErrGraphNotProjected) {
		t.Fatalf("Investigate() error = %v, must NOT satisfy errors.Is(_, ErrGraphNotProjected) -- this was an ordinary failure", err)
	}
	if len(telemetry.subjectlessTerminalReasons) != 0 {
		t.Fatalf("subjectlessTerminalReasons = %#v, want none -- an ordinary error must never reach the clean-terminal telemetry path", telemetry.subjectlessTerminalReasons)
	}
}

type ordinaryFailingGraphReader struct{}

func (ordinaryFailingGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "ordinary-failing-key", Epoch: 0}, nil
}

func (ordinaryFailingGraphReader) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, errors.New("resolve subjects: a genuine dependency failure, unrelated to graph existence")
}

func (ordinaryFailingGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return GraphContext{}, nil
}

// TestEngineInvestigate_ProjectedOrgWithZeroCandidatesStillCallsDiscoverContext
// is the positive-side complement to the two tests above (team-lead
// ruling, CHAOS-4077): a PROJECTED org whose resolution genuinely,
// ordinarily comes up empty (no error at all -- ResolveSubjects succeeded)
// must still (a) call DiscoverContext, proving the engine.go short-circuit
// is scoped to the ErrGraphNotProjected SENTINEL, never to "the resolution
// happened to be empty" in general, and (b) report "empty_pool", never
// "graph_not_projected" -- the two are genuinely different situations and
// must never be conflated in either direction.
func TestEngineInvestigate_ProjectedOrgWithZeroCandidatesStillCallsDiscoverContext(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	reader := &discoverContextCallCounter{}
	engine := mustEngineForTerminalReasonTest(t, reader, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if reader.discoverContextCalls != 1 {
		t.Fatalf("DiscoverContext was called %d times, want exactly 1 -- an ordinary empty resolution (no ErrGraphNotProjected) must NOT be short-circuited", reader.discoverContextCalls)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if want := []string{"empty_pool"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("subjectlessTerminalReasons = %#v, want %#v -- an ordinary empty pool on a projected graph must never read as graph_not_projected", telemetry.subjectlessTerminalReasons, want)
	}
}

// discoverContextCallCounter is a GraphReader stub for a PROJECTED org: its
// ResolveSubjects succeeds (no error at all) with a genuinely empty
// resolution, and it counts how many times DiscoverContext is actually
// invoked -- the positive-control counterpart to neverProjectedGraphReader's
// t.Fatal-if-called negative control above.
type discoverContextCallCounter struct {
	discoverContextCalls int
}

func (g *discoverContextCallCounter) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "projected-empty-key", Epoch: 0}, nil
}

func (g *discoverContextCallCounter) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, StructureOfferMaterial{}, nil, nil, nil
}

func (g *discoverContextCallCounter) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	g.discoverContextCalls++
	return GraphContext{Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}}, nil
}
