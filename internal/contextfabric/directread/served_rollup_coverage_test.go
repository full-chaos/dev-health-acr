package directread

import (
	"regexp"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var internalTokenPattern = regexp.MustCompile(`^[a-z]+:|\b[a-z0-9]+(?:_[a-z0-9]+)+\b`)

func internalToken(reason string) string { return internalTokenPattern.FindString(reason) }

func TestInternalTokenCensusCatchesPlantedTokens(t *testing.T) {
	planted := []string{
		"unexpanded:policy_unavailable: no resolved subject holds this",
		"pruned:subject_kind_unsupported: no resolved subject has a kind",
		"narrowed: 1 subject(s) were not queried",
		"scope expansion reached only some (expansion: expanded_partial; policy: x)",
		"read failed (policy: project_work_item_pull_request_v1)",
	}
	for _, reason := range planted {
		if internalToken(reason) == "" {
			t.Errorf("census missed a planted token in %q", reason)
		}
	}
	for _, plain := range []string{
		"the requested subjects that hold this capability's facts directly were read; facts reachable only through scope expansion were not added because no scope expander is wired on this server; this capability covers project,team,repository subjects and the request named a project",
		"no resolved subject has a kind this capability supports (resolved kinds: team; supported kinds: repository)",
	} {
		if token := internalToken(plain); token != "" {
			t.Errorf("census flags a plain sentence for %q: %q", token, plain)
		}
	}
}

func rollupCapability(kind contextfabric.FactKind) contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: kind, Name: string(kind) + "_rollup_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectRepository},
		RequiresEvidence:      true, Dimension: contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles: []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:       []contextfabric.FactFieldDeclaration{{Name: "open", Type: contextfabric.FactFieldInteger}},
	}
}

func rollupProvider(kind contextfabric.FactKind, servedIDs map[string]bool, truncated bool) *stubProvider {
	return &stubProvider{capability: rollupCapability(kind), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		result := contextfabric.FactProviderResult{State: contextfabric.SourceAvailable, Truncated: truncated}
		for _, s := range query.Subjects {
			if !servedIDs[s.CanonicalID] {
				continue
			}
			result.Facts = append(result.Facts, contextfabric.CanonicalFact{Kind: kind, Subject: s,
				Fields:         map[string]contextfabric.FactValue{"open": intValue(3)},
				EvidenceRefIDs: []string{contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, "jira:Q")}, SourceState: contextfabric.SourceAvailable})
		}
		return result, nil
	}}
}

