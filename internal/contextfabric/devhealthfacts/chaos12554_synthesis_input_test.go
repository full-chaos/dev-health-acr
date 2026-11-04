package devhealthfacts_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// countingCaller counts the calls it forwards.
type countingCaller struct {
	inner devhealthfacts.OperationCaller
	calls int
}

func (c *countingCaller) CallOperation(ctx context.Context, principal storage.Principal, operation string, variables json.RawMessage) (devhealthfacts.OperationOutcome, error) {
	c.calls++
	return c.inner.CallOperation(ctx, principal, operation, variables)
}

// synthesisInputDigest reads a team's investment and digests the facts the
// synthesis model is given.
func synthesisInputDigest(t *testing.T, options devhealthfacts.ProviderOptions) (string, []contextfabric.CanonicalFact) {
	t.Helper()
	provider := findProvider(t, devhealthfacts.NewProvidersWithOptions(&fakeClient{tables: teamMixTables("CHAOS")}, options), contextfabric.FactInvestment)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	facing := synthesisprompt.ModelFacingFacts(result.Facts)
	raw, err := json.Marshal(struct {
		Facts          []contextfabric.CanonicalFact
		Reason, Note   string
		SubjectReasons map[string]string
		State          contextfabric.SourceState
	}{facing, result.Reason, result.RequestNote, result.SubjectReasons, result.State})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), facing
}

// CHAOS-12554 (lead ruling): with the sunburst switch off, the synthesis input
// is byte-identical to a deployment without the switch, even with an operation
// caller bound, and nothing is called. On, the input names the detail table, so
// this digest comparison can fail.
func TestCHAOS12554OffTheSynthesisInputIsAsBefore(t *testing.T) {
	t.Parallel()
	before, _ := synthesisInputDigest(t, devhealthfacts.ProviderOptions{})
	caller := &countingCaller{inner: sunburstBreakdownCaller()}
	off, _ := synthesisInputDigest(t, devhealthfacts.ProviderOptions{Operations: sourceHealthHolder(caller)})
	if off != before {
		t.Fatalf("switch off: input digest %s, want %s", off, before)
	}
	if caller.calls != 0 {
		t.Fatalf("switch off: %d calls, want none", caller.calls)
	}
	on, facing := synthesisInputDigest(t, devhealthfacts.ProviderOptions{Operations: sourceHealthHolder(caller), InvestmentSunburstEnabled: true})
	if on == before {
		t.Fatal("switch on: the input did not change, so this comparison cannot fail")
	}
	if caller.calls != 2 {
		t.Fatalf("switch on: %d calls, want THEME then SUBCATEGORY", caller.calls)
	}
	found := false
	for _, fact := range facing {
		if slices.Contains(fact.DetailTables, "subcategory_breakdown") {
			found = true
		}
		if _, leaked := fact.Fields["subcategory_breakdown"]; leaked {
			t.Fatal("the model saw the detail table's rows")
		}
	}
	if !found {
		t.Fatal("switch on: no fact lists subcategory_breakdown among its detail tables")
	}
}
