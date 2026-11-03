package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// CHAOS-12681: askdev-1 request req_3d9c2ed3 logged the offers-only pass under
// the window gate as "failed" and nothing else: the pass's error was dropped,
// so the run's own artifacts could not say why the turn gated on the window
// alone. A failed pass now names its fault by a closed class, never by the
// error's text.
func TestAFailedOffersOnlyPassNamesItsCause(t *testing.T) {
	t.Parallel()
	pick := handleStateTestPicks[0]
	for _, tc := range []struct {
		name string
		err  error
		want GatedOfferErrorClass
	}{
		{"a deadline", fmt.Errorf("evidence round: %w", context.DeadlineExceeded), GatedOfferErrorDeadlineExceeded},
		{"a cancel", fmt.Errorf("evidence round: %w", context.Canceled), GatedOfferErrorCanceled},
		{"a dependency", fmt.Errorf("graph query: %w", ErrUnavailable), GatedOfferErrorDependencyUnavailable},
		{"anything else", errors.New("boom"), GatedOfferErrorInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			interpreter := &countingInterpreter{
				interpretation: singleSubjectInterpretation(pick.class, pick.confidence),
				family:         QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Frame: handleStateFrame(SubjectRepository, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent)},
			}
			request := validInvestigationRequest()
			request.Question = "What is the status of full-chaos/dev-health-web?"
			engine, graph := handleStateEngine(t, interpreter, telemetry)
			graph.err = tc.err
			result, _ := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if result.Status != InvestigationClarificationRequired {
				t.Fatalf("status = %q, want the window gate", result.Status)
			}
			if !reflect.DeepEqual(telemetry.gatedOfferResolutions, []GatedOfferResolutionOutcome{GatedOfferResolutionFailed}) {
				t.Fatalf("gated offer resolutions = %v, want one failed", telemetry.gatedOfferResolutions)
			}
			if !reflect.DeepEqual(telemetry.gatedOfferFailures, []GatedOfferErrorClass{tc.want}) {
				t.Fatalf("gated offer failures = %v, want exactly [%s]", telemetry.gatedOfferFailures, tc.want)
			}
		})
	}
}

// The production line names the class and never the error's text, which can
// carry query content.
func TestTheGatedOfferFailureLineNeverCarriesTheErrorText(t *testing.T) {
	t.Parallel()
	pick := handleStateTestPicks[0]
	var buf bytes.Buffer
	interpreter := &countingInterpreter{
		interpretation: singleSubjectInterpretation(pick.class, pick.confidence),
		family:         QuestionFamilyOutcome{Family: QuestionFamilySubjectInvestigation, Frame: handleStateFrame(SubjectRepository, []InvestigationGoal{GoalAssessState}, TemporalIntentCurrent)},
	}
	engine, graph := handleStateEngine(t, interpreter, &recordingTelemetry{})
	engine.telemetry = NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	graph.err = errors.New("search for SECRET-TERM-12681 failed")
	request := validInvestigationRequest()
	request.Question = "What is the status of full-chaos/dev-health-web?"
	if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
		t.Logf("Investigate() error = %v (not under test)", err)
	}
	if !strings.Contains(buf.String(), `"msg":"context fabric gated offer resolution failed"`) || !strings.Contains(buf.String(), `"error_class":"internal"`) {
		t.Fatalf("no failure line naming the class: %s", buf.String())
	}
	if strings.Contains(buf.String(), "SECRET-TERM-12681") {
		t.Fatalf("a line carries the error text: %s", buf.String())
	}
}