func readRollup(t *testing.T, kind contextfabric.FactKind, served map[string]bool, truncated bool, subjects ...contextfabric.SubjectRef) FactsResponse {
	t.Helper()
	request := FactsRequest{Kinds: []string{string(kind)}}
	for _, s := range subjects {
		request.Subjects = append(request.Subjects, RequestSubject{Kind: string(s.Kind), CanonicalID: s.CanonicalID})
	}
	response, err := newTestFactsReader(t, graph7073(), rollupProvider(kind, served, truncated)).Read(requestContext(), unrestrictedA(), request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

// assertRowsHonest is the census over the read_facts outcome sites: a row
// whose own fact was served never carries an outcome that denies it, and no
// served reason leads with or carries an internal vocabulary token.
func assertRowsHonest(t *testing.T, response FactsResponse) {
	t.Helper()
	servedFact := map[string]bool{}
	for _, fact := range response.Facts {
		servedFact[fact.Kind+"\x00"+fact.Subject.Kind+"\x00"+fact.Subject.CanonicalID] = true
	}
	for _, row := range response.Coverage {
		if servedFact[row.Kind+"\x00"+row.Subject.Kind+"\x00"+row.Subject.CanonicalID] {
			switch row.Outcome {
			case OutcomeUnavailable, OutcomeNotApplicable, OutcomeReadNoFact, OutcomeWithheldBudget:
				if row.Outcome != OutcomeWithheldBudget {
					t.Errorf("row %s/%s served a fact but says %q: %+v", row.Kind, row.Subject.CanonicalID, row.Outcome, row)
				}
			}
		}
		if token := internalToken(row.Reason); token != "" {
			t.Errorf("row %s/%s reason carries the internal token %q: %q", row.Kind, row.Subject.CanonicalID, token, row.Reason)
		}
	}
}

func TestServedRollupRowsWhenExpansionDidNotRun(t *testing.T) {
	cases := []struct {
		name        string
		kind        contextfabric.FactKind
		subject     contextfabric.SubjectRef
		served      bool
		wantOutcome string
		wantState   string
		wantNote    bool
	}{
		{"project with a served rollup", contextfabric.FactPullRequests, projectQ7073, true, OutcomeFactServed, "truncated", true},
		{"team with a served rollup", contextfabric.FactPullRequests, teamT, true, OutcomeFactServed, "truncated", true},
		{"blockers project with a served rollup", contextfabric.FactBlockers, projectQ7073, true, OutcomeFactServed, "truncated", true},
		{"project with no fact keeps its state", contextfabric.FactPullRequests, projectQ7073, false, OutcomeReadNoFact, "", true},
		{"non-additive kind stays complete", contextfabric.FactHealth, projectQ7073, true, OutcomeFactServed, "available", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := readRollup(t, tc.kind, map[string]bool{tc.subject.CanonicalID: tc.served}, false, tc.subject)
			assertRowsHonest(t, response)
			row := coverageOf(t, response, string(tc.kind), tc.subject)
			if row.Outcome != tc.wantOutcome {
				t.Errorf("outcome %q, want %q: %+v", row.Outcome, tc.wantOutcome, row)
			}
			if tc.wantState != "" && row.KindState != tc.wantState {
				t.Errorf("kind_state %q, want %q: %+v", row.KindState, tc.wantState, row)
			}
			if (row.Expansion != nil) != tc.wantNote {
				t.Errorf("expansion note = %+v, want present %v", row.Expansion, tc.wantNote)
			}
			if tc.wantNote {
				if row.Expansion.Outcome != "policy_unavailable" || row.Expansion.Policy == "" {
					t.Errorf("expansion note = %+v", row.Expansion)
				}
				if strings.Contains(row.Reason, "no resolved subject holds") {
					t.Errorf("reason says no subject holds the facts directly: %q", row.Reason)
				}
				if tc.served && !strings.Contains(row.Reason, "were read") {
					t.Errorf("reason does not say what was read: %q", row.Reason)
				}
			}
		})
	}
}

func TestProviderTruncationOfAServedRollupStaysTruncated(t *testing.T) {
	response := readRollup(t, contextfabric.FactPullRequests, map[string]bool{projectQ7073.CanonicalID: true}, true, projectQ7073)
	assertRowsHonest(t, response)
	row := coverageOf(t, response, "pull_requests", projectQ7073)
	if row.Outcome != OutcomeTruncated {
		t.Errorf("a provider-cut read says %q, want truncated: %+v", row.Outcome, row)
	}
}

func TestMixedSubjectsEachRowTellsItsOwnTruth(t *testing.T) {
	response := readRollup(t, contextfabric.FactPullRequests, map[string]bool{projectQ7073.CanonicalID: true}, false, projectQ7073, teamT)
	assertRowsHonest(t, response)
	if got := coverageOf(t, response, "pull_requests", projectQ7073).Outcome; got != OutcomeFactServed {
		t.Errorf("project with a fact: %q", got)
	}
	if got := coverageOf(t, response, "pull_requests", teamT).Outcome; got != OutcomeReadNoFact {
		t.Errorf("team with no fact: %q", got)
	}
}

func TestKindOnlyReachableByExpansionServesAPlainReasonAndAStructuredNote(t *testing.T) {
	capability := rollupCapability(contextfabric.FactDeployments)
	capability.SupportedSubjectKinds = []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository}
	provider := &stubProvider{capability: capability, read: func(contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
	}}
	response, err := newTestFactsReader(t, graph7073(), provider).Read(requestContext(), unrestrictedA(), FactsRequest{
		Kinds: []string{"deployments"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ7073.CanonicalID}}})
	if err != nil {
		t.Fatal(err)
	}
	assertRowsHonest(t, response)
	row := coverageOf(t, response, "deployments", projectQ7073)
	if row.Outcome != OutcomeUnavailable || row.Expansion == nil || row.Expansion.Outcome != "policy_unavailable" {
		t.Errorf("row %+v, want unavailable with the expansion outcome on the structured note", row)
	}
}
