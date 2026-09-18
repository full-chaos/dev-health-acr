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
	// SemanticStatePayloadRejectedDecision refuses a tuple before storage.
	SemanticStatePayloadRejectedDecision SemanticStatePersistenceDecision = "payload_rejected"
	// SemanticStateReplayConflictDecision: a row with this id and an
	// identical payload already exists with a DIFFERENT semantic state; the
	// stored row is untouched.
	SemanticStateReplayConflictDecision SemanticStatePersistenceDecision = "replay_conflict"
	// SemanticStateSupersededDecision: the result lost a structure
	// supersession claim, so neither it nor its snapshot was persisted.
	SemanticStateSupersededDecision SemanticStatePersistenceDecision = "superseded"
	// SemanticStateSaveFailedDecision: Save failed for any other reason.
	SemanticStateSaveFailedDecision SemanticStatePersistenceDecision = "save_failed"
)

func semanticStatePersistenceDecisions() []SemanticStatePersistenceDecision {
	return []SemanticStatePersistenceDecision{
		SemanticStatePersisted,
		SemanticStatePayloadRejectedDecision,
		SemanticStateReplayConflictDecision,
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

var errWorkItemTuplePayloadRejected = errors.New("work item tuple payload rejected")

// classifySemanticStatePersistence maps Save's error to the closed decision.
func classifySemanticStatePersistence(err error) SemanticStatePersistenceDecision {
	var superseded *ErrStructureOfferSuperseded
	switch {
	case err == nil:
		return SemanticStatePersisted
	case errors.Is(err, errWorkItemTuplePayloadRejected):
		return SemanticStatePayloadRejectedDecision
	case errors.Is(err, ErrSemanticStateReplayConflict):
		return SemanticStateReplayConflictDecision
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
	// Bound is the bound a snapshot_oversized capture breached, "" otherwise.
	Bound SemanticStateBound
	// State is the snapshot the write carried, nil for an absence.
	State *PersistedSemanticState
}

// saveResult is the engine's only Save. It persists, then emits the decision.
func (e *Engine) saveResult(
	ctx context.Context, principal storage.Principal, site BudgetAssertStage, result InvestigationResult,
	watermark SourceWatermarkSnapshot, epoch RebuildEpoch, timeAxisKey string, graphEpoch int64, parentResultID string,
	capture semanticStateCapture,
) error {
	capture, anchorEvent := capture.attachAnchorBinding(site, result)
	if anchorEvent == nil && !e.anchorBindingShadowDisabled {
		unrecorded := unrecordedAnchorBindingEvent(site, result)
		anchorEvent = &unrecorded
	}
	var err error
	if workItemTupleSemanticState(capture.Write.State) && !workItemTuplePreMembershipTerminal(site, result, capture.Write.State) {
		if payloadErr := ValidateWorkItemTuplePayload(result, principal); payloadErr != nil {
			err = errors.Join(errWorkItemTuplePayloadRejected, payloadErr)
		}
	}
	if err == nil {
		err = e.results.Save(ctx, principal, result, watermark, epoch, timeAxisKey,
			e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, graphEpoch, parentResultID, capture.Write)
	}
	if e.telemetry != nil {
		e.telemetry.RecordSemanticStatePersistence(ctx, principal, SemanticStatePersistenceEvent{
			ResultID:       result.ResultID,
			ParentResultID: parentResultID,
			Site:           site,
			Decision:       classifySemanticStatePersistence(err),
			Absence:        capture.Write.Absence,
			EncodedBytes:   capture.EncodedBytes,
			Bound:          capture.Bound,
			State:          capture.Write.State,
		})
	}
	if anchorEvent != nil {
		if anchorEvent.Persisted == "" {
			anchorEvent.Persisted = AnchorBindingPersistence(classifySemanticStatePersistence(err))
		}
		e.recordAnchorBindingTransition(ctx, principal, *anchorEvent)
	}
	return err
}

// SemanticStatePersistenceLineVocabulary is the persistence line's closed
// vocabulary for one key, read from the producers rather than retyped. The
// eventspec declaration and the emitter's own membership guards read this one
// list, so a member cannot exist in one and not the other.
//
// A key with no finite vocabulary (the ids) is absent here, and the
// specification declares it open.
func SemanticStatePersistenceLineVocabulary(key string) []string {
	tokens := func(values []string) []string { return append([]string{}, values...) }
	switch key {
	case "site":
		out := []string{}
		stages := BudgetAssertStageVocabulary()
		for _, stage := range stages[:] {
			out = append(out, string(stage))
		}
		return append(out, continuationTelemetryUnrecognised)
	case "decision":
		out := []string{}
		for _, decision := range semanticStatePersistenceDecisions() {
			out = append(out, string(decision))
		}
		return append(out, continuationTelemetryUnrecognised)
	case "absence":
		// "none" is the explicit token beside a snapshot: an absence key that
		// could be empty would make "no absence" and "not written" the same
		// reading of the line.
		out := []string{"none"}
		for _, absence := range semanticStateAbsences() {
			out = append(out, string(absence))
		}
		return append(out, continuationTelemetryUnrecognised)
	case "oversized_bound":
		out := []string{"none"}
		for _, bound := range semanticStateBounds() {
			out = append(out, string(bound))
		}
		return append(out, continuationTelemetryUnrecognised)
	default:
		return tokens(nil)
	}
}
