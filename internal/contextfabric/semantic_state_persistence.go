package contextfabric

// The one place the engine persists a result, and the Info event that makes
// every persisted semantic-state decision readable from the trace alone.
//
// EVERY Save the engine makes goes through saveResult, so the persistence
// decision -- what snapshot (or which closed absence) a result was saved with,
// how large it was against the cap, and what the store did with it -- is
// emitted once per Save, from one site, with the values. A decision that could
// only be recovered from the database row is an observability defect.
//
// WHAT THE EVENT CANNOT PROVE. It is emitted AFTER Save returns, so a process
// that dies between the commit and the log line leaves a committed row with no
// line. Closing that gap needs a transactional outbox (a second table), which
// this change does not add.

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SemanticStatePersistenceDecision is the CLOSED outcome of one Save, as far as
// the semantic state is concerned.
type SemanticStatePersistenceDecision string

const (
	// SemanticStatePersisted: the store accepted the row -- a first insert,
	// or a replay identical in payload AND semantic state.
	SemanticStatePersisted SemanticStatePersistenceDecision = "persisted"
	// SemanticStateReplayConflictDecision: a row with this id and an
	// identical payload already exists with a DIFFERENT semantic state; the
	// stored row is untouched.
	SemanticStateReplayConflictDecision SemanticStatePersistenceDecision = "replay_conflict"
	// SemanticStateRejectedDecision: the store refused the semantic-state
	// argument itself (neither/both halves, a non-member absence, a snapshot
	// failing validation or a bound). Nothing was persisted.
	SemanticStateRejectedDecision SemanticStatePersistenceDecision = "rejected"
	// SemanticStateSupersededDecision: the result lost a structure
	// supersession claim, so neither it nor its snapshot was persisted.
	SemanticStateSupersededDecision SemanticStatePersistenceDecision = "superseded"
	// SemanticStateSaveFailedDecision: Save failed for any other reason.
	SemanticStateSaveFailedDecision SemanticStatePersistenceDecision = "save_failed"
)

func semanticStatePersistenceDecisions() []SemanticStatePersistenceDecision {
	return []SemanticStatePersistenceDecision{
		SemanticStatePersisted,
		SemanticStateReplayConflictDecision,
		SemanticStateRejectedDecision,
		SemanticStateSupersededDecision,
		SemanticStateSaveFailedDecision,
	}
}

// ValidSemanticStatePersistenceDecision reports membership.
func ValidSemanticStatePersistenceDecision(value SemanticStatePersistenceDecision) bool {
	for _, member := range semanticStatePersistenceDecisions() {
		if member == value {
			return true
		}
	}
	return false
}

// classifySemanticStatePersistence maps Save's error to the closed decision.
func classifySemanticStatePersistence(err error) SemanticStatePersistenceDecision {
	var superseded *ErrStructureOfferSuperseded
	switch {
	case err == nil:
		return SemanticStatePersisted
	case errors.Is(err, ErrSemanticStateReplayConflict):
		return SemanticStateReplayConflictDecision
	case errors.Is(err, ErrSemanticStateRejected):
		return SemanticStateRejectedDecision
	case errors.As(err, &superseded):
		return SemanticStateSupersededDecision
	default:
		return SemanticStateSaveFailedDecision
	}
}

// SemanticStatePersistenceEvent is one Save's semantic-state decision.
type SemanticStatePersistenceEvent struct {
	ResultID       string
	ParentResultID string
	// Site is the exit that saved, named by the same closed vocabulary the
	// final budget assertion uses for exits.
	Site     BudgetAssertStage
	Decision SemanticStatePersistenceDecision
	// Absence is the closed reason there is no snapshot, "" when there is
	// one. EncodedBytes is the canonical encoded size measured at capture,
	// 0 for an absence that never built one.
	Absence      SemanticStateAbsence
	EncodedBytes int
	// State is the snapshot the write carried, nil for an absence.
	State *PersistedSemanticState
}

// saveResult is the engine's only Save. It persists, then emits the decision.
func (e *Engine) saveResult(
	ctx context.Context, principal storage.Principal, site BudgetAssertStage, result InvestigationResult,
	watermark SourceWatermarkSnapshot, epoch RebuildEpoch, timeAxisKey string, graphEpoch int64, parentResultID string,
	capture semanticStateCapture,
) error {
	err := e.results.Save(ctx, principal, result, watermark, epoch, timeAxisKey,
		e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, graphEpoch, parentResultID, capture.Write)
	if e.telemetry != nil {
		e.telemetry.RecordSemanticStatePersistence(ctx, principal, SemanticStatePersistenceEvent{
			ResultID:       result.ResultID,
			ParentResultID: parentResultID,
			Site:           site,
			Decision:       classifySemanticStatePersistence(err),
			Absence:        capture.Write.Absence,
			EncodedBytes:   capture.EncodedBytes,
			State:          capture.Write.State,
		})
	}
	return err
}
