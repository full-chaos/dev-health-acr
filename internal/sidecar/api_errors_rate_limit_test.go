package sidecar

import (
	"net/http"
	"testing"
)

func TestAPIErrorRateLimitTokenIsClosedAndScopedTo429RateLimited(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		code    string
		details map[string]any
		want    string
	}{
		{"budget", http.StatusTooManyRequests, "rate_limited", map[string]any{"limit": "request_budget"}, "request_budget"},
		{"unknown token", http.StatusTooManyRequests, "rate_limited", map[string]any{"limit": "anything else"}, ""},
		{"absent", http.StatusTooManyRequests, "rate_limited", map[string]any{"retry_after_seconds": 3}, ""},
		{"wrong code", http.StatusTooManyRequests, "invalid_request", map[string]any{"limit": "request_budget"}, ""},
		{"wrong status", http.StatusServiceUnavailable, "rate_limited", map[string]any{"limit": "request_budget"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodedAPIError(t, tc.status, synthesisEnvelope(t, tc.status, tc.code, tc.details))
			if got.RateLimit != tc.want {
				t.Fatalf("RateLimit = %q, want %q", got.RateLimit, tc.want)
			}
		})
	}
}
