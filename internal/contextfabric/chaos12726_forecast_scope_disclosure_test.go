package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12726: a forecast claim that covers fewer work scopes than the team has
// says so, over every provider's team, and a team whose scopes are all covered
// (or has one) says nothing.

func scopeFacts(provider string, scopes int) ([]CanonicalFact, SubjectRef) {
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:" + provider + ":T", Label: "Team " + provider}
	facts := make([]CanonicalFact, 0, scopes)
	for i := 0; i < scopes; i++ {
		fact := summaryFact(fmt.Sprintf("scope-%02d", i), "available", days(5), days(8))
		fact.Subject = subject
		facts = append(facts, fact)
	}
	return facts, subject
}

func scopeClaims(facts []CanonicalFact, subject SubjectRef, claimed ...int) []ClaimedFact {
	claims := make([]ClaimedFact, 0, len(claimed))
	for _, index := range claimed {
		claim := forecastClaim(fmt.Sprintf("claim_%d", index), nil)
		claim.Subject = contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: subject.CanonicalID, Label: subject.Label}
		claim.CompletionForecast = nil
		claims = append(claims, claim)
	}
	// each claim is grounded on its own scope's fact, as the model's claim is
	for i, index := range claimed {
		stamped := discloseCompletionForecast([]ClaimedFact{claims[i]}, []CanonicalFact{facts[index]})
		claims[i] = stamped[0]
	}
	return claims
}

func TestAForecastOfOneScopeNamesTheScopeAndTheTeamsScopeCountForEveryProvider(t *testing.T) {
	for _, provider := range []string{"jira", "gitlab", "github", "linear"} {
		t.Run(provider, func(t *testing.T) {
			facts, subject := scopeFacts(provider, 14)
			result := InvestigationResult{ClaimedFacts: scopeClaims(facts, subject, 3)}
			discloseForecastScopes(&result, CanonicalFactBundle{Facts: facts})
			want := fmt.Sprintf(`This forecast for team "Team %s" covers work scope "scope-03" only: 1 of the team's 14 work scopes. Left out of this answer: 13 work scopes.`, provider)
			if len(result.Limitations) != 1 || result.Limitations[0] != want {
				t.Fatalf("limitations = %q, want [%q]", result.Limitations, want)
			}
			if !contractsv1.IsContextFabricServiceAuthoredLimitation(want) {
				t.Fatal("the sentence is not recognised as service-authored, so a composer could displace it")
			}
			summary := result.ClaimedFacts[0].CompletionForecast
			if summary.ScopeCount == nil || *summary.ScopeCount != 14 || summary.ScopeCountAtLeast || summary.WorkScopeID != "scope-03" {
				t.Fatalf("summary = %+v, want scope-03 of 14", summary)
			}
			if !result.Coverage.Partial {
				t.Fatal("coverage.partial = false, want true: the answer does not cover the team's other scopes")
			}
		})
	}
}

func TestSeveralClaimedScopesAreCountedOnceEach(t *testing.T) {
	facts, subject := scopeFacts("linear", 5)
	claims := scopeClaims(facts, subject, 0, 2, 2)
	result := InvestigationResult{ClaimedFacts: claims}
	discloseForecastScopes(&result, CanonicalFactBundle{Facts: facts})
	want := `This forecast for team "Team linear" covers 2 of the team's 5 work scopes. Left out of this answer: 3 work scopes.`
	if len(result.Limitations) != 1 || result.Limitations[0] != want {
		t.Fatalf("limitations = %q, want [%q]", result.Limitations, want)
	}
}

func TestATeamWhoseScopesAreAllCoveredOrIsSingleScopeSaysNothing(t *testing.T) {
	facts, subject := scopeFacts("github", 3)
	all := InvestigationResult{ClaimedFacts: scopeClaims(facts, subject, 0, 1, 2)}
	discloseForecastScopes(&all, CanonicalFactBundle{Facts: facts})
	one, oneSubject := scopeFacts("jira", 1)
	single := InvestigationResult{ClaimedFacts: scopeClaims(one, oneSubject, 0)}
	discloseForecastScopes(&single, CanonicalFactBundle{Facts: one})
	for name, result := range map[string]InvestigationResult{"all covered": all, "single scope": single} {
		if len(result.Limitations) != 0 || result.Coverage.Partial {
			t.Fatalf("%s: limitations = %q partial = %v, want none", name, result.Limitations, result.Coverage.Partial)
		}
		if summary := result.ClaimedFacts[0].CompletionForecast; summary.ScopeCount == nil || summary.ScopeCountAtLeast {
			t.Fatalf("%s: summary = %+v, want a count and no floor", name, summary)
		}
	}
}

