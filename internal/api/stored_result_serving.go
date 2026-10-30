package api

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// storedResultServing is the one decision whether a stored result is served
// to a caller (CHAOS-11948, design §6 T4): the organization-scoped read, the
// stored-result gate on the reader's surface, then the read's serving rules
// (the work-item tuple, the flood rule). The result-by-id route, the replay
// of a done submission and the thread read all take it, so a thread names a
// result id exactly when the read by id would serve the result.
type storedResultServing struct {
	stored        contextfabric.StoredInvestigationResult
	authorization contextfabric.StoredResultAuthorization
	// gated is false when the read failed before the gate decided.
	gated bool
	tuple contextfabric.WorkItemTupleByIDDecision
	// result is the serving copy (the tuple rule may transform it).
	result contextfabric.InvestigationResult
	// outcome is the closed outcome; err is what the read by id answers
	// for every outcome but served.
	outcome storedResultServingOutcome
	err     error
}

// storedResultServingOutcome is the closed outcome of the decision.
type storedResultServingOutcome string

const (
	servingServed storedResultServingOutcome = "served"
	// servingNotFound: no row for the caller (unknown, another
	// organization's, or only a closed thread's).
	servingNotFound storedResultServingOutcome = "not_found"
	// servingDenied: the gate refused the reader.
	servingDenied storedResultServingOutcome = "denied"
	// servingTupleWithheld: the stored work-item tuple's census refused it.
	servingTupleWithheld storedResultServingOutcome = "tuple_withheld"
	// servingFloodWithheld: a flooded limitation list cannot show the
	// service disclosures.
	servingFloodWithheld storedResultServingOutcome = "flood_withheld"
	// servingUnavailable: the decision could not be taken.
	servingUnavailable storedResultServingOutcome = "unavailable"
)

// storedResultServingOutcomeVocabulary is the closed set of outcomes.
func storedResultServingOutcomeVocabulary() [6]storedResultServingOutcome {
	return [6]storedResultServingOutcome{servingServed, servingNotFound, servingDenied, servingTupleWithheld, servingFloodWithheld, servingUnavailable}
}

// decideStoredResultServing takes the decision. It writes nothing and logs
// nothing: each surface records it its own way.
func decideStoredResultServing(ctx context.Context, principal storage.Principal, results contextfabric.InvestigationResultStore, gate StoredResultAuthorizer, resultID string, surface contextfabric.StoredResultSurface) storedResultServing {
	stored, err := results.Get(ctx, principal, resultID)
	if err != nil {
		outcome := servingUnavailable
		if errors.Is(err, contextfabric.ErrInvestigationResultNotFound) {
			outcome = servingNotFound
		}
		return storedResultServing{outcome: outcome, err: err}
	}
	// No gate configured is the same failure as an undecidable read, never
	// a pass.
	authorization := contextfabric.StoredResultAuthorization{Surface: surface, Decision: contextfabric.StoredResultUnavailable, Reason: contextfabric.StoredResultReasonAuthorizerMissing}
	if gate != nil {
		authorization = gate.Authorize(ctx, principal, stored, surface)
	}
	serving := storedResultServing{stored: stored, authorization: authorization, gated: true}
	if err := authorization.ServingError(); err != nil {
		serving.err = err
		serving.outcome = servingUnavailable
		if authorization.Decision == contextfabric.StoredResultDenied {
			serving.outcome = servingDenied
		}
		return serving
	}
	serving.result = stored.Result
	serving.tuple = contextfabric.ServeStoredWorkItemTuple(stored.Result, stored.SemanticState, stored.SemanticStateRead, principal)
	if serving.tuple.Err != nil {
		serving.err, serving.outcome = serving.tuple.Err, servingUnavailable
		return serving
	}
	switch serving.tuple.Disposition {
	case contextfabric.WorkItemTupleByIDNotFound:
		serving.err, serving.outcome = contextfabric.ErrInvestigationResultNotFound, servingTupleWithheld
		return serving
	case contextfabric.WorkItemTupleByIDServed, contextfabric.WorkItemTupleByIDStored:
		serving.result = serving.tuple.Result
	case contextfabric.WorkItemTupleByIDNotApplicable:
		if contextfabric.StoredResultFloodWithheld(serving.result) {
			serving.err, serving.outcome = contextfabric.ErrInvestigationResultNotFound, servingFloodWithheld
			return serving
		}
	}
	serving.outcome = servingServed
	return serving
}
