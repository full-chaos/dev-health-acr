package hosted

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12554: the one sunburst switch reaches the engine's selection, the
// answer-reuse key and the investment provider from the deployment's config.
// Off, the reuse key is the one a deployment with every switch off had before
// the switch existed.
func TestBuildContextFabricInvestigator_wiresTheInvestmentSunburstSwitch(t *testing.T) {
	configureGraph(t)
	versions := map[bool]string{}
	for _, on := range []bool{false, true} {
		request, postgres, clickhouse := investigatorBuildRequest(t)
		request.config.AnswerReuseMaxAge = time.Hour
		request.config.ContextFabricInvestmentSunburstEnabled = on
		// open() makes this holder before composing, and binds the runner to it.
		request.sourceHealthOperations = devhealthfacts.NewOperationHolder()
		investigator, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgres, clickhouse, nil)
		if err != nil {
			t.Fatalf("buildContextFabricInvestigator() = %v", err)
		}
		engine, ok := investigator.(*contextfabric.Engine)
		if !ok {
			t.Fatalf("investigator is a %T, want *contextfabric.Engine", investigator)
		}
		options := mustField(t, reflect.ValueOf(engine).Elem(), "renderShapeOptions")
		if got := mustField(t, options, "InvestmentSubcategoryShare").Bool(); got != on {
			t.Fatalf("config on=%v: the engine's InvestmentSubcategoryShare = %v", on, got)
		}
		authorities := mustField(t, reflect.ValueOf(engine).Elem(), "reuseVersionAuthorities")
		got := mustField(t, authorities, "QuestionFamilyVersion").String()
		if want := contextfabric.ReuseQuestionFamilyVersion(contextfabric.RenderShapeOptions{InvestmentSubcategoryShare: on}); got != want {
			t.Errorf("sunburst on=%v: QuestionFamilyVersion = %q, want %q", on, got, want)
		}
		if !on && got != contextfabric.QuestionFamilyTableVersion {
			t.Errorf("sunburst off: QuestionFamilyVersion = %q, want the table version %q, as before the switch", got, contextfabric.QuestionFamilyTableVersion)
		}
		versions[on] = got
		provider := findInvestmentProvider(reflect.ValueOf(engine), map[uintptr]bool{}, 0)
		if !provider.IsValid() {
			t.Fatal("no investment provider reachable from the engine")
		}
		if got := mustField(t, provider.Elem(), "subcategoryBreakdown").Bool(); got != on {
			t.Fatalf("config on=%v: the investment provider's subcategoryBreakdown = %v", on, got)
		}
		if got := mustField(t, provider.Elem(), "operations"); got.IsNil() || got.Pointer() != reflect.ValueOf(request.sourceHealthOperations).Pointer() {
			t.Fatal("the investment provider does not read through the runtime's operation holder")
		}
	}
	if versions[false] == versions[true] {
		t.Fatalf("the sunburst switch does not reach the reuse key: %q either way", versions[false])
	}
}

var investmentProviderType = reflect.TypeOf(&devhealthfacts.InvestmentProvider{})

// findInvestmentProvider walks the engine's values to the investment provider
// the composition built, through unexported fields, without calling anything.
func findInvestmentProvider(value reflect.Value, seen map[uintptr]bool, depth int) reflect.Value {
	if !value.IsValid() || depth > 14 {
		return reflect.Value{}
	}
	if value.Type() == investmentProviderType {
		if value.IsNil() {
			return reflect.Value{}
		}
		return value
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() || seen[value.Pointer()] {
			return reflect.Value{}
		}
		seen[value.Pointer()] = true
		return findInvestmentProvider(value.Elem(), seen, depth+1)
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Value{}
		}
		return findInvestmentProvider(value.Elem(), seen, depth+1)
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if found := findInvestmentProvider(value.Field(i), seen, depth+1); found.IsValid() {
				return found
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if found := findInvestmentProvider(value.Index(i), seen, depth+1); found.IsValid() {
				return found
			}
		}
	case reflect.Map:
		iter := value.MapRange()
		for iter.Next() {
			if found := findInvestmentProvider(iter.Value(), seen, depth+1); found.IsValid() {
				return found
			}
		}
	}
	return reflect.Value{}
}

// admittingGraph admits every subject the gate asks about: the subject gate's
// graph authority, for a caller whose team and repository are in its graph.
type admittingGraph struct{}

func (admittingGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "g", Epoch: 1}, nil
}

