package main

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
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
	database, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: fixture.dsn, AllowInsecure: true})
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
	grants := []string{
		`GRANT USAGE ON SCHEMA acr TO acr_runtime`,
		`GRANT SELECT ON acr.schema_migrations TO acr_runtime`,
		`GRANT SELECT, UPDATE ON acr.client_credentials TO acr_runtime`,
		`GRANT SELECT, INSERT ON acr.agent_episodes TO acr_runtime`,
		`GRANT INSERT ON acr.audit_events TO acr_runtime`,
	}
	if access == postgresRuntimeAccessMissingPacketInsert {
		grants = append(grants, `GRANT SELECT ON acr.context_packet_snapshots TO acr_runtime`)
	} else {
		grants = append(grants, `GRANT SELECT, INSERT, DELETE, UPDATE ON acr.context_packet_snapshots TO acr_runtime`)
	}
	for _, grant := range grants {
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

func newUnmigratedPostgresFixture(t *testing.T, ctx context.Context) *postgresFixture {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
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

type entitlementFixture struct {
	server    *httptest.Server
	baseURL   string
	caPath    string
	token     string
	tokenPath string
	tokenMu   sync.RWMutex
	stopOnce  sync.Once
}

func TestEntitlementFixture_rotates_accepted_token_bytes(t *testing.T) {
	// Given
	fixture := newEntitlementFixture(t)
	oldToken := fixture.token

	// When
	newToken := fixture.RotateToken(t)

	// Then
	if entitlementFixtureStatus(t, fixture, oldToken) != http.StatusUnauthorized {
		t.Fatal("old entitlement token remained accepted after rotation")
	}
	if entitlementFixtureStatus(t, fixture, newToken) != http.StatusOK {
		t.Fatal("rotated entitlement token was not accepted")
	}
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
	token := randomFixtureSecret(t)
	fixture := &entitlementFixture{token: token, tokenPath: writeRestrictedFixture(t, "entitlement-token", []byte(token))}
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

func (f *entitlementFixture) acceptsAuthorization(value string) bool {
	f.tokenMu.RLock()
	defer f.tokenMu.RUnlock()
	return value == "Bearer "+f.token
}

func (f *entitlementFixture) RotateToken(t *testing.T) string {
	t.Helper()
	rotated := randomFixtureSecret(t)
	directory := filepath.Dir(f.tokenPath)
	temporary, err := os.CreateTemp(directory, ".entitlement-token-")
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		t.Fatal(err)
	}
	if _, err := temporary.WriteString(rotated); err != nil {
		temporary.Close()
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	if err := os.Rename(temporaryPath, f.tokenPath); err != nil {
		t.Fatal(err)
	}
	f.token = rotated
	return rotated
}

func (f *entitlementFixture) Stop() {
	f.stopOnce.Do(f.server.Close)
}
