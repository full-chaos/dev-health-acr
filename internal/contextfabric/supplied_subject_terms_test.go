package contextfabric

import (
	"reflect"
	"testing"
)

func TestFrameSubjectTermsReadsEveryNamingVariantAndNoOther(t *testing.T) {
	frame := func(expression SubjectExpression) *QuestionFrame {
		return &QuestionFrame{SubjectExpression: expression}
	}
	cases := []struct {
		name  string
		frame *QuestionFrame
		want  []string
	}{
		{"nil frame", nil, nil},
		{"named", frame(SubjectExpression{Named: &NamedSubjectExpression{Terms: []string{" Alpha ", "alpha", "Beta"}}}), []string{"Alpha", "Beta"}},
		{"scoped anchor", frame(SubjectExpression{Scoped: &ScopedSetExpression{AnchorTerms: []string{"Repo One"}}}), []string{"Repo One"}},
		{"comparison operands", frame(SubjectExpression{Explicit: &ExplicitSetExpression{Operands: []SubjectOperand{
			{Named: &NamedSubjectExpression{Terms: []string{"A"}}},
			{Scoped: &ScopedSetExpression{AnchorTerms: []string{"B"}}},
		}}}), []string{"A", "B"}},
		{"discovered names none", frame(SubjectExpression{Discovered: &DiscoveredSetExpression{MemberKind: SubjectTeam}}), nil},
		{"organization names none", frame(SubjectExpression{Org: &OrganizationScopeExpression{}}), nil},
	}
	for _, tc := range cases {
		if got := FrameSubjectTerms(tc.frame); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: terms = %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestDeriveSuppliedSubjectTermsLeavesFlatTermsAndHintsAlone(t *testing.T) {
	frame := &QuestionFrame{SubjectExpression: SubjectExpression{Named: &NamedSubjectExpression{Terms: []string{"Framed"}}}}
	withFlat := InterpretedQuestion{SubjectTerms: []string{"Flat"}}
	if got, input := deriveSuppliedSubjectTerms(InvestigationRequest{}, withFlat, frame); input != SubjectTermsFlat || !reflect.DeepEqual(got.SubjectTerms, []string{"Flat"}) {
		t.Fatalf("flat terms: input = %q terms = %#v, want the flat terms untouched", input, got.SubjectTerms)
	}
	hinted := InvestigationRequest{}
	hinted.RequestedScope.SubjectHints = []SubjectHint{{Label: "Hinted"}}
	if got, input := deriveSuppliedSubjectTerms(hinted, InterpretedQuestion{}, frame); input != SubjectTermsFlat || len(got.SubjectTerms) != 0 {
		t.Fatalf("hint: input = %q terms = %#v, want nothing derived beside a hint", input, got.SubjectTerms)
	}
	if got, input := deriveSuppliedSubjectTerms(InvestigationRequest{}, InterpretedQuestion{}, frame); input != SubjectTermsFromFrame || !reflect.DeepEqual(got.SubjectTerms, []string{"Framed"}) {
		t.Fatalf("frame: input = %q terms = %#v, want the frame's", input, got.SubjectTerms)
	}
	if _, input := deriveSuppliedSubjectTerms(InvestigationRequest{}, InterpretedQuestion{}, nil); input != SubjectTermsMissing {
		t.Fatalf("no frame: input = %q, want missing", input)
	}
}
