package sidecar

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const refusalSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func interpretationContractEnvelope(t *testing.T, status int, code string, value any) []byte {
	t.Helper()
	body, err := json.Marshal(contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_0123456789abcdef",
		Error: contractsv1.ErrorDetail{
			Code: code, Message: "hosted prose", HTTPStatus: status,
			Details: map[string]any{contractsv1.ContextFabricInterpretationContractDetailsKey: value},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func validRefusalValue() map[string]any {
	return map[string]any{
		"mismatch": []string{"model_output_version", "prompt_version"},
		"current":  map[string]any{"model_output_version": "out-v2", "prompt_version": "prompt-v2", "system_sha256": refusalSHA},
	}
}

func TestInterpretationContractRefusalIsParsedFromA409(t *testing.T) {
	decoded := decodeAPIError(http.StatusConflict, "req_0123456789abcdef", "", interpretationContractEnvelope(t, http.StatusConflict, "invalid_request", validRefusalValue()))
	var got *APIError
	if !errors.As(decoded, &got) {
		t.Fatalf("decodeAPIError = %T, want *APIError", decoded)
	}
	if !errors.Is(got, ErrInvalidRequest) {
		t.Fatalf("sentinel = %v, want ErrInvalidRequest", got)
	}
	want := contractsv1.ContextFabricInterpretationContractRefusal{
		Mismatch: []string{"model_output_version", "prompt_version"},
		Current:  contractsv1.ContextFabricInterpretationContract{ModelOutputVersion: "out-v2", PromptVersion: "prompt-v2", SystemSHA256: refusalSHA},
	}
	if got.InterpretationContract == nil || strings.Join(got.InterpretationContract.Mismatch, ",") != strings.Join(want.Mismatch, ",") || got.InterpretationContract.Current != want.Current {
		t.Fatalf("InterpretationContract = %#v, want %#v", got.InterpretationContract, want)
	}
	if got.Message != interpretationContractSafeMessage {
		t.Errorf("message = %q, want the fixed message", got.Message)
	}
	if strings.Contains(got.Error(), "hosted prose") {
		t.Errorf("error text %q carries hosted prose", got.Error())
	}
}

func TestInterpretationContractRefusalIsIgnoredWhenItIsNotValid(t *testing.T) {
	cases := map[string]struct {
		status int
		code   string
		value  any
	}{
		"not a 409":           {http.StatusBadRequest, "invalid_request", validRefusalValue()},
		"not invalid_request": {http.StatusConflict, "not_found", validRefusalValue()},
		"unknown field name": {http.StatusConflict, "invalid_request", map[string]any{
			"mismatch": []string{"prompt_version", "other"},
			"current":  map[string]any{"model_output_version": "o", "prompt_version": "p", "system_sha256": refusalSHA},
		}},
		"duplicate field": {http.StatusConflict, "invalid_request", map[string]any{
			"mismatch": []string{"prompt_version", "prompt_version"},
			"current":  map[string]any{"model_output_version": "o", "prompt_version": "p", "system_sha256": refusalSHA},
		}},
		"empty mismatch": {http.StatusConflict, "invalid_request", map[string]any{
			"mismatch": []string{},
			"current":  map[string]any{"model_output_version": "o", "prompt_version": "p", "system_sha256": refusalSHA},
		}},
		"current sha missing": {http.StatusConflict, "invalid_request", map[string]any{
			"mismatch": []string{"prompt_version"},
			"current":  map[string]any{"model_output_version": "o", "prompt_version": "p"},
		}},
		"extra key": {http.StatusConflict, "invalid_request", map[string]any{
			"mismatch": []string{"prompt_version"}, "note": "hosted prose",
			"current": map[string]any{"model_output_version": "o", "prompt_version": "p", "system_sha256": refusalSHA},
		}},
		"not an object": {http.StatusConflict, "invalid_request", "text"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			decoded := decodeAPIError(tc.status, "req_0123456789abcdef", "", interpretationContractEnvelope(t, tc.status, tc.code, tc.value))
			var got *APIError
			if !errors.As(decoded, &got) {
				t.Fatalf("decodeAPIError = %T, want *APIError", decoded)
			}
			if got.InterpretationContract != nil {
				t.Errorf("InterpretationContract = %#v, want nil", got.InterpretationContract)
			}
			if got.Message == interpretationContractSafeMessage {
				t.Errorf("message = %q, want the generic message", got.Message)
			}
		})
	}
}
