package contextfabric

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestFactCapabilityWithoutDimensionFailsRegistration is CHAOS-4633's own
// acceptance criterion, verbatim: "Registry test: a FactKind without a
// Dimension fails CI." CHAOS-4468's deliverable 2 is that a new FactKind
// registered without a HealthDimension declaration is a build-time-visible
// error, not a silent gap in acr/docs' generated mapping table.
func TestFactCapabilityWithoutDimensionFailsRegistration(t *testing.T) {
	provider := &factProviderStub{capability: FactCapability{
		Kind: FactStatus, Name: "no-dimension", Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectProject},
		SubjectRoles:          []FactRole{FactRoleSubject},
		// Dimension deliberately omitted.
	}}
	_, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err == nil {
		t.Fatal("NewFactCapabilityRegistry() error = nil, want an error: a FactKind without a Dimension must fail registration")
	}
	if !strings.Contains(err.Error(), "dimension") {
		t.Errorf("error = %q, want it to name the missing dimension (CHAOS-4468)", err.Error())
	}
}

// TestFactCapabilityWithoutSubjectRolesFailsRegistration -- the SubjectRoles
// half of the same declaration (design doc §5.3): a capability that
// declares no role at all cannot be planned against.
func TestFactCapabilityWithoutSubjectRolesFailsRegistration(t *testing.T) {
	provider := &factProviderStub{capability: FactCapability{
		Kind: FactStatus, Name: "no-roles", Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectProject},
		Dimension:             HealthDimensionExecutionCompletion,
	}}
	_, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err == nil {
		t.Fatal("NewFactCapabilityRegistry() error = nil, want an error: a capability without SubjectRoles must fail registration")
	}
}

// TestFactCapabilityTablesMustBeDeclaredOnlyForSupportedSubjectKinds pins
// the per-subject-kind rule the design's own MetricsProvider example
// exists to prevent: a Tables entry for a subject kind the capability does
// not even support is a contradiction, not a widening.
func TestFactCapabilityTablesMustBeDeclaredOnlyForSupportedSubjectKinds(t *testing.T) {
	provider := &factProviderStub{capability: FactCapability{
		Kind: FactStatus, Name: "mismatched-tables", Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectProject},
		Dimension:             HealthDimensionExecutionCompletion,
		SubjectRoles:          []FactRole{FactRoleSubject},
		Tables: map[SubjectKind][]FactTableShape{
			SubjectTeam: {FactTableBreakdown}, // never declared supported above
		},
	}}
	_, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err == nil {
		t.Fatal("NewFactCapabilityRegistry() error = nil, want an error: a Tables entry for an unsupported subject kind must fail registration")
	}
}

// TestFactCapabilityRegistryRecordsFactTableDeclarationTelemetry is the
// wiring pin for RecordFactTableDeclaration (design doc §4.3): fired once
// per declared table a provider's result carries, at the registry's own
// receipt of that result -- BEFORE any renderer downstream sees it.
func TestFactCapabilityRegistryRecordsFactTableDeclarationTelemetry(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	table := FactTable{
		Shape:    FactTableBreakdown,
		Key:      []string{"team_id"},
		Measures: []string{"commits_count", "prs_merged"},
		Rows: []FactValueRow{
			{Fields: map[string]FactValue{"team_id": StringFactValue("team_a"), "commits_count": IntegerFactValue(3), "prs_merged": IntegerFactValue(1)}},
			{Fields: map[string]FactValue{"team_id": StringFactValue("team_b"), "commits_count": IntegerFactValue(5), "prs_merged": IntegerFactValue(2)}},
		},
	}
	provider := &factProviderStub{
		capability: FactCapability{
			Kind: FactMetrics, Name: "ops-metrics", Version: "v1",
			SupportedSubjectKinds: []SubjectKind{SubjectProject},
			Dimension:             HealthDimensionDeliveryFlow,
			SubjectRoles:          []FactRole{FactRoleSubject},
			Tables:                map[SubjectKind][]FactTableShape{SubjectProject: {FactTableBreakdown}},
		},
		result: FactProviderResult{
			State: SourceAvailable, Version: "v1",
			Facts: []CanonicalFact{{
				Kind: FactMetrics, Subject: project,
				Fields:         map[string]FactValue{"team_breakdown": TableFactValue(table)},
				SourceState:    SourceAvailable,
				EvidenceRefIDs: []string{"evidence_1"},
			}},
		},
	}
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{Logger: logger})
		if err != nil {
			t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
		}
		_, err = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_sink_test"}, canonicalFactRequest(project, FactMetrics))
		if err != nil {
			t.Fatalf("ReadFacts() error = %v", err)
		}
	})
	var found map[string]any
	for _, record := range records {
		if record["msg"] == "context fabric fact table declaration" {
			found = record
			break
		}
	}
	if found == nil {
		t.Fatalf("no fact table declaration record found in %v", records)
	}
	for key, want := range map[string]any{
		"org_id":        "org_sink_test",
		"kind":          string(FactMetrics),
		"shape":         string(FactTableBreakdown),
		"key_arity":     float64(1),
		"measure_count": float64(2),
	} {
		got, ok := found[key]
		if !ok {
			t.Errorf("record has no %q key -- an operator greps for this; a field that never reaches the sink is not telemetry", key)
			continue
		}
		if got != want {
			t.Errorf("record[%q] = %v, want %v", key, got, want)
		}
	}
}
