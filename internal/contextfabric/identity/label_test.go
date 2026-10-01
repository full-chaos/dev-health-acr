package identity

import "testing"

func TestPullRequestLabelsKeepTheProjectedFormat(t *testing.T) {
	t.Parallel()
	if got := PullRequestLabel(745); got != "PR #745" {
		t.Fatalf("PullRequestLabel(745) = %q, want %q", got, "PR #745")
	}
	if got := PullRequestReviewLabel(745); got != "PR #745 review" {
		t.Fatalf("PullRequestReviewLabel(745) = %q, want %q", got, "PR #745 review")
	}
}
