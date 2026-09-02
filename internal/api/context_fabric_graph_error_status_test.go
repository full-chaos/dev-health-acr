package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4874 family A, read path. This route's comments used to CLAIM that
// falkorgraph wraps its rate limit into contextfabric.ErrRateLimited and its
// credential failure into contextfabric.ErrUnavailable. Neither was true, so a
// graph rate limit or an auth refusal answered a generic 500 while the source
// said 429 / 503. These cases make the comments true BY CONSTRUCTION: they
// inject falkorgraph's OWN exported sentinels -- not an error shaped like one
// -- and assert the documented status and classification.
//
// Scope note, so this test does not overclaim: it proves the mapping the
// sentinels carry. The residual arm (an unrecognised driver error, which has
// no sentinel to inject) is only reachable through safeDependencyError, an
// unexported function; that arm is proven in falkorgraph's own
// TestFalkorErrorOriginsClassifyForProjectionRun.
func TestGraphBackendSentinelsGetTheirDocumentedHTTPStatus(t *testing.T) {
	cases := []struct {
		name           string
		err            error
		wantStatus     int
		wantClassified string
	}{
		{
			name:           "a graph rate limit is a 429, not a 500",
			err:            fmt.Errorf("resolve subjects: %w", falkorgraph.ErrRateLimited),
			wantStatus:     http.StatusTooManyRequests,
			wantClassified: "rate_limited",
		},
		{
			name:           "ACR's own refused credential is a 503, never a caller-facing 401",
			err:            fmt.Errorf("resolve subjects: %w", falkorgraph.ErrUnauthorized),
			wantStatus:     http.StatusServiceUnavailable,
			wantClassified: "dependency_unavailable",
		},
		{
			name:           "a write the store rejected as contract-invalid is invalid_result",
			err:            fmt.Errorf("apply projection batch: %w", falkorgraph.ErrConstraintViolation),
			wantStatus:     http.StatusInternalServerError,
			wantClassified: "invalid_result",
		},
		{
			name:           "a confirmed absence stays out of the dependency buckets",
			err:            fmt.Errorf("read watermark: %w", falkorgraph.ErrNotFound),
			wantStatus:     http.StatusInternalServerError,
			wantClassified: "unclassified",
		},
	}
	reached := 0
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
			if got := entry["failure_classification"]; got != testCase.wantClassified {
				t.Fatalf("failure_classification = %v, want %q", got, testCase.wantClassified)
			}
			reached++
		})
	}
	if reached != len(cases) {
		t.Fatalf("assertion reach: %d of %d cases reached their assertions", reached, len(cases))
	}
}
