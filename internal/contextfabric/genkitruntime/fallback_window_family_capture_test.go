package genkitruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func captureFields(r contextfabric.ModelExecutionReceipt) map[string]any {
	return map[string]any{
		"WindowClass": r.WindowClass, "WindowConfidence": r.WindowConfidence, "WindowClassUnrecognized": r.WindowClassUnrecognized,
		"QuestionFamily": r.QuestionFamily, "QuestionFamilyUnrecognized": r.QuestionFamilyUnrecognized,
		"GroupKind": r.GroupKind, "GroupKindUnrecognized": r.GroupKindUnrecognized,
		"ScopeAnchorTerm": r.ScopeAnchorTerm, "ScopeAnchorTermTruncated": r.ScopeAnchorTermTruncated,
		"ScopeAnchorKind": r.ScopeAnchorKind, "ScopeAnchorKindUnrecognized": r.ScopeAnchorKindUnrecognized,
		"RequestedSubjectKind": r.RequestedSubjectKind, "RequestedSubjectKindUnrecognized": r.RequestedSubjectKindUnrecognized,
	}
}

func TestFallbackReceiptCarriesWindowAndFamilyCapture(t *testing.T) {
	t.Parallel()
	recognized := validInterpretationOutput()
	recognized.WindowClass = "trend_assessment"
	recognized.WindowConfidence = "high"
	recognized.QuestionFamily = string(contextfabric.QuestionFamilyGroupedCohortStatus)
	recognized.GroupKind = string(contractsv1.ContextFabricSubjectTeam)
	recognized.ScopeAnchorTerm = "fullchaos"
	recognized.ScopeAnchorKind = string(contractsv1.ContextFabricSubjectTeam)
	recognized.RequestedSubjectKind = string(contractsv1.ContextFabricSubjectProject)

	unrecognized := validInterpretationOutput()
	unrecognized.WindowClass = "not_a_class"
	unrecognized.ScopeAnchorTerm = strings.Repeat("x", contextfabric.ScopeAnchorTermMaxBytes+10)
	unrecognized.QuestionFamily = "not_a_family"
	unrecognized.GroupKind = "not_a_kind"
	unrecognized.ScopeAnchorKind = "also_not_a_kind"
	unrecognized.RequestedSubjectKind = "still_not_a_kind"

	invalid := validInterpretationOutput()
	invalid.Shape = "not_a_shape"

	for _, out := range []struct {
		name   string
		output interpretationOutput
	}{{"recognized", recognized}, {"unrecognized", unrecognized}} {
		for _, arm := range []struct {
			name    string
			primary *generatorStub
		}{
			{"primary generation error", &generatorStub{interpretErr: errors.New("primary down")}},
			{"primary invalid output", &generatorStub{interpretation: invalid}},
		} {
			t.Run(out.name+"/"+arm.name, func(t *testing.T) {
				t.Parallel()
				fallback := mustRuntime(t, &generatorStub{interpretation: out.output}, Config{})
				_, direct, err := fallback.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
				if err != nil {
					t.Fatalf("fallback direct error = %v", err)
				}
				want := captureFields(direct)
				if out.name == "recognized" && (direct.WindowClass == "" || direct.QuestionFamily == "" || direct.ScopeAnchorTerm == "" || direct.RequestedSubjectKind == "") {
					t.Fatalf("fixture produced an empty capture, the test would prove nothing: %v", want)
				}
				if out.name == "unrecognized" && !(direct.WindowClassUnrecognized && direct.QuestionFamilyUnrecognized && direct.GroupKindUnrecognized && direct.ScopeAnchorKindUnrecognized && direct.RequestedSubjectKindUnrecognized && direct.ScopeAnchorTermTruncated) {
					t.Fatalf("fixture did not set the unrecognized flags: %v", want)
				}
				primary := mustRuntime(t, arm.primary, Config{Fallback: fallback})
				_, receipt, err := primary.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
				if err != nil {
					t.Fatalf("InterpretQuestion() error = %v", err)
				}
				if !receipt.FallbackUsed || receipt.Outcome != "fallback" {
					t.Fatalf("fallback not used: used=%t outcome=%q", receipt.FallbackUsed, receipt.Outcome)
				}
				got := captureFields(receipt)
				for k, w := range want {
					if got[k] != w {
						t.Errorf("%s = %v, want the fallback's %v", k, got[k], w)
					}
				}
			})
		}
	}
}
