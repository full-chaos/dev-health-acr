package graphrank

import (
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// workItemHandleRound is what a round found for the one work-item handle its
// question binds (CHAOS-12456).
type workItemHandleRound struct {
	value    string
	censused bool
	complete bool
	count    int
}

func workItemHandleRoundOf(question string, attestation Attestation) workItemHandleRound {
	bound := BindHandles(question)
	if len(bound) != 1 || bound[0].Kind != contextfabric.SubjectWorkItem {
		return workItemHandleRound{}
	}
	round := workItemHandleRound{value: bound[0].Value}
	for _, kind := range attestation.Kinds {
		if kind.Kind == contextfabric.SubjectWorkItem && kind.HandleApplied {
			round.censused, round.complete, round.count = true, kind.Complete && !kind.ClosureMismatch, kind.Count
		}
	}
	return round
}

// workItemLinkLimitation is the sentence a turn that resolved no work item owes
// a restricted caller, or "". Everything it reads sits inside the grant, so a
// subject outside the grant cannot change it. A requested repository narrows the
// census, so a count of zero there proves nothing about the grant, and the
// narrowing's own sentence speaks for it.
func workItemLinkLimitation(principal storage.Principal, request contextfabric.InvestigationRequest, deps ResolveDeps, round workItemHandleRound, resolution contextfabric.SubjectResolution) string {
	if !grantScopedCensus(principal) || committedWorkItem(resolution) {
		return ""
	}
	switch {
	case deps.WorkItemLinksUnread != nil && deps.WorkItemLinksUnread():
		return contractsv1.ContextFabricWorkItemLinksUnreadLimitation
	case !round.censused:
		return ""
	case !round.complete:
		return contractsv1.ContextFabricWorkItemLinksUnreadLimitation
	case round.count == 0 && len(request.RequestedScope.RepositorySlugs) == 0:
		return contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation
	case round.count == 1:
		// The census found the link and the node gate did not: projection lag.
		return contractsv1.ContextFabricWorkItemLinksUnreadLimitation
	}
	return ""
}

func committedWorkItem(resolution contextfabric.SubjectResolution) bool {
	for _, subject := range resolution.Committed {
		if subject.Kind == contextfabric.SubjectWorkItem {
			return true
		}
	}
	return false
}

// redeemableHandleOffer drops the handle options a caller could never redeem, so
// no option dead-ends. A project-only caller has no census to verify any handle.
// A restricted caller's work-item option stands on evidence from inside its
// grant: the round's count, or a candidate the node gate admitted.
func redeemableHandleOffer(principal storage.Principal, offer contextfabric.StructureOfferMaterial, round workItemHandleRound, pool []contextfabric.SubjectCandidate) contextfabric.StructureOfferMaterial {
	switch auth.ClassifyGrant(principal) {
	case auth.GrantProjectOnly:
		offer.HandleOptions = nil
		return offer
	case auth.GrantRestricted:
	default:
		return offer
	}
	admitted := map[string]struct{}{}
	if round.complete && round.count >= 1 {
		admitted[round.value] = struct{}{}
	}
	extractor := handleGraphExtractors[contextfabric.SubjectWorkItem]
	for _, candidate := range pool {
		if candidate.Subject.Kind != contextfabric.SubjectWorkItem {
			continue
		}
		if value, ok := extractor.extract(candidate.Subject.CanonicalID); ok {
			admitted[value] = struct{}{}
		}
	}
	kept := make([]contractsv1.ContextFabricHandleOption, 0, len(offer.HandleOptions))
	for _, option := range offer.HandleOptions {
		if option.Kind == contextfabric.SubjectWorkItem {
			if _, ok := admitted[option.Value]; !ok {
				continue
			}
		}
		kept = append(kept, option)
	}
	offer.HandleOptions = kept
	return offer
}
