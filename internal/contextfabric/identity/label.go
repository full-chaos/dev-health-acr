package identity

import "fmt"

// PullRequestLabel is the label of a pull request subject when no title is
// carried for it: the projection's fallback for an empty title, and the label
// of every pull request subject derived from a row that has no title column.
func PullRequestLabel(number int64) string {
	return fmt.Sprintf("PR #%d", number)
}

// PullRequestReviewLabel is the label of a pull request review subject.
func PullRequestReviewLabel(number int64) string {
	return fmt.Sprintf("PR #%d review", number)
}
