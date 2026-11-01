package contextfabric

import (
	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// projectScopeWithheld reports whether this caller is withheld every fact
// read (CHAOS-12330). No fact provider could be proven to serve a project-only
// caller only what its grant covers (CHAOS-12299 F5/D3), so the registry
// refuses them all, once, before any provider or scope expander runs. A
// refusal is a typed degrading detail, never an empty read: the two read the
// same to a caller only when the second is a false statement about the source.
//
// The design is docs/design/context-fabric-project-only-scope.md.
func projectScopeWithheld(principal storage.Principal) bool {
	return auth.ClassifyGrant(principal) == auth.GrantProjectOnly
}

// withholdProjectScopeReads marks every read a project-only caller planned as
// withheld. A pruned read stays pruned: no subject of the kind fits the
// capability for any caller, so nothing is being refused. A scope gap is the
// refusal itself here, and the answer's own disclosure of the gap would state
// a second, weaker cause, so the gap is dropped from the read scope.
func withholdProjectScopeReads(plans []factPlanEntry, scope *FactReadScope) {
	for i := range plans {
		if plans[i].Pruned {
			continue
		}
		plans[i].Withheld = true
		if scope != nil {
			delete(scope.Gaps, plans[i].Kind)
		}
	}
}

// appendProjectScopeWithheld files the kind's refusal: the one place the code,
// the state and the reason are put together. The reason is the label the
// contract composes from the kind, so the raw text and the label cannot
// disagree, and no model wrote either.
func appendProjectScopeWithheld(bundle *CanonicalFactBundle, kind FactKind) {
	appendFactCoverage(bundle, kind, SourceUnauthorized, nil, "", contractsv1.ContextFabricProjectScopeWithheldFactLabel(kind),
		coverageDetailSpec{Code: contractsv1.ContextFabricCoverageDetailProjectScopeWithheld})
}
