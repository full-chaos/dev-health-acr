package contextfabric

import (
	"context"
	"regexp"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12632: one Info line per interpreted turn that says what the
// interpretation kept of the question, with no question text: how many
// subject terms, the stated kind, whether the question text binds a handle,
// and whether a term has the shape of a repository slug. A model that keeps
// only "1557" of "status of pull request 1557 in owner/repo" shows as
// handle_bound=bound with repository_slug_term=false, from the run's own log.

// QuestionHandleProbe reports whether the question text binds a handle (the
// graph package's free-text handle grammar). Wired by the composition root,
// because this package cannot import the graph package.
type QuestionHandleProbe func(question string) bool

// InterpretationHandleBound is the closed vocabulary of the line's
// handle_bound field.
type InterpretationHandleBound string

const (
	InterpretationHandleBoundYes     InterpretationHandleBound = "bound"
	InterpretationHandleBoundNo      InterpretationHandleBound = "unbound"
	InterpretationHandleBoundUnwired InterpretationHandleBound = "unwired"
)

// InterpretationHandleBoundVocabulary is the closed handle_bound vocabulary,
// in published order.
func InterpretationHandleBoundVocabulary() []string {
	return []string{string(InterpretationHandleBoundYes), string(InterpretationHandleBoundNo), string(InterpretationHandleBoundUnwired)}
}

// InterpretationTermsEvent is the line's content. Counts, closed values and
// booleans only.
type InterpretationTermsEvent struct {
	SubjectTermCount   int
	StatedKind         SubjectKind
	HandleBound        InterpretationHandleBound
	RepositorySlugTerm bool
}

// repositorySlugTerm is an owner/name or group/subgroup/name path: two or
// more segments of letters, digits, '.', '_' or '-', joined by '/'.
var repositorySlugTerm = regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+$`)

// interpretationTermsEvent composes the line for one interpreted turn.
func interpretationTermsEvent(question string, interpretation InterpretedQuestion, frame *QuestionFrame, probe QuestionHandleProbe) InterpretationTermsEvent {
	event := InterpretationTermsEvent{SubjectTermCount: len(interpretation.SubjectTerms), HandleBound: InterpretationHandleBoundUnwired}
	if frame != nil && frame.SubjectExpression.Kind == SubjectExpressionNamed && frame.SubjectExpression.Named != nil && frame.SubjectExpression.Named.ExpectedKind != nil {
		event.StatedKind = *frame.SubjectExpression.Named.ExpectedKind
	}
	if probe != nil {
		event.HandleBound = InterpretationHandleBoundNo
		if probe(question) {
			event.HandleBound = InterpretationHandleBoundYes
		}
	}
	for _, term := range interpretation.SubjectTerms {
		if repositorySlugTerm.MatchString(strings.TrimSpace(term)) {
			event.RepositorySlugTerm = true
			break
		}
	}
	return event
}

// recordInterpretationTerms writes the line; nil-safe.
func (e *Engine) recordInterpretationTerms(ctx context.Context, principal storage.Principal, question string, interpretation InterpretedQuestion, frame *QuestionFrame) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordInterpretationTerms(ctx, principal, interpretationTermsEvent(question, interpretation, frame, e.questionHandleProbe))
}
