package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3811. A failed investigation used to record one line -- failure_class
// -- identical for a graph outage, a rejected fact request, and an ACR-side
// invariant breach. This pins the bounded stage + classification pair that
// replaced it, and pins that NOTHING derived from the error's own text is
// logged at any level.

func decodeFailureLog(t *testing.T, logs string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		entry := map[string]any{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry["msg"] == "context fabric investigation failed" {
			return entry
		}
	}
	t.Fatalf("no investigation failure log line found in:\n%s", logs)
	return nil
}

func TestContextFabricInvestigationFailuresCarryStageAndClassification(t *testing.T) {
	staged := func(stage contextfabric.InvestigationStage, err error) error {
		// The route reads the stage through the exported StageError, the
		// same way Engine attaches it.
		return &contextfabric.StageError{Stage: stage, Err: err}
	}
	cases := []struct {
		name           string
		err            error
		wantStatus     int
		wantStage      string
		wantClassified string
		wantLevel      string
	}{
		{
			name: "graph unavailable during resolution", wantStatus: http.StatusServiceUnavailable,
			err:       staged(contextfabric.StageResolution, fmt.Errorf("resolve subjects: %w", contextfabric.ErrUnavailable)),
			wantStage: "resolution", wantClassified: "dependency_unavailable", wantLevel: "ERROR",
		},
		{
			name: "no investigation subjects at the fact read", wantStatus: http.StatusInternalServerError,
			err:       staged(contextfabric.StageFactRead, fmt.Errorf("read canonical facts: %w", contextfabric.ErrNoInvestigationSubjects)),
			wantStage: "fact_read", wantClassified: "no_investigation_subjects", wantLevel: "ERROR",
		},
		{
			name: "invalid result at validation", wantStatus: http.StatusInternalServerError,
			err:       staged(contextfabric.StageValidation, fmt.Errorf("%w: paths", contextfabric.ErrInvalidResult)),
			wantStage: "validation", wantClassified: "invalid_result", wantLevel: "ERROR",
		},
		{
			name: "model output invalid at synthesis", wantStatus: http.StatusBadGateway,
			err:       staged(contextfabric.StageSynthesis, fmt.Errorf("synthesize investigation: %w", contextfabric.ErrModelOutput)),
			wantStage: "synthesis", wantClassified: "model_output_invalid", wantLevel: "ERROR",
		},
		{
			name: "an unanswerable time bound is a caller problem", wantStatus: http.StatusBadRequest,
			err:       fmt.Errorf("%w: as-of time is in the future", contextfabric.ErrInvalidTimeBound),
			wantStage: "unknown", wantClassified: "invalid_time_bound", wantLevel: "WARN",
		},
		{
			name: "an unstaged, unclassified error is named as such", wantStatus: http.StatusInternalServerError,
			err:       errors.New("something unexpected broke"),
			wantStage: "unknown", wantClassified: "unclassified", wantLevel: "ERROR",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				return contextfabric.InvestigationResult{}, testCase.err
			}))
			response := httptest.NewRecorder()

			app.Handler().ServeHTTP(response, investigationRequest(t, token))

			if response.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d body=%s", response.Code, testCase.wantStatus, response.Body.String())
			}
			entry := decodeFailureLog(t, logs.String())
			if got := entry["failure_stage"]; got != testCase.wantStage {
				t.Fatalf("failure_stage = %v, want %q", got, testCase.wantStage)
			}
			if got := entry["failure_classification"]; got != testCase.wantClassified {
				t.Fatalf("failure_classification = %v, want %q", got, testCase.wantClassified)
			}
			if got := entry["level"]; got != testCase.wantLevel {
				t.Fatalf("level = %v, want %q", got, testCase.wantLevel)
			}
			if got := entry["failure_class"]; got != "context_fabric_investigation" {
				t.Fatalf("failure_class = %v, want the pre-existing value to be preserved", got)
			}
		})
	}
}

// The standing rule: a bounded classification is a guarantee only if it holds
// at every level. A failure whose cause carries a raw dependency message must
// log the classification and NOTHING of that message -- no debug hatch.
func TestContextFabricInvestigationFailureLogsNeverCarryRawCauseText(t *testing.T) {
	const secret = "dial tcp 10.1.2.3:16379: connection refused by falkordb-primary"
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, &contextfabric.StageError{
			Stage: contextfabric.StageGraph,
			Err:   fmt.Errorf("discover graph context: %s: %w", secret, contextfabric.ErrUnavailable),
		}
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if strings.Contains(logs.String(), "10.1.2.3") || strings.Contains(logs.String(), "falkordb") {
		t.Fatalf("failure log carries raw cause text:\n%s", logs.String())
	}
	if strings.Contains(response.Body.String(), "10.1.2.3") || strings.Contains(response.Body.String(), "falkordb") {
		t.Fatalf("response body carries raw cause text: %s", response.Body.String())
	}
	if got := decodeFailureLog(t, logs.String())["failure_stage"]; got != "graph" {
		t.Fatalf("failure_stage = %v, want \"graph\"", got)
	}
}

