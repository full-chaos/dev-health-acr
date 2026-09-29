package directread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ---------------------------------------------------------------- grants

func grantGraph(n int, slugOf func(i int) string) *lookupFakeGraph {
	nodes := make([]LookupNode, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("repository:%08d-0000-4000-8000-000000000000", i)
		nodes = append(nodes, repoNode(id, slugOf(i), slugOf(i)))
	}
	return &lookupFakeGraph{orgs: map[string]*lookupOrgGraph{orgA: {nodes: nodes}}}
}

// The grant listing returns only the repositories the gate admits for the
// caller: a restricted caller granted 2 of 5 gets exactly those 2.
func TestGrantedRepositoriesReturnsAdmittedOnly(t *testing.T) {
	graph := grantGraph(5, func(i int) string { return fmt.Sprintf("acme/r%d", i) })
	grants := NewGrantedRepositories(newLookup(graph, nil))
	refs, err := grants.GrantedRepositories(context.Background(), lookupPrincipal(orgA, "acme/r1", "acme/r3"))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0].CanonicalID != "repository:00000001-0000-4000-8000-000000000000" || refs[1].CanonicalID != "repository:00000003-0000-4000-8000-000000000000" {
		t.Fatalf("granted %v", refs)
	}
	for _, ref := range refs {
		if ref.Kind != contextfabric.SubjectRepository {
			t.Fatalf("kind %v", ref.Kind)
		}
	}
}

// A grant that reaches more than MaxGrantedRepositories repositories is an
// error, never a partial list.
func TestGrantedRepositoriesCapHitIsAnError(t *testing.T) {
	slug := func(i int) string { return fmt.Sprintf("acme/r%04d", i) }
	var scopes []string
	for i := 0; i < MaxGrantedRepositories+1; i++ {
		scopes = append(scopes, slug(i))
	}
	graph := grantGraph(MaxGrantedRepositories+1, slug)
	refs, err := NewGrantedRepositories(newLookup(graph, nil)).GrantedRepositories(context.Background(), lookupPrincipal(orgA, scopes...))
	if !errors.Is(err, ErrGrantedRepositoriesIncomplete) || refs != nil {
		t.Fatalf("cap hit: refs %d err %v", len(refs), err)
	}
	// Exactly at the cap is complete.
	graph = grantGraph(MaxGrantedRepositories, slug)
	refs, err = NewGrantedRepositories(newLookup(graph, nil)).GrantedRepositories(context.Background(), lookupPrincipal(orgA, scopes[:MaxGrantedRepositories]...))
	if err != nil || len(refs) != MaxGrantedRepositories {
		t.Fatalf("at the cap: refs %d err %v", len(refs), err)
	}
}

// A graph scan that was truncated (the lookup's scan bound) is an error:
// the admitted list may be missing granted repositories.
func TestGrantedRepositoriesTruncatedScanIsAnError(t *testing.T) {
	graph := grantGraph(MaxFindScanNodes+1, func(i int) string { return fmt.Sprintf("acme/r%05d", i) })
	_, err := NewGrantedRepositories(newLookup(graph, nil)).GrantedRepositories(context.Background(), lookupPrincipal(orgA, "acme/r00001"))
	if !errors.Is(err, ErrGrantedRepositoriesIncomplete) {
		t.Fatalf("truncated scan: %v", err)
	}
}

// A lookup failure is returned (the runner then answers unavailable), and
// a nil lookup fails closed.
func TestGrantedRepositoriesLookupFailureFailsClosed(t *testing.T) {
	graph := grantGraph(2, func(i int) string { return fmt.Sprintf("acme/r%d", i) })
	graph.listErr = contextfabric.ErrUnavailable
	if _, err := NewGrantedRepositories(newLookup(graph, nil)).GrantedRepositories(context.Background(), lookupPrincipal(orgA, "acme/r1")); !errors.Is(err, ErrFindUnavailable) {
		t.Fatalf("lookup failure: %v", err)
	}
	if _, err := NewGrantedRepositories(nil).GrantedRepositories(context.Background(), lookupPrincipal(orgA, "acme/r1")); err == nil {
		t.Fatal("nil lookup served")
	}
}

