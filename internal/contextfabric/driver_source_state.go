package contextfabric

import (
	"context"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type driverSourceStateCorrectionRecorder interface {
	RecordDriverSourceStateCorrected(ctx context.Context, principal storage.Principal, corrected int)
}

// groundedDriverSourceStates sets each driver's source_state from the state of
// the canonical facts its claims ground to: truncated when any grounded fact is
// truncated, else stale when any is stale, empty otherwise. A draft's own value
// is never trusted.
func groundedDriverSourceStates(drivers []DriverJudgment, claims []ClaimedFact, facts []CanonicalFact) ([]DriverJudgment, int) {
	byID := make(map[string]ClaimedFact, len(claims))
	for _, claim := range claims {
		byID[claim.ClaimID] = claim
	}
	out := cloneSlice(drivers)
	corrected := 0
	for index := range out {
		var derived SourceState
		for _, id := range out[index].ClaimedFactIDs {
			claim, ok := byID[id]
			if !ok {
				continue
			}
			derived = worseDriverSourceState(derived, claimSourceState(facts, claim))
		}
		if out[index].SourceState != derived {
			out[index].SourceState = derived
			corrected++
		}
	}
	return out, corrected
}

func worseDriverSourceState(a, b SourceState) SourceState {
	rank := func(state SourceState) int {
		switch state {
		case SourceTruncated:
			return 2
		case SourceStale:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// claimSourceState is the non-available state a grounded claim rests on:
// truncated or stale, or empty. A derived claim takes the worst state over its
// input facts.
func claimSourceState(facts []CanonicalFact, claim ClaimedFact) SourceState {
	fact, outcome := groundClaim(facts, claim)
	if outcome != claimGrounded {
		return ""
	}
	if len(claim.DerivedFrom) == 0 {
		return worseDriverSourceState("", fact.SourceState)
	}
	var derived SourceState
	for _, input := range claim.DerivedFrom {
		for _, candidate := range facts {
			if candidate.ID != "" && candidate.ID == input.FactID {
				derived = worseDriverSourceState(derived, candidate.SourceState)
			}
		}
	}
	return derived
}

func driverSourceStateQualifier(state SourceState) string {
	switch state {
	case SourceTruncated:
		return " (partial: the source read was truncated)"
	case SourceStale:
		return " (stale: the source read is out of date)"
	}
	return ""
}

func qualifiedDriverCategories(drivers []DriverJudgment) []string {
	var qualified []string
	for _, driver := range drivers {
		if qualifier := driverSourceStateQualifier(driver.SourceState); qualifier != "" {
			qualified = append(qualified, driver.Category+qualifier)
		}
	}
	sort.Strings(qualified)
	return qualified
}
