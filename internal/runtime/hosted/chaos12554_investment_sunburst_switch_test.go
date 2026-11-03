package hosted

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
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

// The widened caller sends its variables to the runner, which holds them to the
// operation's policy. Today the policy of the scoped investment entries does
// not list ISSUE_SIZE among the measures, so the ruled sunburst call is refused
// before anything goes upstream; the provider serves that as a closed reason.
// Without variables the same operation is refused for another reason, so the
// variables did reach the policy.
func TestFactOperationCallerSendsVariablesToThePolicy(t *testing.T) {
	client := &recordingQueryClient{}
	runner, _ := sourceHealthRunner(t, client)
	caller := newFactOperationCaller(runner)
	principal := storage.Principal{OrgID: "11111111-1111-4111-8111-111111111111"}
	variables := []byte(`{"batch":{"breakdowns":[{"dimension":"THEME","measure":"ISSUE_SIZE","dateRange":{"startDate":"2026-09-01","endDate":"2026-10-01"},"topN":100}],"useInvestment":true,"filters":{"scope":{"level":"TEAM","ids":["team:linear:CHAOS"]}}}}`)
	with, err := caller.CallOperation(context.Background(), principal, devhealthfacts.InvestmentBreakdownTeamOperationName, variables)
	if err != nil {
		t.Fatal(err)
	}
	without, err := caller.CallOperation(context.Background(), principal, devhealthfacts.InvestmentBreakdownTeamOperationName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if with.Served || with.Reason == "" || with.Reason == without.Reason {
		t.Fatalf("with variables %+v, without %+v: want two different closed refusals", with, without)
	}
	if with.Reason != "refused:variable_not_allowed" {
		t.Fatalf("with variables: reason %q, want the policy's ISSUE_SIZE refusal", with.Reason)
	}
	if len(client.calls) != 0 {
		t.Fatalf("a refused call went upstream: %d calls", len(client.calls))
	}
}
