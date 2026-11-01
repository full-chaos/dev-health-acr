package devhealthsource_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/reposlug"
)

type grantedKind struct {
	name        string
	kind        graphrank.CensusKind
	column      string
	handle      string
	handleBound bool
	anchorKind  contextfabric.SubjectKind
	anchorID    string
	anchorBound bool
}

// Every repository-column kind of the census registry.
func grantedKinds() []grantedKind {
	repository := "repository:7b9583ee-4d24-2be7-4d09-34f815bebdd7"
	return []grantedKind{
		{name: "pull request", kind: contextfabric.SubjectPullRequest, column: "p.repo_id", handle: "5063", handleBound: true},
		{name: "ci run", kind: contractsv1.ContextFabricSubjectCIRun, column: "c.repo_id", handle: "554433", handleBound: true},
		{name: "pull request review", kind: contractsv1.ContextFabricSubjectPullRequestReview, column: "r.repo_id", anchorKind: contextfabric.SubjectRepository, anchorID: repository, anchorBound: true},
	}
}

func grantClause(column string, slots int) string {
	clauses := make([]string, slots)
	for i := range clauses {
		clauses[i] = fmt.Sprintf("lower(trimBoth(repo)) = {census_grant_%d:String}", i)
	}
	return fmt.Sprintf("toString(%s) IN (SELECT toString(id) FROM repos FINAL WHERE org_id = {census_org_id:String} AND (%s))", column, strings.Join(clauses, " OR "))
}

func bindingValues(bindings []contextpacket.ClickHouseBinding) map[string]any {
	values := map[string]any{}
	for _, binding := range bindings {
		values[binding.Name] = binding.Value
	}
	return values
}

func TestGrantedCensusCarriesTheGrantOnEveryStatement(t *testing.T) {
	t.Parallel()
	for _, tc := range grantedKinds() {
		for _, shape := range []struct {
			name  string
			count uint64
			keys  []string
			want  int
		}{
			{"count only", 0, nil, 1},
			{"count and row read", 1, []string{"o:r1:5063"}, 2},
			{"count and set read", 2, []string{"o:r1:5063", "o:r2:5063"}, 2},
		} {
			t.Run(tc.name+" "+shape.name, func(t *testing.T) {
				t.Parallel()
				client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: shape.count, aggregateReadAt: time.Now().UTC(), rowKeys: shape.keys}}
				ctx := contextfabric.WithCensusGrant(context.Background(), []string{"ACME/Alpha", " acme/beta "})
				outcome, err := devhealthsource.NewCensusFunc(client)(ctx, "org_1", tc.kind, tc.handle, tc.handleBound, tc.anchorKind, tc.anchorID, tc.anchorBound)
				if err != nil {
					t.Fatalf("census: %v", err)
				}
				if !outcome.GrantApplied {
					t.Fatal("GrantApplied = false")
				}
				if outcome.RepositoryFilterApplied {
					t.Fatal("RepositoryFilterApplied = true: the grant is not the caller's narrowing")
				}
				if len(client.calls) != shape.want {
					t.Fatalf("statements = %d, want %d", len(client.calls), shape.want)
				}
				for i, statement := range client.calls {
					if !strings.Contains(statement, grantClause(tc.column, 2)) {
						t.Fatalf("statement %d = %q, want the grant inside it", i, statement)
					}
					values := bindingValues(client.bindings[i])
					if values["census_grant_0"] != "acme/alpha" || values["census_grant_1"] != "acme/beta" {
						t.Fatalf("statement %d bindings = %v, want the normalized grant", i, values)
					}
				}
			})
		}
	}
}

