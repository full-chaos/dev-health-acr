package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestReusedResultNamesItsInterpreter serves stored results through
// Investigate's reuse return. A row stored before the provenance fields
// existed names the service as its interpreter and keeps its model identity
// absent, because the row does not record which model interpreted. A row that
// carries both fields is served with them unchanged. A row whose
// interpretation version is the unwired placeholder records no interpretation
// and is served without a source.
func TestReusedResultNamesItsInterpreter(t *testing.T) {
	t.Parallel()

	const storedIdentity = "test-provider/interpret-model"
	for _, tc := range []struct {
		name         string
		stored       VersionSet
		wantSource   InterpretationSource
		wantIdentity string
	}{
		{name: "row stored before the provenance fields existed", wantSource: InterpretationSourceServer},
		{name: "row that records no interpretation", stored: VersionSet{InterpretationVersion: unwiredVersion}},
		{
			name:       "row stored with its provenance",
			stored:     VersionSet{InterpretationSource: InterpretationSourceServer, InterpretationModelIdentity: storedIdentity},
			wantSource: InterpretationSourceServer, wantIdentity: storedIdentity,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project, candidate := reusableCandidate()
			candidate.Versions.InterpretationSource = tc.stored.InterpretationSource
			candidate.Versions.InterpretationModelIdentity = tc.stored.InterpretationModelIdentity
			if tc.stored.InterpretationVersion != "" {
				candidate.Versions.InterpretationVersion = tc.stored.InterpretationVersion
			}
			if err := candidate.Validate(); err != nil {
				t.Fatalf("stored fixture is not a valid result: %v", err)
			}
			engine := mustReuseTestEngine(t, EngineDependencies{
				Graph:   graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
				Results: &resultStoreStub{},
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			})

			served, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if !served.Reused || served.ResultID != candidate.ResultID {
				t.Fatalf("Reused = %v ResultID = %q, want the stored row %q served from reuse", served.Reused, served.ResultID, candidate.ResultID)
			}
			if got := served.Versions; got.InterpretationSource != tc.wantSource || got.InterpretationModelIdentity != tc.wantIdentity {
				t.Fatalf("served interpretation_source = %q interpretation_model_identity = %q, want %q and %q",
					got.InterpretationSource, got.InterpretationModelIdentity, tc.wantSource, tc.wantIdentity)
			}
			if err := ValidateResult(served); err != nil {
				t.Fatalf("served result fails its own contract: %v", err)
			}
		})
	}
}
