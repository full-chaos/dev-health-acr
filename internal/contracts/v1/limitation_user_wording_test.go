package v1

import (
	"regexp"
	"strings"
	"testing"
)

var rawWireTokenInProse = regexp.MustCompile(`[a-z]+_[a-z_]+`)

func TestServedLimitationSentencesCarryNoBasisClause(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		sentence string
	}{
		{"member kind refusal", ContextFabricRefusalBasisLimitation(ContextFabricSubjectRepository)},
		{"continuation unverifiable", ContextFabricContinuationContextUnverifiableLimitation},
		{"subject identity unconfirmed", ContextFabricSubjectIdentityUnconfirmedLimitation},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if strings.Contains(testCase.sentence, "on the basis") {
				t.Fatalf("sentence names a basis: %q", testCase.sentence)
			}
			if token := rawWireTokenInProse.FindString(testCase.sentence); token != "" {
				t.Fatalf("sentence carries wire token %q: %q", token, testCase.sentence)
			}
		})
	}
}

func TestServedKindSentencesUseDisplayNouns(t *testing.T) {
	kind := ContextFabricSubjectPullRequestReview
	for _, testCase := range []struct {
		name     string
		sentence string
		recog    func(string) bool
	}{
		{"member kind refusal", ContextFabricRefusalBasisLimitation(kind), IsContextFabricRefusalBasisLimitation},
		{"grouping refusal", ContextFabricGroupingRefusalLimitation(kind, ContextFabricSubjectCIRun), IsContextFabricGroupingRefusalLimitation},
		{"grouping unplaceable", ContextFabricGroupingUnplaceableLimitation(kind), IsContextFabricGroupingUnplaceableLimitation},
		{"group unread", ContextFabricGroupReadUnreadLimitation(kind), IsContextFabricGroupReadUnreadLimitation},
		{"group over bound", ContextFabricGroupListOverBoundLimitation(kind), IsContextFabricGroupListOverBoundLimitation},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if token := rawWireTokenInProse.FindString(testCase.sentence); token != "" {
				t.Fatalf("sentence carries wire token %q: %q", token, testCase.sentence)
			}
			if !testCase.recog(testCase.sentence) {
				t.Fatalf("composed sentence is not recognised: %q", testCase.sentence)
			}
		})
	}
}

func TestEverySubjectKindHasDistinctDisplayNouns(t *testing.T) {
	seen := map[string]ContextFabricSubjectKind{}
	for _, kind := range contextFabricSubjectKinds {
		singular, plural := ContextFabricSubjectKindNoun(kind, 1), ContextFabricSubjectKindNoun(kind, 2)
		for _, noun := range []string{singular, plural} {
			if noun == "" || strings.Contains(noun, "_") {
				t.Fatalf("kind %q has display noun %q", kind, noun)
			}
			if other, dup := seen[noun]; dup {
				t.Fatalf("noun %q shared by %q and %q", noun, other, kind)
			}
			seen[noun] = kind
		}
	}
	if got := ContextFabricSubjectKindNoun(ContextFabricSubjectRepository, 2); got != "repositories" {
		t.Fatalf("repository plural = %q", got)
	}
}

