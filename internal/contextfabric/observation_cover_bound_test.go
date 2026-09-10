package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// boundProvider is the minimum a provider must be for the registry to accept it.
type boundProvider struct{ capability FactCapability }

func (p *boundProvider) Capability() FactCapability { return p.capability }
func (p *boundProvider) ReadFacts(context.Context, storage.Principal, FactQuery) (FactProviderResult, error) {
	return FactProviderResult{}, nil
}

func boundCapability(kind FactKind, keyed bool) FactCapability {
	c := FactCapability{
		Kind: kind, Name: string(kind), Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectTeam},
		RequiresEvidence:      true,
		Timeout:               time.Second,
		Dimension:             HealthDimensionDeliveryFlow,
		SubjectRoles:          []FactRole{FactRoleSubject},
		Obligations:           map[SubjectKind][]AnswerObligation{SubjectTeam: nil},
	}
	if keyed {
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectTeam: {"one_shared_observation"}}
	}
	return c
}

// boundKinds is the REAL fact-kind vocabulary, not invented kinds: Validate
// refuses a kind outside it, so a synthetic registry cannot exercise the bound
// at all. There are 22 registered kinds and the bound is 20, so both the
// at-bound control and the one-past case are reachable with real ones.
var boundKinds = []FactKind{
	FactIdentity, FactMembership, FactStatus, FactActualCompletion, FactWork,
	FactBlockers, FactRequiredChildren, FactPullRequests, FactReviews,
	FactContinuousIntegration, FactDeployments, FactIncidents, FactMetrics,
	FactHealth, FactWorkload, FactInvestment, FactReadiness,
	FactOperationalDeficiencies, FactSourceHealth, FactEvidence, FactFlow,
	FactLandscape,
}

func boundProviders(keyed int) []FactProvider {
	if keyed > len(boundKinds) {
		panic("more keyed kinds requested than the vocabulary has")
	}
	out := make([]FactProvider, 0, keyed)
	for i := 0; i < keyed; i++ {
		out = append(out, &boundProvider{capability: boundCapability(boundKinds[i], true)})
	}
	return out
}

func boundCapabilities(keyed int) []FactCapability {
	out := make([]FactCapability, 0, keyed)
	for i := 0; i < keyed; i++ {
		out = append(out, boundCapability(boundKinds[i], true))
	}
	return out
}

// TestARegistryPastTheCoverBoundIsREFUSEDAtConstruction is the pin for a
// SURVIVOR the hosted battery found: widening the bound check so it stops
// refusing changed no test result at all.
//
// The refusal exists because past the bound the counter must fall back, every
// approximation to minimum set cover is an UPPER bound, and an over-count
// reports more distinct sources than exist -- silently, in the direction this
// whole mechanism was built to remove. Before the refusal was written, 21 kinds
// sharing ONE observation returned a cover of 21. So the construction-time
// refusal is the thing standing between the declaration and that over-count,
// and until this test existed nothing proved it fired.
func TestARegistryPastTheCoverBoundIsREFUSEDAtConstruction(t *testing.T) {
	t.Parallel()

	// THE GUARD ITSELF, called directly. The hosted battery mutated the bound
	// so it stopped refusing and NOTHING failed -- so this assertion is the one
	// that was missing, not a duplicate of the constructor case below.
	if err := ValidateObservationCoverBound(boundCapabilities(observationCoverKindGuard)); err != nil {
		t.Fatalf("exactly %d keyed kinds at one subject kind was refused by the guard: %v", observationCoverKindGuard, err)
	}
	if err := ValidateObservationCoverBound(boundCapabilities(observationCoverKindGuard + 1)); err == nil {
		t.Fatalf("%d keyed kinds at one subject kind passed the guard; past the bound the counter falls back to an over-count",
			observationCoverKindGuard+1)
	}

	// AT the bound: accepted. This is the control, and it is what makes the
	// refusal below meaningful rather than a registry that refuses everything.
	atBound, err := NewFactCapabilityRegistry(boundProviders(observationCoverKindGuard), FactRegistryOptions{})
	if err != nil {
		t.Fatalf("a registry with exactly %d keyed kinds at one subject kind was refused: %v", observationCoverKindGuard, err)
	}
	if atBound == nil {
		t.Fatal("registry is nil without an error")
	}

	// ONE PAST the bound: refused, loudly, at construction.
	_, err = NewFactCapabilityRegistry(boundProviders(observationCoverKindGuard+1), FactRegistryOptions{})
	if err == nil {
		t.Fatalf("a registry with %d keyed kinds at one subject kind was ACCEPTED; past the bound the counter falls back to an over-count, which is the defect the bound exists to prevent",
			observationCoverKindGuard+1)
	}
	if !strings.Contains(err.Error(), "observation-keyed") {
		t.Fatalf("refusal does not name the cause: %v", err)
	}

	// AND the bound is PER SUBJECT KIND, not per registry: the same number of
	// keyed kinds spread across two subject kinds must still be accepted.
	// Without this the test above would also pass against a bound that counted
	// the whole registry, which would refuse legitimate growth.
	spread := boundProviders(observationCoverKindGuard)
	for i := range spread {
		if i%2 == 0 {
			continue
		}
		c := spread[i].(*boundProvider).capability
		c.SupportedSubjectKinds = []SubjectKind{SubjectProject}
		c.Obligations = map[SubjectKind][]AnswerObligation{SubjectProject: nil}
		c.ObservationKey = map[SubjectKind][]ObservationKey{SubjectProject: {"one_shared_observation"}}
		spread[i] = &boundProvider{capability: c}
	}
	if _, err := NewFactCapabilityRegistry(spread, FactRegistryOptions{}); err != nil {
		t.Fatalf("keyed kinds spread across two subject kinds were refused: %v -- the bound is per subject kind, not per registry", err)
	}
}
