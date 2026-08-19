package graphrank

import (
	"context"

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

// filterCandidatesByConfirmedKind (CHAOS-3900 P1.D) narrows
// candidatesBySubject to ONLY the confirmed kind, when one is present --
// design brief §2.1: "the confirmed kind becomes the census scope (drops
// non-confirmed kinds from the hypothesis set)."
//
// A nil confirmed returns candidatesBySubject UNCHANGED -- the overwhelming
// common case (no kindr_ receipt confirmed), and what keeps an ordinary
// request's pool composition provably byte-identical to the pre-P1.D code
// path (TestFilterCandidatesByConfirmedKind_NilIsNoOp pins this).
//
// Deliberately typed on *contextfabric.ConfirmedExpectedKind, not a bare
// contextfabric.SubjectKind -- see that type's own doc comment
// (internal/contextfabric/ports.go) for why this is the §2.0
// kind-insensitivity rule's own enforcement mechanism, not merely a
// convenience wrapper: only canonicalizeStructure's receipt-confirmation
// path can construct one.
func filterCandidatesByConfirmedKind(candidatesBySubject map[string]contextfabric.SubjectCandidate, confirmed *contextfabric.ConfirmedExpectedKind) map[string]contextfabric.SubjectCandidate {
	if confirmed == nil {
		return candidatesBySubject
	}
	filtered := make(map[string]contextfabric.SubjectCandidate, len(candidatesBySubject))
	for key, candidate := range candidatesBySubject {
		if candidate.Subject.Kind == confirmed.Kind {
			filtered[key] = candidate
		}
	}
	return filtered
}

// kindInsensitivityOutcome is the closed vocabulary CHAOS-3900 P1.D's
// insensitivity proof reports (design brief §2.0/§4's kind_sensitive_outcome
// degradation reason, split into its three concrete verdicts here).
type kindInsensitivityOutcome string

const (
	// kindInsensitivityCommitSound: the all-kinds census found EXACTLY
	// one satisfier across every pre-narrowing hypothesis kind -- a
	// decisive commit is sound regardless of which kind an inferred
	// narrowing picked.
	kindInsensitivityCommitSound kindInsensitivityOutcome = "commit_sound"
	// kindInsensitivityNoMatchSound: the all-kinds census found ZERO
	// satisfiers -- a literal no_match is sound regardless of narrowing.
	kindInsensitivityNoMatchSound kindInsensitivityOutcome = "no_match_sound"
	// kindInsensitivitySensitive: any other combination (>1 satisfier,
	// a census error, or a pre-narrowing kind outside the closed
	// registry -- the registry-miss poison rule) -- an inferred
	// narrowing's decisive outcome is NOT provably sound; the design
	// brief's own rule demotes this to clarify.
	kindInsensitivitySensitive kindInsensitivityOutcome = "kind_sensitive_outcome"
)

// kindInsensitivityProof is design brief §2.0's own all-kinds census
// proof, implementing both its stated implementation pins: (a) runs over
// preNarrowingKinds -- the PRE-narrowing hypothesis kind-set, which the
// caller must capture BEFORE any narrowing was applied, never the
// already-narrowed set; (b) a pre-narrowing kind outside the closed
// census registry poisons the round (reuses splitCensusKinds' own
// registry-miss split -- the identical primitive
// chaos3899_evidence_round.go already established for this exact shape).
//
// STANDALONE, PURE (besides the injected CensusFunc), and UNIT-TESTED --
// but DELIBERATELY UNWIRED into any decisive-path gate today (P1.D
// scoping, confirmed by repo-wide grep: no inferred-tier or
// explicit-unattributed kind-narrowing mechanism exists anywhere in this
// codebase yet, so there is no live branch for such a gate to guard).
// Wiring this into an actual decisive-path check is a HARD PRECONDITION
// of introducing any such kind source (tracked on CHAOS-3927 and the
// P3/P5 commissioning checklists) -- see ConfirmedExpectedKind's own doc
// comment (internal/contextfabric/ports.go) for the type-level half of
// this same guard.
func kindInsensitivityProof(ctx context.Context, orgID string, preNarrowingKinds []CensusKind, handleValue string, handleBound bool, anchorKind contextfabric.SubjectKind, anchorCanonicalID string, anchorBound bool, census CensusFunc) kindInsensitivityOutcome {
	censused, nonCensusedSurvivor := splitCensusKinds(preNarrowingKinds)
	if nonCensusedSurvivor || census == nil || len(censused) == 0 {
		return kindInsensitivitySensitive
	}
	total := 0
	for _, kind := range censused {
		outcome, err := census(ctx, orgID, kind, handleValue, handleBound, anchorKind, anchorCanonicalID, anchorBound)
		if err != nil {
			return kindInsensitivitySensitive
		}
		total += outcome.Count
	}
	switch total {
	case 0:
		return kindInsensitivityNoMatchSound
	case 1:
		return kindInsensitivityCommitSound
	default:
		return kindInsensitivitySensitive
	}
}