func TestEverySubjectKindNounPairIsExact(t *testing.T) {
	pair := func(singular string) [2]string { return [2]string{singular, singular + "s"} }
	want := map[ContextFabricSubjectKind][2]string{
		ContextFabricSubjectOrganization:      pair("organization"),
		ContextFabricSubjectTeam:              pair("team"),
		ContextFabricSubjectProject:           pair("project"),
		ContextFabricSubjectRepository:        {"repository", "repositories"},
		ContextFabricSubjectWorkItem:          pair("work item"),
		ContextFabricSubjectPullRequest:       pair("pull request"),
		ContextFabricSubjectDeployment:        pair("deployment"),
		ContextFabricSubjectIncident:          pair("incident"),
		ContextFabricSubjectDocument:          pair("document"),
		ContextFabricSubjectDecision:          pair("decision"),
		ContextFabricSubjectEpisode:           pair("episode"),
		ContextFabricSubjectMetric:            pair("metric"),
		ContextFabricSubjectPullRequestReview: pair("pull request review"),
		ContextFabricSubjectCIRun:             pair("CI pipeline run"),
		ContextFabricSubjectWorkItemRef:       pair("work item reference"),
	}
	if len(want) != len(contextFabricSubjectKinds) {
		t.Fatalf("table covers %d kinds, vocabulary has %d", len(want), len(contextFabricSubjectKinds))
	}
	for _, kind := range contextFabricSubjectKinds {
		pair, ok := want[kind]
		if !ok {
			t.Fatalf("kind %q missing from the expected table", kind)
		}
		if got := ContextFabricSubjectKindNoun(kind, 1); got != pair[0] {
			t.Errorf("%q singular = %q, want %q", kind, got, pair[0])
		}
		if got := ContextFabricSubjectKindNoun(kind, 0); got != pair[1] {
			t.Errorf("%q plural = %q, want %q", kind, got, pair[1])
		}
	}
}

func TestKindSentenceRecognisersRejectEveryNonNounSegment(t *testing.T) {
	kind := ContextFabricSubjectRepository
	other := ContextFabricSubjectTeam
	singular := ContextFabricSubjectKindNoun(kind, 1)
	plural := ContextFabricSubjectKindNoun(kind, 2)
	token := string(ContextFabricSubjectPullRequest)
	for _, testCase := range []struct {
		name     string
		sentence string
		recog    func(string) bool
	}{
		{"grouping refusal planned plural", contextFabricGroupingRefusalLimitationPrefix + plural + contextFabricGroupingRefusalLimitationMiddle + ContextFabricSubjectKindNoun(other, 1) + contextFabricGroupingRefusalLimitationSuffix, IsContextFabricGroupingRefusalLimitation},
		{"grouping refusal source plural", contextFabricGroupingRefusalLimitationPrefix + ContextFabricSubjectKindNoun(other, 1) + contextFabricGroupingRefusalLimitationMiddle + plural + contextFabricGroupingRefusalLimitationSuffix, IsContextFabricGroupingRefusalLimitation},
		{"grouping refusal planned empty", contextFabricGroupingRefusalLimitationPrefix + contextFabricGroupingRefusalLimitationMiddle + singular + contextFabricGroupingRefusalLimitationSuffix, IsContextFabricGroupingRefusalLimitation},
		{"grouping refusal source token", contextFabricGroupingRefusalLimitationPrefix + singular + contextFabricGroupingRefusalLimitationMiddle + token + contextFabricGroupingRefusalLimitationSuffix, IsContextFabricGroupingRefusalLimitation},
		{"grouping refusal planned token", contextFabricGroupingRefusalLimitationPrefix + token + contextFabricGroupingRefusalLimitationMiddle + singular + contextFabricGroupingRefusalLimitationSuffix, IsContextFabricGroupingRefusalLimitation},
		{"unplaceable plural", contextFabricGroupingRefusalLimitationPrefix + plural + contextFabricGroupingUnplaceableLimitationSuffix, IsContextFabricGroupingUnplaceableLimitation},
		{"unplaceable token", contextFabricGroupingRefusalLimitationPrefix + token + contextFabricGroupingUnplaceableLimitationSuffix, IsContextFabricGroupingUnplaceableLimitation},
		{"unplaceable empty", contextFabricGroupingRefusalLimitationPrefix + contextFabricGroupingUnplaceableLimitationSuffix, IsContextFabricGroupingUnplaceableLimitation},
		{"unread plural", contextFabricGroupReadUnreadLimitationPrefix + plural + contextFabricGroupReadUnreadLimitationSuffix, IsContextFabricGroupReadUnreadLimitation},
		{"unread token", contextFabricGroupReadUnreadLimitationPrefix + token + contextFabricGroupReadUnreadLimitationSuffix, IsContextFabricGroupReadUnreadLimitation},
		{"unread empty", contextFabricGroupReadUnreadLimitationPrefix + contextFabricGroupReadUnreadLimitationSuffix, IsContextFabricGroupReadUnreadLimitation},
		{"over bound plural", contextFabricGroupListOverBoundLimitationPrefix + plural + contextFabricGroupListOverBoundLimitationSuffix, IsContextFabricGroupListOverBoundLimitation},
		{"over bound token", contextFabricGroupListOverBoundLimitationPrefix + token + contextFabricGroupListOverBoundLimitationSuffix, IsContextFabricGroupListOverBoundLimitation},
		{"over bound empty", contextFabricGroupListOverBoundLimitationPrefix + contextFabricGroupListOverBoundLimitationSuffix, IsContextFabricGroupListOverBoundLimitation},
		{"refusal singular", contextFabricRefusalBasisLimitationPrefix + singular + contextFabricRefusalBasisLimitationMiddle, IsContextFabricRefusalBasisLimitation},
		{"refusal token", contextFabricRefusalBasisLimitationPrefix + token + contextFabricRefusalBasisLimitationMiddle, IsContextFabricRefusalBasisLimitation},
		{"refusal empty", contextFabricRefusalBasisLimitationPrefix + contextFabricRefusalBasisLimitationMiddle, IsContextFabricRefusalBasisLimitation},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.recog(testCase.sentence) {
				t.Fatalf("recognised %q", testCase.sentence)
			}
			if IsContextFabricServiceAuthoredLimitation(testCase.sentence) {
				t.Fatalf("service-authored registry accepts %q", testCase.sentence)
			}
		})
	}
}

