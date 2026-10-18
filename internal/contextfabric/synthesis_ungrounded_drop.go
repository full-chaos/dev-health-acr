package contextfabric

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// DropUngroundedSynthesisItems leaves out of a draft every claimed fact that no
// stored fact supports, and every driver or finding that rested on one, so the
// grounded remainder can be served as a partial answer. Without it one such
// item makes ValidateAgainst reject the whole draft.
//
// Only the grounding rules are relaxed. A claim is dropped only when it is
// otherwise well formed, about a subject the investigation allows, under its
// bound label, and its value is not supported. A driver or finding is dropped
// only when everything but its claim grounding passes. Anything else is left
// for ValidateAgainst to reject exactly as before, so a forged subject or an
// unknown evidence ref is never turned into a silent drop.
//
// A draft that would be left with no claim, driver or finding is returned
// unchanged: there is no grounded remainder to serve.
func DropUngroundedSynthesisItems(input SynthesisInput, draft SynthesisDraft) SynthesisDraft {
	allowedSubjects := synthesisSubjects(input)
	labels := canonicalSubjectLabels(input)
	allowedPaths, allowedEvidence := synthesisAllowedPathsAndEvidence(input)

	reasons := map[SynthesisRejectionReason]int{}
	idCount := make(map[string]int, len(draft.ClaimedFacts))
	for _, claim := range draft.ClaimedFacts {
		idCount[claim.ClaimID]++
	}
	removed := map[string]struct{}{}
	kept := make([]ClaimedFact, 0, len(draft.ClaimedFacts))
	for _, claim := range draft.ClaimedFacts {
		duplicate := idCount[claim.ClaimID] > 1
		reason, ungrounded := ungroundedClaimReason(input, claim, allowedSubjects, labels)
		if duplicate || !ungrounded {
			kept = append(kept, claim)
			continue
		}
		removed[claim.ClaimID] = struct{}{}
		reasons[reason]++
	}
	claimed := make(map[string]ClaimedFact, len(kept))
	for _, claim := range kept {
		claimed[claim.ClaimID] = claim
	}

	itemOK := func(subjects []SubjectRef) bool {
		for _, subject := range subjects {
			if _, ok := allowedSubjects[subjectKeyForModel(subject)]; !ok {
				return false
			}
			if requireBoundLabel("", subject, labels) != nil {
				return false
			}
		}
		return true
	}
	evidenceOK := func(ids []string) bool {
		for _, id := range ids {
			if _, ok := allowedEvidence[id]; !ok {
				return false
			}
		}
		return true
	}
	prune := func(ids []string) []string {
		left := make([]string, 0, len(ids))
		for _, id := range ids {
			if _, gone := removed[id]; !gone {
				left = append(left, id)
			}
		}
		return left
	}

	drivers := make([]DriverJudgment, 0, len(draft.Drivers))
	for _, driver := range draft.Drivers {
		wellFormed := driver.Validate() == nil
		driver.ClaimedFactIDs = prune(driver.ClaimedFactIDs)
		pathsOK := true
		for _, pathID := range driver.PathIDs {
			if _, ok := allowedPaths[pathID]; !ok {
				pathsOK = false
			}
		}
		if wellFormed && itemOK(driver.AffectedSubjects) && pathsOK && evidenceOK(driver.EvidenceRefIDs) &&
			requireGroupMembershipClosure(driver.AffectedSubjects, input.Graph.Cohort) == nil &&
			requireGroundedClaims("driver", driver.Category, driver.AffectedSubjects, allowedSubjects, driver.ClaimedFactIDs, claimed) != nil {
			reasons[RejectionReasonDriverClaimUngrounded]++
			continue
		}
		drivers = append(drivers, driver)
	}

	sections := [3][]Finding{draft.RemainingWork, draft.ReadinessGaps, draft.Conflicts}
	for index, findings := range sections {
		keptFindings := make([]Finding, 0, len(findings))
		for _, finding := range findings {
			wellFormed := finding.Validate() == nil
			finding.ClaimedFactIDs = prune(finding.ClaimedFactIDs)
			if wellFormed && itemOK(finding.Subjects) && evidenceOK(finding.EvidenceRefIDs) &&
				requireGroundedClaims("finding", finding.Kind, finding.Subjects, allowedSubjects, finding.ClaimedFactIDs, claimed) != nil {
				reasons[RejectionReasonFindingClaimUngrounded]++
				continue
			}
			keptFindings = append(keptFindings, finding)
		}
		sections[index] = keptFindings
	}

	total := 0
	for _, count := range reasons {
		total += count
	}
	if total == 0 {
		return draft
	}
	if len(kept) == 0 && len(drivers) == 0 && len(sections[0])+len(sections[1])+len(sections[2]) == 0 {
		return draft
	}

	draft.ClaimedFacts = kept
	draft.Drivers = drivers
	draft.RemainingWork, draft.ReadinessGaps, draft.Conflicts = sections[0], sections[1], sections[2]
	draft.ungroundedDropped += total
	merged := make(map[SynthesisRejectionReason]int, len(draft.ungroundedReasons)+len(reasons))
	for reason, count := range draft.ungroundedReasons {
		merged[reason] += count
	}
	for reason, count := range reasons {
		merged[reason] += count
	}
	draft.ungroundedReasons = merged
	return draft
}

