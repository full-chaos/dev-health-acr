package contextfabric_test

import (
	"bytes"
	"log/slog"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
)

// TestReadFallbackDecisionLineCertifies pins the line's keys and closed
// labels; an outcome outside the vocabulary fails certification.
func TestReadFallbackDecisionLineCertifies(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	sink, ctx := answerLineSink(&buf, slog.LevelInfo)
	sink.RecordReadFallbackDecision(ctx, principalOrg1, contextfabric.ReadFallbackDecisionEvent{
		Reason: contextfabric.ReadFallbackReasonJudgmentUnservable, Outcome: contextfabric.ReadFallbackOutcomeApplied, PlannedKinds: 1,
	})
	pinKeys(t, bytes.NewBuffer(buf.Bytes()), []string{"time", "level", "msg", "org_id", "reason", "outcome", "planned_kinds", "error_class", "rejection", "request_id"}, 1)
	certifyRequestLine(t, buf.Bytes(), eventspec.ReadFallbackDecision, map[string]any{"org_id": "org_1", "reason": "judgment_unservable", "outcome": "applied", "planned_kinds": 1})

	var none bytes.Buffer
	noneSink, noneCtx := answerLineSink(&none, slog.LevelInfo)
	noneSink.RecordReadFallbackDecision(noneCtx, principalOrg1, contextfabric.ReadFallbackDecisionEvent{Outcome: contextfabric.ReadFallbackOutcomeNotApplicable})
	certifyRequestLine(t, none.Bytes(), eventspec.ReadFallbackDecision, map[string]any{"reason": "none", "outcome": "not_applicable", "planned_kinds": 0})

	var failed bytes.Buffer
	failedSink, failedCtx := answerLineSink(&failed, slog.LevelInfo)
	failedSink.RecordReadFallbackDecision(failedCtx, principalOrg1, contextfabric.ReadFallbackDecisionEvent{
		Reason: contextfabric.ReadFallbackReasonJudgmentUnservable, Outcome: contextfabric.ReadFallbackOutcomePlanInvalid, Rejection: contextfabric.ReadPlanRejectionKindNotOffered,
	})
	certifyRequestLine(t, failed.Bytes(), eventspec.ReadFallbackDecision, map[string]any{"outcome": "plan_invalid", "rejection": "kind_not_offered"})

	var outside bytes.Buffer
	outsideSink, outsideCtx := answerLineSink(&outside, slog.LevelInfo)
	outsideSink.RecordReadFallbackDecision(outsideCtx, principalOrg1, contextfabric.ReadFallbackDecisionEvent{Outcome: "replanned"})
	log, err := certify.Parse(outside.Bytes())
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.ReadFallbackDecision, Want: map[string]any{"org_id": "org_1"}}); err == nil {
		t.Fatalf("a line naming an outcome outside the vocabulary certified:\n%s", outside.Bytes())
	}
}

// TestCycleTimeQuestionOverTheProductionCapabilities runs the family-fit test
// and the offered-kind list over the registry's own declarations: the
// ranking family asked for performance fires F1, the planner is offered the
// team's metrics and never a repository-only kind, and a work-item kind the
// team reaches through its fact-scope chain does not fire F3.
func TestCycleTimeQuestionOverTheProductionCapabilities(t *testing.T) {
	t.Parallel()
	capabilities := liveCapabilityList(t)
	ranking, _ := contextfabric.LookupQuestionFamily(contextfabric.QuestionFamilyDiscoveredCohortRanking)
	subject, _ := contextfabric.LookupQuestionFamily(contextfabric.QuestionFamilySubjectInvestigation)
	if got := contextfabric.DecideReadFallback(contextfabric.ReadFallbackDecisionInput{
		Definition: ranking, JudgmentKind: contextfabric.RequestedJudgmentKindPerformance,
		InterpretedKinds: []contextfabric.FactKind{contextfabric.FactBlockers}, MemberKind: contextfabric.SubjectTeam, Capabilities: capabilities,
	}); got != contextfabric.ReadFallbackReasonJudgmentUnservable {
		t.Fatalf("cycle-time decision = %q, want judgment_unservable", got)
	}
	offered := contextfabric.ReadPlanOfferedKinds(capabilities, contextfabric.SubjectTeam)
	if !slices.Contains(offered, contextfabric.FactMetrics) || slices.Contains(offered, contextfabric.FactFileHotspots) || slices.Contains(offered, contextfabric.FactEvidence) {
		t.Fatalf("kinds offered for a team = %v, want metrics and no repository-only or providerless kind", offered)
	}
	if kinds, rejection := contextfabric.ValidateReadPlan(contextfabric.ReadPlan{Steps: []contextfabric.ReadPlanStep{{Tool: contextfabric.ReadPlanToolReadFacts, Kinds: []contextfabric.FactKind{contextfabric.FactMetrics}}}}, contextfabric.SubjectTeam, capabilities); rejection != "" || !slices.Equal(kinds, []contextfabric.FactKind{contextfabric.FactMetrics}) {
		t.Fatalf("the cycle-time plan = %v %q, want [metrics]", kinds, rejection)
	}
	if got := contextfabric.DecideReadFallback(contextfabric.ReadFallbackDecisionInput{
		Definition: subject, InterpretedKinds: []contextfabric.FactKind{contextfabric.FactStatus},
		MemberKind: contextfabric.SubjectTeam, Capabilities: capabilities,
	}); got != "" {
		t.Fatalf("a team status question fires %q; the team reaches status through its work items", got)
	}
}