func TestATruncatedWorkloadReadWordsTheCountAsAFloor(t *testing.T) {
	facts, subject := scopeFacts("gitlab", 4)
	result := InvestigationResult{ClaimedFacts: scopeClaims(facts, subject, 1)}
	bundle := CanonicalFactBundle{Facts: facts, Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:workload", State: SourceTruncated}}}}
	discloseForecastScopes(&result, bundle)
	want := `This forecast for team "Team gitlab" covers work scope "scope-01" only: 1 of at least 4 work scopes. Left out of this answer: at least 3 work scopes.`
	if len(result.Limitations) != 1 || result.Limitations[0] != want {
		t.Fatalf("limitations = %q, want [%q]", result.Limitations, want)
	}
	if summary := result.ClaimedFacts[0].CompletionForecast; !summary.ScopeCountAtLeast {
		t.Fatalf("summary = %+v, want a floor", summary)
	}
}

func TestAnOtherTeamsScopesAreNotCounted(t *testing.T) {
	facts, subject := scopeFacts("linear", 2)
	others, _ := scopeFacts("jira", 6)
	result := InvestigationResult{ClaimedFacts: scopeClaims(facts, subject, 0)}
	discloseForecastScopes(&result, CanonicalFactBundle{Facts: append(append([]CanonicalFact{}, facts...), others...)})
	if len(result.Limitations) != 1 || !strings.Contains(result.Limitations[0], "1 of the team's 2 work scopes") {
		t.Fatalf("limitations = %q, want the claimed team's own 2 scopes", result.Limitations)
	}
}

func TestTheScopeSentenceIsRefusedWhenItWouldBeFalse(t *testing.T) {
	for name, call := range map[string]string{
		"nothing left out": contractsv1.ContextFabricForecastScopeLimitation("T", "s", 3, 3, false),
		"none covered":     contractsv1.ContextFabricForecastScopeLimitation("T", "s", 0, 3, false),
		"no team":          contractsv1.ContextFabricForecastScopeLimitation("", "s", 1, 3, false),
	} {
		if call != "" {
			t.Fatalf("%s: composed %q, want none", name, call)
		}
	}
	if contractsv1.IsContextFabricServiceAuthoredLimitation(`This forecast for team "T" covers work scope "s" only: 1 of the team's 14 work scopes.`) {
		t.Fatal("a model caveat that only starts alike must not be recognised")
	}
}

// serveForecastClaim runs one investigation over a team with `scopes` forecast
// facts whose answer claims only the first scope's backlog_size.
func serveForecastClaim(t *testing.T, scopes int) InvestigationResult {
	t.Helper()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:linear:T", Label: "Fullchaos"}
	bundle := bootstrapFactBundle(team)
	for i := 0; i < scopes; i++ {
		fact := summaryFact(fmt.Sprintf("scope-%02d", i), "available", days(5), days(8))
		fact.Subject = team
		fact.Fields["backlog_size"] = IntegerFactValue(int64(10 + i))
		fact.EvidenceRefIDs = []string{fmt.Sprintf("evidence_workload_%04d", i+1)}
		fact.SourceState, fact.Source, fact.SourceVersion = SourceAvailable, "ops", "v1"
		bundle.Facts = append(bundle.Facts, fact)
	}
	bundle.Coverage.Sources = append(bundle.Coverage.Sources, SourceObservation{Source: "canonical_fact:workload", State: SourceAvailable})
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bundle, nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{team}},
		context:    bootstrapGraphContext(team),
	}
	draft := bootstrapDraft(team)
	backlog := int64(10)
	draft.ClaimedFacts = append(draft.ClaimedFacts, ClaimedFact{
		ClaimID: "claim_backlog", Kind: FactWorkload, Subject: team, Field: "backlog_size",
		Value: contractsv1.ContextFabricScalarValue{Integer: &backlog},
	})
	draft.Drivers[0].ClaimedFactIDs = append(draft.Drivers[0].ClaimedFactIDs, "claim_backlog")
	draft.EvidenceRefIDs = append(draft.EvidenceRefIDs, "evidence_workload_0001")
	draft.Drivers[0].EvidenceRefIDs = append(draft.Drivers[0].EvidenceRefIDs, "evidence_workload_0001")
	engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), draft, newMapResultStore())
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	return result
}

