package contextfabric

import (
	"context"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SubjectTermsInput names which input a supplied interpretation's subjects
// were read from. Resolution reads only the flat subject terms, so a supplied
// interpretation that carries a question frame and no flat terms has them
// derived from the frame here, once, before resolution.
type SubjectTermsInput string

const (
	// SubjectTermsFlat: the interpretation carried subject_terms, or the
	// request carried a subject hint. Nothing was derived.
	SubjectTermsFlat SubjectTermsInput = ""
	// SubjectTermsFromFrame: no flat terms; the terms are the frame's.
	SubjectTermsFromFrame SubjectTermsInput = "question_frame"
	// SubjectTermsMissing: no flat terms and the frame names none.
	SubjectTermsMissing SubjectTermsInput = "missing"
	// SubjectTermsFrameOverBound: the frame names more distinct subjects
	// than subject_terms may hold; none are derived.
	SubjectTermsFrameOverBound SubjectTermsInput = "frame_over_bound"
)

type subjectTermsInputKey struct{}

func withSubjectTermsInput(ctx context.Context, input SubjectTermsInput) context.Context {
	return context.WithValue(ctx, subjectTermsInputKey{}, input)
}

func subjectTermsInputFrom(ctx context.Context) SubjectTermsInput {
	input, _ := ctx.Value(subjectTermsInputKey{}).(SubjectTermsInput)
	return input
}

// FrameSubjectTerms returns the distinct retrieval terms a frame's subject
// expression names: named terms, scope anchor terms, and the same for each
// comparison operand. Discovered, grouped and organization expressions name
// no subject and give none.
func FrameSubjectTerms(frame *QuestionFrame) []string {
	if frame == nil {
		return nil
	}
	var raw []string
	collectNamed := func(named *NamedSubjectExpression) {
		if named != nil {
			raw = append(raw, named.Terms...)
		}
	}
	collectScoped := func(scoped *ScopedSetExpression) {
		if scoped != nil {
			raw = append(raw, scoped.AnchorTerms...)
		}
	}
	expression := frame.SubjectExpression
	collectNamed(expression.Named)
	collectScoped(expression.Scoped)
	if expression.Explicit != nil {
		for _, operand := range expression.Explicit.Operands {
			collectNamed(operand.Named)
			collectScoped(operand.Scoped)
		}
	}
	seen := make(map[string]struct{}, len(raw))
	var terms []string
	for _, term := range raw {
		term = strings.TrimSpace(term)
		key := strings.ToLower(term)
		if term == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

// deriveSuppliedSubjectTerms fills the flat subject terms of a supplied
// interpretation from its frame when it has none and the request carries no
// subject hint. It returns the interpretation to resolve with and which input
// the subjects came from.
func deriveSuppliedSubjectTerms(request InvestigationRequest, interpreted InterpretedQuestion, frame *QuestionFrame) (InterpretedQuestion, SubjectTermsInput) {
	for _, term := range interpreted.SubjectTerms {
		if strings.TrimSpace(term) != "" {
			return interpreted, SubjectTermsFlat
		}
	}
	for _, hint := range request.RequestedScope.SubjectHints {
		if strings.TrimSpace(hint.Label) != "" || strings.TrimSpace(hint.ID) != "" {
			return interpreted, SubjectTermsFlat
		}
	}
	terms := FrameSubjectTerms(frame)
	if len(terms) > contractsv1.ContextFabricSubjectTermsMaxCount {
		return interpreted, SubjectTermsFrameOverBound
	}
	if len(terms) == 0 {
		return interpreted, SubjectTermsMissing
	}
	interpreted.SubjectTerms = terms
	return interpreted, SubjectTermsFromFrame
}