// CHAOS-3810 codex round-1 P3. A panic inside the investigator used to skip
// the endpoint's one exit point entirely: the global recovery middleware
// wrote a 500 with no failure_stage and no failure_classification, so the
// single most alarming failure mode was the least diagnosable one.
//
// The panic value is arbitrary, caller-influenced data. It must reach neither
// the classification, nor the log line, nor the body -- only the closed enum
// pair does.
func TestContextFabricInvestigationPanicExitsThroughTheOneFailurePath(t *testing.T) {
	const panicSecret = "dial tcp 10.9.8.7:16379: falkordb-primary credentials rejected"
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		panic(errors.New(panicSecret))
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 body=%s", response.Code, response.Body.String())
	}
	entry := decodeFailureLog(t, logs.String())
	if got := entry["failure_classification"]; got != "panic" {
		t.Fatalf("failure_classification = %v, want \"panic\"", got)
	}
	if got := entry["failure_stage"]; got != "unknown" {
		t.Fatalf("failure_stage = %v, want \"unknown\": a panic carries no stage", got)
	}
	if got := entry["failure_class"]; got != "context_fabric_investigation" {
		t.Fatalf("failure_class = %v, want the endpoint's own failure class", got)
	}
	for _, fragment := range []string{"10.9.8.7", "falkordb", "credentials"} {
		if strings.Contains(logs.String(), fragment) {
			t.Fatalf("panic value fragment %q reached the logs:\n%s", fragment, logs.String())
		}
		if strings.Contains(response.Body.String(), fragment) {
			t.Fatalf("panic value fragment %q reached the response body: %s", fragment, response.Body.String())
		}
	}
	// The response is still the ordinary opaque envelope, not a panic report.
	if !strings.Contains(response.Body.String(), "internal_error") {
		t.Fatalf("body = %s, want the standard internal_error envelope", response.Body.String())
	}
}

// A non-error panic value (a bare string) must be handled identically -- the
// recovery must not depend on the value being an error.
func TestContextFabricInvestigationPanicWithANonErrorValueIsClassifiedToo(t *testing.T) {
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		panic("bare string panic at 10.9.8.7")
	}))
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, investigationRequest(t, token))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if got := decodeFailureLog(t, logs.String())["failure_classification"]; got != "panic" {
		t.Fatalf("failure_classification = %v, want \"panic\"", got)
	}
	if strings.Contains(logs.String(), "10.9.8.7") {
		t.Fatalf("panic value reached the logs:\n%s", logs.String())
	}
}

// CHAOS-3811 codex round-2 F3. A panic used to lose to both context checks:
// racing a client disconnect it returned with no log at all, and under an
// exceeded deadline it was reported as a timeout. The failure mode P3 exists
// to make visible was therefore invisible exactly when a request was already
// going badly.
//
// The LOG must fire regardless of context state. The RESPONSE still obeys the
// cancellation rule -- a disconnected client has nothing to receive.
//
// The cancellation is triggered INSIDE the investigator, immediately before
// the panic, rather than on the request handed to ServeHTTP: a context
// canceled before the handler runs never reaches the investigator at all (the
// credential lookup fails first and answers 503), so that arrangement would
// prove nothing. This ordering is deterministic -- no sleeps, no deadlines
// racing real work.
func TestContextFabricInvestigationPanicIsLoggedEvenWhenTheRequestIsCanceled(t *testing.T) {
	var cancel context.CancelFunc
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		cancel()
		panic("panic racing a client disconnect at 10.9.8.7")
	}))
	response := httptest.NewRecorder()
	request := investigationRequest(t, token)
	ctx, cancelRequest := context.WithCancel(request.Context())
	cancel = cancelRequest
	defer cancelRequest()

	app.Handler().ServeHTTP(response, request.WithContext(ctx))

	entry := decodeFailureLog(t, logs.String())
	if got := entry["failure_classification"]; got != "panic" {
		t.Fatalf("failure_classification = %v, want \"panic\" even though the request was canceled", got)
	}
	if got := entry["http_status"]; got != float64(http.StatusInternalServerError) {
		t.Fatalf("http_status = %v, want 500", got)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %s, want no response written to a canceled request", response.Body.String())
	}
	if strings.Contains(logs.String(), "10.9.8.7") {
		t.Fatalf("panic value reached the logs:\n%s", logs.String())
	}
}

// The deadline half of F3, exercised directly against the classifier: an
// expired request context must not turn a panic into a timeout. Called
// directly rather than through ServeHTTP because an already-expired context
// fails authentication long before the investigator runs -- and manufacturing
// the race with a short deadline plus a sleeping investigator would be a
// wall-clock flake, which this wave is in the business of removing.
func TestContextFabricInvestigationPanicUnderAnExceededDeadlineIsNotReportedAsATimeout(t *testing.T) {
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, nil
	}))
	response := httptest.NewRecorder()
	request := investigationRequest(t, token)
	ctx, cancel := context.WithDeadline(request.Context(), time.Now().Add(-time.Second))
	defer cancel()

	app.writeContextFabricError(response, request.WithContext(ctx), errContextFabricPanic)

	if got := decodeFailureLog(t, logs.String())["failure_classification"]; got != "panic" {
		t.Fatalf("failure_classification = %v, want \"panic\": the request did not run out of time, it broke", got)
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, not 504: an exceeded deadline must not mask a panic", response.Code)
	}
	if !strings.Contains(response.Body.String(), "internal_error") {
		t.Fatalf("body = %s, want the internal_error envelope, not a timeout envelope", response.Body.String())
	}
}
