package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type driverSourceStateCorrectionRecorder interface {
	RecordDriverSourceStateCorrected(ctx context.Context, principal storage.Principal, corrected int)
}

// groundedDriverSourceStates sets each driver's source_state from the state of
// the canonical facts its claims ground to: truncated when any grounded fact is
// truncated, empty otherwise. A draft's own value is never trusted.
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
			if fact, outcome := groundClaim(facts, claim); outcome == claimGrounded && fact.SourceState == SourceTruncated {
				derived = SourceTruncated
				break
			}
		}
		if out[index].SourceState != derived {
			out[index].SourceState = derived
			corrected++
		}
	}
	return out, corrected
}
