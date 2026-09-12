package falkorgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDiscoverContextOrganizationScopeWithoutACountGoalIsNotAdmitted is the
// engine cell for the organization-scope admission rule, driven through
// DiscoverContext and read off the emitted Info line an operator would see.
//
// The frame is the one the corpus produced for "where should we focus next?":
// organization_scope, an interpreter-supplied servable member kind, goals
// [rank_or_survey allocate_investment]. It must not reach the census, and the
// line must say WHY -- the member kind is there, the count goal is not.
//
// The control is the same frame with count_or_aggregate, which is the shape of
// the questions this admission exists for ("how many repositories are there
// across the organization"); it must be admitted under its own basis. Without
// the control, a gate that refused every organization scope would pass.
func TestDiscoverContextOrganizationScopeWithoutACountGoalIsNotAdmitted(t *testing.T) {
	t.Parallel()
	memberKind := contextfabric.SubjectTeam
	for _, testCase := range []struct {
		name         string
		goals        []contextfabric.InvestigationGoal
		wantAdmitted bool
		wantBasis    CohortExactNameCensusBasis
	}{
		{"no count goal", []contextfabric.InvestigationGoal{contextfabric.GoalRankOrSurvey, contextfabric.GoalAllocateInvestment}, false, CohortExactNameCensusBasisOrganizationScopeCountGoalAbsent},
		{"control: count goal", []contextfabric.InvestigationGoal{contextfabric.GoalCountOrAggregate}, true, CohortExactNameCensusBasisOrganizationScopeMemberKind},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			telemetry := SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))}
			fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) { return nil, nil }}
			adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
			request := cohortDiscoveryRequest(contextfabric.ShapeDiscoveredCohort)
			request.Frame = &contextfabric.QuestionFrame{
				Goals: testCase.goals,
				SubjectExpression: contextfabric.SubjectExpression{
					Kind: contextfabric.SubjectExpressionOrganizationScope,
					Org:  &contextfabric.OrganizationScopeExpression{MemberKind: &memberKind},
				},
				Temporal: contextfabric.TemporalIntentCurrent,
				Version:  contextfabric.QuestionFrameVersion,
			}
			if _, err := adapter.DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request); err != nil {
				t.Fatalf("DiscoverContext() error = %v", err)
			}
			var gate map[string]any
			for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
				var fields map[string]any
				if json.Unmarshal(line, &fields) == nil && fields["msg"] == "context_fabric: cohort exact-name census gate" {
					gate = fields
				}
			}
			if gate == nil {
				t.Fatalf("no census gate line was emitted, so the decision is invisible to an operator; log=%s", buf.String())
			}
			if gate["level"] != "INFO" {
				t.Errorf("census gate line level = %v, want INFO", gate["level"])
			}
			if gate["admitted"] != testCase.wantAdmitted || gate["basis"] != string(testCase.wantBasis) {
				t.Errorf("census gate line = admitted:%v basis:%v, want admitted:%v basis:%q", gate["admitted"], gate["basis"], testCase.wantAdmitted, testCase.wantBasis)
			}
		})
	}
}
