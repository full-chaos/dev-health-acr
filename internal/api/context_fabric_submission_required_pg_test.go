package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	postgresstore "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12093 integration: two App instances, each with its own pool and
// submission store, over one real PostgreSQL whose credential store holds a
// browser-class row. Every refusal asserts the body, zero submission rows and
// zero model attempts, on both instances.

const browserSubmissionOrg = "33333333-3333-4333-8333-333333333333"

// issueBrowserCredential issues a credential and makes its row browser class.
// No issuance path derives the browser class yet (the browser OAuth client,
// filed separately, adds it), so the fixture writes the row that path will
// write: the class, an approving user, the client, a session and a family,
// with a 10-minute expiry inside the family (migration 0061 refuses a browser
// row without its client or outliving its family). The provenance trigger
// refuses changing a set class, so this one fixture write runs with it
// disabled, and the test then reads the class back through authentication.
func issueBrowserCredential(t *testing.T, admin *sql.DB, credentials *storage.CredentialLifecycle, now time.Time) string {
	t.Helper()
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: browserSubmissionOrg, Name: "browser fixture", RepositoryScopes: []string{hostedTestRepository},
		Scopes: []string{auth.ScopeContextRead}, CreatedBy: "test_actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{query: `ALTER TABLE acr.client_credentials DISABLE TRIGGER trg_client_credentials_provenance_immutable`},
		{query: `UPDATE acr.client_credentials SET client_class = 'browser', approving_user_id = 'user_browser_fixture', client_id = $2,
			session_id = 'session_browser_fixture', family_id = 'family_browser_fixture', expires_at = $3, family_expires_at = $4
			WHERE credential_id = $1`,
			args: []any{issued.Credential.CredentialID, auth.DevBrowserClientID, now.Add(10 * time.Minute), now.Add(12 * time.Hour)}},
		{query: `ALTER TABLE acr.client_credentials ENABLE TRIGGER trg_client_credentials_provenance_immutable`},
	} {
		if _, err := admin.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("browser fixture: %v", err)
		}
	}
	var class string
	if err := admin.QueryRowContext(ctx, `SELECT client_class FROM acr.client_credentials WHERE credential_id = $1`, issued.Credential.CredentialID).Scan(&class); err != nil || class != "browser" {
		t.Fatalf("browser fixture class = %q (%v)", class, err)
	}
	return issued.Token
}

// postSubmissionHeaders is post with the given X-ACR-Submission-ID values
// (none, one, or several), so malformed and repeated headers reach the route.
func (h *submissionHarness) postSubmissionHeaders(app int, token string, values []string) *httptest.ResponseRecorder {
	h.t.Helper()
	body := investigationRequestBody()
	body.Question = submissionQuestion
	encoded, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", submissionTestRequestID)
	for _, value := range values {
		request.Header.Add(contractsv1.ContextFabricSubmissionIDHeader, value)
	}
	recorder := httptest.NewRecorder()
	h.apps[app].Handler().ServeHTTP(recorder, request)
	return recorder
}

func countSubmissionRows(t *testing.T, admin *sql.DB) int {
	t.Helper()
	var n int
	if err := admin.QueryRowContext(context.Background(), `SELECT count(*) FROM acr.context_fabric_investigation_submissions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestABrowserCreateWithoutASubmissionIDIsRefusedOnPostgres(t *testing.T) {
	dsn, admin := submissionPostgres(t)
	audit, err := postgresstore.NewAuditStore(admin)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := postgresstore.NewCredentialStore(admin, audit)
	if err != nil {
		t.Fatal(err)
	}
	var stores []submissionTestStore
	for index := 0; index < 2; index++ {
		store, err := pginvestigation.NewStore(openSubmissionPool(t, dsn))
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	harness := newSubmissionHarnessWithCredentials(t, credentials, browserSubmissionOrg, stores...)
	browser := issueBrowserCredential(t, admin, credentials, harness.clock.now)
	golden, err := os.ReadFile(repopath.Path(t, "contracts", "examples", "v1", "error_context_fabric_submission_required.v1.json"))
	if err != nil {
		t.Fatal(err)
	}

	for name, values := range submissionHeaderCases() {
		if name == "valid" {
			continue
		}
		for app := 0; app < 2; app++ {
			recorder := harness.postSubmissionHeaders(app, browser, values)
			if recorder.Code != http.StatusBadRequest || !bytes.Equal(append(bytes.TrimRight(recorder.Body.Bytes(), "\n"), '\n'), golden) {
				t.Fatalf("browser, header %s, instance %d: status %d body %s, want 400 and the published golden", name, app, recorder.Code, recorder.Body.String())
			}
		}
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("model attempts after the refusals = %d, want 0", got)
	}
	if got := countSubmissionRows(t, admin); got != 0 {
		t.Fatalf("submission rows after the refusals = %d, want 0", got)
	}

	// One well-formed id: admitted once, on either instance, and replayed.
	resultID := servedResultID(t, harness.postSubmissionHeaders(0, browser, submissionHeaderCases()["valid"]))
	if again := servedResultID(t, harness.postSubmissionHeaders(1, browser, submissionHeaderCases()["valid"])); again != resultID {
		t.Fatalf("replay on the other instance served %s, want %s", again, resultID)
	}
	if got := harness.investigator.executions.Load(); got != 1 {
		t.Fatalf("model attempts with a valid id = %d, want 1", got)
	}
	if row := readSubmissionRow(t, admin, "submission-valid-001"); row.count != 1 {
		t.Fatalf("submission rows for the valid id = %d, want 1", row.count)
	}

	// Another class is unchanged: a malformed id is 400 invalid_request, and a
	// missing one runs.
	for app := 0; app < 2; app++ {
		recorder := harness.postSubmissionHeaders(app, harness.tokenA, submissionHeaderCases()["malformed"])
		if envelope := errorEnvelopeOf(t, recorder); recorder.Code != http.StatusBadRequest || envelope.Error.Code != "invalid_request" {
			t.Fatalf("operator, malformed id, instance %d: status %d code %s, want 400 invalid_request", app, recorder.Code, envelope.Error.Code)
		}
	}
	servedResultID(t, harness.postSubmissionHeaders(0, harness.tokenA, nil))
	if got := harness.investigator.executions.Load(); got != 2 {
		t.Fatalf("model attempts after the operator's header-less create = %d, want 2", got)
	}
}
