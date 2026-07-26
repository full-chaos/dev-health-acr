package api

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestRuntimeDependencies_rejects_typed_nil_required_dependencies(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	valid := *app.runtime
	valid.ReadinessChecks = exactRuntimeChecks()
	var nilCredentials *storage.CredentialLifecycle
	var nilAudit *memory.AuditStore
	var nilEntitlement *typedNilEntitlement
	var nilAssembler *contextpacket.Assembler
	var nilEvidence *typedNilEvidence
	var nilDevices *typedNilDeviceAuthorizationStore
	var nilLimiter *typedNilDeviceAuthorizationLimiter
	tests := []struct {
		name   string
		mutate func(*RuntimeDependencies)
	}{
		{name: "credentials", mutate: func(runtime *RuntimeDependencies) { runtime.Credentials = nilCredentials }},
		{name: "audit", mutate: func(runtime *RuntimeDependencies) { runtime.Audit = nilAudit }},
		{name: "entitlement", mutate: func(runtime *RuntimeDependencies) { runtime.Entitlements = nilEntitlement }},
		{name: "assembler", mutate: func(runtime *RuntimeDependencies) { runtime.Assembler = nilAssembler }},
		{name: "evidence", mutate: func(runtime *RuntimeDependencies) { runtime.Evidence = nilEvidence }},
		{name: "device authorizations", mutate: func(runtime *RuntimeDependencies) { runtime.DeviceAuthorizations = nilDevices }},
		{name: "device authorization limiter", mutate: func(runtime *RuntimeDependencies) { runtime.DeviceAuthorizationLimiter = nilLimiter }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := valid
			test.mutate(&runtime)

			// When
			err := runtime.validate()

			// Then
			if err == nil {
				t.Fatalf("typed-nil %s dependency was accepted", test.name)
			}
		})
	}
}

func TestRuntimeDependencies_requires_exact_named_readiness_checks(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	runtime := *app.runtime
	runtime.ReadinessChecks = []ReadinessCheck{
		CheckFunc{CheckName: "credential_store"},
		CheckFunc{CheckName: "entitlement_provider"},
		CheckFunc{CheckName: "evidence_store"},
	}

	// When
	err := runtime.validate()

	// Then
	if err == nil {
		t.Fatal("runtime accepted readiness checks without exact hosted dependency names")
	}
}

func TestRuntimeDependencies_allows_additional_readiness_checks_beyond_required_three(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	runtime := *app.runtime
	runtime.ReadinessChecks = []ReadinessCheck{
		CheckFunc{CheckName: "postgres"},
		CheckFunc{CheckName: "clickhouse"},
		CheckFunc{CheckName: "entitlement"},
		CheckFunc{CheckName: "packet_purge_loop"},
	}

	// When
	err := runtime.validate()

	// Then
	if err != nil {
		t.Fatalf("runtime rejected an additional readiness check beyond the required three: %v", err)
	}
}

func TestRuntimeDependencies_rejects_duplicate_required_readiness_check_names(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	runtime := *app.runtime
	runtime.ReadinessChecks = []ReadinessCheck{
		CheckFunc{CheckName: "postgres"},
		CheckFunc{CheckName: "postgres"},
		CheckFunc{CheckName: "clickhouse"},
		CheckFunc{CheckName: "entitlement"},
	}

	// When
	err := runtime.validate()

	// Then
	if err == nil {
		t.Fatal("runtime accepted a duplicate required readiness check name")
	}
}

func TestRuntimeDependencies_rejects_typed_nil_readiness_check_without_panic(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	runtime := *app.runtime
	var nilCheck *typedNilCheck
	runtime.ReadinessChecks = []ReadinessCheck{CheckFunc{CheckName: "postgres"}, nilCheck, CheckFunc{CheckName: "entitlement"}}

	// When
	err := runtime.validate()

	// Then
	if err == nil {
		t.Fatal("runtime accepted a typed-nil readiness check")
	}
}

func TestRuntimeDependencies_rejects_typed_nil_optional_episode_creator(t *testing.T) {
	// Given
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	runtime := *app.runtime
	var nilEpisodes *typedNilEpisodeCreator
	runtime.Episodes = nilEpisodes

	// When
	err := runtime.validate()

	// Then
	if err == nil {
		t.Fatal("runtime accepted a typed-nil optional episode creator")
	}
}

