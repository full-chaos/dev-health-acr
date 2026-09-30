package sourcerow_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	orgID      = "org_1"
	grantedID  = "20000000-0000-4000-8000-000000000002"
	grantedRep = "acme/api"
	secretID   = "30000000-0000-4000-8000-000000000003"
	secretRep  = "other-org/secret"
)

var observedAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// world is a fake ClickHouse: repositories by id, discovery results, and
// catalog rows by (repo, query, locator). Every
// call is recorded with all its arguments.
type world struct {
	repos      map[string]string
	discovered map[string][]contractsv1.ResolvedScope
	rows       map[string][]contextpacket.EvidenceReference
	orgRows    map[string][]contextpacket.EvidenceReference
	fail       map[string]error
	calls      []string
	gate       sourcerow.SubjectGate
}

func newWorld() *world {
	return &world{repos: map[string]string{}, discovered: map[string][]contractsv1.ResolvedScope{}, rows: map[string][]contextpacket.EvidenceReference{}, orgRows: map[string][]contextpacket.EvidenceReference{}, fail: map[string]error{}}
}

func scope(id, slug string) contractsv1.ResolvedScope {
	return contractsv1.ResolvedScope{RepoID: id, RepoSlug: slug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{}}
}

func (w *world) RepositoryByID(_ context.Context, org, repoID string) ([]contractsv1.ResolvedScope, error) {
	w.calls = append(w.calls, fmt.Sprintf("repository_by_id(%s,%s)", org, repoID))
	if err := w.fail["repository_by_id"]; err != nil {
		return nil, err
	}
	if slug, ok := w.repos[repoID]; ok {
		return []contractsv1.ResolvedScope{scope(repoID, slug)}, nil
	}
	return []contractsv1.ResolvedScope{}, nil
}

func (w *world) SourceRowRepositories(_ context.Context, org string, discovery contextpacket.SourceRowDiscovery, entityID string) ([]contractsv1.ResolvedScope, error) {
	w.calls = append(w.calls, fmt.Sprintf("discover(%s,%s,%s)", org, discovery, entityID))
	if err := w.fail["discover"]; err != nil {
		return nil, err
	}
	return append([]contractsv1.ResolvedScope{}, w.discovered[string(discovery)+"|"+entityID]...), nil
}

func (w *world) ResolveOrganizationRow(_ context.Context, org string, read contextpacket.SourceRowRead) ([]contextpacket.EvidenceReference, error) {
	w.calls = append(w.calls, fmt.Sprintf("org_row(%s,%s,%s)", org, read.QueryID, read.Locator))
	if err := w.fail["org_row"]; err != nil {
		return nil, err
	}
	return w.orgRows[read.QueryID+"|"+read.Locator], nil
}

func (w *world) ResolveSourceRow(_ context.Context, org string, target contractsv1.ResolvedScope, read contextpacket.SourceRowRead) ([]contextpacket.EvidenceReference, error) {
	w.calls = append(w.calls, fmt.Sprintf("row(%s,%s,%s,%s,%s)", org, target.RepoID, read.QueryID, read.Locator, read.TaskRef))
	if err := w.fail["row"]; err != nil {
		return nil, err
	}
	return w.rows[target.RepoID+"|"+read.QueryID+"|"+read.Locator], nil
}

// catalogRow is a row as the catalog statement returns it.
func catalogRow(queryID, locator, entityType, entityID, slug string) contextpacket.EvidenceReference {
	return contextpacket.EvidenceReference{RepoSlug: slug, Excerpt: "citation of " + entityID, Evidence: contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: locator, SourceVersion: queryID,
		Source:     contractsv1.EvidenceSource{System: "dev_health", EntityType: entityType, EntityID: entityID, DisplayLabel: "label of " + entityID},
		Provenance: "native", Confidence: 1, Citation: "citation of " + entityID, ObservedAt: observedAt, Availability: contractsv1.EvidenceAvailable,
	}}
}

func (w *world) addRow(repoID, queryID, locator string, reference contextpacket.EvidenceReference) {
	key := repoID + "|" + queryID + "|" + locator
	w.rows[key] = append(w.rows[key], reference)
}

