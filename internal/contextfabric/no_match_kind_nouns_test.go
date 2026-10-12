package contextfabric

import (
	"strings"
	"testing"
)

func TestTheSubjectNotFoundSentenceNamesKindsByDisplayNoun(t *testing.T) {
	t.Parallel()
	got := noMatchLimitationSubjectNotFound([]string{"Phantom"}, SubjectProject, []string{"ci_pipeline_run", "pull_request", "work_item"})
	if !strings.Contains(got, "the closest matches were CI pipeline runs, pull requests, work items.") {
		t.Fatalf("sentence = %q", got)
	}
	if strings.Contains(got, "_") {
		t.Fatalf("sentence = %q carries a wire token", got)
	}
}

func TestTheNearMissKindsKeepUnknownTokens(t *testing.T) {
	t.Parallel()
	if got := nearMissKindsText([]string{"team", "mystery_kind"}); got != "teams, mystery_kind" {
		t.Fatalf("got %q", got)
	}
}
