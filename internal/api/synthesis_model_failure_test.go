package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const modelFailureWarn = "context fabric synthesis model call failed, degraded answer served"

// A model call that fails after every fact was read. The classes the runtime
// will not retry are served as a degraded answer: the facts and coverage
// stay, no model prose is served, and the cause is named.
func TestAModelCallThatFailsIsServedAsADegradedAnswer(t *testing.T) {
	cases := []struct {
		name      string
		model     scriptedSynthesisModel
		sink      cf.ModelReceiptSink
		wantClass string
	}{
		{name: "an invalid model output", model: scriptedSynthesisModel{err: fmt.Errorf("%w: provider status", cf.ErrModelOutput), receipt: true}, wantClass: "model_output_invalid"},
		{name: "an invalid model output with a receipt that is also refused", model: scriptedSynthesisModel{err: fmt.Errorf("%w: provider status", cf.ErrModelOutput), receipt: true}, sink: failingReceiptSink{}, wantClass: "model_output_invalid"},
		{name: "a draft that fails the bounds after the draw budget", model: scriptedSynthesisModel{err: fmt.Errorf("%w: %w: claim is not grounded", cf.ErrSynthesisRejected, cf.ErrModelOutput), receipt: true}, wantClass: "synthesis_rejected"},
		{name: "a receipt the sink refuses after a valid draft", model: scriptedSynthesisModel{}, sink: failingReceiptSink{}, wantClass: "model_receipt_unrecorded"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app, token := teamSynthesisApp(t, testCase.model, testCase.sink, teamExpander{targets: 3, complete: true, workItemsOnly: true}, logs)

			response := postTeamQuestion(t, app, token)

			result := decodeTeamAnswer(t, response, logs)
			if result.Status != cf.InvestigationDegraded || result.Completeness.TerminalStatus != cf.InvestigationDegraded {
				t.Fatalf("status = %q terminal_status = %q, want degraded", result.Status, result.Completeness.TerminalStatus)
			}
			// The reason names the first disclosure channel in the contract's
			// precedence. The answer has no claim to keep the team committed,
			// so the engine's commit gate adds its limitation, which outranks
			// the warning. The warning below is what names the cause.
			if !hasLimitation(result, contractsv1.ContextFabricCommitRetractionLimitation) || len(result.Limitations) != 1 {
				t.Fatalf("limitations = %q, want only the commit retraction", result.Limitations)
			}
			if result.Completeness.TerminalReason != contractsv1.ContextFabricTerminalReasonLimitationDisclosed {
				t.Fatalf("terminal_reason = %q, want limitation_disclosed", result.Completeness.TerminalReason)
			}
			if !result.Coverage.Partial {
				t.Fatal("coverage.partial = false, want true")
			}
			wantWarning := "answer text unavailable: the model call failed (class: " + testCase.wantClass + "); the facts below were read and are served without model prose"
			if len(result.Warnings) != 1 || result.Warnings[0] != wantWarning {
				t.Fatalf("warnings = %q, want [%q]", result.Warnings, wantWarning)
			}
			if len(result.Drivers) != 0 || len(result.StrongestPressures) != 0 || len(result.RemainingWork) != 0 || len(result.ReadinessGaps) != 0 || len(result.Conflicts) != 0 {
				t.Fatalf("the answer carries model-authored content: %+v", result)
			}
			if strings.Contains(result.DeterministicAnswer, "Platform team has open work") {
				t.Fatalf("deterministic_answer = %q, want server-composed text only", result.DeterministicAnswer)
			}
			if strings.Contains(result.CurrentState, "No canonical facts were observed") || result.CurrentState == "" {
				t.Fatalf("current_state = %q, want a sentence that does not say no facts were observed", result.CurrentState)
			}
			available := 0
			for _, source := range result.Coverage.Sources {
				if source.State == cf.SourceAvailable {
					available++
				}
			}
			if available == 0 {
				t.Fatalf("coverage.sources = %+v, want the facts that were read to stay", result.Coverage.Sources)
			}
			entries := logLines(t, logs.String(), modelFailureWarn)
			if len(entries) != 1 || entries[0]["level"] != "WARN" || entries[0]["class"] != testCase.wantClass {
				t.Fatalf("warn lines = %v, want one WARN with class=%s", entries, testCase.wantClass)
			}
			for _, field := range []string{"attempts", "elapsed_ms"} {
				if _, present := entries[0][field]; !present {
					t.Fatalf("warn line = %v, want %s", entries[0], field)
				}
			}
			if strings.Contains(logs.String(), "connection reset") || strings.Contains(logs.String(), "claim is not grounded") {
				t.Fatalf("the log carries the cause's own text:\n%s", logs.String())
			}
		})
	}
}

// Control: the same question with a model that answers is not degraded and
// says nothing about a failed call.
func TestAModelCallThatSucceedsIsNotDegraded(t *testing.T) {
	logs := &bytes.Buffer{}
	provider := newRecordedModelProvider(t)
	provider.draft = teamClaimSynthesisJSON
	app, token := teamSynthesisApp(t, productionModelRuntime(t, provider, logs), nil, teamExpander{targets: 3, complete: true, workItemsOnly: true}, logs)

	result := decodeTeamAnswer(t, postTeamQuestion(t, app, token), logs)

	if result.Status != cf.InvestigationComplete || result.Completeness.TerminalReason != "" {
		t.Fatalf("status = %q terminal_reason = %q, want complete with none", result.Status, result.Completeness.TerminalReason)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "model call failed") {
			t.Fatalf("warnings = %q, want none about a failed call", result.Warnings)
		}
	}
	if entries := logLines(t, logs.String(), modelFailureWarn); len(entries) != 0 {
		t.Fatalf("warn lines = %v, want none", entries)
	}
}

// The classes whose retry signal is the error itself keep it, and a caller
// that is gone is never served an answer.
func TestAModelCallThatFailsTransientlyKeepsItsErrorStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "rate limited", err: fmt.Errorf("%w: provider status", cf.ErrModelRateLimited), wantStatus: http.StatusTooManyRequests},
		{name: "unavailable", err: fmt.Errorf("%w: model generation failed", cf.ErrModelUnavailable), wantStatus: http.StatusServiceUnavailable},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout},
		{name: "rate limited beside an unrecorded receipt", err: errors.Join(fmt.Errorf("%w: provider status", cf.ErrModelRateLimited), fmt.Errorf("%w: record", cf.ErrModelReceiptUnrecorded)), wantStatus: http.StatusTooManyRequests},
		{name: "an input no bounding fits", err: &cf.ModelInputOverflow{Bytes: 900_000, MaxBytes: 524_288}, wantStatus: http.StatusInternalServerError},
		{name: "a bare runtime error", err: errors.New("generator closed"), wantStatus: http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app, token := teamSynthesisApp(t, scriptedSynthesisModel{err: testCase.err}, nil, teamExpander{targets: 3}, logs)

			response := postTeamQuestion(t, app, token)

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			if entries := logLines(t, logs.String(), modelFailureWarn); len(entries) != 0 {
				t.Fatalf("warn lines = %v, want none: no degraded answer was served", entries)
			}
		})
	}
}
