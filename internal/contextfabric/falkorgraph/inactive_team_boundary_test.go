package falkorgraph

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type boundaryProvider struct{}

func (boundaryProvider) Capability() contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: contextfabric.FactHealth, Dimension: contextfabric.HealthDimensionCodeOwnershipRisk, RequiresEvidence: true, Name: "boundary_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contextfabric.SubjectTeam},
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "commits_count", Type: contextfabric.FactFieldInteger}},
	}
}

func (boundaryProvider) ReadFacts(context.Context, storage.Principal, contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
}

// The read_facts answer for an explicit inactive team id, through the real
// adapter and the real subject gate, read as the wire JSON a client receives.
func TestReadFactsAnswersTeamInactiveWithTheActiveTwinForAnExplicitInactiveTeam(t *testing.T) {
	fake := &fakeConn{queryFunc: func(_ context.Context, _, _ string, params map[string]interface{}, _ bool) ([]row, error) {
		if _, byKey := params["targets"]; byKey {
			return []row{boundaryTeamRow("team:platform", false, "acme/api")}, nil
		}
		return []row{boundaryTeamRow("team:jira:platform", true, "acme/api")}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{boundaryProvider{}}, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reader := directread.NewFactsReader(directread.NewSubjectGate(adapter, nil), directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	response, err := reader.Read(ctx, storage.Principal{OrgID: "org-1"}, directread.FactsRequest{
		Kinds: []string{"health"}, Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: "team:platform"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Request struct {
			SubjectsRefused []map[string]string `json:"subjects_refused"`
		} `json:"request"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Request.SubjectsRefused) != 1 {
		t.Fatalf("subjects_refused = %v", wire.Request.SubjectsRefused)
	}
	got := wire.Request.SubjectsRefused[0]
	if got["answer"] != "team_inactive" || got["active_canonical_id"] != "team:jira:platform" {
		t.Fatalf("refused subject = %v, want answer team_inactive with active_canonical_id team:jira:platform", got)
	}
}

func boundaryTeamRow(id string, active bool, repos ...string) row {
	return lookupRow("org-1", "team", id, "Platform", map[string]interface{}{propPropertyPrefix + "is_active": active, propAuthzRepos: repos})
}
