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