func TestAGrantOwnerWildcardIsApplied(t *testing.T) {
	t.Parallel()
	client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateReadAt: time.Now().UTC()}}
	ctx := contextfabric.WithCensusGrant(context.Background(), []string{"ACME/*"})
	outcome, err := devhealthsource.NewCensusFunc(client)(ctx, "org_1", contextfabric.SubjectPullRequest, "5063", true, "", "", false)
	if err != nil || !outcome.GrantApplied {
		t.Fatalf("census = %+v %v, want the grant applied", outcome, err)
	}
	shape := "startsWith(lower(trimBoth(repo)), {census_grant_0:String}) AND match(lower(trimBoth(repo)), '^[^/]+/" + reposlug.NamePattern + "$')"
	if !strings.Contains(client.calls[0], shape) || bindingValues(client.bindings[0])["census_grant_0"] != "acme/" {
		t.Fatalf("statement = %q bindings %v, want the owner prefix and the slug shape", client.calls[0], client.bindings[0])
	}
}

// A requested narrowing ANDs a second filter under its own binding names: the
// grant and the request are both in the statement, and neither replaces the
// other. A request that cannot apply keeps its fallback; the grant stays.
func TestARequestedNarrowingIsANDedBesideTheGrant(t *testing.T) {
	t.Parallel()
	census := func(requested []string) (*recordingCensusClient, graphrank.CensusOutcome) {
		client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: 2, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:5063", "o:r2:5063"}}}
		ctx := graphrank.WithCensusRepositoryFilter(contextfabric.WithCensusGrant(context.Background(), []string{"acme/alpha", "acme/beta"}), requested)
		outcome, err := devhealthsource.NewCensusFunc(client)(ctx, "org_1", contextfabric.SubjectPullRequest, "5063", true, "", "", false)
		if err != nil {
			t.Fatalf("census: %v", err)
		}
		return client, outcome
	}
	client, outcome := census([]string{"acme/beta"})
	if !outcome.GrantApplied || !outcome.RepositoryFilterApplied {
		t.Fatalf("outcome = %+v, want both applied", outcome)
	}
	for i, statement := range client.calls {
		if !strings.Contains(statement, grantClause("p.repo_id", 2)) || !strings.Contains(statement, "lower(trimBoth(repo)) = {census_repo_0:String}") {
			t.Fatalf("statement %d = %q, want the grant and the request", i, statement)
		}
		values := bindingValues(client.bindings[i])
		if values["census_grant_0"] != "acme/alpha" || values["census_grant_1"] != "acme/beta" || values["census_repo_0"] != "acme/beta" {
			t.Fatalf("statement %d bindings = %v", i, values)
		}
	}
	client, outcome = census([]string{"*"})
	if !outcome.GrantApplied || outcome.RepositoryFilterApplied {
		t.Fatalf("outcome = %+v, want the grant alone", outcome)
	}
	for i, statement := range client.calls {
		if !strings.Contains(statement, grantClause("p.repo_id", 2)) {
			t.Fatalf("statement %d = %q, want the grant", i, statement)
		}
	}
}

func TestACensusThatCannotApplyTheGrantReadsNothing(t *testing.T) {
	t.Parallel()
	tooMany := make([]string, 201)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("acme/repo-%d", i)
	}
	cases := map[string][]string{
		"over the slug limit":     tooMany,
		"a bare wildcard":         {"acme/alpha", "*"},
		"an invalid slug":         {"acme/alpha", "not a slug"},
		"a slug with no owner":    {"/alpha"},
		"an empty owner wildcard": {"/*"},
		"the reserved value":      {contractsv1.ProjectOnlyRepositoryScope},
		"a reserved slug":         {contractsv1.ReservedRepositoryScopePrefix + "acme/alpha"},
		"a reserved owner":        {contractsv1.ReservedRepositoryScopePrefix + "acme/*"},
		"an empty grant":          {},
	}
	for name, grant := range cases {
		for _, tc := range grantedKinds() {
			t.Run(name+" "+tc.name, func(t *testing.T) {
				t.Parallel()
				client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:5063"}}}
				ctx := contextfabric.WithCensusGrant(context.Background(), grant)
				outcome, err := devhealthsource.NewCensusFunc(client)(ctx, "org_1", tc.kind, tc.handle, tc.handleBound, tc.anchorKind, tc.anchorID, tc.anchorBound)
				if !errors.Is(err, graphrank.ErrCensusGrantNotApplied) {
					t.Fatalf("err = %v, want ErrCensusGrantNotApplied", err)
				}
				if len(client.calls) != 0 {
					t.Fatalf("statements = %v, want none", client.calls)
				}
				if outcome.Count != 0 || outcome.GrantApplied {
					t.Fatalf("outcome = %+v, want nothing", outcome)
				}
			})
		}
	}
}

