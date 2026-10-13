package contextfabric

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestServedRollupGapReasonCarriesNoInternalTokenOnEveryCoverageSurface(t *testing.T) {
	observed := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	project := subject(SubjectProject, "project.v2:jira:Q")
	provider := &factProviderStub{
		capability: planCapability(FactPullRequests, "pull_requests", SubjectProject, SubjectRepository),
		result: FactProviderResult{
			State: SourceAvailable, Version: "pr-v1", ObservedAt: &observed,
			Facts: []CanonicalFact{{
				Kind: FactPullRequests, Subject: project,
				Fields:      map[string]FactValue{"open": IntegerFactValue(3)},
				ObservedAt:  &observed,
				SourceState: SourceAvailable,
			}},
		},
	}
	registry, err := NewFactCapabilityRegistry([]FactProvider{provider}, FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
		scopeRequest([]SubjectRef{project}, []FactRequirement{{Kind: FactPullRequests}}, TemporalCurrent))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Facts) != 1 {
		t.Fatalf("facts = %+v, want the project's rollup served", bundle.Facts)
	}
	check := func(surface, text string) {
		t.Helper()
		if strings.Contains(text, "unexpanded:") || strings.Contains(text, "policy_unavailable:") || strings.Contains(text, "no resolved subject holds") {
			t.Errorf("%s carries an internal token or a false sentence: %q", surface, text)
		}
	}
	found := false
	for _, source := range bundle.Coverage.Sources {
		if source.Source == "canonical_fact:"+string(FactPullRequests) {
			found = true
			check("observation reason", source.Reason)
			if !strings.Contains(source.Reason, "were read") {
				t.Errorf("observation reason does not say what was read: %q", source.Reason)
			}
		}
	}
	if !found {
		t.Fatal("no pull_requests observation")
	}
	for _, reason := range bundle.Coverage.DegradedReasons {
		check("degraded reason", reason)
	}
	structured := false
	for _, detail := range bundle.Coverage.Details {
		check("coverage detail raw", detail.Raw)
		check("coverage detail label", detail.Label)
		if detail.ScopeOutcome == string(FactScopePolicyUnavailable) && detail.Policy != "" {
			structured = true
		}
	}
	if !structured {
		t.Errorf("the scope outcome and policy are not on a structured coverage detail: %+v", bundle.Coverage.Details)
	}
}