// The cap fits one gate decision and every restricted operation's forced
// variable.
func TestGrantedRepositoriesCapFitsGateAndPolicy(t *testing.T) {
	if MaxGrantedRepositories > MaxSubjectsPerRequest {
		t.Fatalf("cap %d exceeds one gate decision (%d)", MaxGrantedRepositories, MaxSubjectsPerRequest)
	}
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range cat.Operations(CallerRestricted) {
		rule, ok := op.Variable(op.Scope(CallerRestricted).ForcedVariablePath)
		if !ok || rule.MaxItems == 0 || MaxGrantedRepositories > rule.MaxItems {
			t.Fatalf("%s forced variable max_items %d < cap %d", op.Name, rule.MaxItems, MaxGrantedRepositories)
		}
	}
}

// ---------------------------------------------------------------- catalog

func catalogFor(t *testing.T, class PrincipalClass, dataRead, servable bool, sections ...string) DataCatalog {
	t.Helper()
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCatalogSections(strings.Join(sections, ","))
	if err != nil {
		t.Fatal(err)
	}
	return BuildDataCatalog(cat, CatalogCaller{PrincipalClass: class, Scopes: []string{"context:read", "data:read"}, DataRead: dataRead, OperationsServable: servable}, parsed)
}

func TestDataCatalogOperationsPerCallerClass(t *testing.T) {
	restricted := catalogFor(t, ClassRestricted, true, true)
	unrestricted := catalogFor(t, ClassUnrestricted, true, true)
	universal := catalogFor(t, ClassUniversal, true, true)
	if got := len(restricted.Operations.Operations); got != 3 {
		t.Fatalf("restricted: %d operations", got)
	}
	if got := len(unrestricted.Operations.Operations); got != 16 || len(universal.Operations.Operations) != 16 {
		t.Fatalf("unrestricted: %d, universal %d", got, len(universal.Operations.Operations))
	}
	for _, op := range restricted.Operations.Operations {
		if op.ScopeClass != ScopeForcedGrant || op.ForcedVariable == "" || !op.Available {
			t.Fatalf("restricted entry %+v", op)
		}
	}
	// Restricted: the 13 operations served only to unrestricted callers are
	// listed as not served with the class refusal.
	classRefused := 0
	for _, ns := range restricted.Operations.NotServed {
		if ns.Code == RefusalOperationNotServedForCaller {
			classRefused++
		}
	}
	if classRefused != 13 {
		t.Fatalf("restricted: %d class refusals, want 13", classRefused)
	}
	cat, _ := DefaultCatalogue()
	if got := len(unrestricted.Operations.NotServed); got != len(cat.NotServed()) {
		t.Fatalf("unrestricted not_served %d, catalogue %d", got, len(cat.NotServed()))
	}
}

func TestDataCatalogEveryServedOperationHasAPurpose(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, op := range cat.Operations(CallerUnrestricted) {
		served[op.Name] = true
		purpose := OperationPurpose(op.Name)
		if purpose == "" || len(purpose) > 200 {
			t.Errorf("%s purpose %q", op.Name, purpose)
		}
	}
	for name := range operationPurposes {
		if !served[name] {
			t.Errorf("purpose for %s, which is not a served operation", name)
		}
	}
}

func TestDataCatalogAvailabilityAndCaller(t *testing.T) {
	noData := catalogFor(t, ClassUnrestricted, false, true)
	if noData.Operations.Available || noData.Operations.Reason != CatalogUnavailableScopeMissing || len(noData.Operations.Operations) != 16 {
		t.Fatalf("no data:read: %+v", noData.Operations)
	}
	for _, op := range noData.Operations.Operations {
		if op.Available || op.Reason != CatalogUnavailableScopeMissing {
			t.Fatalf("entry %s available without data:read", op.Name)
		}
	}
	off := catalogFor(t, ClassUnrestricted, true, false)
	if off.Operations.Available || off.Operations.Reason != CatalogUnavailableQueryNotConfigure {
		t.Fatalf("not configured: %+v", off.Operations)
	}
	// The caller section: scopes and grant class only.
	cat, _ := DefaultCatalogue()
	sections, _ := ParseCatalogSections("")
	built := BuildDataCatalog(cat, CatalogCaller{PrincipalClass: ClassRestricted, Scopes: []string{"data:read", "context:read", "acme/secret-repo", "data:read"}, DataRead: true, OperationsServable: true}, sections)
	encoded, _ := json.Marshal(built.Caller)
	if string(encoded) != `{"scopes":["context:read","data:read"],"grant_class":"restricted"}` {
		t.Fatalf("caller section %s", encoded)
	}
}

