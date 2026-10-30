package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12093 (Ask Dev direct-browser design §3.4, T8): a bearer holder
// controls the headers, so the web client's habit of sending
// X-ACR-Submission-ID is no control. A create by a browser-class credential
// names one well-formed submission id, or it is refused 400
// submission_required before admission and model work. Every other class keeps
// today's behaviour: a missing id is an implicit admission (CHAOS-11997), a
// malformed or repeated one is 400 invalid_request.

// submissionHeaderCases are the header states of a create, by name.
func submissionHeaderCases() map[string][]string {
	return map[string][]string{
		"missing":   nil,
		"malformed": {"bad id!"},
		"too short": {"short"},
		"duplicate": {"submission-dup-0001", "submission-dup-0002"},
		"repeated":  {"submission-dup-0001", "submission-dup-0001"},
		"valid":     {"submission-valid-001"},
	}
}

func createRequestWithSubmissionHeaders(values []string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, nil)
	for _, value := range values {
		request.Header.Add(contractsv1.ContextFabricSubmissionIDHeader, value)
	}
	return request
}

func TestOnlyABrowserCreateWithoutOneWellFormedSubmissionIDIsRefused(t *testing.T) {
	classes := []storage.CredentialClientClass{
		storage.CredentialClientClassBrowser, storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice,
		storage.CredentialClientClassOperator, storage.CredentialClientClassWorkload, "",
	}
	for _, class := range classes {
		for name, values := range submissionHeaderCases() {
			principal := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential, ClientClass: class}
			got := browserSubmissionRequired(principal, createRequestWithSubmissionHeaders(values))
			want := class == storage.CredentialClientClassBrowser && name != "valid"
			if got != want {
				t.Errorf("class %q, header %s (%q): refused = %v, want %v", class, name, values, got, want)
			}
		}
	}
	// A web assertion carries no class: never a browser-class refusal.
	assertion := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion}
	if browserSubmissionRequired(assertion, createRequestWithSubmissionHeaders(nil)) {
		t.Fatal("a web assertion with no submission id was refused as browser class")
	}
}

// The refusal's body, written by the route's own writer, is the published
// golden and validates against error.v1. ACR_WRITE_SUBMISSION_GOLDENS=1
// rewrites it. Its admission line names the outcome and the class, never a
// header value.
func TestSubmissionRequiredGoldenIsTheWritersOwnBody(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	logs := harness.logs[0]
	logs.Reset()
	request := createRequestWithSubmissionHeaders([]string{"secret header value!"})
	request = request.WithContext(context.WithValue(request.Context(), requestIDContextKey, submissionTestRequestID))
	recorder := httptest.NewRecorder()
	harness.apps[0].writeSubmissionRequired(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	body := append(bytes.TrimRight(recorder.Body.Bytes(), "\n"), '\n')
	if err := contractcheck.ValidateSerializedStrict("", "error.v1.schema.json", body); err != nil {
		t.Fatalf("schema rejects the body: %v", err)
	}
	var envelope struct {
		Error struct {
			Code       string `json:"code"`
			HTTPStatus int    `json:"http_status"`
			Retryable  bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != contractsv1.ErrorCodeSubmissionRequired || envelope.Error.HTTPStatus != http.StatusBadRequest || envelope.Error.Retryable {
		t.Fatalf("envelope = %+v, want 400 %s, not retryable", envelope.Error, contractsv1.ErrorCodeSubmissionRequired)
	}
	path := repopath.Path(t, "contracts", "examples", "v1", "error_context_fabric_submission_required.v1.json")
	if os.Getenv("ACR_WRITE_SUBMISSION_GOLDENS") == "1" {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if golden, err := os.ReadFile(path); err != nil || !bytes.Equal(golden, body) {
		t.Fatalf("golden differs from the writer's body (err %v):\n%s\nwant\n%s", err, body, golden)
	}
	line := logs.String()
	if !strings.Contains(line, `"outcome":"`+string(SubmissionOutcomeRefusedSubmissionRequired)+`"`) || !strings.Contains(line, `"client_class":"browser"`) {
		t.Fatalf("admission line does not name the outcome and the class:\n%s", line)
	}
	if strings.Contains(line, "secret header value") {
		t.Fatalf("the header value reached the log:\n%s", line)
	}
}
