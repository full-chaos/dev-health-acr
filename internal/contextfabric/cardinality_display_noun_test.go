package contextfabric

import "testing"

func TestCardinalitySentenceUsesDisplayNouns(t *testing.T) {
	for _, testCase := range []struct {
		kind   SubjectKind
		served int
		want   string
	}{
		{SubjectPullRequest, 2, "Counted 2 pull requests."},
		{SubjectPullRequest, 1, "Counted 1 pull request."},
		{SubjectRepository, 3, "Counted 3 repositories."},
		{SubjectRepository, 1, "Counted 1 repository."},
		{SubjectWorkItem, 2, "Counted 2 work items."},
	} {
		got := cardinalityAnswerSentence(MembershipCardinality{Resolved: true, Kind: testCase.kind, Served: testCase.served, Declared: testCase.served})
		if got != testCase.want {
			t.Errorf("%s x%d = %q, want %q", testCase.kind, testCase.served, got, testCase.want)
		}
	}
}
