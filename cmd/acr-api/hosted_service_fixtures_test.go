package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/full-chaos/dev-health-acr/internal/chfixture/startterm"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	postgresmigrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	hostedIntegrationOrg        = "11111111-1111-4111-8111-111111111111"
	hostedIntegrationRepository = "full-chaos/acr-integration"
)

type postgresFixture struct {
	container testcontainers.Container
	dsn       string
	token     string
	stopOnce  sync.Once
	stopErr   error
}

type postgresRuntimeAccess uint8

const (
	postgresRuntimeAccessComplete postgresRuntimeAccess = iota
	postgresRuntimeAccessMissingPacketInsert
)

func newPostgresFixture(t *testing.T, ctx context.Context) *postgresFixture {
	return newPostgresFixtureWithAccess(t, ctx, postgresRuntimeAccessComplete)
}

func newPostgresFixtureWithAccess(t *testing.T, ctx context.Context, access postgresRuntimeAccess) *postgresFixture {
	t.Helper()
	fixture := newUnmigratedPostgresFixture(t, ctx)
	database, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: fixture.dsn})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := postgresmigrations.Embedded()
	if err != nil {
		if closeErr := database.Close(); closeErr != nil {
			t.Logf("close PostgreSQL after migration loader failure: %v", closeErr)
		}
		t.Fatal(err)
	}
	if _, err := runner.Apply(ctx, database); err != nil {
		if closeErr := database.Close(); closeErr != nil {
			t.Logf("close PostgreSQL after migration failure: %v", closeErr)
		}
		t.Fatal(err)
	}
	audit, err := storagepostgres.NewAuditStore(database)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := storagepostgres.NewCredentialStore(database, audit)
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(credentials, auth.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Create(ctx, auth.CreateCredentialRequest{
		OrgID: hostedIntegrationOrg, Name: "hosted integration", RepositoryScopes: []string{hostedIntegrationRepository},
		Scopes: []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite}, CreatedBy: "integration-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimePassword := randomFixtureSecret(t)
	if _, err := database.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE acr_runtime LOGIN PASSWORD '%s'`, runtimePassword)); err != nil {
		t.Fatal(err)
	}
	for _, grant := range runtimeFixtureGrantStatements("acr_runtime", access) {
		if _, err := database.ExecContext(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	runtimeDSN, err := url.Parse(fixture.dsn)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDSN.User = url.UserPassword("acr_runtime", runtimePassword)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.token = issued.Token
	fixture.dsn = runtimeDSN.String()
	return fixture
}

// runtimeFixtureFeatures is the optional-feature set the real-binary fixture
// grants for: episode writeback, because TestHostedRuntime_real_binary_serves_and_fails_readiness_safely starts a
// second process with ACR_ENABLE_EPISODE_WRITEBACK=true on the same database.
// The workload exchange and browser class are not configured by
// hostedProcessEnvironment, so their rows are not granted.
var runtimeFixtureFeatures = map[string]bool{string(dbacl.EpisodeWriteback): true}

// runtimeFixtureGrantStatements is what the fixture applies to the runtime
// role: schema USAGE (the init script's job in a deployment) and the grants
// of internal/dbacl, the one source of record, as cmd/acr-migrate
// grant-runtime-acl applies them. The missing-packet-insert variant then
// revokes the write privileges on the packet snapshots so the readiness check
// refuses it.
func runtimeFixtureGrantStatements(role string, access postgresRuntimeAccess) []string {
	features := dbacl.Features{}
	for name, on := range runtimeFixtureFeatures {
		features[dbacl.Feature(name)] = on
	}
	statements := []string{`GRANT USAGE ON SCHEMA acr TO ` + dbacl.QuoteIdent(role)}
	for _, statement := range dbacl.GrantStatements(dbacl.QuoteIdent(role), features) {
		statements = append(statements, statement.SQL)
	}
	if access == postgresRuntimeAccessMissingPacketInsert {
		statements = append(statements, `REVOKE INSERT, UPDATE, DELETE ON TABLE acr.context_packet_snapshots FROM `+dbacl.QuoteIdent(role))
	}
	return statements
}

func newUnmigratedPostgresFixture(t *testing.T, ctx context.Context) *postgresFixture {
	t.Helper()
	chfixture.RequireContainers(t)
	// CHAOS-4855: pinned by digest (was a bare tag) so
	// TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX resolves this to the ghcr.io
	// mirror by digest, same as every other postgres:18-alpine pull in
	// this module.
	container, err := startterm.PostgresRun(ctx, "postgres:18-alpine@sha256:10a5c69a83035ab5239f5620d708520e8b28f5aa1f2b20143e6024532edadfb5",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		if terminateErr := container.Terminate(ctx); terminateErr != nil {
			t.Logf("terminate PostgreSQL after DSN failure: %v", terminateErr)
		}
		t.Fatal(err)
	}
	fixture := &postgresFixture{container: container, dsn: dsn}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := fixture.Stop(cleanupCtx); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func (f *postgresFixture) Stop(ctx context.Context) error {
	f.stopOnce.Do(func() { f.stopErr = f.container.Terminate(ctx) })
	return f.stopErr
}

// entitlementFixture is the Dev Health entitlement service and the auth-service workload-token exchange acr-api calls.
// The exchange (plain HTTP, as in a cluster) mints a workload token per request; the entitlement service (TLS) accepts
// only a token it minted since the last RotateToken.
type entitlementFixture struct {
	server      *httptest.Server
	exchange    *httptest.Server
	baseURL     string
	exchangeURL string
	caPath      string
	subjectPath string
	token       string
	tokenMu     sync.RWMutex
	accepted    map[string]bool
	stopOnce    sync.Once
}

const entitlementFixtureIssuer = "https://auth.example.test"

func TestEntitlementFixture_rotates_accepted_token_bytes(t *testing.T) {
	// Given
	fixture := newEntitlementFixture(t)
	oldToken := entitlementFixtureMint(t, fixture)

	// When
	fixture.RotateToken(t)
	newToken := entitlementFixtureMint(t, fixture)

	// Then
	if oldToken == newToken {
		t.Fatal("the exchange minted the same token twice")
	}
	if entitlementFixtureStatus(t, fixture, oldToken) != http.StatusUnauthorized {
		t.Fatal("old entitlement token remained accepted after rotation")
	}
	if entitlementFixtureStatus(t, fixture, newToken) != http.StatusOK {
		t.Fatal("rotated entitlement token was not accepted")
	}
	if entitlementFixtureStatus(t, fixture, randomFixtureSecret(t)) != http.StatusUnauthorized {
		t.Fatal("a token the exchange never minted was accepted")
	}
}

func entitlementFixtureMint(t *testing.T, fixture *entitlementFixture) string {
	t.Helper()
	response, err := http.PostForm(fixture.exchangeURL+"/internal/v1/token/exchange", url.Values{"audience": {"dev-health-api"}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var answer struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil || answer.AccessToken == "" {
		t.Fatalf("exchange answer: %v", err)
	}
	return answer.AccessToken
}

func entitlementFixtureStatus(t *testing.T, fixture *entitlementFixture, token string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, fixture.baseURL+"/api/v1/internal/acr/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := fixture.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func newEntitlementFixture(t *testing.T) *entitlementFixture {
	t.Helper()
	enc := base64.RawURLEncoding
	subject := enc.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + enc.EncodeToString([]byte(`{"sub":"acr-api"}`)) + "." + enc.EncodeToString([]byte("sig"))
	fixture := &entitlementFixture{accepted: map[string]bool{}, subjectPath: writeRestrictedFixture(t, "workload-subject", []byte(subject))}
	fixture.exchange = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		token := fixture.mint(t, request.PostForm.Get("audience"))
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","issued_token_type":"urn:ietf:params:oauth:token-type:jwt","expires_in":300}`, token); err != nil {
			t.Error(err)
		}
	}))
	fixture.exchangeURL = fixture.exchange.URL
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if !fixture.acceptsAuthorization(request.Header.Get("Authorization")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/internal/acr/health":
			if _, err := fmt.Fprint(w, `{"schema_version":"acr_service_health.v1","service":"dev-health-ops","status":"ok"}`); err != nil {
				t.Error(err)
			}
		case "/api/v1/internal/acr/entitlements/" + hostedIntegrationOrg:
			if _, err := fmt.Fprintf(w, `{"schema_version":"acr_entitlement.v1","org_id":%q,"agent_context_runtime":true}`, hostedIntegrationOrg); err != nil {
				t.Error(err)
			}
		default:
			http.NotFound(w, request)
		}
	}))
	certificate := server.Certificate()
	fixture.server = server
	fixture.baseURL = server.URL
	fixture.caPath = writeRestrictedFixture(t, "entitlement-ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	t.Cleanup(fixture.Stop)
	return fixture
}

