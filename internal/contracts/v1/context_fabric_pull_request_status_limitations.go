package v1

// The pull request status-filter disclosures. A pull request member set read
// under a status filter states the filter in one fixed sentence per status, and
// a set whose question named a status no read applies says so. Each sentence
// is fixed here and recognised here, so a composer and the recogniser cannot
// drift apart.
var contextFabricPullRequestStatusFilterLimitations = map[string]string{
	"open":              "Members are the pull requests whose current state is open; the state is read as of now, over no period.",
	"merged":            "Members are the pull requests whose current state is merged; the state is read as of now, over no period.",
	"closed":            "Members are the pull requests whose current state is closed without a merge; the state is read as of now, over no period.",
	"awaiting_review":   "Members are the open pull requests with no submitted review; the state is read as of now, over no period.",
	"changes_requested": "Members are the open pull requests whose latest approving or change-requesting review requests changes; reviews are read per pull request, not per reviewer, as of now.",
	"approved":          "Members are the open pull requests whose latest approving or change-requesting review approves them; reviews are read per pull request, not per reviewer, as of now.",
}

// ContextFabricPullRequestStatusUnappliedLimitation is served beside pull
// request members when the question named a state that no read applied.
const ContextFabricPullRequestStatusUnappliedLimitation = "Members are not filtered by state: the state the question names is not one the pull request read applies (open, merged, closed, awaiting review, changes requested or approved)."

// ContextFabricPullRequestStatusFilterLimitation is the sentence for one status
// of the pull request status set, or empty for a value outside it.
func ContextFabricPullRequestStatusFilterLimitation(status string) string {
	return contextFabricPullRequestStatusFilterLimitations[status]
}

// IsContextFabricPullRequestStatusLimitation reports whether one limitation is
// a pull request status-filter disclosure.
func IsContextFabricPullRequestStatusLimitation(limitation string) bool {
	if limitation == ContextFabricPullRequestStatusUnappliedLimitation {
		return true
	}
	for _, sentence := range contextFabricPullRequestStatusFilterLimitations {
		if limitation == sentence {
			return true
		}
	}
	return false
}
