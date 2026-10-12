package contextfabric

import (
	"strings"
	"testing"
)

func TestScopeGapCauseSeparatesUnwiredFromUnconfigured(t *testing.T) {
	capability := FactCapability{SupportedSubjectKinds: []SubjectKind{SubjectKind("repository")}}
	cases := []struct {
		name        string
		reason      FactScopeDecisionReason
		unavailable bool
	}{
		{"expander unwired", FactScopeDecisionExpanderUnwired, true},
		{"policy disabled", FactScopeDecisionPolicyDisabled, false},
		{"policy none", FactScopeDecisionPolicyNone, false},
		{"axis unsupported", FactScopeDecisionAxisUnsupported, false},
		{"time bounds invalid", FactScopeDecisionTimeBoundsInvalid, false},
	}
	for _, tc := range cases {
		gap := FactScopeGap{Outcome: FactScopePolicyUnavailable, DecisionReason: tc.reason, OriginKind: SubjectKind("project")}
		if got := gap.ScopeExpansionUnavailable(); got != tc.unavailable {
			t.Errorf("%s: ScopeExpansionUnavailable = %v, want %v", tc.name, got, tc.unavailable)
		}
		if got := strings.Contains(unexpandedReason(capability, gap), "scope expansion is not available on this read path"); got != tc.unavailable {
			t.Errorf("%s: reason names the unavailable expansion = %v, want %v", tc.name, got, tc.unavailable)
		}
		if got := factScopeGapSourceState(gap.Outcome); got != SourceUnconfigured {
			t.Errorf("%s: source state moved to %s", tc.name, got)
		}
	}
	failed := FactScopeGap{Outcome: FactScopeFailed, DecisionReason: FactScopeDecisionExpanderUnwired}
	if failed.ScopeExpansionUnavailable() {
		t.Error("a failed expansion is not an unavailable one")
	}
}