// mint returns a workload-token.v1 shaped token for audience (the client checks the shape and claims, not the
// signature) and adds it to the accepted set.
func (f *entitlementFixture) mint(t *testing.T, audience string) string {
	enc := base64.RawURLEncoding
	now := time.Now().Unix()
	claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"prn_AAAAAAAAAAAAAAAAAAAAAA","principal_type":"workload","jti":%q,"iat":%d,"nbf":%d,"exp":%d}`,
		entitlementFixtureIssuer, audience, randomFixtureSecret(t), now-5, now-5, now+295)
	token := enc.EncodeToString([]byte(`{"alg":"EdDSA","typ":"workload+jwt","kid":"fixture"}`)) + "." + enc.EncodeToString([]byte(claims)) + "." + enc.EncodeToString(make([]byte, 64))
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	f.accepted[token] = true
	f.token = token
	return token
}

func (f *entitlementFixture) acceptsAuthorization(value string) bool {
	f.tokenMu.RLock()
	defer f.tokenMu.RUnlock()
	token, ok := strings.CutPrefix(value, "Bearer ")
	return ok && f.accepted[token]
}

// RotateToken refuses every token minted so far; acr-api's cached token is refused until it exchanges again.
func (f *entitlementFixture) RotateToken(t *testing.T) {
	t.Helper()
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	f.accepted = map[string]bool{}
}

func (f *entitlementFixture) Stop() {
	f.stopOnce.Do(func() {
		f.server.Close()
		f.exchange.Close()
	})
}
