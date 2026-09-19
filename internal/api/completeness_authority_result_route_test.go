package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// newCompletenessAuthorityTestApp is
// newContextFabricTestAppWithResultsAndLogs (context_fabric_routes_test.go)
// with ONE difference: it takes the ServerCompletenessAuthorityEnabled
// value explicitly, since the shared helper hardcodes AppConfig and this is
// the one route-level test that needs to vary it.
func newCompletenessAuthorityTestApp(t *testing.T, results contextfabric.InvestigationResultStore, authorityEnabled bool) (*App, string, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	token := issueScopedCredential(t, credentials, audit, now, []string{auth.ScopeContextRead}, []string{hostedTestRepository})
	entitlements := EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil })
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, PerOrgConcurrency: 4, Policies: limits.PolicySet{
		Auth:    limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100},
		Context: limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	provider := StaticCapabilitiesProvider{Now: func() time.Time { return now }, Value: hostedCapabilities()}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second, ServerCompletenessAuthorityEnabled: authorityEnabled}, Dependencies{
		Capabilities: provider, Limits: manager, Now: func() time.Time { return now },
		Runtime: &RuntimeDependencies{
			Credentials: credentials, Audit: audit, Entitlements: entitlements,
			Assembler: noopAssembler{}, Evidence: noopEvidenceStore{},
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: NewDeviceAuthorizationLimiter(ClockFunc(func() time.Time { return now })),
			ReadinessChecks:            exactRuntimeChecks(),
			InvestigationResults:       results,
			StoredResultGate:           testStoredResultGate(nil),
		},
	}, testLogger(logs))
	if err != nil {
		t.Fatal(err)
	}
	return app, token, logs
}

// degradedByIDResult is a stored, immutable result whose model status is
// `complete` but whose own outcome rows say `degraded` -- the shape a
// by-id read must not silently re-serve unmeasured.
func degradedByIDResult() contractsv1.ContextFabricInvestigationResult {
	result := validContextFabricInvestigationResult()
	result.ResultID = "result_byid_degraded01"
	result.Completeness.Outcomes = []contractsv1.ContextFabricPlanRequirementOutcomeRow{{
		Stage:         contractsv1.ContextFabricOutcomeStageAssembledResult,
		Requirement:   "evidence/subject/team",
		Obligation:    "evidence",
		Outcome:       contractsv1.ContextFabricRequirementUnavailable,
		Impact:        contractsv1.ContextFabricAnswerImpactDimension,
		CauseCoverage: contractsv1.ContextFabricCoverageDetailFactUnconfigured,
		CauseObserved: true,
	}}
	result.Completeness.State = contractsv1.DeriveContextFabricAnswerCompletenessState(result.Completeness.Outcomes)
	return result
}

// TestByIDRoute_CompletenessAuthorityIsMeasured pins that a stored row read
// by id is measured too: this route reads storage directly and never
// reaches the engine's finalizeServed, so the route must take its own
// measurement against the row's own outcome rows.
func TestByIDRoute_CompletenessAuthorityIsMeasured(t *testing.T) {
	result := degradedByIDResult()
	app, token, logs := newCompletenessAuthorityTestApp(t, legacyResultStore{result: result}, false)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, result.ResultID))

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	const line = "context fabric completeness authority"
	if !strings.Contains(logs.String(), line) {
		t.Fatalf("the by-id route must emit the completeness authority measurement, got: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"server_state":"degraded"`) {
		t.Fatalf("the measurement must name the derived degraded state: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"disagreed":true`) {
		t.Fatalf("the measurement must disagree (model says complete, outcomes say degraded): %s", logs.String())
	}
}

// TestByIDRoute_CompletenessAuthorityCorrectsWhenEnabled proves the flip
// reaches a stored row re-evaluated LIVE against the deployment's current
// configuration, not whatever was true when the row was first saved.
func TestByIDRoute_CompletenessAuthorityCorrectsWhenEnabled(t *testing.T) {
	result := degradedByIDResult()
	app, token, _ := newCompletenessAuthorityTestApp(t, legacyResultStore{result: result}, true)

	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, investigationResultRequest(t, token, result.ResultID))

	if recorder.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"status":"degraded"`) {
		t.Fatalf("the served body must carry the corrected status when the knob is enabled: %s", recorder.Body.String())
	}
	// The flip changes Status, which the terminal reason is derived FROM --
	// decode the actual served (canonical-view) document and validate it
	// as a whole, not just the one field the flip wrote directly.
	var served contractsv1.ContextFabricInvestigationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &served); err != nil {
		t.Fatalf("served body did not decode as a canonical result: %v (body %s)", err, recorder.Body.String())
	}
	if want := contractsv1.ContextFabricTerminalReasonUndisclosed; served.Completeness.TerminalReason != want {
		t.Fatalf("served.Completeness.TerminalReason = %q, want %q (no Coverage/Limitations/Warnings disclosure on this fixture)", served.Completeness.TerminalReason, want)
	}
	if err := served.Validate(); err != nil {
		t.Fatalf("a flipped served document must still validate as a whole: %v", err)
	}
}
