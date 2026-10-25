package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// StoredResultFloodWithheld reports a stored result whose limitation list is
// at the cap with no model-authored entry. The engine never composes one, and
// drafts lose such entries when vetted, so only a row stored before that could
// hold one: no disclosure could be appended to it, so a serving route that
// cannot show the service disclosures withholds it instead of serving it.
// It is the one flood rule every route that serves a stored result to a
// caller calls after the stored-result authorization decision, for every kind
// of result. It is a content rule, not an authorization decision.
func StoredResultFloodWithheld(result InvestigationResult) bool {
	limitations := result.Limitations
	return len(limitations) >= contractsv1.ContextFabricLimitationsMaxCount && lastModelAuthoredLimitation(limitations) < 0
}