func resolver(t *testing.T, w *world) *sourcerow.Resolver {
	t.Helper()
	r, err := sourcerow.New(w, contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{Now: func() time.Time { return observedAt.Add(time.Hour) }}), w.gate)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var (
	unrestricted = storage.Principal{OrgID: orgID}
	universal    = storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}
	restricted   = storage.Principal{OrgID: orgID, RepositoryScopes: []string{grantedRep}}
	ownerWide    = storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/*"}}
)

// repoKindCase is one repository-anchored kind: the id its producer mints and
// the catalog read it must turn into.
type repoKindCase struct {
	kind       contractsv1.ContextFabricEvidenceEntityType
	id         string
	query      string
	locator    string
	entityType string
	taskRef    string
}

// The ids are minted the way each producer mints them (file:line in acr at
// this change's base): repoID + ":" + <row id>, the repository kind the UUID
// alone. Work item ids hold ':' (provider-prefixed keys) on purpose.
func repoKindCases() []repoKindCase {
	return []repoKindCase{
		// devhealthsource/tables.go:219, devhealthfacts/identity.go:86
		{kind: contractsv1.ContextFabricEvidenceEntityRepository, id: grantedID, query: "repository_freshness.v1", locator: "acr:v1:repository:" + grantedID, entityType: "repository"},
		// devhealthsource/tables.go:317, devhealthfacts/workitems.go:75
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItem, id: grantedID + ":jira:ABC-1", query: "work_items.v1", locator: "acr:v1:work-item:jira:ABC-1", entityType: "work_item", taskRef: "jira:ABC-1"},
		// devhealthsource/tables.go:397, devhealthfacts/pullrequests.go:81
		{kind: contractsv1.ContextFabricEvidenceEntityPullRequest, id: grantedID + ":532", query: "pull_requests.v1", locator: "acr:v1:pull-request:532", entityType: "pull_request"},
		// devhealthsource/tables.go:998, devhealthfacts/pullrequests.go:135
		{kind: contractsv1.ContextFabricEvidenceEntityReview, id: grantedID + ":rev-9", query: "pull_request_reviews.v1", locator: "acr:v1:review:rev-9", entityType: "pull_request_review"},
		// devhealthsource/tables.go:1076, devhealthfacts/ci.go:126
		{kind: contractsv1.ContextFabricEvidenceEntityCI, id: grantedID + ":run:77", query: "ci_pipeline_runs.v1", locator: "acr:v1:ci:run:77", entityType: "ci_pipeline_run"},
		// devhealthsource/tables.go:458, devhealthfacts/deployments.go:117
		{kind: contractsv1.ContextFabricEvidenceEntityDeployment, id: grantedID + ":dep-1", query: "deployments.v1", locator: "acr:v1:deployment:dep-1", entityType: "deployment"},
	}
}

func TestRepositoryKindsReadTheirCatalogRow(t *testing.T) {
	for _, tc := range repoKindCases() {
		t.Run(string(tc.kind), func(t *testing.T) {
			w := newWorld()
			w.repos[grantedID] = grantedRep
			w.addRow(grantedID, tc.query, tc.locator, catalogRow(tc.query, tc.locator, tc.entityType, strings.TrimPrefix(tc.locator, "acr:v1:"+string(tc.kind)+":"), grantedRep))
			ref := contractsv1.EvidenceRefID(tc.kind, tc.id)
			expanded, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, string(tc.kind), tc.id)
			if decision.Reason != contextfabric.SourceRowServed {
				t.Fatalf("decision = %+v, calls %v", decision, w.calls)
			}
			wantCalls := []string{
				fmt.Sprintf("repository_by_id(%s,%s)", orgID, grantedID),
				fmt.Sprintf("row(%s,%s,%s,%s,%s)", orgID, grantedID, tc.query, tc.locator, tc.taskRef),
			}
			if !reflect.DeepEqual(w.calls, wantCalls) {
				t.Fatalf("calls = %v, want %v", w.calls, wantCalls)
			}
			if decision.Query != tc.query || decision.Grammar != contextfabric.SourceRowGrammarRepoAnchored || decision.Repositories != 1 || decision.Admitted != 1 || decision.Rows != 1 {
				t.Fatalf("decision = %+v", decision)
			}
			assertSourceRowExpansion(t, expanded, ref, string(tc.kind), grantedID, grantedRep, tc.query)
		})
	}
}

func assertSourceRowExpansion(t *testing.T, expanded contractsv1.ExpandedEvidence, ref, kind, repoID, slug, query string) {
	t.Helper()
	if err := expanded.Validate(); err != nil {
		t.Fatalf("expansion invalid: %v", err)
	}
	evidence := expanded.Evidence
	if evidence.EvidenceRefID != ref || evidence.Source.System != contextfabric.SourceRowSystem || evidence.Provenance == contextfabric.ContextFabricEvidenceProvenance {
		t.Fatalf("evidence = %+v", evidence)
	}
	for key, want := range map[string]any{"record": "source_row", "row_state": "current", "row_observed_at": "2026-09-01T12:00:00.000Z", "source_query": query, "source_query_version": contextpacket.SourceRowQueryVersionV1, "catalog_version": contextpacket.SourceQueryCatalogVersionV1} {
		if evidence.Metadata[key] != want {
			t.Fatalf("metadata[%s] = %v, want %v", key, evidence.Metadata[key], want)
		}
	}
	for key, want := range map[string]any{"entity_type": kind, "repository_id": repoID, "repository": slug} {
		if expanded.Structured[key] != want {
			t.Fatalf("structured[%s] = %v, want %v", key, expanded.Structured[key], want)
		}
	}
	if !strings.HasPrefix(expanded.Excerpt, "citation of ") || expanded.Availability != contractsv1.EvidenceAvailable {
		t.Fatalf("excerpt %q availability %q", expanded.Excerpt, expanded.Availability)
	}
}

// R1: the caller classes of the direct data tools. An unrestricted caller
// (no grant list) and a "*" caller read any repository of the organization;
// a restricted caller reads its listed repositories (exact or owner
// wildcard) and nothing else.
func TestCallerClassesDecideTheRepository(t *testing.T) {
	cases := []struct {
		name      string
		principal storage.Principal
		repoID    string
		slug      string
		served    bool
	}{
		{"unrestricted reads any repository", unrestricted, secretID, secretRep, true},
		{"universal reads any repository", universal, secretID, secretRep, true},
		{"restricted reads its repository", restricted, grantedID, grantedRep, true},
		{"restricted reads a case-different slug of its repository", storage.Principal{OrgID: orgID, RepositoryScopes: []string{"ACME/API"}}, grantedID, grantedRep, true},
		{"owner wildcard reads its owner's repository", ownerWide, grantedID, grantedRep, true},
		{"restricted does not read another repository", restricted, secretID, secretRep, false},
		{"owner wildcard does not read another owner", ownerWide, secretID, secretRep, false},
		{"a grant of malformed entries reads nothing", storage.Principal{OrgID: orgID, RepositoryScopes: []string{"not a slug"}}, grantedID, grantedRep, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			w.repos[tc.repoID] = tc.slug
			locator := "acr:v1:pull-request:7"
			w.addRow(tc.repoID, "pull_requests.v1", locator, catalogRow("pull_requests.v1", locator, "pull_request", "7", tc.slug))
			_, decision := resolver(t, w).ResolveSourceRow(context.Background(), tc.principal, "pull-request", tc.repoID+":7")
			if got := decision.Reason == contextfabric.SourceRowServed; got != tc.served {
				t.Fatalf("served = %v, want %v (%+v)", got, tc.served, decision)
			}
			if !tc.served && (decision.Reason != contextfabric.SourceRowNoRow || decision.Repositories != 1 || decision.Admitted != 0) {
				t.Fatalf("refusal decision = %+v", decision)
			}
		})
	}
}

// P2: a restricted caller's refusal runs the SAME reads, with the same
// arguments, as an id that names nothing, and ends in the same decision on
// every field a caller could ever observe. Only the trace counts differ.
func TestRefusalRunsTheSameReadsAsAbsence(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		id    string
		exist func(w *world)
	}{
		{"repository-anchored", "work-item", secretID + ":jira:SEC-1", func(w *world) {
			w.repos[secretID] = secretRep
			w.addRow(secretID, "work_items.v1", "acr:v1:work-item:jira:SEC-1", catalogRow("work_items.v1", "acr:v1:work-item:jira:SEC-1", "work_item", "jira:SEC-1", secretRep))
		}},
		{"repository itself", "repository", secretID, func(w *world) {
			w.repos[secretID] = secretRep
			w.addRow(secretID, "repository_freshness.v1", "acr:v1:repository:"+secretID, catalogRow("repository_freshness.v1", "acr:v1:repository:"+secretID, "repository", secretID, secretRep))
		}},
		{"row-anchored incident", "incident", "INC-7", func(w *world) {
			w.discovered[string(contextpacket.SourceRowDiscoveryIncident)+"|INC-7"] = []contractsv1.ResolvedScope{scope(secretID, secretRep)}
			w.addRow(secretID, "incidents.v1", "acr:v1:incident:INC-7", catalogRow("incidents.v1", "acr:v1:incident:INC-7", "incident", "INC-7", secretRep))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			present, absent := newWorld(), newWorld()
			tc.exist(present)
			// The row is real: an org-wide caller reads it.
			if _, decision := resolver(t, present).ResolveSourceRow(context.Background(), unrestricted, tc.kind, tc.id); decision.Reason != contextfabric.SourceRowServed {
				t.Fatalf("fixture row not served to an unrestricted caller: %+v", decision)
			}
			present.calls = nil
			refusedExpansion, refused := resolver(t, present).ResolveSourceRow(context.Background(), restricted, tc.kind, tc.id)
			missingExpansion, missing := resolver(t, absent).ResolveSourceRow(context.Background(), restricted, tc.kind, tc.id)
			if !reflect.DeepEqual(present.calls, absent.calls) {
				t.Fatalf("reads differ:\n refused %v\n absent  %v", present.calls, absent.calls)
			}
			if refused.Reason != contextfabric.SourceRowNoRow || missing.Reason != contextfabric.SourceRowNoRow {
				t.Fatalf("reasons: refused %+v, absent %+v", refused, missing)
			}
			refused.Repositories, missing.Repositories, refused.Admitted, missing.Admitted = 0, 0, 0, 0
			if !reflect.DeepEqual(refused, missing) || !reflect.DeepEqual(refusedExpansion, missingExpansion) {
				t.Fatalf("decisions differ: refused %+v, absent %+v", refused, missing)
			}
		})
	}
}

// A row-anchored incident mapped to several repositories is served from the
// first one, in slug order, that the caller may read.
func TestIncidentIsServedFromAnAdmittedRepository(t *testing.T) {
	w := newWorld()
	w.discovered[string(contextpacket.SourceRowDiscoveryIncident)+"|INC-1"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep), scope(secretID, secretRep)}
	w.addRow(grantedID, "incidents.v1", "acr:v1:incident:INC-1", catalogRow("incidents.v1", "acr:v1:incident:INC-1", "incident", "INC-1", grantedRep))
	w.addRow(secretID, "incidents.v1", "acr:v1:incident:INC-1", catalogRow("incidents.v1", "acr:v1:incident:INC-1", "incident", "INC-1", secretRep))
	restrictedToSecret := storage.Principal{OrgID: orgID, RepositoryScopes: []string{secretRep}}
	expanded, decision := resolver(t, w).ResolveSourceRow(context.Background(), restrictedToSecret, "incident", "INC-1")
	if decision.Reason != contextfabric.SourceRowServed || decision.Repositories != 2 || decision.Admitted != 1 || decision.Grammar != contextfabric.SourceRowGrammarRowAnchored {
		t.Fatalf("decision = %+v", decision)
	}
	assertSourceRowExpansion(t, expanded, "acr:v1:incident:INC-1", "incident", secretID, secretRep, "incidents.v1")
	for _, call := range w.calls {
		if strings.Contains(call, "row(") && strings.Contains(call, grantedID) {
			t.Fatalf("read a repository the caller may not read: %v", w.calls)
		}
	}
	// An org-wide caller is served from the first repository in slug order.
	w.calls = nil
	expanded, decision = resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, "incident", "INC-1")
	if decision.Reason != contextfabric.SourceRowServed || expanded.Structured["repository"] != grantedRep || len(w.calls) != 2 {
		t.Fatalf("decision = %+v, structured %v, calls %v", decision, expanded.Structured, w.calls)
	}
}

// A statement that returns two rows for one catalog evidence id, and a row
// mapped to more repositories than the bound, are refused as ambiguous.
func TestAmbiguousRowsAreRefused(t *testing.T) {
	w := newWorld()
	w.repos[grantedID] = grantedRep
	locator := "acr:v1:deployment:dep-1"
	w.addRow(grantedID, "deployments.v1", locator, catalogRow("deployments.v1", locator, "deployment", "dep-1", grantedRep))
	w.addRow(grantedID, "deployments.v1", locator, catalogRow("deployments.v1", locator, "deployment", "dep-1", grantedRep))
	if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "deployment", grantedID+":dep-1"); decision.Reason != contextfabric.SourceRowAmbiguous {
		t.Fatalf("two rows: %+v", decision)
	}
	saturated := newWorld()
	var many []contractsv1.ResolvedScope
	for i := 0; i <= contextpacket.MaxSourceRowRepositories(); i++ {
		many = append(many, scope(fmt.Sprintf("40000000-0000-4000-8000-%012d", i), fmt.Sprintf("acme/r%03d", i)))
	}
	saturated.discovered[string(contextpacket.SourceRowDiscoveryIncident)+"|INC-9"] = many
	if _, decision := resolver(t, saturated).ResolveSourceRow(context.Background(), unrestricted, "incident", "INC-9"); decision.Reason != contextfabric.SourceRowAmbiguous {
		t.Fatalf("saturated: %+v", decision)
	}
	for _, call := range saturated.calls {
		if strings.HasPrefix(call, "row(") {
			t.Fatalf("a saturated repository list was read: %v", saturated.calls)
		}
	}
}

// P3: an id is decided from its text before any read. The repository UUID
// has a fixed length, so everything after it is the row id verbatim, ':' and
// all; a naive split on ':' would read the wrong row.
func TestMalformedIdsReadNothing(t *testing.T) {
	cases := []struct{ kind, id string }{
		{"repository", "not-a-uuid"},
		{"repository", "A" + grantedID[1:]},
		{"repository", grantedID + ":extra"},
		{"work-item", grantedID},
		{"work-item", grantedID + ":"},
		{"work-item", "jira:ABC-1"},
		{"pull-request", grantedID + "-532"},
		{"pull-request", " " + grantedID + ":532"},
	}
	for _, tc := range cases {
		w := newWorld()
		_, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, tc.kind, tc.id)
		if decision.Reason != contextfabric.SourceRowIDMalformed || len(w.calls) != 0 {
			t.Fatalf("%s %q: %+v, calls %v", tc.kind, tc.id, decision, w.calls)
		}
	}
}

// A failed read is unavailable, never a no-row: the failed read may hide
// the row this caller can read.
func TestFailedReadsAreUnavailable(t *testing.T) {
	boom := errors.New("clickhouse down")
	for _, op := range []string{"repository_by_id", "row"} {
		w := newWorld()
		w.repos[grantedID] = grantedRep
		w.fail[op] = boom
		_, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "ci", grantedID+":run-1")
		if decision.Reason != contextfabric.SourceRowUnavailable || !errors.Is(decision.Err, boom) {
			t.Fatalf("%s: %+v", op, decision)
		}
	}
	w := newWorld()
	w.fail["discover"] = boom
	if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "incident", "INC-1"); decision.Reason != contextfabric.SourceRowUnavailable {
		t.Fatalf("discover: %+v", decision)
	}
	// A not-found from the row read is no row, not a failure.
	w = newWorld()
	w.repos[grantedID] = grantedRep
	w.fail["row"] = storage.ErrNotFound
	if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "ci", grantedID+":run-1"); decision.Reason != contextfabric.SourceRowNoRow {
		t.Fatalf("not found: %+v", decision)
	}
}

// A kind on the record route reads nothing, whatever its id.
func TestRecordKindsReadNothing(t *testing.T) {
	for kind, plan := range contextfabric.SourceRowPlans() {
		if plan.Route != contextfabric.SourceRowRouteRecord {
			continue
		}
		w := newWorld()
		_, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, string(kind), grantedID+":x")
		if decision.Reason != contextfabric.SourceRowKindOnRecord || len(w.calls) != 0 {
			t.Fatalf("%s: %+v, calls %v", kind, decision, w.calls)
		}
	}
}

func TestNewRequiresRowsAndExpander(t *testing.T) {
	if _, err := sourcerow.New(nil, contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}), nil); err == nil {
		t.Fatal("nil rows accepted")
	}
	var typedNil *contextpacket.EvidenceResolver
	if _, err := sourcerow.New(newWorld(), typedNil, nil); err == nil {
		t.Fatal("typed-nil expander accepted")
	}
}

// A row the contract cannot carry (here an unmapped provenance, which the
// catalog renders as ”) is row_invalid, not a retryable failure: the route
// answers from the persisted record and the trace names the statement.
func TestAnInvalidRowIsNamedNotRetried(t *testing.T) {
	w := newWorld()
	w.repos[grantedID] = grantedRep
	locator := "acr:v1:ci:run-1"
	row := catalogRow("ci_pipeline_runs.v1", locator, "ci_pipeline_run", "run-1", grantedRep)
	row.Evidence.Provenance = ""
	w.addRow(grantedID, "ci_pipeline_runs.v1", locator, row)
	expanded, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "ci", grantedID+":run-1")
	if decision.Reason != contextfabric.SourceRowInvalid || decision.Err == nil || decision.Rows != 1 || expanded.SchemaVersion != "" {
		t.Fatalf("decision = %+v", decision)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, decision := resolver(t, w).ResolveSourceRow(canceled, restricted, "ci", grantedID+":run-1"); decision.Reason != contextfabric.SourceRowUnavailable {
		t.Fatalf("canceled: %+v", decision)
	}
}

// fakeGate answers the subject gate with a fixed outcome per canonical id
// (absent when unlisted) and records every request, into the world's call
// log so gate and row reads interleave in one sequence.
type fakeGate struct {
	world    *world
	outcomes map[string]directread.SubjectOutcome
	decision directread.Decision
	err      error
}

func (g *fakeGate) Authorize(_ context.Context, principal storage.Principal, requested []contextfabric.SubjectRef) (directread.AuthorizedSubjects, directread.Authorization) {
	for _, subject := range requested {
		g.world.calls = append(g.world.calls, fmt.Sprintf("gate(%s,%s,%s)", principal.OrgID, subject.Kind, subject.CanonicalID))
	}
	if g.decision == directread.DecisionUnavailable {
		return directread.AuthorizedSubjects{}, directread.Authorization{Decision: directread.DecisionUnavailable, Reason: directread.ReasonGraphReadFailed, Err: g.err}
	}
	authorization := directread.Authorization{Decision: directread.DecisionDenied}
	for _, subject := range requested {
		outcome, ok := g.outcomes[subject.CanonicalID]
		if !ok {
			outcome = directread.SubjectAbsent
		}
		if outcome == directread.SubjectAdmitted {
			authorization.Decision = directread.DecisionAdmitted
		}
		authorization.Outcomes = append(authorization.Outcomes, directread.GatedSubject{Subject: subject, Outcome: outcome})
	}
	return directread.AuthorizedSubjects{}, authorization
}

const (
	// An Atlassian team ARI: a team id holds ':' freely (it is the whole id).
	teamID = "ari:cloud:identity::team/abc"
	// A provider-composite project id: the provider splits at the FIRST ':'.
	projectID = "gitlab:org-1:gitlab:71133891"
)

func projectCanonical(t *testing.T) string {
	t.Helper()
	canonical, omitted, err := identity.Derive(identity.KindProject, []string{"gitlab", "org-1:gitlab:71133891"}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project: %v %v", err, omitted)
	}
	return canonical
}

func orgRow(queryID, locator, entityType, entityID string) contextpacket.EvidenceReference {
	row := catalogRow(queryID, locator, entityType, entityID, "")
	row.RepoSlug = ""
	return row
}

// CHAOS-7227: a team or project row is served when the direct data tools'
// subject gate admits the subject, and read by its organization-level key.
func TestOwnershipKindsAreServedThroughTheSubjectGate(t *testing.T) {
	for _, tc := range []struct {
		kind, id, query, subjectKind, canonical string
	}{
		{"team", teamID, "teams.v1", "team", contextfabric.TeamCanonicalID(teamID)},
		{"project", projectID, "projects.v1", "project", projectCanonical(t)},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			w := newWorld()
			w.gate = &fakeGate{world: w, outcomes: map[string]directread.SubjectOutcome{tc.canonical: directread.SubjectAdmitted}}
			locator := "acr:v1:" + tc.kind + ":" + tc.id
			w.orgRows[tc.query+"|"+locator] = []contextpacket.EvidenceReference{orgRow(tc.query, locator, tc.kind, tc.id)}
			expanded, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, tc.kind, tc.id)
			if decision.Reason != contextfabric.SourceRowServed || decision.Grammar != contextfabric.SourceRowGrammarOrgKeyed || decision.Repositories != 1 || decision.Admitted != 1 || decision.Rows != 1 {
				t.Fatalf("decision = %+v, calls %v", decision, w.calls)
			}
			want := []string{
				fmt.Sprintf("gate(%s,%s,%s)", orgID, tc.subjectKind, tc.canonical),
				fmt.Sprintf("org_row(%s,%s,%s)", orgID, tc.query, locator),
			}
			if !reflect.DeepEqual(w.calls, want) {
				t.Fatalf("calls = %v, want %v", w.calls, want)
			}
			if err := expanded.Validate(); err != nil {
				t.Fatal(err)
			}
			if expanded.Evidence.EvidenceRefID != locator || expanded.Evidence.Metadata["record"] != "source_row" || expanded.Structured["subject"] != tc.canonical {
				t.Fatalf("expanded = %+v %v", expanded.Evidence, expanded.Structured)
			}
			if _, has := expanded.Structured["repository"]; has {
				t.Fatalf("an organization-level row names a repository: %v", expanded.Structured)
			}
		})
	}
}

// P7: the gate is the whole authorization. Every refused outcome (denied by
// the node check, absent, ownership unproven, invalid) is one no_row that
// reads no row, and the refused and absent subjects run the same calls.
func TestOwnershipRefusalReadsNoRow(t *testing.T) {
	canonical := contextfabric.TeamCanonicalID(teamID)
	var baseline []string
	for _, outcome := range []directread.SubjectOutcome{directread.SubjectAbsent, directread.SubjectDenied, directread.SubjectOwnershipUnproven, directread.SubjectInvalid} {
		w := newWorld()
		w.gate = &fakeGate{world: w, outcomes: map[string]directread.SubjectOutcome{canonical: outcome}}
		locator := "acr:v1:team:" + teamID
		w.orgRows["teams.v1|"+locator] = []contextpacket.EvidenceReference{orgRow("teams.v1", locator, "team", teamID)}
		expanded, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "team", teamID)
		if decision.Reason != contextfabric.SourceRowNoRow || decision.Admitted != 0 || decision.Rows != 0 || expanded.SchemaVersion != "" {
			t.Fatalf("%s: decision %+v", outcome, decision)
		}
		if baseline == nil {
			baseline = w.calls
		} else if !reflect.DeepEqual(w.calls, baseline) {
			t.Fatalf("%s: calls %v differ from absent %v", outcome, w.calls, baseline)
		}
		for _, call := range w.calls {
			if strings.HasPrefix(call, "org_row(") {
				t.Fatalf("%s: a refused subject read its row: %v", outcome, w.calls)
			}
		}
	}
}

// A gate that cannot decide is a retryable 503, never a no_row; no gate
// composed keeps the kinds on the record without a read.
func TestOwnershipGateUnavailableOrAbsent(t *testing.T) {
	boom := errors.New("graph down")
	w := newWorld()
	w.gate = &fakeGate{world: w, decision: directread.DecisionUnavailable, err: boom}
	if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "project", projectID); decision.Reason != contextfabric.SourceRowUnavailable || !errors.Is(decision.Err, boom) {
		t.Fatalf("unavailable: %+v", decision)
	}
	w = newWorld()
	if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "team", teamID); decision.Reason != contextfabric.SourceRowBackendAbsent || decision.Query != "teams.v1" || len(w.calls) != 0 {
		t.Fatalf("no gate: %+v, calls %v", decision, w.calls)
	}
	w = newWorld()
	w.gate = &fakeGate{world: w, outcomes: map[string]directread.SubjectOutcome{contextfabric.TeamCanonicalID(teamID): directread.SubjectAdmitted}}
	w.fail["org_row"] = boom
	if _, decision := resolver(t, w).ResolveSourceRow(context.Background(), restricted, "team", teamID); decision.Reason != contextfabric.SourceRowUnavailable {
		t.Fatalf("row read failed: %+v", decision)
	}
}

// The project grammar is injective only with a colon-free provider; any id
// that does not split into one is malformed and asks the gate nothing.
func TestOwnershipMalformedIdsAskNothing(t *testing.T) {
	for _, id := range []string{"nocolon", "Jira:x", ":x", "jira:", "ji ra:x", "ji:ra:x:"} {
		w := newWorld()
		w.gate = &fakeGate{world: w}
		_, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, "project", id)
		if id == "ji:ra:x:" {
			// provider "ji", project id "ra:x:": well-formed.
			if decision.Reason == contextfabric.SourceRowIDMalformed {
				t.Fatalf("%q refused as malformed", id)
			}
			continue
		}
		if decision.Reason != contextfabric.SourceRowIDMalformed || len(w.calls) != 0 {
			t.Fatalf("%q: %+v, calls %v", id, decision, w.calls)
		}
	}
}
