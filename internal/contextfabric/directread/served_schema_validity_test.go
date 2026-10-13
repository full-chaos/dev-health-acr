package directread

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

func TestServedReadFactsSatisfiesSchemaForEveryProviderState(t *testing.T) {
	states := []string{"fired", "measured_zero", "stale", "before_range", "never_evaluated", "withheld_capped_read"}
	var teams []contextfabric.SubjectRef
	for _, state := range states {
		teams = append(teams, subject(contractsv1.ContextFabricSubjectTeam, "team:"+state))
	}
	provider := &stubProvider{capability: deficiencyLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Evaluation: &contextfabric.FactEvaluationCoverage{}}
		for index, team := range query.Subjects {
			result.Evaluation.Members = append(result.Evaluation.Members, contextfabric.FactEvaluationMember{Subject: team, State: states[index]})
			if states[index] == "fired" {
				result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: contextfabric.FactOperationalDeficiencies, Subject: team,
					Fields: map[string]contextfabric.FactValue{"rule_id": strValue("r1")}, EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, strings.TrimPrefix(team.CanonicalID, "team:"))}, SourceState: contextfabric.SourceAvailable})
			}
			if states[index] == "measured_zero" {
				result.EvaluatedSubjects = append(result.EvaluatedSubjects, team)
			}
		}
		return result, nil
	}}
	request := FactsRequest{Kinds: []string{"operational_deficiencies"}}
	for _, team := range teams {
		request.Subjects = append(request.Subjects, RequestSubject{Kind: "team", CanonicalID: team.CanonicalID})
	}
	response, err := newTestFactsReader(t, teamGraph(teams...), provider).Read(requestContext(), unrestrictedA(), request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	validateAgainst(t, "mcp_read_facts_response.v1.schema.json", encoded)

	want := map[string][2]string{
		"team:fired": {OutcomeFactServed, "fired"}, "team:measured_zero": {OutcomeMeasuredZero, "measured_zero"},
		"team:stale": {OutcomeStale, "stale"}, "team:before_range": {OutcomeReadNoFact, "before_range"},
		"team:never_evaluated": {OutcomeReadNoFact, "never_evaluated"}, "team:withheld_capped_read": {OutcomeTruncated, "withheld_capped_read"},
	}
	for _, row := range response.Coverage {
		if got := [2]string{row.Outcome, row.ProviderState}; got != want[row.Subject.CanonicalID] {
			t.Errorf("%s: served (outcome, provider_state) = %v, want %v", row.Subject.CanonicalID, got, want[row.Subject.CanonicalID])
		}
	}
}

func TestEveryProviderStateMapsIntoTheClosedCoverageVocabulary(t *testing.T) {
	for _, schemaPath := range []string{
		readFactsSchemaPath(t, "mcp_read_facts_response.v1.schema.json"),
		repopath.Path(t, "internal", "mcp", "schemas", "mcp_read_facts_response.v1.schema.json"),
	} {
		raw, err := os.ReadFile(schemaPath)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		props := doc["properties"].(map[string]any)["coverage"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		allowed := map[string]bool{}
		for _, v := range schemaVocab(props["outcome"].(map[string]any)) {
			allowed[v.(string)] = true
		}
		for _, outcome := range []string{OutcomeFactServed, OutcomeReadNoFact, OutcomeMeasuredZero, OutcomeNotApplicable, OutcomeUnavailable, OutcomeStale, OutcomeTruncated, OutcomeWithheldBudget} {
			if !allowed[outcome] {
				t.Errorf("%s: enum lacks constant %q", schemaPath, outcome)
			}
		}
		for state := range providerMemberStates {
			if mapped := providerStateOutcome(state); !allowed[mapped] {
				t.Errorf("%s: provider state %q maps to %q, not in the enum", schemaPath, state, mapped)
			}
		}
	}
}

func schemaVocab(node map[string]any) []any {
	if raw, ok := node["enum"].([]any); ok {
		return raw
	}
	raw, _ := node["x-documented-values"].([]any)
	return raw
}
