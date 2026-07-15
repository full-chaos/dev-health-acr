package hosted

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/config"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestOpen_constructs_checks_then_closes_in_reverse_order(t *testing.T) {
	// Given
	events := []string{}
	request := testBuildRequest(t, &events, "")
	request.config.EnableEpisodeWriteback = true

	// When
	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := runtime.Close()

	// Then
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	want := []string{
		"postgres.open", "clickhouse.open", "entitlement.open", "episode.new",
		"postgres.check", "clickhouse.check", "entitlement.check",
		"entitlement.close", "clickhouse.close", "postgres.close",
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	checks := runtime.Dependencies.Runtime.ReadinessChecks
	if len(checks) != 3 || checks[0].Name() != "postgres" || checks[1].Name() != "clickhouse" || checks[2].Name() != "entitlement" {
		t.Fatalf("readiness checks = %#v, want postgres/clickhouse/entitlement", checks)
	}
	if runtime.Dependencies.Runtime.Episodes == nil {
		t.Fatal("episode service is nil when explicitly enabled")
	}
	if runtime.Dependencies.UsageTelemetry == nil {
		t.Fatal("credential usage telemetry is not lifecycle-owned by the hosted runtime")
	}
}

func TestRuntime_Close_isIdempotentAndDoesNotRepeatCloseCalls(t *testing.T) {
	// Given
	events := []string{}
	request := testBuildRequest(t, &events, "")
	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	// When
	firstErr := runtime.Close()
	secondErr := runtime.Close()

	// Then
	if firstErr != nil || secondErr != nil {
		t.Fatalf("Close() = (%v, %v), want (nil, nil)", firstErr, secondErr)
	}
	closeEvents := 0
	for _, event := range events {
		if event == "entitlement.close" || event == "clickhouse.close" || event == "postgres.close" {
			closeEvents++
		}
	}
	if closeEvents != 3 {
		t.Fatalf("close events = %d after two Close() calls, want 3 (each closer runs exactly once)", closeEvents)
	}
}

func TestOpen_cleans_partial_resources_when_stage_fails(t *testing.T) {
	tests := []struct {
		stage string
		want  []string
	}{
		{stage: "postgres.open", want: []string{"postgres.open"}},
		{stage: "clickhouse.open", want: []string{"postgres.open", "clickhouse.open", "postgres.close"}},
		{stage: "entitlement.open", want: []string{"postgres.open", "clickhouse.open", "entitlement.open", "clickhouse.close", "postgres.close"}},
		{stage: "episode.new", want: []string{"postgres.open", "clickhouse.open", "entitlement.open", "episode.new", "entitlement.close", "clickhouse.close", "postgres.close"}},
		{stage: "postgres.check", want: []string{"postgres.open", "clickhouse.open", "entitlement.open", "episode.new", "postgres.check", "entitlement.close", "clickhouse.close", "postgres.close"}},
		{stage: "clickhouse.check", want: []string{"postgres.open", "clickhouse.open", "entitlement.open", "episode.new", "postgres.check", "clickhouse.check", "entitlement.close", "clickhouse.close", "postgres.close"}},
		{stage: "entitlement.check", want: []string{"postgres.open", "clickhouse.open", "entitlement.open", "episode.new", "postgres.check", "clickhouse.check", "entitlement.check", "entitlement.close", "clickhouse.close", "postgres.close"}},
	}
	for _, test := range tests {
		t.Run(test.stage, func(t *testing.T) {
			// Given
			events := []string{}
			request := testBuildRequest(t, &events, test.stage)
			request.config.EnableEpisodeWriteback = true

			// When
			runtime, err := open(context.Background(), request)

			// Then
			if err == nil || runtime != nil {
				t.Fatalf("runtime = %#v, error = %v; want construction failure", runtime, err)
			}
			if !reflect.DeepEqual(events, test.want) {
				t.Fatalf("events = %#v, want %#v", events, test.want)
			}
		})
	}
}

