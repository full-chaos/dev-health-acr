package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// rememberedWindowAxisDecision is the axis decision for a turn whose window is
// the one the confirmed-need ledger remembered from its parent: the pre-entry
// state (the fresh axis, the parent it came from and the axis that parent
// recorded), the decision, and the decided axis -- all on one Info line.
type rememberedWindowAxisDecision struct {
	SourceResultID  string
	CarrierRead     ContinuationCarrierRead
	InterpretedAxis contractsv1.ContextFabricTemporalAxis
	CarriedAxis     contractsv1.ContextFabricTemporalAxis
	DecidedAxis     contractsv1.ContextFabricTemporalAxis
	Outcome         ContinuationAxisOutcome
}

// decideRememberedWindowAxis applies the confirmed-window axis rule to a
// remembered window. The ledger admitted it only for a parent at this turn's
// graph epoch asked the identical question under the identical request
// identity (resolveConfirmedNeedLedger), so an APPLIED remembered window is a
// window confirmed for this question exactly as a redeemed receipt is; the
// carried axis is read from that parent (normally the per-request memo entry
// admission loaded), and a parent that cannot be read decides nothing.
func (e *Engine) decideRememberedWindowAxis(ctx context.Context, principal storage.Principal, app ledgerWindowApplication, fresh TimeContext, freshAnswerable bool, requestTime TimeContext, windowCommitted bool) (TimeContext, rememberedWindowAxisDecision) {
	decision := rememberedWindowAxisDecision{SourceResultID: app.SourceResultID, CarrierRead: ContinuationCarrierNotRead, InterpretedAxis: fresh.Axis}
	if app.Applied() && e.results != nil {
		stored, err := carryLoadResult(ctx, e.results, principal, app.SourceResultID)
		if err != nil {
			decision.CarrierRead = ContinuationCarrierReadFailed
		} else {
			decision.CarrierRead = ContinuationCarrierReadOK
			decision.CarriedAxis = stored.Result.Interpretation.TimeContext.Axis
		}
	}
	// THE CARRIED AXIS IS THE PROOF. The ledger's own admission already
	// established the identical question; what this site adds is the axis the
	// parent recorded, and it is set only by a successful read of an applied
	// remembered window. Every other path leaves it empty, which the rule
	// refuses exactly as it refuses an unconfirmed window.
	decided, outcome := decideConfirmedWindowAxis(true, decision.CarriedAxis, fresh, freshAnswerable, requestTime, windowCommitted)
	decision.DecidedAxis, decision.Outcome = decided.Axis, outcome
	return decided, decision
}

func (e *Engine) recordRememberedWindowAxis(ctx context.Context, principal storage.Principal, decision rememberedWindowAxisDecision) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordRememberedWindowAxis(ctx, principal, decision)
}

// RememberedWindowAxisLineVocabulary returns every value one CLOSED field of
// the remembered-window axis line may carry, read from the production
// vocabularies, so the event specification declares from production. nil for
// a key that is not a closed field of the line.
func RememberedWindowAxisLineVocabulary(key string) []string {
	switch key {
	case "carrier_read":
		return tokenStrings(continuationCarrierReadVocabulary())
	case "interpreted_axis", "carried_axis", "decided_axis":
		axes := contractsv1.ContextFabricTemporalAxisVocabulary()
		return append(tokenStrings(axes[:]), "")
	case "outcome":
		return tokenStrings(continuationAxisOutcomes())
	default:
		return nil
	}
}