func TestRuntimeDependencies_rejectsMissingDeviceAuthorizationControls(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RuntimeDependencies)
	}{
		{name: "verification URL", mutate: func(runtime *RuntimeDependencies) { runtime.DeviceVerificationURL = "" }},
		{name: "malformed verification URL", mutate: func(runtime *RuntimeDependencies) { runtime.DeviceVerificationURL = "/verify" }},
		{name: "limiter", mutate: func(runtime *RuntimeDependencies) { runtime.DeviceAuthorizationLimiter = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
			runtime := *app.runtime
			test.mutate(&runtime)

			// When
			err := runtime.validate()

			// Then
			if err == nil {
				t.Fatalf("runtime accepted missing %s", test.name)
			}
		})
	}
}

func exactRuntimeChecks() []ReadinessCheck {
	return []ReadinessCheck{
		CheckFunc{CheckName: "postgres"},
		CheckFunc{CheckName: "clickhouse"},
		CheckFunc{CheckName: "entitlement"},
	}
}

type typedNilEntitlement struct{}

func (*typedNilEntitlement) HasEntitlement(context.Context, string, string) (bool, error) {
	return false, nil
}

type typedNilEvidence struct{}

func (*typedNilEvidence) ResolveScope(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, nil
}
func (*typedNilEvidence) ContextForTask(context.Context, storage.Principal, contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	return storage.EvidenceBundle{}, nil
}
func (*typedNilEvidence) ResolveEvidence(context.Context, storage.Principal, string) (contractsv1.ExpandedEvidence, error) {
	return contractsv1.ExpandedEvidence{}, nil
}

type typedNilCheck struct{}

func (*typedNilCheck) Name() string                { return "clickhouse" }
func (*typedNilCheck) Check(context.Context) error { return nil }

type typedNilEpisodeCreator struct{}

func (*typedNilEpisodeCreator) Create(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	return contractsv1.AgentEpisode{}, false, nil
}

type typedNilDeviceAuthorizationStore struct{}

func (*typedNilDeviceAuthorizationStore) Create(context.Context, storage.DeviceAuthorizationCreateInput) (storage.DeviceAuthorization, error) {
	return storage.DeviceAuthorization{}, nil
}
func (*typedNilDeviceAuthorizationStore) GetByDeviceCodeHash(context.Context, storage.DeviceCodeHash) (storage.DeviceAuthorization, error) {
	return storage.DeviceAuthorization{}, nil
}
func (*typedNilDeviceAuthorizationStore) GetByUserCodeHash(context.Context, storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	return storage.DeviceAuthorization{}, nil
}
func (*typedNilDeviceAuthorizationStore) Poll(context.Context, storage.DeviceCodeHash) (storage.DeviceAuthorization, error) {
	return storage.DeviceAuthorization{}, nil
}
func (*typedNilDeviceAuthorizationStore) Approve(context.Context, storage.UserCodeHash, storage.DeviceAuthorizationGrant) (storage.DeviceAuthorization, error) {
	return storage.DeviceAuthorization{}, nil
}
func (*typedNilDeviceAuthorizationStore) Deny(context.Context, storage.UserCodeHash) (storage.DeviceAuthorization, error) {
	return storage.DeviceAuthorization{}, nil
}
func (*typedNilDeviceAuthorizationStore) Redeem(context.Context, storage.DeviceCodeHash, storage.CredentialCreateInput) (contractsv1.ClientCredential, error) {
	return contractsv1.ClientCredential{}, nil
}

type typedNilDeviceAuthorizationLimiter struct{}

func (*typedNilDeviceAuthorizationLimiter) AllowDeviceCreation(string) DeviceAuthorizationLimitDecision {
	return DeviceAuthorizationLimitDecision{}
}
func (*typedNilDeviceAuthorizationLimiter) AllowTokenRequest(string) DeviceAuthorizationLimitDecision {
	return DeviceAuthorizationLimitDecision{}
}
func (*typedNilDeviceAuthorizationLimiter) AllowApprovalAttempt(string, storage.UserCodeHash) DeviceAuthorizationLimitDecision {
	return DeviceAuthorizationLimitDecision{}
}