// ungroundedClaimReason reports whether a claim is otherwise admissible and
// unsupported by the stored facts, and the closed rejection reason that names
// why. A claim that fails any other rule reports false and is left to
// ValidateAgainst.
func ungroundedClaimReason(input SynthesisInput, claim ClaimedFact, allowedSubjects map[string]struct{}, labels map[string]string) (SynthesisRejectionReason, bool) {
	if claim.Validate() != nil || len(claim.Rows) > 0 || len(claim.TimeSeriesRows) > 0 || strings.HasPrefix(claim.ClaimID, cardinalityClaimIDPrefix) {
		return "", false
	}
	if _, ok := allowedSubjects[subjectKeyForModel(claim.Subject)]; !ok {
		return "", false
	}
	if requireBoundLabel("claimed fact", claim.Subject, labels) != nil {
		return "", false
	}
	switch _, outcome := groundClaim(input.Facts.Facts, claim); outcome {
	case claimGrounded:
		return "", false
	case claimNoCanonicalFact, claimFactIDUnknown:
		return RejectionReasonClaimNoCanonicalFact, true
	case claimDerivationInputUnknown:
		return RejectionReasonClaimDerivationInputUnknown, true
	case claimDerivationValueMismatch:
		return RejectionReasonClaimDerivationValueMismatch, true
	case claimFieldUnobserved:
		return RejectionReasonClaimFieldUnobserved, true
	default:
		return RejectionReasonClaimValueContradicts, true
	}
}

// ungroundedItemDropRecorder is the optional telemetry for items left out of a
// SERVED answer because the stored facts do not support them. It is called at
// composition, so it counts only answers that were served.
type ungroundedItemDropRecorder interface {
	RecordUngroundedItemsDropped(ctx context.Context, principal storage.Principal, reasons map[SynthesisRejectionReason]int)
}

// RecordUngroundedItemsDropped implements ungroundedItemDropRecorder. Content-safe: an org id and one count per closed
// rejection reason.
func (t SlogEngineTelemetry) RecordUngroundedItemsDropped(ctx context.Context, principal storage.Principal, reasons map[SynthesisRejectionReason]int) {
	attrs := []any{"org_id", SanitizeLogAttr(principal.OrgID)}
	for reason, count := range reasons {
		if !ValidSynthesisRejectionReason(reason) {
			reason = RejectionReasonUnclassified
		}
		attrs = append(attrs, "dropped_"+SanitizeLogAttr(string(reason)), count)
	}
	attrs = append(attrs, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric served answer left out items the stored facts do not support", attrs...)
}
