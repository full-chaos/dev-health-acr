package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-3900 P1.C (pivot-intent design brief §2.1). ResolveSubjects
// (resolve.go) calls kindOfferMaterial with the SAME candidate pool it
// already assembled, right before it returns -- this file owns turning
// that pool into the expected_kind disclosure StructureOfferMaterial
// carries back to the engine.
//
// SCOPE NOTE (P1.C increment 1, flagged rather than silently absent):
// AnchorOptions/HandleOptions are NOT built here yet. Their own candidate
// material (identity-universe unique-claimant candidates, handle-grammar
// bindings against question text) needs the SAME identity/alias data
// runShadowEvidenceRoundForResolution (resolve.go) already threads --
// which only exists on the gated shadow-evidence-round path
// (deps.CensusFunc != nil && len(resolution.Committed) == 0 &&
// searchTruncated), unlike expected_kind offers, which need only the
// unconditionally-available candidate pool. Building anchor/handle offers
// correctly needs that same gated data threaded out to this file too --
// a follow-up increment, not a design change.

// structureOfferKinds is the closed set of subject kinds an expected_kind
// offer may name (design brief §1.1's expected_kind row: "the census-kind
// registry... + the identity-scoped kinds"). A superset of
// censusKindRegistry -- repository/project/team are identity-scoped, not
// census kinds (they never run a census; IsCensusKindRegistered would
// wrongly exclude them), but the SAME expected_kind member still covers
// disambiguating "is this about a repository or a project" the way it
// covers "is this a PR or a work item".
var structureOfferKinds = map[contractsv1.ContextFabricSubjectKind]bool{
	contractsv1.ContextFabricSubjectPullRequest:       true,
	contractsv1.ContextFabricSubjectWorkItem:          true,
	contractsv1.ContextFabricSubjectCIRun:             true,
	contractsv1.ContextFabricSubjectPullRequestReview: true,
	contractsv1.ContextFabricSubjectRepository:        true,
	contractsv1.ContextFabricSubjectProject:           true,
	contractsv1.ContextFabricSubjectTeam:              true,
}

// kindOfferMaterial builds the expected_kind disclosure from the pool of
// SubjectCandidate a resolution already assembled (design brief §1.2
// reading 1: "kind disambiguation is the cheapest, highest-leverage
// elicitation... a closed enum a human can pick in one tap"). Offered
// ONLY when the pool spans MORE THAN ONE distinct offerable kind -- a
// single-kind (or empty) pool has nothing to disambiguate on this axis,
// so offering it would disclose a choice the question does not actually
// present.
func kindOfferMaterial(candidates []contextfabric.SubjectCandidate) contextfabric.StructureOfferMaterial {
	seen := make(map[contractsv1.ContextFabricSubjectKind]bool, len(candidates))
	var distinct []contractsv1.ContextFabricSubjectKind
	for _, candidate := range candidates {
		kind := candidate.Subject.Kind
		if seen[kind] || !structureOfferKinds[kind] {
			continue
		}
		seen[kind] = true
		distinct = append(distinct, kind)
	}
	if len(distinct) < 2 {
		return contextfabric.StructureOfferMaterial{}
	}
	options := make([]contractsv1.ContextFabricKindOption, 0, len(distinct))
	for _, kind := range distinct {
		options = append(options, contractsv1.ContextFabricKindOption{
			Kind:        kind,
			Label:       kindOfferLabel(kind),
			OfferSource: contractsv1.ContextFabricStructureOfferEngine,
		})
	}
	return contextfabric.StructureOfferMaterial{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: options,
	}
}

// kindOfferLabel renders a server-owned, closed-vocabulary label for one
// kind offer -- mirrors WindowOption's own server-rendered label
// discipline (CHAOS-3900 W1): never model- or source-derived prose, a
// fixed sentence per closed-enum member.
func kindOfferLabel(kind contractsv1.ContextFabricSubjectKind) string {
	switch kind {
	case contractsv1.ContextFabricSubjectPullRequest:
		return "a pull request"
	case contractsv1.ContextFabricSubjectPullRequestReview:
		return "a pull request review"
	case contractsv1.ContextFabricSubjectCIRun:
		return "a CI pipeline run"
	case contractsv1.ContextFabricSubjectWorkItem:
		return "a work item"
	case contractsv1.ContextFabricSubjectRepository:
		return "a repository"
	case contractsv1.ContextFabricSubjectProject:
		return "a project"
	case contractsv1.ContextFabricSubjectTeam:
		return "a team"
	default:
		// Unreachable given structureOfferKinds' own closed membership --
		// kindOfferMaterial never calls this with a kind outside that map.
		return string(kind)
	}
}