func (admittingGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i := range out {
		out[i] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}

func (admittingGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

func admittingRunner(t *testing.T, client directread.QueryClient) *directread.OperationRunner {
	t.Helper()
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: catalogue, Gate: directread.NewSubjectGate(admittingGraph{}, directread.NewSlogRecorder(slog.Default())), Client: client,
		Now: func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// The widened caller sends its variables to the runner, which holds them to
// each scoped entry's policy. The provider's variables name no useInvestment:
// the policy forces it, and refuses a client that names it (the third case). Since 09bf92e6 (CHAOS-12691) both scoped entries
// allow measure ISSUE_SIZE, so the provider's exact variables are admitted for
// a team and for a repository: one upstream call each, with the measure, the
// forced investment source, the level and the id in its ops form. A refused
// call (CHURN_LOC is still out) reaches the provider as the closed call status
// it serves as a coverage reason, and nothing goes upstream. With no variables
// the same entry is refused for another reason, so the variables are what the
// policy read.
func TestFactOperationCallerSendsVariablesToThePolicy(t *testing.T) {
	const orgID, repoID = "11111111-1111-4111-8111-111111111111", "8d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a11"
	principal := storage.Principal{OrgID: orgID, Subject: "user-1111", CredentialID: "cred-1111"}
	ctx := observability.WithRequestID(context.Background(), "req_0123456789abcdef0123456789abcdef")
	answer := `{"data":{"analytics":{"breakdowns":[{"dimension":"theme","measure":"issue_size","items":[{"key":"quality","value":2.5,"__typename":"BreakdownItem"}],"__typename":"BreakdownResult"}],"evidenceQualityDistribution":null,"evidenceQualityStats":null,"__typename":"AnalyticsResult"}}}`
	variables := func(level, id, measure string) []byte {
		return []byte(`{"batch":{"breakdowns":[{"dimension":"THEME","measure":"` + measure + `","dateRange":{"startDate":"2026-09-01","endDate":"2026-10-01"},"topN":100}],"filters":{"scope":{"level":"` + level + `","ids":["` + id + `"]}}}}`)
	}
	for _, entry := range []struct {
		operation, level, id, opsID string
	}{
		{devhealthfacts.InvestmentBreakdownTeamOperationName, "TEAM", "team:linear:CHAOS", "linear:CHAOS"},
		{devhealthfacts.InvestmentBreakdownRepositoryOperationName, "REPO", "repository:" + repoID, repoID},
	} {
		t.Run(entry.operation, func(t *testing.T) {
			client := &recordingQueryClient{result: directread.QueryResult{StatusCode: 200, Body: []byte(answer)}}
			caller := newFactOperationCaller(admittingRunner(t, client))
			served, err := caller.CallOperation(ctx, principal, entry.operation, variables(entry.level, entry.id, "ISSUE_SIZE"))
			if err != nil {
				t.Fatal(err)
			}
			if !served.Served || !served.Complete || !strings.Contains(string(served.Data), `"analytics"`) {
				t.Fatalf("ISSUE_SIZE: outcome %+v, want served and complete", served)
			}
			if len(client.calls) != 1 {
				t.Fatalf("ISSUE_SIZE: upstream calls %d, want 1", len(client.calls))
			}
			sent, _ := json.Marshal(client.calls[0].Variables)
			for _, want := range []string{`"measure":"ISSUE_SIZE"`, `"dimension":"THEME"`, `"useInvestment":true`, `"level":"` + entry.level + `"`, `"ids":["` + entry.opsID + `"]`, `"orgId":"` + orgID + `"`} {
				if !strings.Contains(string(sent), want) {
					t.Fatalf("ISSUE_SIZE: sent variables %s lack %s", sent, want)
				}
			}

			refused, err := caller.CallOperation(ctx, principal, entry.operation, variables(entry.level, entry.id, "CHURN_LOC"))
			if err != nil {
				t.Fatal(err)
			}
			if refused.Served || refused.Reason != "refused:variable_not_allowed" || len(client.calls) != 1 {
				t.Fatalf("CHURN_LOC: outcome %+v after %d calls, want the closed refusal and nothing upstream", refused, len(client.calls))
			}
			forced := []byte(strings.Replace(string(variables(entry.level, entry.id, "ISSUE_SIZE")), `"filters"`, `"useInvestment":true,"filters"`, 1))
			named, err := caller.CallOperation(ctx, principal, entry.operation, forced)
			if err != nil {
				t.Fatal(err)
			}
			if named.Served || named.Reason != "refused:variable_not_allowed" || len(client.calls) != 1 {
				t.Fatalf("useInvestment named by the client: outcome %+v, want the policy's refusal", named)
			}
			without, err := caller.CallOperation(ctx, principal, entry.operation, nil)
			if err != nil {
				t.Fatal(err)
			}
			if without.Served || without.Reason == "" || without.Reason == refused.Reason || len(client.calls) != 1 {
				t.Fatalf("no variables: outcome %+v, want another closed refusal and nothing upstream", without)
			}
		})
	}
}
