package devhealthfacts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12690 I2: with InvestmentTeamFromOps the team mix is the ops team
// answer (owner-share weighted in ops); off, nothing changes.

var opsTeam = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: teamPrefix + "linear:CHAOS", Label: "CHAOS"}

func readTeamFromOps(t *testing.T, enabled bool, caller OperationCaller, window *contractsv1.ContextFabricRequestedEvidenceWindow, subjects ...contextfabric.SubjectRef) (contextfabric.FactProviderResult, *statementCountingClient) {
	t.Helper()
	holder := NewOperationHolder()
	holder.Set(caller)
	client := &statementCountingClient{}
	provider := newInvestmentProvider(client)
	provider.operations = holder
	provider.teamFromOps = enabled
	if len(subjects) == 0 {
		subjects = []contextfabric.SubjectRef{opsTeam}
	}
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: window},
		Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result, client
}

func TestCHAOS12690TeamMixIsTheOpsAnswer(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	caller := opsMixCaller(&sized)
	result, client := readTeamFromOps(t, true, caller, sunburstWindow())
	if client.queries.Load() != 0 {
		t.Fatalf("the team mix read ClickHouse %d times; it is the ops answer", client.queries.Load())
	}
	if len(caller.calls) != 2 || caller.calls[0].operation != "investmentBreakdownTeam" || !strings.Contains(caller.calls[0].variables, `"level":"TEAM"`) || !strings.Contains(caller.calls[0].variables, `"team:linear:CHAOS"`) {
		t.Fatalf("calls %+v", caller.calls)
	}
	fact := investmentFactOf(result, opsTeam)
	if fact == nil {
		t.Fatalf("no fact; reasons %q", servedReasons(result, opsTeam))
	}
	if got := fact.Fields[contextfabric.FactFieldTheme("feature_delivery")].Number; got == nil || *got != roundMixEffort(19.75/30) {
		t.Errorf("feature_delivery = %v", got)
	}
	for field, want := range map[string]string{"mix_source": "ops.investmentBreakdownTeam", "attribution_basis": "ops_team_owner_share_lines_changed"} {
		if got := fact.Fields[field].String; got == nil || *got != want {
			t.Errorf("%s = %v, want %s", field, got, want)
		}
	}
	if got := fact.Fields["sizing_coverage"].Number; got == nil || *got != 0.75 {
		t.Errorf("sizing_coverage = %v", got)
	}
	if got := fact.Fields["work_unit_count"].Integer; got == nil || *got != 4 {
		t.Errorf("work_unit_count = %v", got)
	}
	if len(fact.EvidenceRefIDs) != 1 || !strings.Contains(fact.EvidenceRefIDs[0], "linear:CHAOS") {
		t.Errorf("evidence refs %v", fact.EvidenceRefIDs)
	}
	// Not served by ops, so not emitted on this path.
	for _, field := range []string{"owned_repository_count", "project_reached_repository_count"} {
		if _, ok := fact.Fields[field]; ok {
			t.Errorf("%s is emitted from the ops path", field)
		}
	}
	for _, theme := range canonicalInvestmentThemes {
		if _, ok := fact.Fields[contextfabric.FactFieldPriorTheme(theme)]; ok {
			t.Errorf("prior_theme_%s on a current-axis read", theme)
		}
	}
}

func TestCHAOS12690TeamSwitchOffMakesNoOpsCall(t *testing.T) {
	t.Parallel()
	caller := opsMixCaller(nil)
	readTeamFromOps(t, false, caller, sunburstWindow())
	if len(caller.calls) != 0 {
		t.Fatalf("switch off made %d ops calls", len(caller.calls))
	}
}

func TestCHAOS12690TeamPriorWindowIsReadForAnExplicitRange(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	caller := opsMixCaller(&sized)
	start, end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	holder := NewOperationHolder()
	holder.Set(caller)
	provider := newInvestmentProvider(&statementCountingClient{})
	provider.operations = holder
	provider.teamFromOps = true
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{opsTeam}, TimeEndExclusive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fact := investmentFactOf(result, opsTeam)
	if fact == nil {
		t.Fatalf("no fact: %q", servedReasons(result, opsTeam))
	}
	if len(caller.calls) != 3 || !strings.Contains(caller.calls[2].variables, `"startDate":"2026-08-17","endDate":"2026-08-31"`) {
		t.Fatalf("calls %+v", caller.calls)
	}
	if got := fact.Fields[contextfabric.FactFieldPriorTheme("feature_delivery")].Number; got == nil {
		t.Error("no prior_theme_feature_delivery")
	}
}

func TestCHAOS12690TeamWithNoAnswerGetsAClosedReason(t *testing.T) {
	t.Parallel()
	caller := &breakdownCaller{theme: OperationOutcome{Reason: "refused:operation_not_served_for_caller"}}
	result, _ := readTeamFromOps(t, true, caller, sunburstWindow())
	if fact := investmentFactOf(result, opsTeam); fact != nil {
		t.Fatalf("a fact is served: %+v", fact.Fields)
	}
	if reasons := servedReasons(result, opsTeam); !strings.Contains(reasons, opsMixWithheldPrefix) {
		t.Fatalf("no named reason: %q", reasons)
	}
}

func TestCHAOS12690TeamPriorAnswerWithAnUnknownPositiveThemeServesNoPrior(t *testing.T) {
	t.Parallel()
	sized := int64(3)
	caller := opsMixCaller(&sized)
	prior := sunburstThemes()
	prior["future_theme"] = 100
	start, end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	holder := NewOperationHolder()
	holder.Set(priorOverrideCaller{inner: caller, prior: breakdownAnswer("THEME", prior)})
	provider := newInvestmentProvider(&statementCountingClient{})
	provider.operations = holder
	provider.teamFromOps = true
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{opsTeam}, TimeEndExclusive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	fact := investmentFactOf(result, opsTeam)
	if fact == nil {
		t.Fatal("the current-window fact must still be served")
	}
	if _, ok := fact.Fields[contextfabric.FactFieldPriorTheme("feature_delivery")]; ok {
		t.Fatal("a prior answer with an unknown positive theme served a prior share")
	}
	if reasons := servedReasons(result, opsTeam); !strings.Contains(reasons, RepoOpsWithheldNoPrior) {
		t.Fatalf("no named reason: %q", reasons)
	}
}

// priorOverrideCaller answers the prior-window call (start 2026-08-17) with
// prior and every other call with inner.
type priorOverrideCaller struct {
	inner *breakdownCaller
	prior OperationOutcome
}

func (c priorOverrideCaller) CallOperation(ctx context.Context, p storage.Principal, operation string, variables json.RawMessage) (OperationOutcome, error) {
	if strings.Contains(string(variables), `"startDate":"2026-08-17"`) {
		return c.prior, nil
	}
	return c.inner.CallOperation(ctx, p, operation, variables)
}