func TestDataCatalogSectionsAndStaticParts(t *testing.T) {
	if _, err := ParseCatalogSections("operations,bogus"); err == nil {
		t.Fatal("unknown section accepted")
	}
	only := catalogFor(t, ClassUnrestricted, true, true, "limits", "limits")
	if only.Operations != nil || only.Subjects != nil || only.Limits == nil || len(only.Sections) != 1 {
		t.Fatalf("filter: %+v", only)
	}
	all := catalogFor(t, ClassUnrestricted, true, true)
	if all.Facts == nil || all.Facts.Served || all.Facts.Note != CatalogFactsNote || len(all.Facts.Kinds) != 0 {
		t.Fatalf("facts %+v", all.Facts)
	}
	if len(all.Relationships.Types) != 12 {
		t.Fatalf("relationships %d", len(all.Relationships.Types))
	}
	seen := map[string]bool{}
	for _, rel := range all.Relationships.Types {
		if !contractsv1.ValidContextFabricRelationshipType(contractsv1.ContextFabricRelationshipType(rel.Type)) || seen[rel.Type] || len(rel.TargetKinds) == 0 {
			t.Fatalf("relationship %+v", rel)
		}
		seen[rel.Type] = true
		for _, kind := range append(append([]string{}, rel.SourceKinds...), rel.TargetKinds...) {
			if !contractsv1.ValidContextFabricSubjectKind(contractsv1.ContextFabricSubjectKind(kind)) {
				t.Fatalf("%s end kind %q", rel.Type, kind)
			}
		}
	}
	if len(all.Subjects.Kinds) != contractsv1.ContextFabricSubjectKindCount {
		t.Fatalf("subjects %d", len(all.Subjects.Kinds))
	}
	if all.Limits.MaxBytesDefault != 32768 || all.Limits.MaxBytesCap != 262144 || all.Limits.DeadlineSeconds != 30 {
		t.Fatalf("limits %+v", all.Limits)
	}
	cat, _ := DefaultCatalogue()
	if all.Versions.Contract != "acr-data.v1" || all.Versions.SchemaDigest != cat.SchemaDigest() || all.Versions.OpsSourceSHA != cat.Source().Commit {
		t.Fatalf("versions %+v", all.Versions)
	}
	if all.Consistency != "best_effort" || !all.UntrustedContent.Untrusted || all.UntrustedContent.Notice == "" {
		t.Fatalf("label %+v", all.UntrustedContent)
	}
}

// ---------------------------------------------------------------- runner

// An incomplete grant listing refuses with scope_required and sends nothing.
func TestRunnerIncompleteGrantIsScopeRequired(t *testing.T) {
	cat, err := DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	graph := grantGraph(1, func(int) string { return "acme/r" })
	client := &countingClient{}
	runner, err := NewOperationRunner(OperationRunnerConfig{Catalogue: cat, Gate: NewSubjectGate(graph, nil), Client: client, Grants: incompleteGrants{}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.Run(context.Background(), lookupPrincipal(orgA, "acme/r"), OperationRequest{Operation: "compoundingRisk", Variables: json.RawMessage(`{"filter":{"breakout":"REPO"}}`)})
	if err != nil || response.Call != CallRefused || response.Refusal == nil || response.Refusal.Code != RefusalScopeRequired {
		t.Fatalf("incomplete grant: %+v %v", response, err)
	}
	if client.calls != 0 {
		t.Fatal("an incomplete grant reached the query service")
	}
}

type incompleteGrants struct{}

func (incompleteGrants) GrantedRepositories(context.Context, storage.Principal) ([]contextfabric.SubjectRef, error) {
	return nil, ErrGrantedRepositoriesIncomplete
}

type countingClient struct{ calls int }

func (c *countingClient) Execute(context.Context, QueryCall) (QueryResult, error) {
	c.calls++
	return QueryResult{Body: []byte(`{"data":{}}`), StatusCode: 200}, nil
}
