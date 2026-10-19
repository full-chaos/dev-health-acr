package sidecar

import (
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestServiceModelRefusalKeepsItsReasonOnlyOnTheForbiddenFeatureCode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		detail contractsv1.ErrorDetail
		reason string
	}{
		{name: "service model refusal", status: 403, detail: contractsv1.ErrorDetail{Code: "feature_not_enabled", HTTPStatus: 403, Details: map[string]any{"reason": "service_model_not_entitled"}}, reason: "service_model_not_entitled"},
		{name: "own model gate closed refusal", status: 403, detail: contractsv1.ErrorDetail{Code: "feature_not_enabled", HTTPStatus: 403, Details: map[string]any{"reason": "own_model_gate_closed"}}, reason: "own_model_gate_closed"},
		{name: "own model gate closed reason on another code", status: 403, detail: contractsv1.ErrorDetail{Code: "insufficient_scope", HTTPStatus: 403, Details: map[string]any{"reason": "own_model_gate_closed"}}},
		{name: "ai features turned off refusal", status: 403, detail: contractsv1.ErrorDetail{Code: "feature_not_enabled", HTTPStatus: 403, Details: map[string]any{"reason": "ai_features_turned_off"}}, reason: "ai_features_turned_off"},
		{name: "no investigate config refusal", status: 403, detail: contractsv1.ErrorDetail{Code: "feature_not_enabled", HTTPStatus: 403, Details: map[string]any{"reason": "byo_no_investigate_config"}}, reason: "byo_no_investigate_config"},
		{name: "runtime not entitled", status: 403, detail: contractsv1.ErrorDetail{Code: "feature_not_enabled", HTTPStatus: 403}},
		{name: "other feature reason", status: 501, detail: contractsv1.ErrorDetail{Code: "feature_not_enabled", HTTPStatus: 501, Details: map[string]any{"reason": "data_query_not_configured"}}},
		{name: "reason on another code", status: 403, detail: contractsv1.ErrorDetail{Code: "insufficient_scope", HTTPStatus: 403, Details: map[string]any{"reason": "service_model_not_entitled"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiErr := newAPIError(tc.status, tc.detail, "req_1", "")
			if apiErr.Reason != tc.reason {
				t.Fatalf("Reason = %q, want %q", apiErr.Reason, tc.reason)
			}
			if tc.detail.Code == "feature_not_enabled" && !errors.Is(apiErr, ErrFeatureNotEnabled) {
				t.Fatalf("error %v does not wrap ErrFeatureNotEnabled", apiErr)
			}
		})
	}
}
