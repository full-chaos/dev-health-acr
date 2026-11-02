package contextfabric

import (
	"context"
	"strings"
	"testing"
)

// CHAOS-12632: the interpretation terms line tells, from the run's own log,
// whether the model kept the repository a handle question names. Through the
// real Engine.Investigate, one line per interpreted turn.
func TestInterpretationTermsLineSaysWhatTheInterpretationKept(t *testing.T) {
	t.Parallel()
	question := "What is the status of pull request 1557 in full-chaos/dev-health-web?"
	probe := func(q string) bool { return strings.Contains(q, "pull request 1557") }
	for _, tc := range []struct {
		name  string
		terms []string
		probe QuestionHandleProbe
		want  InterpretationTermsEvent
	}{
		{"number only (reach turn 7)", []string{"1557"}, probe, InterpretationTermsEvent{SubjectTermCount: 1, StatedKind: SubjectPullRequest, HandleBound: InterpretationHandleBoundYes}},
		{"the repository kept", []string{"full-chaos/dev-health-web"}, probe, InterpretationTermsEvent{SubjectTermCount: 1, StatedKind: SubjectPullRequest, HandleBound: InterpretationHandleBoundYes, RepositorySlugTerm: true}},
		{"no probe wired", []string{"full-chaos/dev-health-web"}, nil, InterpretationTermsEvent{SubjectTermCount: 1, StatedKind: SubjectPullRequest, HandleBound: InterpretationHandleBoundUnwired, RepositorySlugTerm: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			interpretation := singleSubjectInterpretation(WindowClassStateSnapshot, WindowConfidenceHigh)
			interpretation.SubjectTerms = tc.terms
			frame := handleStateFrame(SubjectPullRequest, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent)
			frame.SubjectExpression.Named.Terms = tc.terms
			interpreter := &countingInterpreter{interpretation: interpretation, family: QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Frame: frame}}
			engine, _ := handleStateEngine(t, interpreter, telemetry)
			engine.questionHandleProbe = tc.probe
			request := validInvestigationRequest()
			request.Question = question
			_, _ = engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if len(telemetry.interpretationTerms) != 1 || telemetry.interpretationTerms[0] != tc.want {
				t.Fatalf("interpretation terms lines = %+v, want exactly %+v", telemetry.interpretationTerms, tc.want)
			}
		})
	}
}

// A term is a repository slug only in owner/name (or group/.../name) shape.
func TestRepositorySlugTermShape(t *testing.T) {
	t.Parallel()
	for term, want := range map[string]bool{
		"full-chaos/dev-health-web": true, "group/sub/project": true, " owner/repo.name ": true,
		"1557": false, "dev-health-web": false, "a / b": false, "owner/": false, "/repo": false, "": false,
	} {
		event := interpretationTermsEvent("q", InterpretedQuestion{SubjectTerms: []string{term}}, nil, nil)
		if event.RepositorySlugTerm != want {
			t.Errorf("%q: repository_slug_term = %v, want %v", term, event.RepositorySlugTerm, want)
		}
	}
}
