package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SHIP-TIME PIN (CHAOS-3778, deferred from the review rounds because this
// branch's base predated CHAOS-3786's reuse path).
//
// Orchestrator ruling on codex round-1 F4: a REUSED answer carries its stored
// degradation marker forward VERBATIM. The marker describes the ANSWER'S
// provenance, and a reused answer IS that earlier answer, so stripping it
// would present a degraded-provenance answer as clean -- measurement failing
// toward "fine".
//
// The ordering that makes this hold: Engine.tryReuse returns before
// ResolveSubjects runs, so a reuse hit computes no marker of its own and
// cannot overwrite the stored one.
func TestShip_ReuseCarriesStoredDegradationMarkerVerbatim(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	// The stored answer was produced under degraded retrieval.
	candidate.SubjectResolution.RetrievalDegraded = true
	candidate.Limitations = []string{retrievalDegradedLimitation}
	candidate.Coverage.Partial = true

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("this test must exercise the REUSE path; got a fresh result")
	}
	if !result.SubjectResolution.RetrievalDegraded {
		t.Fatal("a reused answer must carry its stored degradation marker, not have it stripped")
	}
	if !result.Coverage.Partial {
		t.Fatal("a reused degraded answer must stay partial")
	}
	if !hasRetrievalDegradedLimitation(result.Limitations) {
		t.Fatalf("a reused answer must carry its stored limitation, got %#v", result.Limitations)
	}
	// Reused=true plus the marker is what makes the pair complete information:
	// "this is a reused answer, produced under degraded retrieval".
	if len(result.Limitations) != 1 {
		t.Fatalf("the stored limitation must be carried verbatim, not augmented: %#v", result.Limitations)
	}
}

// RIDER 1 at ship time: a stored answer written BEFORE the provenance
// rewording carries the LEGACY spelling. It must survive a reuse hit
// untouched -- not rewritten to the current wording, not duplicated, not
// treated as unrecognized. An InvestigationResult is immutable and reuse keys
// on its stored bytes.
func TestShip_ReuseCarriesLegacySpellingLimitationUntouched(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()
	candidate.SubjectResolution.RetrievalDegraded = true
	candidate.Limitations = []string{retrievalDegradedLimitationLegacy}
	candidate.Coverage.Partial = true

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("this test must exercise the REUSE path; got a fresh result")
	}
	if len(result.Limitations) != 1 {
		t.Fatalf("a stored legacy limitation must be carried alone, not augmented: %#v", result.Limitations)
	}
	if result.Limitations[0] != retrievalDegradedLimitationLegacy {
		t.Fatalf("a stored legacy limitation must survive VERBATIM, got %q", result.Limitations[0])
	}
	// And it must still be recognized as the degradation limitation, so
	// nothing downstream treats a pre-rewording answer as unmarked.
	if !hasRetrievalDegradedLimitation(result.Limitations) {
		t.Fatal("the legacy spelling must still be recognized on a reused answer")
	}
}

// The negative direction, closing the pair: a HEALTHY stored answer must not
// acquire a degradation marker from a reuse hit.
func TestShip_ReuseDoesNotInventADegradationMarker(t *testing.T) {
	t.Parallel()

	project, candidate := reusableCandidate()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			return candidate, true, nil
		}),
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.SubjectResolution.RetrievalDegraded {
		t.Fatal("a healthy stored answer must not acquire a degradation marker on reuse")
	}
	if hasRetrievalDegradedLimitation(result.Limitations) {
		t.Fatalf("a healthy reused answer must carry no degradation limitation, got %#v", result.Limitations)
	}
}
