package main

import (
	"context"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestHostedRuntime_real_binary_serves_and_fails_readiness_safely(t *testing.T) {
	if os.Getenv("ACR_HOSTED_INTEGRATION") != "1" {
		t.Skip("set ACR_HOSTED_INTEGRATION=1 to run disposable hosted dependencies")
	}
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	binary := buildACRAPIBinary(t)
	assertMissingRuntimeConfigDoesNotListen(t, ctx, binary)
	clickhouse := newClickHouseFixture(t, ctx)
	assertNativeClickHouseFixtureIntegration(t, ctx, clickhouse)
	entitlement := newEntitlementFixture(t)
	unmigratedPostgres := newUnmigratedPostgresFixture(t, ctx)
	failedAddress := reserveAddress(t)
	failedEnvironment := hostedProcessEnvironment(hostedEnvironmentInput{address: failedAddress, postgres: unmigratedPostgres, clickhouse: clickhouse, entitlement: entitlement})
	assertAPIStartupFails(t, ctx, apiProcessRequest{binary: binary, environment: failedEnvironment})
	if err := unmigratedPostgres.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	insufficientPostgres := newPostgresFixtureWithAccess(t, ctx, postgresRuntimeAccessMissingPacketInsert)
	insufficientAddress := reserveAddress(t)
	insufficientEnvironment := hostedProcessEnvironment(hostedEnvironmentInput{address: insufficientAddress, postgres: insufficientPostgres, clickhouse: clickhouse, entitlement: entitlement})
	assertAPIStartupFails(t, ctx, apiProcessRequest{binary: binary, environment: insufficientEnvironment})
	if err := insufficientPostgres.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	postgres := newPostgresFixture(t, ctx)
	address := reserveAddress(t)
	environment := hostedProcessEnvironment(hostedEnvironmentInput{address: address, postgres: postgres, clickhouse: clickhouse, entitlement: entitlement})
	process := startAPIProcess(t, ctx, apiProcessRequest{binary: binary, environment: environment})
	apiClient := hostedAPI{client: &http.Client{Timeout: 30 * time.Second}, baseURL: "http://" + address, token: postgres.token}

	// When
	ready := apiClient.readiness(t)
	capabilities := apiClient.capabilities(t)
	packet := apiClient.contextPacket(t)

	// Then
	if ready.Status != "ready" || ready.checkStatus("postgres") != "ready" || ready.checkStatus("clickhouse") != "ready" || ready.checkStatus("entitlement") != "ready" {
		t.Fatalf("initial readiness = %#v", ready)
	}
	entitlement.RotateToken(t)
	ready = apiClient.readiness(t)
	if ready.checkStatus("entitlement") != "not_ready" {
		t.Fatalf("readiness with rejected pre-rotation application token = %#v", ready)
	}
	if err := process.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if output := process.Output(); strings.Contains(output, entitlement.token) {
		t.Fatal("acr-api output leaked a rotated entitlement token")
	}
	process = startAPIProcess(t, ctx, apiProcessRequest{binary: binary, environment: environment})
	ready = apiClient.readiness(t)
	if ready.Status != "ready" || ready.checkStatus("entitlement") != "ready" {
		t.Fatalf("readiness with rotated application token = %#v", ready)
	}
	if !capabilities.Entitlements.AgentContextRuntime || !capabilities.Permissions.ContextRead || !capabilities.Permissions.EvidenceRead {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if slices.Contains(capabilities.EnabledTools, "record_episode") {
		t.Fatalf("episode tool enabled by default: %#v", capabilities.EnabledTools)
	}
	if packet.Status == contractsv1.PacketDegraded || len(packet.Items) == 0 || len(packet.Items[0].EvidenceRefIDs) == 0 {
		t.Fatalf("context packet did not contain seeded evidence: %#v", packet)
	}
	evidence := apiClient.evidence(t, packet.Items[0].EvidenceRefIDs[0])
	if evidence.Availability != contractsv1.EvidenceAvailable || evidence.Evidence.EvidenceRefID == "" {
		t.Fatalf("expanded evidence = %#v", evidence)
	}
	enabledAddress := reserveAddress(t)
	enabledEnvironment := maps.Clone(environment)
	enabledEnvironment["ACR_ADDR"] = enabledAddress
	enabledEnvironment["ACR_ENABLE_EPISODE_WRITEBACK"] = "true"
	enabledProcess := startAPIProcess(t, ctx, apiProcessRequest{binary: binary, environment: enabledEnvironment})
	enabledAPI := hostedAPI{client: &http.Client{Timeout: 30 * time.Second}, baseURL: "http://" + enabledAddress, token: postgres.token}
	enabledCapabilities := enabledAPI.capabilities(t)
	if !enabledCapabilities.Permissions.EpisodeWrite || !slices.Contains(enabledCapabilities.EnabledTools, "record_episode") {
		t.Fatalf("explicit episode writeback was not enabled: %#v", enabledCapabilities)
	}
	if err := enabledProcess.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	if err := clickhouse.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	ready = apiClient.readiness(t)
	if ready.checkStatus("clickhouse") != "not_ready" || ready.checkStatus("postgres") != "ready" || ready.checkStatus("entitlement") != "ready" {
		t.Fatalf("readiness after ClickHouse loss = %#v", ready)
	}
	if err := postgres.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	ready = apiClient.readiness(t)
	if ready.checkStatus("postgres") != "not_ready" {
		t.Fatalf("readiness after PostgreSQL loss = %#v", ready)
	}
	entitlement.Stop()
	ready = apiClient.readiness(t)
	if ready.checkStatus("entitlement") != "not_ready" || ready.Status != "not_ready" {
		t.Fatalf("readiness after entitlement loss = %#v", ready)
	}
	select {
	case <-process.done:
		t.Fatalf("acr-api crashed after dependency loss: %v", process.WaitError())
	default:
	}
	if output := process.Output(); strings.Contains(output, postgres.dsn) || strings.Contains(output, clickhouse.dsn) || strings.Contains(output, entitlement.token) {
		t.Fatal("acr-api output leaked a dependency secret")
	}
	if err := process.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertNativeClickHouseFixtureIntegration(t *testing.T, ctx context.Context, fixture *clickHouseFixture) {
	t.Helper()
	command := exec.CommandContext(ctx, "go", "test", "../../internal/runtime/clickhouse", "-run", "^(TestIntegrationClient_native_readonly_fixture_is_not_skipped|TestIntegrationSourceExecutor_(filters_and_deduplicates_before_read_cap|preserves_ranked_provenance_before_read_cap))$", "-count=1", "-v")
	command.Env = mergedEnvironment(map[string]string{
		"ACR_CLICKHOUSE_INTEGRATION_DSN":             fixture.dsn,
		"ACR_CLICKHOUSE_INTEGRATION_CA_FILE":         fixture.caPath,
		"ACR_CLICKHOUSE_INTEGRATION_ISOLATED":        "1",
		"ACR_CLICKHOUSE_INTEGRATION_REQUIRED":        "1",
		"ACR_CLICKHOUSE_INTEGRATION_TLS_SERVER_NAME": "localhost",
	})
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("mandatory native ClickHouse fixture integration failed: %v", err)
	}
	if strings.Contains(string(output), "--- SKIP:") {
		t.Fatal("mandatory native ClickHouse fixture integration was skipped")
	}
}

type hostedEnvironmentInput struct {
	address     string
	postgres    *postgresFixture
	clickhouse  *clickHouseFixture
	entitlement *entitlementFixture
}

func hostedProcessEnvironment(input hostedEnvironmentInput) map[string]string {
	return map[string]string{
		"ACR_ENVIRONMENT": "test", "ACR_REQUIRE_BACKING_STORES": "true", "ACR_ADDR": input.address,
		"ACR_ALLOW_INSECURE_POSTGRES": "true",
		"ACR_POSTGRES_DSN":            input.postgres.dsn, "ACR_CLICKHOUSE_DSN": input.clickhouse.dsn,
		"ACR_EVIDENCE_ID_ACTIVE_KID": "current", "ACR_EVIDENCE_ID_KEYS": "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL": input.entitlement.baseURL, "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": input.entitlement.tokenPath,
		"ACR_DEVICE_VERIFICATION_URL":          "https://verify.example.test/device",
		"ACR_DEV_HEALTH_ENTITLEMENT_CA_BUNDLE": input.entitlement.caPath, "ACR_DEV_HEALTH_ENTITLEMENT_TIMEOUT": "2s",
		"ACR_CLICKHOUSE_CA_BUNDLE": input.clickhouse.caPath, "ACR_ENABLE_EPISODE_WRITEBACK": "false",
		"ACR_POSTGRES_CONNECTION_KIND": "direct",
	}
}

func assertMissingRuntimeConfigDoesNotListen(t *testing.T, ctx context.Context, binary string) {
	t.Helper()
	for _, missing := range []string{
		"ACR_POSTGRES_DSN", "ACR_CLICKHOUSE_DSN", "ACR_EVIDENCE_ID_ACTIVE_KID", "ACR_EVIDENCE_ID_KEYS",
		"ACR_DEV_HEALTH_ENTITLEMENT_URL", "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE", "ACR_DEVICE_VERIFICATION_URL",
	} {
		t.Run("missing_"+missing, func(t *testing.T) {
			address := reserveAddress(t)
			environment := map[string]string{
				"ACR_ENVIRONMENT": "test", "ACR_REQUIRE_BACKING_STORES": "true", "ACR_ADDR": address,
				"ACR_POSTGRES_DSN": "postgres://configured", "ACR_CLICKHOUSE_DSN": "clickhouse://configured",
				"ACR_EVIDENCE_ID_ACTIVE_KID": "current", "ACR_EVIDENCE_ID_KEYS": "current=MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
				"ACR_DEV_HEALTH_ENTITLEMENT_URL": "https://ops.example.test", "ACR_DEV_HEALTH_ENTITLEMENT_TOKEN_FILE": "/run/secrets/ops-token",
				"ACR_DEVICE_VERIFICATION_URL":  "https://verify.example.test/device",
				"ACR_POSTGRES_CONNECTION_KIND": "direct",
			}
			environment[missing] = ""
			command := exec.CommandContext(ctx, binary, "serve")
			command.Env = mergedEnvironment(environment)
			output, err := command.CombinedOutput()
			if err == nil || strings.Contains(string(output), "HTTP server started") {
				t.Fatalf("missing %s started a listener: error=%v output=%s", missing, err, output)
			}
		})
	}
}
