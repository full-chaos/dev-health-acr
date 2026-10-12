package contextfabric

import (
	"strings"
	"testing"
)

func TestScopeGapCauseSeparatesUnsupportedWindowFromUnconfigured(t *testing.T) {
	capability := FactCapability{SupportedSubjectKinds: []SubjectKind{SubjectKind("repository")}}
	cases := []struct {
		name            string
		reason          FactScopeDecisionReason
		currentOnly     bool
		windowUnsupport bool
		wantText        string
	}{
		{"axis current only", FactScopeDecisionAxisUnsupported, true, true, "supports the current window only; ask without a window to read it"},
		{"axis observed time", FactScopeDecisionAxisUnsupported, false, true, "does not support an observed-time window"},
		{"policy disabled", FactScopeDecisionPolicyDisabled, false, false, ""},
		{"policy none", FactScopeDecisionPolicyNone, false, false, ""},
		{"expander unwired", FactScopeDecisionExpanderUnwired, true, false, ""},
		{"time bounds invalid", FactScopeDecisionTimeBoundsInvalid, false, false, ""},
	}
	for _, tc := range cases {
		gap := FactScopeGap{Outcome: FactScopePolicyUnavailable, DecisionReason: tc.reason, CurrentAxisOnly: tc.currentOnly, OriginKind: SubjectKind("project")}
		if got := gap.WindowUnsupported(); got != tc.windowUnsupport {
			t.Errorf("%s: WindowUnsupported = %v, want %v", tc.name, got, tc.windowUnsupport)
		}
		reason := unexpandedReason(capability, gap)
		if tc.windowUnsupport && !strings.Contains(reason, tc.wantText) {
			t.Errorf("%s: reason %q lacks %q", tc.name, reason, tc.wantText)
		}
		if !tc.windowUnsupport && strings.Contains(reason, "window") {
			t.Errorf("%s: reason names a window: %q", tc.name, reason)
		}
		if got := factScopeGapSourceState(gap.Outcome); got != SourceUnconfigured {
			t.Errorf("%s: source state moved to %s", tc.name, got)
		}
	}
	failed := FactScopeGap{Outcome: FactScopeFailed, DecisionReason: FactScopeDecisionAxisUnsupported}
	if failed.WindowUnsupported() {
		t.Error("a failed expansion is not an unsupported window")
	}
}