func testBuildRequest(t *testing.T, events *[]string, failAt string) buildRequest {
	t.Helper()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	postgres := postgresComponents{
		credentials: credentials,
		audit:       audit,
		packets:     memory.NewPacketStore(nowTime(now)),
		episodes:    memory.NewEpisodeStore(),
		check:       stageFunction(events, failAt, "postgres.check"),
		close:       stageCloseFunction(events, failAt, "postgres.close"),
	}
	clickhouse := clickHouseComponents{
		evidence: fakeEvidenceStore{},
		check:    stageFunction(events, failAt, "clickhouse.check"),
		close:    stageCloseFunction(events, failAt, "clickhouse.close"),
	}
	entitlement := &fakeEntitlement{events: events, failAt: failAt}
	return buildRequest{
		config:  testRuntimeConfig(t),
		options: Options{ServiceVersion: "test", Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), Now: nowTime(now)},
		factories: componentFactories{
			openPostgres: func(context.Context, config.Config, *slog.Logger) (postgresComponents, error) {
				*events = append(*events, "postgres.open")
				if failAt == "postgres.open" {
					return postgresComponents{}, errors.New("postgres failed")
				}
				return postgres, nil
			},
			openClickHouse: func(context.Context, clickHouseOpenRequest) (clickHouseComponents, error) {
				*events = append(*events, "clickhouse.open")
				if failAt == "clickhouse.open" {
					return clickHouseComponents{}, errors.New("clickhouse failed")
				}
				return clickhouse, nil
			},
			newEntitlement: func(config.Config) (entitlementChecker, error) {
				*events = append(*events, "entitlement.open")
				if failAt == "entitlement.open" {
					return nil, errors.New("entitlement failed")
				}
				return entitlement, nil
			},
			newEpisode: func(episodeServiceRequest) (api.EpisodeCreator, error) {
				*events = append(*events, "episode.new")
				if failAt == "episode.new" {
					return nil, errors.New("episode failed")
				}
				return fakeEpisodeCreator{}, nil
			},
		},
	}
}

func testRuntimeConfig(t *testing.T) config.Config {
	t.Helper()
	class := config.ClassLimitConfig{Window: time.Minute, Requests: 60}
	return config.Config{
		Environment: "test", RequireBackingStores: true, ClickHouseDSN: "clickhouse://configured", PostgresDSN: "postgres://configured",
		DevHealthEntitlementURL: "https://ops.example.test", DevHealthEntitlementTokenFile: "token", EvidenceIDActiveKID: "current",
		EvidenceIDKeys: map[string][]byte{"current": []byte("01234567890123456789012345678901")},
		MaxItems:       30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60,
		RequestControls: config.RequestControlsConfig{
			Auth: class, Context: class, Evidence: class, Snapshot: class, Episode: class,
			AuthFailures: 20, AuthTrackedKeys: 128, PerOrgConcurrency: 8, MaxTrackedOrganizations: 128, MaxCredentialsPerOrg: 128,
			StateRetention: time.Hour, ConcurrencyRetryAfter: time.Second, MaximumRetryAfter: time.Minute,
		},
	}
}

func stageFunction(events *[]string, failAt, name string) func(context.Context) error {
	return func(context.Context) error {
		*events = append(*events, name)
		if failAt == name {
			return errors.New("stage failed")
		}
		return nil
	}
}

func stageCloseFunction(events *[]string, failAt, name string) func() error {
	return func() error {
		*events = append(*events, name)
		if failAt == name {
			return errors.New("stage failed")
		}
		return nil
	}
}

func nowTime(now time.Time) func() time.Time { return func() time.Time { return now } }

type fakeEntitlement struct {
	events *[]string
	failAt string
}

func (f *fakeEntitlement) HasEntitlement(context.Context, string, string) (bool, error) {
	return true, nil
}
func (f *fakeEntitlement) Check(context.Context) error {
	*f.events = append(*f.events, "entitlement.check")
	if f.failAt == "entitlement.check" {
		return errors.New("entitlement check failed")
	}
	return nil
}
func (f *fakeEntitlement) Close() error {
	*f.events = append(*f.events, "entitlement.close")
	return nil
}

type fakeEvidenceStore struct{}

func (fakeEvidenceStore) ResolveScope(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, nil
}
func (fakeEvidenceStore) ContextForTask(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return storage.EvidenceBundle{}, nil
}
func (fakeEvidenceStore) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, nil
}

type fakeEpisodeCreator struct{}

func (fakeEpisodeCreator) Create(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	return contractsv1.AgentEpisode{}, false, nil
}
