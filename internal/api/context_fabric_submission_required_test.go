package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
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
	var admission map[string]any
	for _, entry := range strings.Split(strings.TrimSpace(line), "\n") {
		var decoded map[string]any
		if json.Unmarshal([]byte(entry), &decoded) == nil && decoded["msg"] == ContextFabricSubmissionLogMessage {
			admission = decoded
		}
	}
	if admission == nil || admission["level"] != "INFO" || admission["outcome"] != string(SubmissionOutcomeRefusedSubmissionRequired) ||
		admission["client_class"] != "browser" || admission["request_id"] != submissionTestRequestID {
		t.Fatalf("admission line = %v, want an Info line naming the outcome, the class and the request id:\n%s", admission, line)
	}
	if strings.Contains(line, "secret header value") {
		t.Fatalf("the header value reached the log:\n%s", line)
	}
}

// classCredentialStore authenticates every bearer as one credential row of
// the given client class. No issuance path derives the browser class yet, so
// the route-level tests below put the class on the row directly; the
// PostgreSQL test reads a real row through authentication.
type classCredentialStore struct{ row contractsv1.ClientCredential }

func (s classCredentialStore) List(context.Context, string) ([]contractsv1.ClientCredential, error) {
	return nil, nil
}

func (s classCredentialStore) GetByID(context.Context, string, string) (contractsv1.ClientCredential, error) {
	return s.row, nil
}

func (s classCredentialStore) FindByTokenHash(context.Context, string) (contractsv1.ClientCredential, error) {
	return s.row, nil
}

func (s classCredentialStore) TouchLastUsed(context.Context, string, string, string, string, time.Time) error {
	return nil
}

func authenticateAsClass(t *testing.T, app *App, class storage.CredentialClientClass) {
	t.Helper()
	authenticator, err := auth.NewAuthenticator(classCredentialStore{row: contractsv1.ClientCredential{
		CredentialID: "class_test_credential", OrgID: "org_1", Scopes: []string{auth.ScopeContextRead},
		RepositoryScopes: []string{hostedTestRepository},
		Provenance:       contractsv1.CredentialProvenance{ClientClass: string(class), ApprovingUserID: "user_class_test"},
	}}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	app.authenticator = authenticator
}

// createShapes are the request shapes whose own refusals sit before the
// submission header checks in the create route, plus a plain JSON create.
func createShapes() map[string]func(*http.Request) {
	return map[string]func(*http.Request){
		"json":       func(*http.Request) {},
		"stream off": func(request *http.Request) { request.Header.Set("Accept", "text/event-stream") },
		"bad body": func(request *http.Request) {
			request.Body = io.NopCloser(strings.NewReader("{"))
			request.ContentLength = 1
		},
	}
}

func postCreateShape(t *testing.T, app *App, token string, values []string, shape func(*http.Request)) (*httptest.ResponseRecorder, string) {
	t.Helper()
	body := investigationRequestBody()
	body.Question = submissionQuestion
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	for _, value := range values {
		request.Header.Add(contractsv1.ContextFabricSubmissionIDHeader, value)
	}
	shape(request)
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
	return recorder, envelope.Error.Code
}

// Through the route: a browser-class create without one well-formed
// submission id is refused submission_required FIRST, before the stream flag
// and the body checks, and runs nothing; with a valid id the old order holds.
// An operator credential meets the old order with every header state.
func TestTheCreateRouteRefusesABrowserCreateWithoutASubmissionIDFirst(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	if app.config.ContextFabricEventStreamEnabled {
		t.Fatal("precondition: the harness must run with the event stream off")
	}
	golden, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", "error_context_fabric_submission_required.v1.json"))
	if err != nil {
		t.Fatal(err)
	}

	authenticateAsClass(t, app, storage.CredentialClientClassBrowser)
	for header, values := range submissionHeaderCases() {
		if header == "valid" {
			continue
		}
		for shapeName, shape := range createShapes() {
			recorder, _ := postCreateShape(t, app, harness.tokenA, values, shape)
			if recorder.Code != http.StatusBadRequest || !bytes.Equal(append(bytes.TrimRight(recorder.Body.Bytes(), "\n"), '\n'), golden) {
				t.Errorf("browser, header %s, %s: status %d body %s, want 400 and the published golden", header, shapeName, recorder.Code, recorder.Body.String())
			}
		}
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("model attempts after the refusals = %d, want 0", got)
	}
	valid := submissionHeaderCases()["valid"]
	for shapeName, want := range map[string]struct {
		status int
		code   string
	}{
		"stream off": {http.StatusNotAcceptable, contractsv1.ErrorCodeStreamUnavailable},
		"bad body":   {http.StatusBadRequest, "invalid_request"},
		"json":       {http.StatusOK, ""},
	} {
		recorder, code := postCreateShape(t, app, harness.tokenA, valid, createShapes()[shapeName])
		if recorder.Code != want.status || code != want.code {
			t.Errorf("browser, valid id, %s: status %d code %q, want %d %q: %s", shapeName, recorder.Code, code, want.status, want.code, recorder.Body.String())
		}
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("model attempts after one valid JSON create = %d, want 1", got)
	}

	authenticateAsClass(t, app, storage.CredentialClientClassOperator)
	for header, values := range submissionHeaderCases() {
		for shapeName, shape := range createShapes() {
			recorder, code := postCreateShape(t, app, harness.tokenA, values, shape)
			if code == contractsv1.ErrorCodeSubmissionRequired {
				t.Errorf("operator, header %s, %s: refused submission_required: %s", header, shapeName, recorder.Body.String())
			}
			if shapeName == "stream off" && (recorder.Code != http.StatusNotAcceptable || code != contractsv1.ErrorCodeStreamUnavailable) {
				t.Errorf("operator, header %s, stream off: status %d code %q, want 406 %s", header, recorder.Code, code, contractsv1.ErrorCodeStreamUnavailable)
			}
			if shapeName == "bad body" && (recorder.Code != http.StatusBadRequest || code != "invalid_request") {
				t.Errorf("operator, header %s, bad body: status %d code %q, want 400 invalid_request", header, recorder.Code, code)
			}
		}
	}
}
