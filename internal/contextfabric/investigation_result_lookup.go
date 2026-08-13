package contextfabric

import "errors"

// ErrInvestigationResultNotFound is the backend-neutral classification for
// "this principal's organization has no such investigation result"
// (CHAOS-3746).
//
// Before this existed, each InvestigationResultStore adapter carried its own
// unexported-by-convention ErrNotFound (pginvestigation.ErrNotFound,
// memoryinvestigation.ErrNotFound). That is fine for a caller that knows
// which adapter it holds, but the result-retrieval route does not: it holds
// the InvestigationResultStore interface, and matching an adapter's own
// sentinel there would either couple the HTTP boundary to a specific
// backend or, worse, silently fall through to a 500 the day composition
// selects a different adapter. Both adapters now wrap this sentinel, so the
// route classifies not-found through the port rather than through a vendor.
//
// This deliberately carries no distinction between "no such result_id" and
// "that result belongs to another organization". Those two cases MUST be
// indistinguishable to a caller: a distinguishable one turns result_id into
// a cross-tenant existence oracle, letting an attacker enumerate which
// identifiers are real in organizations they cannot read. Both map to the
// same not-found answer -- see InvestigationResultStore's org-scoping
// precondition.
var ErrInvestigationResultNotFound = errors.New("context fabric: investigation result not found")