func TestAServedForecastClaimOfOneOfFourScopesIsDisclosedAndAControlIsNot(t *testing.T) {
	result := serveForecastClaim(t, 4)
	claim := servedClaim(t, result, "claim_backlog")
	if claim.CompletionForecast == nil || claim.CompletionForecast.ScopeCount == nil || *claim.CompletionForecast.ScopeCount != 4 || claim.CompletionForecast.WorkScopeID != "scope-00" {
		t.Fatalf("summary = %+v, want scope-00 of 4", claim.CompletionForecast)
	}
	want := `This forecast for team "Fullchaos" covers work scope "scope-00" only: 1 of the team's 4 work scopes. Left out of this answer: 3 work scopes.`
	found := false
	for _, limitation := range result.Limitations {
		found = found || limitation == want
	}
	if !found || !result.Coverage.Partial {
		t.Fatalf("limitations = %q partial = %v, want %q and partial", result.Limitations, result.Coverage.Partial, want)
	}
	control := serveForecastClaim(t, 1)
	for _, limitation := range control.Limitations {
		if contractsv1.IsContextFabricForecastScopeLimitation(limitation) {
			t.Fatalf("a one-scope team was disclosed as partial: %q", limitation)
		}
	}
}

func TestOnlyTheClaimedTeamsOwnForecastScopesAreCounted(t *testing.T) {
	first, _ := scopeFacts("jira", 2)
	second, secondSubject := scopeFacts("linear", 5)
	all := append(append([]CanonicalFact{}, first...), second...)
	result := InvestigationResult{ClaimedFacts: scopeClaims(second, secondSubject, 4)}
	discloseForecastScopes(&result, CanonicalFactBundle{Facts: all})
	if got := *result.ClaimedFacts[0].CompletionForecast.ScopeCount; got != 5 {
		t.Fatalf("scope_count = %d, want 5 (the claimed team's own, not the first team's 2)", got)
	}
}

func TestOnlyScopesThatServeAForecastAreCounted(t *testing.T) {
	facts, subject := scopeFacts("github", 3)
	for i := 0; i < 2; i++ {
		bare := summaryFact(fmt.Sprintf("bare-%d", i), "", days(5), nil)
		delete(bare.Fields, "completion_distribution_state")
		bare.Subject = subject
		facts = append(facts, bare)
	}
	result := InvestigationResult{ClaimedFacts: scopeClaims(facts, subject, 0)}
	discloseForecastScopes(&result, CanonicalFactBundle{Facts: facts})
	if got := *result.ClaimedFacts[0].CompletionForecast.ScopeCount; got != 3 {
		t.Fatalf("scope_count = %d, want 3: a scope that serves no forecast is not one the answer could have claimed", got)
	}
}

func TestAProjectFactOfTheSameIDIsNotATeamScope(t *testing.T) {
	facts, subject := scopeFacts("gitlab", 2)
	project := summaryFact("project-scope", "available", days(5), days(8))
	project.Subject = SubjectRef{Kind: SubjectProject, CanonicalID: subject.CanonicalID, Label: "P"}
	all := append(append([]CanonicalFact{}, facts...), project)
	result := InvestigationResult{ClaimedFacts: scopeClaims(facts, subject, 0)}
	discloseForecastScopes(&result, CanonicalFactBundle{Facts: all})
	if got := *result.ClaimedFacts[0].CompletionForecast.ScopeCount; got != 2 {
		t.Fatalf("scope_count = %d, want 2", got)
	}
}

func TestAnotherSourcesTruncationIsNotAFloorOnTheScopeCount(t *testing.T) {
	facts, subject := scopeFacts("jira", 3)
	result := InvestigationResult{ClaimedFacts: scopeClaims(facts, subject, 0)}
	discloseForecastScopes(&result, CanonicalFactBundle{Facts: facts, Coverage: Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceTruncated}}}})
	if result.ClaimedFacts[0].CompletionForecast.ScopeCountAtLeast || strings.Contains(result.Limitations[0], "at least") {
		t.Fatalf("a truncated health read worded the workload count as a floor: %+v %q", result.ClaimedFacts[0].CompletionForecast, result.Limitations)
	}
}