// A work item has no repository column: its census reads nothing under a grant
// (PR2 owns that read), for every provider's stored key.
func TestAWorkItemCensusUnderAGrantReadsNothing(t *testing.T) {
	t.Parallel()
	for provider, key := range map[string]string{"linear": "CHAOS-11516", "jira": "OPS-204", "github": "CHAOS-77", "gitlab": "CHAOS-78"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:" + provider + ":" + key}}}
			ctx := contextfabric.WithCensusGrant(context.Background(), []string{"acme/alpha"})
			_, err := devhealthsource.NewCensusFunc(client)(ctx, "org_1", contextfabric.SubjectWorkItem, key, true, "", "", false)
			if !errors.Is(err, graphrank.ErrCensusGrantNotApplied) || len(client.calls) != 0 {
				t.Fatalf("err = %v statements = %v, want the refusal and none", err, client.calls)
			}
		})
	}
}

// A handr_ redemption goes through this adapter: under a grant that cannot
// apply its yes or no comes from no statement at all.
func TestAHandleVerificationThatCannotApplyTheGrantReadsNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		grant     []string
		kind      graphrank.CensusKind
		patternID string
		value     string
	}{
		"project-only caller":          {[]string{contractsv1.ProjectOnlyRepositoryScope}, contextfabric.SubjectPullRequest, "pull_request_number", "5063"},
		"work item, restricted caller": {[]string{"acme/alpha"}, contextfabric.SubjectWorkItem, "work_item_ticket_key", "CHAOS-11516"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:1"}}}
			ctx := contextfabric.WithCensusGrant(context.Background(), tc.grant)
			ok, reason := graphrank.VerifyHandle(ctx, "org_1", tc.kind, tc.patternID, tc.value, devhealthsource.NewCensusFunc(client))
			if ok || reason != graphrank.HandleVerificationCensusUnavailable {
				t.Fatalf("VerifyHandle = %v %q, want the census refused", ok, reason)
			}
			if len(client.calls) != 0 {
				t.Fatalf("statements = %v, want none", client.calls)
			}
		})
	}
}

// With no grant bound the statements are the exact strings they always were.
func TestUnrestrictedCensusStatementsAreByteIdentical(t *testing.T) {
	t.Parallel()
	client := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: 2, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:5063", "o:r2:5063"}}}
	outcome, err := devhealthsource.NewCensusFunc(client)(context.Background(), "org_1", contextfabric.SubjectPullRequest, "5063", true, "", "", false)
	if err != nil || outcome.GrantApplied {
		t.Fatalf("census = %+v %v, want no grant", outcome, err)
	}
	for i, statement := range client.calls {
		if strings.Contains(statement, "census_grant") || strings.Contains(statement, "FROM repos") {
			t.Fatalf("statement %d = %q, want no repository clause", i, statement)
		}
		if _, ok := bindingValues(client.bindings[i])["census_grant_0"]; ok {
			t.Fatalf("statement %d carries a grant binding", i)
		}
	}
	// A requested filter keeps its binding names and its clause.
	filtered := &recordingCensusClient{censusFakeClient: &censusFakeClient{aggregateCount: 1, aggregateReadAt: time.Now().UTC(), rowKeys: []string{"o:r1:5063"}}}
	if _, err := devhealthsource.NewCensusFunc(filtered)(graphrank.WithCensusRepositoryFilter(context.Background(), []string{"acme/alpha"}), "org_1", contextfabric.SubjectPullRequest, "5063", true, "", "", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filtered.calls[0], "AND (lower(trimBoth(repo)) = {census_repo_0:String}))") {
		t.Fatalf("statement = %q, want the requested filter as before", filtered.calls[0])
	}
}
