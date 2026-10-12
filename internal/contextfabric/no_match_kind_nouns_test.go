package contextfabric

import (
	"strings"
	"testing"
)

func TestTheSubjectNotFoundSentenceNamesKindsByDisplayNoun(t *testing.T) {
	t.Parallel()
	got := noMatchLimitationSubjectNotFound([]string{"Phantom"}, []string{"ci_pipeline_run", "pull_request", "work_item"})
	if !strings.Contains(got, "among the kinds searched (CI pipeline runs, pull requests, work items)") {
		t.Fatalf("sentence = %q", got)
	}
	if strings.Contains(got, "_") {
		t.Fatalf("sentence = %q carries a wire token", got)
	}
}

func TestTheSubjectNotFoundKindsKeepNoneAndUnknownTokens(t *testing.T) {
	t.Parallel()
	if got := kindsSearchedText(nil); got != "none" {
		t.Fatalf("got %q", got)
	}
	if got := kindsSearchedText([]string{"team", "mystery_kind"}); got != "teams, mystery_kind" {
		t.Fatalf("got %q", got)
	}
}
