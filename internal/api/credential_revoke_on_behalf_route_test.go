package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const (
	onBehalfRouteOrg      = "org_1"
	onBehalfRouteApprover = "usr_approver_1"
	onBehalfRouteResource = "https://acr.example.test/mcp"
)

type onBehalfRouteRig struct {
	app     *App
	token   string
	lookups *atomic.Int64
	audit   *memory.AuditStore
	logs    *bytes.Buffer
}

// newOnBehalfRouteRig authenticates every bearer as one caller row of class
// and scopes in onBehalfRouteOrg; the revoke acts on the runtime's own
// credential store.
func newOnBehalfRouteRig(t *testing.T, class storage.CredentialClientClass, scopes []string) onBehalfRouteRig {
	t.Helper()
	app, _ := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
	audit, ok := app.runtime.Audit.(*memory.AuditStore)
	if !ok {
		t.Fatalf("test app audit store is %T", app.runtime.Audit)
	}
	rig := onBehalfRouteRig{app: app, lookups: &atomic.Int64{}, audit: audit, logs: &bytes.Buffer{}}
	authenticator, err := auth.NewAuthenticator(countingClassStore{
		lookups: rig.lookups,
		classCredentialStore: classCredentialStore{row: contractsv1.ClientCredential{
			CredentialID: "revoke_caller", OrgID: onBehalfRouteOrg, Scopes: scopes, RepositoryScopes: []string{hostedTestRepository},
			Provenance: contractsv1.CredentialProvenance{ClientClass: string(class)},
		}},
	}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	app.authenticator = authenticator
	app.logger = slog.New(slog.NewJSONHandler(rig.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	rig.token, err = auth.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	return rig
}

func revokeScopes() []string { return []string{auth.ScopeCredentialLifecycleRevoke} }

// seed stores a device-redeemed credential in org, bound to resource when it
// is not empty, approved by onBehalfRouteApprover.
func (r onBehalfRouteRig) seed(t *testing.T, org, id, resource string) string {
	t.Helper()
	input := storage.CredentialCreateInput{
		CredentialID: id, OrgID: org, Name: "mcp client", TokenPrefix: "fcacr_" + id[:8], TokenHash: auth.HashToken("fcacr_" + id + "_secret_value_for_hash"),
		RepositoryScopes: []string{hostedTestRepository}, Scopes: []string{auth.ScopeContextRead}, ActorID: onBehalfRouteApprover,
		IssuanceProvenance: storage.CredentialIssuanceProvenanceDeviceAuthorization, ApprovingUserID: onBehalfRouteApprover, Resource: resource,
	}
	if resource != "" {
		input.ClientID = "acrc_0123456789abcdef0123456789abcdef"
	}
	created, err := r.app.runtime.Credentials.CreateCredential(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return created.CredentialID
}

func (r onBehalfRouteRig) post(id string, body any, bearer *string) *httptest.ResponseRecorder {
	var raw []byte
	switch value := body.(type) {
	case string:
		raw = []byte(value)
	default:
		raw, _ = json.Marshal(value)
	}
	request := httptest.NewRequest(http.MethodPost, strings.Replace(CredentialRevokeOnBehalfPath, "{credential_id}", id, 1), bytes.NewReader(raw))
	request.RemoteAddr = "192.0.2.10:4000"
	request.Header.Set("Content-Type", "application/json")
	if bearer != nil {
		request.Header.Set("Authorization", "Bearer "+*bearer)
	}
	response := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(response, request)
	return response
}

func (r onBehalfRouteRig) live(t *testing.T, org, id string) bool {
	t.Helper()
	credential, err := r.app.runtime.Credentials.GetByID(context.Background(), org, id)
	if err != nil {
		t.Fatal(err)
	}
	return credential.RevokedAt == nil
}

func (r onBehalfRouteRig) revokedRows(id string) []storage.AuditEvent {
	var rows []storage.AuditEvent
	for _, event := range r.audit.Events() {
		if event.Action == storage.AuditActionCredentialRevoked && event.ResourceID == id {
			rows = append(rows, event)
		}
	}
	return rows
}

// decisionLine certifies the one on-behalf decision line and the one oauth
// step line of the request against their declarations.
func (r onBehalfRouteRig) certifyLines(t *testing.T, actorType, decision, reason string, status int) {
	t.Helper()
	parsed, err := certify.Parse(r.logs.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	lines := parsed.LinesWithMsg(eventspec.CredentialRevokeOnBehalfLogMessage)
	if len(lines) != 1 {
		t.Fatalf("%d on-behalf lines, want 1; logs=%s", len(lines), r.logs.String())
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.CredentialRevokeOnBehalf, Want: map[string]any{
		"request_id": lines[0]["request_id"], "actor_type": actorType, "decision": decision, "reason": reason,
	}}); err != nil {
		t.Fatal(err)
	}
	steps := parsed.LinesWithMsg(eventspec.OAuthStepLogMessage)
	if len(steps) != 1 || steps[0]["step"] != oauthvocab.StepCredentialRevoke || steps[0]["status"] != float64(status) {
		t.Fatalf("oauth step lines %v, want one credential_revoke line with status %d", steps, status)
	}
	if strings.Contains(r.logs.String(), "cred_") || strings.Contains(r.logs.String(), onBehalfRouteApprover) || strings.Contains(r.logs.String(), "usr_admin") {
		t.Fatalf("a log line names a credential or an actor id: %s", r.logs.String())
	}
}

func onBehalfBody(actorType, actorID string) contractsv1.CredentialRevokeOnBehalfRequest {
	return contractsv1.CredentialRevokeOnBehalfRequest{SchemaVersion: contractsv1.CredentialRevokeOnBehalfRequestSchema, ActorType: actorType, ActorID: actorID}
}

func TestCredentialRevokeOnBehalfRevokesAnMCPBoundCredentialAndNamesTheActor(t *testing.T) {
	for _, cell := range []struct{ actorType, actorID string }{
		{contractsv1.CredentialRevokeOnBehalfActorAdmin, "usr_admin_9"},
		{contractsv1.CredentialRevokeOnBehalfActorUser, onBehalfRouteApprover},
	} {
		t.Run(cell.actorType, func(t *testing.T) {
			rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
			id := rig.seed(t, onBehalfRouteOrg, "cred_routembound01", onBehalfRouteResource)
			response := rig.post(id, onBehalfBody(cell.actorType, cell.actorID), &rig.token)
			if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
				t.Fatalf("status %d body %q, want 204 and no body", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			if rig.live(t, onBehalfRouteOrg, id) {
				t.Fatal("the credential is still live")
			}
			rows := rig.revokedRows(id)
			if len(rows) != 1 || rows[0].ActorType != cell.actorType || rows[0].ActorID != cell.actorID {
				t.Fatalf("credential_revoked rows %+v, want one with actor %s/%s", rows, cell.actorType, cell.actorID)
			}
			rig.certifyLines(t, cell.actorType, oauthvocab.OnBehalfDecisionRevoked, oauthvocab.OnBehalfReasonNone, http.StatusNoContent)
		})
	}
}

// Every refusal answers the same 404 body, so the answer tells a caller
// nothing about the credential; only the decision line names the reason.
func TestCredentialRevokeOnBehalfAnswersEveryRefusalAlike(t *testing.T) {
	var bodies []string
	for name, cell := range map[string]struct {
		org, resource, actorType, actorID, reason string
		unknown                                   bool
	}{
		"non-approver user": {onBehalfRouteOrg, onBehalfRouteResource, "user", "usr_other_2", oauthvocab.OnBehalfReasonNotApprover, false},
		"unbound row":       {onBehalfRouteOrg, "", "admin", "usr_admin_9", oauthvocab.OnBehalfReasonNotResourceBound, false},
		"other org's row":   {"org_2", onBehalfRouteResource, "admin", "usr_admin_9", oauthvocab.OnBehalfReasonUnknownCredential, false},
		"unknown id":        {onBehalfRouteOrg, onBehalfRouteResource, "admin", "usr_admin_9", oauthvocab.OnBehalfReasonUnknownCredential, true},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
			id := rig.seed(t, cell.org, "cred_routembound02", cell.resource)
			target := id
			if cell.unknown {
				target = "cred_doesnotexist0"
			}
			response := rig.post(target, onBehalfBody(cell.actorType, cell.actorID), &rig.token)
			assertErrorResponse(t, response, http.StatusNotFound, "not_found")
			var envelope map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			delete(envelope, "request_id")
			normalized, _ := json.Marshal(envelope)
			bodies = append(bodies, string(normalized))
			if !rig.live(t, cell.org, id) || len(rig.revokedRows(id)) != 0 {
				t.Fatal("a refused request revoked the credential or wrote an audit row")
			}
			rig.certifyLines(t, cell.actorType, oauthvocab.OnBehalfDecisionRefused, cell.reason, http.StatusNotFound)
		})
	}
	for _, body := range bodies[1:] {
		if body != bodies[0] {
			t.Fatalf("refusal bodies differ:\n%s\n%s", bodies[0], body)
		}
	}
}

func TestCredentialRevokeOnBehalfASecondRevokeIsAConflict(t *testing.T) {
	rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
	id := rig.seed(t, onBehalfRouteOrg, "cred_routembound03", onBehalfRouteResource)
	if response := rig.post(id, onBehalfBody("admin", "usr_admin_9"), &rig.token); response.Code != http.StatusNoContent {
		t.Fatalf("first revoke status %d", response.Code)
	}
	rig.logs.Reset()
	assertErrorResponse(t, rig.post(id, onBehalfBody("user", onBehalfRouteApprover), &rig.token), http.StatusConflict, "credential_lifecycle_conflict")
	if len(rig.revokedRows(id)) != 1 {
		t.Fatal("a second revoke wrote a second audit row")
	}
	rig.certifyLines(t, "user", oauthvocab.OnBehalfDecisionConflict, oauthvocab.OnBehalfReasonNone, http.StatusConflict)
}

func TestCredentialRevokeOnBehalfServesOnlyAWorkloadWithTheRevokeScope(t *testing.T) {
	for _, class := range []storage.CredentialClientClass{
		storage.CredentialClientClassBrowser, storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice,
		storage.CredentialClientClassOrgBearer, storage.CredentialClientClassOperator, "",
	} {
		rig := newOnBehalfRouteRig(t, class, revokeScopes())
		id := rig.seed(t, onBehalfRouteOrg, "cred_routembound04", onBehalfRouteResource)
		assertErrorResponse(t, rig.post(id, onBehalfBody("admin", "usr_admin_9"), &rig.token), http.StatusForbidden, "credential_class_refused")
		if !rig.live(t, onBehalfRouteOrg, id) || rig.lookups.Load() != 1 {
			t.Fatalf("class %q: live=%v lookups=%d, want live and 1 lookup", class, rig.live(t, onBehalfRouteOrg, id), rig.lookups.Load())
		}
	}
	for _, scopes := range [][]string{
		{auth.ScopeCredentialLifecycleRead}, {auth.ScopeContextRead, auth.ScopeEvidenceRead}, {auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite}, {auth.ScopeDataRead},
	} {
		rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, scopes)
		id := rig.seed(t, onBehalfRouteOrg, "cred_routembound05", onBehalfRouteResource)
		assertErrorResponse(t, rig.post(id, onBehalfBody("admin", "usr_admin_9"), &rig.token), http.StatusForbidden, "insufficient_scope")
		if !rig.live(t, onBehalfRouteOrg, id) {
			t.Fatalf("scopes %v revoked the credential", scopes)
		}
	}
}

func TestCredentialRevokeOnBehalfRefusesBeforeAnyLookupWhenTheBearerIsAbsentOrMalformed(t *testing.T) {
	rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
	id := rig.seed(t, onBehalfRouteOrg, "cred_routembound06", onBehalfRouteResource)
	malformed := "fcacr_not-a-token"
	jwt := "eyJhbGciOiJFZERTQSJ9.eyJzdWIiOiJ4In0.c2ln"
	for name, bearer := range map[string]*string{"absent": nil, "malformed": &malformed, "jwt": &jwt} {
		assertErrorResponse(t, rig.post(id, onBehalfBody("admin", "usr_admin_9"), bearer), http.StatusUnauthorized, "invalid_token")
		if rig.lookups.Load() != 0 || !rig.live(t, onBehalfRouteOrg, id) {
			t.Fatalf("%s: lookups %d, live %v", name, rig.lookups.Load(), rig.live(t, onBehalfRouteOrg, id))
		}
	}
}

func TestCredentialRevokeOnBehalfRefusesAValidWebAssertion(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	body, _ := json.Marshal(onBehalfBody("admin", "usr_admin_9"))
	request := httptest.NewRequest(http.MethodPost, strings.Replace(CredentialRevokeOnBehalfPath, "{credential_id}", "cred_routembound07", 1), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(auth.WebAssertionHeader, signAPIAssertionAt(t, private, request, body, "revoke_1", now))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
}

// The body domain: each field absent, null, empty, of the wrong type, out of
// vocabulary, at and past its bound, padded, plus an unknown key. Every cell
// is refused before the service and leaves the credential live.
func TestCredentialRevokeOnBehalfRefusesEveryMalformedBody(t *testing.T) {
	long := strings.Repeat("u", 201)
	cells := map[string]string{
		"empty body":            ``,
		"not json":              `{not json`,
		"array":                 `[]`,
		"null":                  `null`,
		"schema absent":         `{"actor_type":"admin","actor_id":"usr_a"}`,
		"schema null":           `{"schema_version":null,"actor_type":"admin","actor_id":"usr_a"}`,
		"schema other":          `{"schema_version":"credential_revoke_request.v1","actor_type":"admin","actor_id":"usr_a"}`,
		"actor type absent":     `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_id":"usr_a"}`,
		"actor type null":       `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":null,"actor_id":"usr_a"}`,
		"actor type empty":      `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"","actor_id":"usr_a"}`,
		"actor type number":     `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":1,"actor_id":"usr_a"}`,
		"actor type operator":   `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"operator","actor_id":"usr_a"}`,
		"actor type upper":      `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"Admin","actor_id":"usr_a"}`,
		"actor type array":      `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":["admin"],"actor_id":"usr_a"}`,
		"actor id absent":       `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin"}`,
		"actor id null":         `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":null}`,
		"actor id empty":        `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":""}`,
		"actor id blank":        `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":"  "}`,
		"actor id padded":       `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":" usr_a"}`,
		"actor id number":       `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":7}`,
		"actor id object":       `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":{}}`,
		"actor id past bound":   `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":"` + long + `"}`,
		"unknown key":           `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":"usr_a","org_id":"org_2"}`,
		"trailing second value": `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_id":"usr_a"}{}`,
	}
	for name, body := range cells {
		t.Run(name, func(t *testing.T) {
			rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
			id := rig.seed(t, onBehalfRouteOrg, "cred_routembound08", onBehalfRouteResource)
			assertErrorResponse(t, rig.post(id, body, &rig.token), http.StatusBadRequest, "invalid_request")
			if !rig.live(t, onBehalfRouteOrg, id) || len(rig.revokedRows(id)) != 0 {
				t.Fatal("a malformed body revoked the credential")
			}
			parsed, err := certify.Parse(rig.logs.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			steps := parsed.LinesWithMsg(eventspec.OAuthStepLogMessage)
			if len(steps) != 1 || steps[0]["outcome"] != oauthvocab.OutcomeInvalidRequest {
				t.Fatalf("oauth step lines %v, want one invalid_request line", steps)
			}
			if err := certify.CertifyAbsent(parsed, eventspec.CredentialRevokeOnBehalf, map[string]any{"request_id": steps[0]["request_id"]}); err != nil {
				t.Fatal(err)
			}
		})
	}
	// A duplicate key takes its last value, as on every acr route
	// (decodeJSONBody): the last actor_type is the one validated and recorded.
	dup := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
	dupID := dup.seed(t, onBehalfRouteOrg, "cred_routembound10", onBehalfRouteResource)
	response := dup.post(dupID, `{"schema_version":"credential_revoke_on_behalf_request.v1","actor_type":"admin","actor_type":"user","actor_id":"usr_other_2"}`, &dup.token)
	assertErrorResponse(t, response, http.StatusNotFound, "not_found")
	if !dup.live(t, onBehalfRouteOrg, dupID) {
		t.Fatal("a duplicate key that ends on a non-approver user revoked the credential")
	}
	// The bound itself is admitted.
	rig := newOnBehalfRouteRig(t, storage.CredentialClientClassWorkload, revokeScopes())
	id := rig.seed(t, onBehalfRouteOrg, "cred_routembound09", onBehalfRouteResource)
	if response := rig.post(id, onBehalfBody("admin", strings.Repeat("u", 200)), &rig.token); response.Code != http.StatusNoContent {
		t.Fatalf("a 200-character actor id: status %d %s", response.Code, response.Body.String())
	}
}

func TestCredentialRevokeOnBehalfActorVocabularyIsOneList(t *testing.T) {
	contract := []string{contractsv1.CredentialRevokeOnBehalfActorUser, contractsv1.CredentialRevokeOnBehalfActorAdmin}
	if !slices.Equal(contract, oauthvocab.OnBehalfActorVocabulary()) || !slices.Equal(contract, []string{auth.OnBehalfActorUser, auth.OnBehalfActorAdmin}) {
		t.Fatalf("actor vocabularies differ: contract %v, oauthvocab %v", contract, oauthvocab.OnBehalfActorVocabulary())
	}
}
