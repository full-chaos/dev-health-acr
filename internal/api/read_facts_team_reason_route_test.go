package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type teamReachGraph struct{ admitAllGraph }

func (teamReachGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	out := make([][]string, len(subjects))
	for i := range out {
		out[i] = []string{hostedTestRepository}
	}
	return out, nil
}

type mixedOwnershipTeamProvider struct{}

func (mixedOwnershipTeamProvider) Capability() contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: contextfabric.FactOperationalDeficiencies, Name: "deficiency_route_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectTeam},
		RequiresEvidence:      true,
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "rule_id", Type: contextfabric.FactFieldString}},
	}
}

func (mixedOwnershipTeamProvider) ReadFacts(_ context.Context, _ storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	rule := "r1"
	owner, empty := query.Subjects[0], query.Subjects[1]
	return contextfabric.FactProviderResult{
		State:       contextfabric.SourceAvailable,
		RequestNote: "1 of 2 requested teams own no repository in synced ownership, so no rollup applies to them",
		SubjectReasons: map[string]string{
			contextfabric.FactSubjectKey(empty): "the team owns no repository in synced ownership",
		},
		Evaluation: &contextfabric.FactEvaluationCoverage{Members: []contextfabric.FactEvaluationMember{
			{Subject: owner, State: "fired"}, {Subject: empty, State: "never_evaluated"},
		}},
		Facts: []contextfabric.CanonicalFact{{
			Kind: contextfabric.FactOperationalDeficiencies, Subject: owner,
			Fields:         map[string]contextfabric.FactValue{"rule_id": {String: &rule}},
			EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, strings.TrimPrefix(owner.CanonicalID, "team:"))},
			SourceState:    contextfabric.SourceAvailable,
		}},
	}, nil
}

func TestReadFactsRouteServesEachTeamItsOwnCoverageReason(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7073Reader(t, h, teamReachGraph{}, mixedOwnershipTeamProvider{})
	response := h.postFacts(token, `{"kinds":["operational_deficiencies"],"subjects":[{"kind":"team","canonical_id":"team:owner"},{"kind":"team","canonical_id":"team:empty"}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	var document directread.FactsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, row := range document.Coverage {
		reasons[row.Subject.CanonicalID] = row.Reason
	}
	if len(reasons) != 2 {
		t.Fatalf("coverage rows = %+v body=%s", document.Coverage, response.Body.String())
	}
	if strings.Contains(reasons["team:owner"], "owns no repository") || strings.Contains(reasons["team:owner"], "own no repository") {
		t.Errorf("owner row served a false reason: %q", reasons["team:owner"])
	}
	if !strings.Contains(reasons["team:empty"], "owns no repository") {
		t.Errorf("empty team row = %q, want its own reason", reasons["team:empty"])
	}
}
