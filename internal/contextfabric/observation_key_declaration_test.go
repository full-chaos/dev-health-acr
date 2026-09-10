package contextfabric

import (
	"strings"
	"testing"
)

// TestFactCapabilityObservationKeyMustBeDeclaredOnlyForSupportedSubjectKinds
// pins the new Validate clause the same way
// TestFactCapabilityTablesMustBeDeclaredOnlyForSupportedSubjectKinds already
// pins Tables': an ObservationKey entry for a subject kind the capability
// does not even support is a contradiction, not a widening, and must fail
// registration rather than silently describe a pairing no read can ever
// produce.
func TestFactCapabilityObservationKeyMustBeDeclaredOnlyForSupportedSubjectKinds(t *testing.T) {
	provider := &factProviderStub{capability: FactCapability{
		Kind: FactStatus, Name: "mismatched-observation-key", Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectProject},
		Dimension:             HealthDimensionExecutionCompletion,
		SubjectRoles:          []FactRole{FactRoleSubject},
		ObservationKey: map[SubjectKind][]ObservationKey{
			SubjectTeam: {"shared_observation"}, // never declared supported above
		},
	}}
	_, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err == nil {
		t.Fatal("NewFactCapabilityRegistry() error = nil, want an error: an ObservationKey entry for an unsupported subject kind must fail registration")
	}
	if !strings.Contains(err.Error(), "observation key") {
		t.Errorf("error = %q, want it to name the observation key clause", err.Error())
	}
}

// TestFactCapabilityObservationKeyOnASupportedSubjectKindValidates proves the
// positive side of the same clause: a declaration naming only subject kinds
// the capability actually supports passes Validate cleanly, so the clause
// added above rejects exactly the unsupported case and nothing more.
func TestFactCapabilityObservationKeyOnASupportedSubjectKindValidates(t *testing.T) {
	capability := FactCapability{
		Kind: FactHealth, Name: "ops-health", Version: "v1",
		SupportedSubjectKinds: []SubjectKind{SubjectRepository, SubjectTeam},
		Dimension:             HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []FactRole{FactRoleSubject},
		ObservationKey: map[SubjectKind][]ObservationKey{
			SubjectRepository: {"shared_observation_repository"},
			SubjectTeam:       {"shared_observation_team"},
		},
	}
	if err := capability.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil: an ObservationKey declared only for supported subject kinds must pass", err)
	}
}