func TestKindNounLookupRejectsNonMembers(t *testing.T) {
	if got := ContextFabricSubjectKindNoun(ContextFabricSubjectKind("not_a_kind"), 1); got != "" {
		t.Fatalf("noun for a non-member = %q", got)
	}
	if got := ContextFabricSubjectKindNoun("", 2); got != "" {
		t.Fatalf("noun for the empty kind = %q", got)
	}
	if _, ok := contextFabricSubjectKindFromNoun("", 1); ok {
		t.Fatal("the empty noun resolved to a kind")
	}
	if _, ok := contextFabricSubjectKindFromNoun(ContextFabricSubjectKindNoun(ContextFabricSubjectRepository, 2), 1); ok {
		t.Fatal("a plural noun resolved under the singular form")
	}
	if _, ok := contextFabricSubjectKindFromNoun(ContextFabricSubjectKindNoun(ContextFabricSubjectRepository, 1), 2); ok {
		t.Fatal("a singular noun resolved under the plural form")
	}
}

func TestCohortNarrowingLimitationUsesDisplayNoun(t *testing.T) {
	steps := []ContextFabricCohortNarrowingDisclosureStep{{Stage: ContextFabricPlanNarrowingSynthesisInput, Before: 11, After: 10}}
	sentence, ok := ContextFabricCohortNarrowingLimitation(ContextFabricSubjectPullRequestReview, 11, 10, steps)
	if !ok {
		t.Fatal("chain refused")
	}
	if token := rawWireTokenInProse.FindString(sentence); token != "" {
		t.Fatalf("sentence carries wire token %q: %q", token, sentence)
	}
	if !IsContextFabricCohortNarrowingLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatalf("composed sentence not recognised: %q", sentence)
	}
	for name, edited := range map[string]string{
		"wire token":   strings.Replace(sentence, "pull request review", "pull_request_review", 1),
		"plural noun":  strings.Replace(sentence, "pull request review", "pull request reviews", 1),
		"unknown noun": strings.Replace(sentence, "pull request review", "invoice", 1),
		"empty noun":   strings.Replace(sentence, " pull request review subjects", " subjects", 1),
	} {
		if IsContextFabricCohortNarrowingLimitation(edited) {
			t.Errorf("%s: recognised %q", name, edited)
		}
	}
}
