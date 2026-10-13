package contextfabric

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var gapReasonTokenPattern = regexp.MustCompile(`^[a-z]+:|\b[a-z0-9]+(?:_[a-z0-9]+)+\b|\bdevhealth[a-z]*:`)

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
		if token := gapReasonTokenPattern.FindString(text); token != "" || strings.Contains(text, "no resolved subject holds") {
			t.Errorf("%s carries the internal token %q or a false sentence: %q", surface, token, text)
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
		if !strings.HasPrefix(reason, "Pull request: ") {
			t.Errorf("degraded reason does not name the fact kind in plain words: %q", reason)
		}
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

func TestPrunedAndNarrowedReasonsCarryNoInternalToken(t *testing.T) {
	capability := planCapability(FactMetrics, "metrics", SubjectRepository)
	pruned := planFactReads(newFactPlanInput(CanonicalFactRequest{
		Subjects:     []SubjectRef{subject(SubjectIncident, "incident_1")},
		Requirements: []FactRequirement{{Kind: FactMetrics}},
	}), planIndex(capability))[0]
	narrowed := planFactReads(newFactPlanInput(CanonicalFactRequest{
		Subjects:     []SubjectRef{subject(SubjectIncident, "incident_1"), subject(SubjectRepository, "repo_api")},
		Requirements: []FactRequirement{{Kind: FactMetrics}},
	}), planIndex(capability))[0]
	for name, entry := range map[string]factPlanEntry{"pruned": pruned, "narrowed": narrowed} {
		if entry.Reason == "" {
			t.Fatalf("%s entry has no reason: %+v", name, entry)
		}
		if token := gapReasonTokenPattern.FindString(entry.Reason); token != "" {
			t.Errorf("%s reason carries the internal token %q: %q", name, token, entry.Reason)
		}
	}
}

func TestFailedReadAndProviderReasonsAreServedAsPlainSentences(t *testing.T) {
	cases := []struct {
		name     string
		provider *factProviderStub
		want     string
	}{
		{"failed read", &factProviderStub{
			capability: planCapability(FactStatus, "status", SubjectWorkItem),
			err: &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query work item status failed",
				Cause: errors.New("clickhouse: code 241 memory limit exceeded for statement select secret_column")},
		}, "The status read failed; the facts for it are not shown."},
		{"budget refusal", &factProviderStub{
			capability: planCapability(FactStatus, "status", SubjectWorkItem),
			err:        &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query work item status exceeded the read budget (code 3); narrow the window or the subject"},
		}, "The status read failed; the facts for it are not shown."},
		{"timeout", &factProviderStub{
			capability: planCapability(FactStatus, "status", SubjectWorkItem),
			err:        context.DeadlineExceeded,
		}, "The status read timed out; the facts for it are not shown."},
		{"provider sentence", &factProviderStub{
			capability: planCapability(FactStatus, "status", SubjectWorkItem),
			result:     FactProviderResult{State: SourceNoData, Reason: "devhealthfacts: no rows were retained at or before the requested time"},
		}, "no rows were retained at or before the requested time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry, err := NewFactCapabilityRegistry([]FactProvider{tc.provider}, FactRegistryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"},
				scopeRequest([]SubjectRef{subject(SubjectWorkItem, "work_item:linear:T-1")}, []FactRequirement{{Kind: FactStatus}}, TemporalCurrent))
			if err != nil {
				t.Fatal(err)
			}
			var reason string
			for _, source := range bundle.Coverage.Sources {
				if source.Source == "canonical_fact:status" {
					reason = source.Reason
				}
			}
			if !strings.Contains(reason, tc.want) {
				t.Errorf("reason %q, want it to carry %q", reason, tc.want)
			}
			texts := append([]string{reason}, bundle.Coverage.DegradedReasons...)
			for _, detail := range bundle.Coverage.Details {
				texts = append(texts, detail.Raw, detail.Label)
			}
			for _, text := range texts {
				if token := gapReasonTokenPattern.FindString(text); token != "" || strings.Contains(text, "secret_column") || strings.Contains(text, "clickhouse") {
					t.Errorf("served text carries the internal token %q or provider detail: %q", token, text)
				}
			}
		})
	}
}

func TestFailedReadAfterAScopeGapDoesNotClaimTheFactsWereRead(t *testing.T) {
	project := subject(SubjectProject, "project.v2:jira:Q")
	provider := &factProviderStub{
		capability: planCapability(FactPullRequests, "pull_requests", SubjectProject, SubjectRepository),
		err:        &FactReadFailure{State: SourceUnavailable, Reason: "devhealthfacts: query pull requests failed"},
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
	texts := append([]string{}, bundle.Coverage.DegradedReasons...)
	for _, source := range bundle.Coverage.Sources {
		texts = append(texts, source.Reason)
	}
	for _, detail := range bundle.Coverage.Details {
		texts = append(texts, detail.Raw)
	}
	if len(texts) == 0 {
		t.Fatal("no coverage text for a failed read")
	}
	for _, text := range texts {
		if strings.Contains(text, "were read") {
			t.Errorf("a failed read says the facts were read: %q", text)
		}
	}
	if !strings.Contains(texts[0], "read failed") {
		t.Errorf("failed read not named: %q", texts[0])
	}
}
