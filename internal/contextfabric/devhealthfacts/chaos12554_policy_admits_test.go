package devhealthfacts_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// admitAll is a subject gate graph that admits every subject.
type admitAll struct{}

func (admitAll) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "g", Epoch: 1}, nil
}

func (admitAll) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range out {
		out[i] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}

func (admitAll) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

// opsByDimension is the ops upstream: it answers the breakdown the call asks
// for, and keeps every call.
type opsByDimension struct{ calls []directread.QueryCall }

func (o *opsByDimension) Execute(_ context.Context, call directread.QueryCall) (directread.QueryResult, error) {
	o.calls = append(o.calls, call)
	raw, _ := json.Marshal(call.Variables)
	items := `[{"key":"feature_delivery","value":6,"__typename":"BreakdownItem"},{"key":"quality","value":2,"__typename":"BreakdownItem"}]`
	dimension := "theme"
	if strings.Contains(string(raw), `"dimension":"SUBCATEGORY"`) {
		items = `[{"key":"feature_delivery.roadmap","value":6,"__typename":"BreakdownItem"},{"key":"quality.bugfix","value":2,"__typename":"BreakdownItem"}]`
		dimension = "subcategory"
	}
	body := `{"data":{"analytics":{"breakdowns":[{"dimension":"` + dimension + `","measure":"issue_size","items":` + items + `,"__typename":"BreakdownResult"}],"evidenceQualityDistribution":null,"evidenceQualityStats":null,"__typename":"AnalyticsResult"}}}`
	return directread.QueryResult{StatusCode: 200, Body: []byte(body)}, nil
}

// runnerCaller reduces a runner answer the way the hosted fact operation
// caller does.
type runnerCaller struct{ runner *directread.OperationRunner }

func (c runnerCaller) CallOperation(ctx context.Context, principal storage.Principal, operation string, variables json.RawMessage) (devhealthfacts.OperationOutcome, error) {
	resp, err := c.runner.Run(ctx, principal, directread.OperationRequest{Operation: operation, Variables: variables})
	if err != nil {
		return devhealthfacts.OperationOutcome{}, err
	}
	if resp.Call != directread.CallServed {
		reason := string(resp.Call)
		if resp.Refusal != nil {
			reason += ":" + string(resp.Refusal.Code)
		}
		return devhealthfacts.OperationOutcome{Reason: reason}, nil
	}
	return devhealthfacts.OperationOutcome{Served: true, Complete: resp.Completeness == directread.CompletenessDeclaredComplete, Data: resp.Data}, nil
}

// CHAOS-12554 with 09bf92e6 (CHAOS-12691): the provider's own variables pass
// the real run_operation policy of investmentBreakdownTeam, so the team fact
// gets its table through the vendored catalogue, not only through a fake
// caller. A variable the policy refuses (a client-named useInvestment, say)
// fails here.
func TestCHAOS12554ThePolicyAdmitsTheProvidersVariables(t *testing.T) {
	t.Parallel()
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	upstream := &opsByDimension{}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: catalogue, Gate: directread.NewSubjectGate(admitAll{}, directread.NewSlogRecorder(slog.Default())), Client: upstream,
		Now: func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := findProvider(t, devhealthfacts.NewProvidersWithOptions(&fakeClient{tables: teamMixTables("CHAOS")}, devhealthfacts.ProviderOptions{
		Operations: sourceHealthHolder(runnerCaller{runner: runner}), InvestmentSunburstEnabled: true,
	}), contextfabric.FactInvestment)
	principal := storage.Principal{OrgID: "11111111-1111-4111-8111-111111111111", Subject: "user-1111", CredentialID: "cred-1111"}
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if strings.Contains(result.Reason+result.RequestNote, "investment_subcategory_breakdown_withheld") {
		t.Fatalf("withheld: %q %q", result.Reason, result.RequestNote)
	}
	for _, reason := range result.SubjectReasons {
		if strings.Contains(reason, "investment_subcategory_breakdown_withheld") {
			t.Fatalf("withheld: %q", reason)
		}
	}
	found := false
	for _, fact := range result.Facts {
		if value, ok := fact.Fields["subcategory_breakdown"]; ok && value.Table != nil && value.Table.Detail && len(value.Rows) == 15 {
			found = true
		}
	}
	if !found {
		t.Fatal("the team fact carries no subcategory table")
	}
	if len(upstream.calls) != 2 {
		t.Fatalf("upstream calls %d, want THEME then SUBCATEGORY", len(upstream.calls))
	}
	for index, dimension := range []string{"THEME", "SUBCATEGORY"} {
		sent, _ := json.Marshal(upstream.calls[index].Variables)
		for _, want := range []string{`"dimension":"` + dimension + `"`, `"measure":"ISSUE_SIZE"`, `"useInvestment":true`, `"level":"TEAM"`, `"ids":["CHAOS"]`} {
			if !strings.Contains(string(sent), want) {
				t.Fatalf("call %d sent %s, lacks %s", index, sent, want)
			}
		}
	}
}
