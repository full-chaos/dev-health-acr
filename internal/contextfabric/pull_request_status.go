package contextfabric

import (
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The pull request status set: the values the pull request member read can
// apply. open, merged and closed are git_pull_requests.state as ops writes it;
// the review states are decided from the pull request's own review rows
// (git_pull_request_reviews.state) by PullRequestStatusMatches. acr holds no
// synonyms: the interpretation prompt maps the question's words onto the set.
const (
	PullRequestStatusOpen             = "open"
	PullRequestStatusMerged           = "merged"
	PullRequestStatusClosed           = "closed"
	PullRequestStatusAwaitingReview   = "awaiting_review"
	PullRequestStatusChangesRequested = "changes_requested"
	PullRequestStatusApproved         = "approved"
)

var pullRequestStatusVocabulary = [...]string{
	PullRequestStatusOpen, PullRequestStatusMerged, PullRequestStatusClosed,
	PullRequestStatusAwaitingReview, PullRequestStatusChangesRequested, PullRequestStatusApproved,
}

// PullRequestStatusVocabulary returns the closed set in declaration order.
func PullRequestStatusVocabulary() []string {
	out := make([]string, len(pullRequestStatusVocabulary))
	copy(out, pullRequestStatusVocabulary[:])
	return out
}

// InPullRequestStatusVocabulary reports whether value is a member of the set.
func InPullRequestStatusVocabulary(value string) bool {
	for _, member := range pullRequestStatusVocabulary {
		if member == value {
			return true
		}
	}
	return false
}

// PullRequestStatusReviewDerived reports whether value is decided from review
// rows rather than from the pull request's own state.
func PullRequestStatusReviewDerived(value string) bool {
	switch value {
	case PullRequestStatusAwaitingReview, PullRequestStatusChangesRequested, PullRequestStatusApproved:
		return true
	}
	return false
}

// The review row states that decide a review status. COMMENTED, DISMISSED and
// PENDING never decide one; PENDING is a review not yet submitted.
const (
	pullRequestReviewApproved         = "APPROVED"
	pullRequestReviewChangesRequested = "CHANGES_REQUESTED"
	pullRequestReviewPending          = "PENDING"
)

// PullRequestReviewRow is one review row of a pull request: its state as the
// source wrote it and its submitted time in Unix nanoseconds.
type PullRequestReviewRow struct {
	State       string
	SubmittedNs int64
}

// PullRequestStatusMatches decides whether a pull request with state and the
// review rows holds value now.
//
//   - open, merged, closed: the pull request's state is that value.
//   - awaiting_review: the state is open and no submitted review row exists
//     (a PENDING row is not submitted).
//   - changes_requested, approved: the state is open and, of the review rows
//     whose state is APPROVED or CHANGES_REQUESTED, the latest by submitted
//     time is CHANGES_REQUESTED or APPROVED. Equal times decide nothing: a
//     pull request with an APPROVED and a CHANGES_REQUESTED row at the same
//     latest instant holds neither.
//
// The rule is per pull request, not per reviewer: the review rows the graph
// holds carry no reviewer.
func PullRequestStatusMatches(value, state string, reviews []PullRequestReviewRow) bool {
	state = strings.ToLower(strings.TrimSpace(state))
	switch value {
	case PullRequestStatusOpen, PullRequestStatusMerged, PullRequestStatusClosed:
		return state == value
	}
	if state != PullRequestStatusOpen {
		return false
	}
	switch value {
	case PullRequestStatusAwaitingReview:
		for _, review := range reviews {
			if strings.ToUpper(strings.TrimSpace(review.State)) != pullRequestReviewPending {
				return false
			}
		}
		return true
	case PullRequestStatusChangesRequested, PullRequestStatusApproved:
		latest, decision, tied := int64(0), "", false
		for _, review := range reviews {
			reviewState := strings.ToUpper(strings.TrimSpace(review.State))
			if reviewState != pullRequestReviewApproved && reviewState != pullRequestReviewChangesRequested {
				continue
			}
			switch {
			case decision == "" || review.SubmittedNs > latest:
				latest, decision, tied = review.SubmittedNs, reviewState, false
			case review.SubmittedNs == latest && reviewState != decision:
				tied = true
			}
		}
		if tied {
			return false
		}
		if value == PullRequestStatusApproved {
			return decision == pullRequestReviewApproved
		}
		return decision == pullRequestReviewChangesRequested
	}
	return false
}

// PullRequestStatusFilterOf is the status the pull request member read must
// apply for frame, or empty. It is set only for a children_of_scope frame over
// pull_request members whose status qualifier carries a value of the set.
func PullRequestStatusFilterOf(frame *QuestionFrame) string {
	if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionChildrenOfScope || frame.SubjectExpression.Scoped == nil {
		return ""
	}
	scoped := frame.SubjectExpression.Scoped
	if scoped.MemberKind != SubjectPullRequest || scoped.MemberQualifier != MemberQualifierStatus || !InPullRequestStatusVocabulary(scoped.MemberQualifierValue) {
		return ""
	}
	return scoped.MemberQualifierValue
}

// PullRequestStatusUnappliedOf reports whether frame states a status on pull
// request members that no read applies: a status qualifier with no value, or
// a value on an operand of an explicit set.
func PullRequestStatusUnappliedOf(frame *QuestionFrame) bool {
	if frame == nil {
		return false
	}
	switch frame.SubjectExpression.Kind {
	case SubjectExpressionChildrenOfScope:
		scoped := frame.SubjectExpression.Scoped
		return scoped != nil && scoped.MemberKind == SubjectPullRequest && scoped.MemberQualifier == MemberQualifierStatus && scoped.MemberQualifierValue == ""
	case SubjectExpressionExplicitSet:
		if frame.SubjectExpression.Explicit == nil {
			return false
		}
		for _, operand := range frame.SubjectExpression.Explicit.Operands {
			if operand.Kind == SubjectOperandScoped && operand.Scoped != nil && operand.Scoped.MemberKind == SubjectPullRequest && operand.Scoped.MemberQualifier == MemberQualifierStatus {
				return true
			}
		}
	}
	return false
}

// PullRequestStatusFilterDisclosure is the sentence a served pull request
// member set states for the status it was filtered by.
func PullRequestStatusFilterDisclosure(value string) string {
	return contractsv1.ContextFabricPullRequestStatusFilterLimitation(value)
}

// withPullRequestStatusDisclosure states, on a document that serves pull
// request members, the status they were filtered by, or that the state the
// question named was not applied. It adds a sentence once; a re-finalized
// document keeps the one it has.
func withPullRequestStatusDisclosure(result InvestigationResult, frame *QuestionFrame) InvestigationResult {
	if result.Cohort == nil || result.Cohort.Kind != SubjectPullRequest {
		return result
	}
	sentence := ""
	if status := PullRequestStatusFilterOf(frame); status != "" {
		sentence = PullRequestStatusFilterDisclosure(status)
	} else if PullRequestStatusUnappliedOf(frame) {
		sentence = contractsv1.ContextFabricPullRequestStatusUnappliedLimitation
	}
	if sentence == "" {
		return result
	}
	for _, limitation := range result.Limitations {
		if limitation == sentence {
			return result
		}
	}
	composed, displaced := appendBoundedLimitations(result.Limitations, []string{sentence})
	result.Limitations = composed
	result.LimitationsDisplaced += displaced
	return result
}
