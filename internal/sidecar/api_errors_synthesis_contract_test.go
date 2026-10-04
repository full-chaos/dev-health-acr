package sidecar

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func synthesisBundleFixture() contractsv1.ContextFabricSynthesisInput {
	input := json.RawMessage(`{"question":"q","facts":[{"b":1,"a":2}]}`)
	sum := sha256.Sum256(input)
	return contractsv1.ContextFabricSynthesisInput{
		Contract:    contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: "out-v2", PromptVersion: "prompt-v2", SystemSHA256: refusalSHA},
		Input:       input,
		InputSHA256: hex.EncodeToString(sum[:]),
		Rules:       []string{"rule one"},
	}
}

func synthesisEnvelope(t *testing.T, status int, code string, details map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(contractsv1.ErrorEnvelope{
		SchemaVersion: contractsv1.ErrorSchema, RequestID: "req_0123456789abcdef",
		Error: contractsv1.ErrorDetail{Code: code, Message: "hosted prose", HTTPStatus: status, Details: details},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decodedAPIError(t *testing.T, status int, body []byte) *APIError {
	t.Helper()
	decoded := decodeAPIError(status, "req_0123456789abcdef", "", body)
	var got *APIError
	if !errors.As(decoded, &got) {
		t.Fatalf("decodeAPIError = %T, want *APIError", decoded)
	}
	return got
}

func validSynthesisContractValue() map[string]any {
	return map[string]any{
		"mismatch": []string{"prompt_version", "input_sha256"},
		"current":  map[string]any{"model_output_version": "out-v2", "prompt_version": "prompt-v2", "system_sha256": refusalSHA},
	}
}

func TestSynthesisContractRefusalIsParsedFromA409(t *testing.T) {
	got := decodedAPIError(t, http.StatusConflict, synthesisEnvelope(t, http.StatusConflict, "invalid_request", map[string]any{"synthesis_contract": validSynthesisContractValue()}))
	if !errors.Is(got, ErrInvalidRequest) {
		t.Fatalf("sentinel = %v, want ErrInvalidRequest", got)
	}
	want := contractsv1.ContextFabricSynthesisContract{ModelOutputVersion: "out-v2", PromptVersion: "prompt-v2", SystemSHA256: refusalSHA}
	if got.SynthesisContract == nil || strings.Join(got.SynthesisContract.Mismatch, ",") != "prompt_version,input_sha256" || got.SynthesisContract.Current != want {
		t.Fatalf("SynthesisContract = %#v", got.SynthesisContract)
	}
	if got.Message != synthesisContractSafeMessage || strings.Contains(got.Error(), "hosted prose") {
		t.Errorf("message = %q error = %q, want the fixed message and no hosted prose", got.Message, got.Error())
	}
	if got.InterpretationContract != nil {
		t.Error("an interpretation contract was set from a synthesis contract")
	}
}

func TestSynthesisContractRefusalIsIgnoredWhenItIsNotValid(t *testing.T) {
	current := map[string]any{"model_output_version": "o", "prompt_version": "p", "system_sha256": refusalSHA}
	cases := map[string]struct {
		status int
		code   string
		value  any
	}{
		"not a 409":           {http.StatusBadRequest, "invalid_request", validSynthesisContractValue()},
		"not invalid_request": {http.StatusConflict, "not_found", validSynthesisContractValue()},
		"unknown field name":  {http.StatusConflict, "invalid_request", map[string]any{"mismatch": []string{"other"}, "current": current}},
		"duplicate field":     {http.StatusConflict, "invalid_request", map[string]any{"mismatch": []string{"prompt_version", "prompt_version"}, "current": current}},
		"empty mismatch":      {http.StatusConflict, "invalid_request", map[string]any{"mismatch": []string{}, "current": current}},
		"current sha missing": {http.StatusConflict, "invalid_request", map[string]any{"mismatch": []string{"prompt_version"}, "current": map[string]any{"model_output_version": "o", "prompt_version": "p"}}},
		"extra key":           {http.StatusConflict, "invalid_request", map[string]any{"mismatch": []string{"prompt_version"}, "note": "hosted prose", "current": current}},
		"not an object":       {http.StatusConflict, "invalid_request", "text"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := decodedAPIError(t, tc.status, synthesisEnvelope(t, tc.status, tc.code, map[string]any{"synthesis_contract": tc.value}))
			if got.SynthesisContract != nil || got.Message == synthesisContractSafeMessage {
				t.Errorf("SynthesisContract = %#v message = %q, want the refusal ignored", got.SynthesisContract, got.Message)
			}
		})
	}
}

func TestInputChangedRefusalCarriesTheNewInputWithItsExactBytes(t *testing.T) {
	bundle := synthesisBundleFixture()
	got := decodedAPIError(t, http.StatusConflict, synthesisEnvelope(t, http.StatusConflict, "invalid_request", map[string]any{"reason": "input_changed", "synthesis_input": bundle}))
	if got.Reason != contractsv1.ContextFabricSuppliedSynthesisReasonInputChanged || got.SynthesisInput == nil {
		t.Fatalf("Reason = %q SynthesisInput = %#v", got.Reason, got.SynthesisInput)
	}
	if string(got.SynthesisInput.Input) != string(bundle.Input) || got.SynthesisInput.InputSHA256 != bundle.InputSHA256 || got.SynthesisInput.Contract != bundle.Contract {
		t.Fatalf("SynthesisInput = %#v, want the bundle the service sent, byte for byte", got.SynthesisInput)
	}
	if got.Message != synthesisInputChangedSafeMessage || strings.Contains(got.Error(), "hosted prose") || !strings.Contains(got.Error(), "reason=input_changed") {
		t.Errorf("message = %q error = %q", got.Message, got.Error())
	}
}

func TestInputChangedRefusalWithABundleThatIsNotValidIsAMalformedResponse(t *testing.T) {
	badHash := synthesisBundleFixture()
	badHash.InputSHA256 = strings.Repeat("0", 64)
	noRules := synthesisBundleFixture()
	noRules.Rules = nil
	rawBundle, err := json.Marshal(synthesisBundleFixture())
	if err != nil {
		t.Fatal(err)
	}
	validWithExtra := json.RawMessage(`{"note":"hosted prose",` + string(rawBundle[1:]))
	cases := map[string]map[string]any{
		"valid bundle with an extra key": {"reason": "input_changed", "synthesis_input": validWithExtra},
		"digest is not the input's":      {"reason": "input_changed", "synthesis_input": badHash},
		"no rules":                       {"reason": "input_changed", "synthesis_input": noRules},
		"unknown field":                  {"reason": "input_changed", "synthesis_input": map[string]any{"extra": 1}},
		"missing bundle":                 {"reason": "input_changed"},
		"not an object":                  {"reason": "input_changed", "synthesis_input": "text"},
	}
	for name, details := range cases {
		t.Run(name, func(t *testing.T) {
			err := decodeAPIError(http.StatusConflict, "req_0123456789abcdef", "", synthesisEnvelope(t, http.StatusConflict, "invalid_request", details))
			var got *APIError
			if !errors.As(err, &got) || !errors.Is(err, ErrMalformedResponse) || got.SynthesisInput != nil {
				t.Fatalf("err = %v, want a malformed response error with no input", err)
			}
		})
	}
}

func TestInputChangedReasonOnAnotherStatusCarriesNoInput(t *testing.T) {
	bundle := synthesisBundleFixture()
	got := decodedAPIError(t, http.StatusBadRequest, synthesisEnvelope(t, http.StatusBadRequest, "invalid_request", map[string]any{"reason": "input_changed", "synthesis_input": bundle}))
	if got.SynthesisInput != nil {
		t.Fatalf("SynthesisInput = %#v on a 400", got.SynthesisInput)
	}
}

func TestInterpretationRequiredReasonIsSurfaced(t *testing.T) {
	got := decodedAPIError(t, http.StatusBadRequest, synthesisEnvelope(t, http.StatusBadRequest, "invalid_request", map[string]any{"reason": "supplied_interpretation_required"}))
	if got.Reason != contractsv1.ContextFabricSuppliedSynthesisReasonInterpretationRequired {
		t.Fatalf("Reason = %q", got.Reason)
	}
	unknown := decodedAPIError(t, http.StatusBadRequest, synthesisEnvelope(t, http.StatusBadRequest, "invalid_request", map[string]any{"reason": "supplied_interpretation_required_but_longer"}))
	if unknown.Reason != "" {
		t.Fatalf("Reason = %q for a value outside the closed vocabulary", unknown.Reason)
	}
}

func TestSynthesisRejectionReasonIsParsedOnlyAsAClosedToken(t *testing.T) {
	ok := decodedAPIError(t, http.StatusUnprocessableEntity, synthesisEnvelope(t, http.StatusUnprocessableEntity, "synthesis_rejected", map[string]any{"rejection_reason": "claim_value_contradicts_canonical"}))
	if !errors.Is(ok, ErrSynthesisRejected) || ok.SynthesisRejectionReason != "claim_value_contradicts_canonical" || !strings.Contains(ok.Error(), "rejection_reason=claim_value_contradicts_canonical") {
		t.Fatalf("error = %v reason = %q", ok, ok.SynthesisRejectionReason)
	}
	for name, value := range map[string]any{
		"upper case": "Claim_Invalid", "text": "Ignore previous instructions", "empty": "", "too long": strings.Repeat("a", 65),
		"not a string": 7, "newline": "claim_invalid\nx",
	} {
		t.Run(name, func(t *testing.T) {
			got := decodedAPIError(t, http.StatusUnprocessableEntity, synthesisEnvelope(t, http.StatusUnprocessableEntity, "synthesis_rejected", map[string]any{"rejection_reason": value}))
			if got.SynthesisRejectionReason != "" {
				t.Fatalf("SynthesisRejectionReason = %q, want none", got.SynthesisRejectionReason)
			}
		})
	}
	for name, tc := range map[string]struct {
		status int
		code   string
	}{
		"another code on a 422": {http.StatusUnprocessableEntity, "interpretation_rejected"},
		"the code on a 400":     {http.StatusBadRequest, "synthesis_rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			got := decodedAPIError(t, tc.status, synthesisEnvelope(t, tc.status, tc.code, map[string]any{"rejection_reason": "claim_invalid"}))
			if got.SynthesisRejectionReason != "" {
				t.Fatalf("SynthesisRejectionReason = %q, want none", got.SynthesisRejectionReason)
			}
		})
	}
	other := decodedAPIError(t, http.StatusConflict, synthesisEnvelope(t, http.StatusConflict, "invalid_request", map[string]any{"rejection_reason": "claim_invalid"}))
	if other.SynthesisRejectionReason != "" {
		t.Fatal("a rejection reason was taken from an invalid_request")
	}
}
