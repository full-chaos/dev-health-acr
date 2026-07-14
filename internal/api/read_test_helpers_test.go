package api

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

const hostedTestRepository = "example-org/widget-service"

func newHostedTestApp(t *testing.T, provider CapabilitiesProvider, hooks *observability.Hooks, scopes []string, entitlements EntitlementProvider, store storage.EvidenceStore) (*App, string) {
	t.Helper()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	token := issueScopedCredential(t, credentials, audit, now, scopes, []string{hostedTestRepository})
	if hooks == nil {
		value := observability.NewHooks(nil, nil)
		hooks = &value
	}
	if provider == nil {
		provider = StaticCapabilitiesProvider{Now: func() time.Time { return now }, Value: hostedCapabilities()}
	}
	if entitlements == nil {
		entitlements = EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	}
	if store == nil {
		store = seededEvaluationStore(t, "org_1", observability.NewEvidenceExpansionObserver(*hooks))
	}
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 1, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assembler := contextpacket.NewAssembler(store, contextpacket.Options{
		Now: func() time.Time { return now }, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0",
		Observer: observability.NewAssemblyObserver(*hooks), StoreBackend: contextpacket.StoreBackendMemory,
	})
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: provider, Observability: hooks, Limits: manager, Now: func() time.Time { return now },
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: entitlements, Assembler: assembler, Evidence: store,
			ReadinessChecks: []ReadinessCheck{
				CheckFunc{CheckName: "credential_store"}, CheckFunc{CheckName: "entitlement_provider"}, CheckFunc{CheckName: "evidence_store"},
			},
		},
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	return app, token
}

func newMemoryCredentialLifecycle(t *testing.T, audit *memory.AuditStore, now time.Time) *storage.CredentialLifecycle {
	t.Helper()
	store, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func hostedCapabilities() contractsv1.Capabilities {
	return contractsv1.Capabilities{
		SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: "test", MinimumSidecarVersion: "0.1.0",
		SupportedSchemaVersions: []string{contractsv1.ContextPacketRequestSchema, contractsv1.ContextPacketSchema, contractsv1.EvidenceRefSchema, contractsv1.ExpandedEvidenceSchema},
		EnabledTools:            []string{}, Entitlements: contractsv1.CapabilityEntitlements{}, Permissions: contractsv1.CapabilityPermissions{},
		Limits: contractsv1.CapabilityLimits{MaxItems: 50, MaxOutputTokens: 16_000, MaxSerializedBytes: 1 << 20, RequestsPerMinute: 100},
	}
}
