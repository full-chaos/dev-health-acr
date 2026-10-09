package sidecar

import (
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestWindowRefusalMessageIsFixedAndKeyedOnTheClosedMaxDays(t *testing.T) {
	window := func(extra map[string]any) *APIError {
		details := map[string]any{"reason": contractsv1.WindowBeyondKindMaxReason}
		for k, v := range extra {
			details[k] = v
		}
		return newAPIError(400, contractsv1.ErrorDetail{Code: "invalid_request", Message: "hostile <free text>", Details: details}, "req", "")
	}
	got := window(map[string]any{"max_days": float64(60)})
	if got.MaxDays != 60 || !strings.Contains(got.Message, "at most 60 days") || !strings.Contains(got.Message, "read a longer period as several windows") {
		t.Fatalf("60 d: %+v", got)
	}
	got = window(map[string]any{"max_days": float64(365)})
	if got.MaxDays != 365 || !strings.Contains(got.Message, "do not add shorter windows") {
		t.Fatalf("365 d: %+v", got)
	}
	for _, bad := range []any{float64(0), float64(-1), float64(367), 60.5, "60", nil} {
		got = window(map[string]any{"max_days": bad})
		if got.MaxDays != 0 || got.Message != codeSafeMessages["invalid_request"] {
			t.Errorf("max_days %v must leave the generic message: %+v", bad, got)
		}
	}
	other := newAPIError(400, contractsv1.ErrorDetail{Code: "invalid_request", Details: map[string]any{"reason": "invalid_request", "max_days": float64(60)}}, "req", "")
	if other.MaxDays != 0 || other.Message != codeSafeMessages["invalid_request"] {
		t.Errorf("another reason must not carry a window message: %+v", other)
	}
}
