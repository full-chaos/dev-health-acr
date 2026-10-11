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
	"github.com/full-chaos/dev-health-acr/internal/storage"
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
		{name: "a receipt store that is unavailable after a valid draft", model: scriptedSynthesisModel{}, sink: unavailableReceiptSink{}, wantClass: "model_receipt_unrecorded"},
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
			// The facts that were read are served as claims, and the one
			// limitation names the mechanism.
			if !hasLimitation(result, contractsv1.ContextFabricSynthesisFailedFactsOnlyLimitation) || len(result.Limitations) != 1 {
				t.Fatalf("limitations = %q, want only the facts-only disclosure", result.Limitations)
			}
			if result.Completeness.TerminalReason != contractsv1.ContextFabricTerminalReasonLimitationDisclosed {
				t.Fatalf("terminal_reason = %q, want limitation_disclosed", result.Completeness.TerminalReason)
			}
			if result.Completeness.ClaimedFactsCount == 0 || len(result.ClaimedFacts) != result.Completeness.ClaimedFactsCount || len(result.EvidenceRefIDs) == 0 {
				t.Fatalf("claimed_facts = %d count = %d evidence = %d, want the facts that were read", len(result.ClaimedFacts), result.Completeness.ClaimedFactsCount, len(result.EvidenceRefIDs))
			}
			var completion *cf.ClaimedFact
			for index := range result.ClaimedFacts {
				if claim := &result.ClaimedFacts[index]; claim.Kind == cf.FactActualCompletion && claim.Field == "completed" && claim.Subject.Label == "PLAT-0000" {
					completion = claim
				}
			}
			if completion == nil || completion.Value.Boolean == nil || !*completion.Value.Boolean {
				t.Fatalf("claimed_facts = %+v, want actual_completion.completed=true for PLAT-0000", result.ClaimedFacts)
			}
			if !strings.Contains(result.CurrentState, "actual_completion.completed=true for") || !strings.Contains(result.CurrentState, "PLAT-0000") {
				t.Fatalf("current_state = %q, want the canonical values read", result.CurrentState)
			}
			if !result.Coverage.Partial {
				t.Fatal("coverage.partial = false, want true")
			}
			wantWarning := "answer text unavailable: the model call failed (class: " + testCase.wantClass + "); the sources that were read are listed in coverage"
			if len(result.Warnings) != 1 || result.Warnings[0] != wantWarning {
				t.Fatalf("warnings = %q, want [%q]", result.Warnings, wantWarning)
			}
			wantHint := map[string]string{"model_output_invalid": "retry_may_succeed", "model_receipt_unrecorded": "retry_may_succeed", "synthesis_rejected": "retry_unlikely_to_help"}[testCase.wantClass]
			var failureDetails []cf.CoverageDetail
			for _, detail := range result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed {
					failureDetails = append(failureDetails, detail)
				}
			}
			if len(failureDetails) != 1 || string(failureDetails[0].FailureClass) != testCase.wantClass || string(failureDetails[0].RetryHint) != wantHint || failureDetails[0].Degrading {
				t.Fatalf("coverage.details failure rows = %+v, want one non-degrading %s with %s", failureDetails, testCase.wantClass, wantHint)
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

// A transient call class, once the runtime's retries are used, is served as a
// degraded answer that names the class and says a retry may succeed.
func TestAModelCallThatFailsTransientlyIsServedAsADegradedAnswer(t *testing.T) {
	exhausted := func(err error) error { return fmt.Errorf("%w: %w", cf.ErrModelRetriesExhausted, err) }
	cases := []struct {
		name string
		err  error
	}{
		{name: "rate limited", err: exhausted(fmt.Errorf("%w: provider status", cf.ErrModelRateLimited))},
		{name: "unavailable", err: exhausted(fmt.Errorf("%w: model generation failed", cf.ErrModelUnavailable))},
		{name: "deadline exceeded", err: exhausted(context.DeadlineExceeded)},
		{name: "cancelled before it ended", err: exhausted(fmt.Errorf("%w: attempt", cf.ErrModelCancelled))},
		{name: "rate limited beside an unrecorded receipt", err: errors.Join(exhausted(fmt.Errorf("%w: provider status", cf.ErrModelRateLimited)), fmt.Errorf("%w: record", cf.ErrModelReceiptUnrecorded))},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := &bytes.Buffer{}
			app, token := teamSynthesisApp(t, scriptedSynthesisModel{err: testCase.err}, nil, teamExpander{targets: 3, complete: true, workItemsOnly: true}, logs)

			result := decodeTeamAnswer(t, postTeamQuestion(t, app, token), logs)

			var failures []cf.CoverageDetail
			for _, detail := range result.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailSynthesisModelFailed {
					failures = append(failures, detail)
				}
			}
			if result.Status != cf.InvestigationDegraded || len(failures) != 1 || string(failures[0].FailureClass) != "model_call_not_completed" || string(failures[0].RetryHint) != "retry_may_succeed" {
				t.Fatalf("status = %q failure rows = %+v, want degraded with model_call_not_completed and retry_may_succeed", result.Status, failures)
			}
			if entries := logLines(t, logs.String(), modelFailureWarn); len(entries) != 1 {
				t.Fatalf("warn lines = %d, want one", len(entries))
			}
		})
	}
}

// A transient end that came before the runtime's retries were used keeps its
// error and its retry signal, and so do the inputs no retry helps.
func TestAModelCallThatFailsBeforeRetriesAreUsedKeepsItsErrorStatus(t *testing.T) {
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

// unavailableReceiptSink fails the way the production receipt store does when
// its database is down: with the dependency-unavailable sentinel.
type unavailableReceiptSink struct{}

func (unavailableReceiptSink) RecordModelExecution(context.Context, storage.Principal, cf.ModelExecutionReceipt) error {
	return fmt.Errorf("%w: insert model receipt", cf.ErrUnavailable)
}
