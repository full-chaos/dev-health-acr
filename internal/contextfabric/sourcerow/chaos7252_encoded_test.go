package sourcerow_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7252: the ".v2" kinds. Each id is an evidenceref grammar that names
// one row; the resolver reads it in one repository the caller may read, and
// serves it only when every other subject the row names is readable too
// (the read_relationships edge gate).

const zeroRepoID = "00000000-0000-0000-0000-000000000000"

// entityTypes are the entity_type values the ".v2" statements select.
var entityTypes = map[contractsv1.ContextFabricEvidenceEntityType]string{
	contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2: "work_item_dependency",
	contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2:  "work_item_hierarchy",
	contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2:       "work_item_team",
	contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2: "deployment_incident_edge",
}

var (
	workItemDiscovery = string(contextpacket.SourceRowDiscoveryWorkItem)
	incidentDiscovery = string(contextpacket.SourceRowDiscoveryIncident)
)

func mustMint(t *testing.T, kind contractsv1.ContextFabricEvidenceEntityType, values ...string) (ref, id string) {
	t.Helper()
	ref, fits := evidenceref.Mint(kind, values...)
	if !fits {
		t.Fatalf("%s %q does not fit", kind, values)
	}
	return ref, strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix+string(kind)+":")
}

// encodedCase is one ".v2" kind in a world where every subject its row names
// lives in grantedRep, so a caller granted acme/api is served.
type encodedCase struct {
	kind   contractsv1.ContextFabricEvidenceEntityType
	values []string
	query  string
	// seed makes the row's subjects exist; the row is added by the test.
	seed func(w *world)
	// calls are the lookups before the row read, in order.
	calls []string
	// restrictedServed: a caller granted only acme/api is served.
	restrictedServed bool
}

func encodedCases() []encodedCase {
	return []encodedCase{
		{
			kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, values: []string{"jira:A:B", "jira:C", "blocks:fwd"}, query: "work_item_dependencies.v2",
			seed: func(w *world) {
				w.discovered[workItemDiscovery+"|jira:A:B"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
				w.discovered[workItemDiscovery+"|jira:C"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
			},
			calls:            []string{"discover(org_1,work_item_repositories,jira:A:B)", "discover(org_1,work_item_repositories,jira:C)"},
			restrictedServed: true,
		},
		{
			kind: contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, values: []string{grantedID, "jira:P:Q", "jira:R"}, query: "work_item_hierarchy.v2",
			seed: func(w *world) {
				w.repos[grantedID] = grantedRep
				w.discovered[workItemDiscovery+"|jira:R"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
			},
			calls:            []string{"repository_by_id(org_1," + grantedID + ")", "discover(org_1,work_item_repositories,jira:R)"},
			restrictedServed: true,
		},
		{
			kind: contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, values: []string{zeroRepoID, "jira:ABC-1", "ari:cloud:identity::team/1", "native_team"}, query: "work_item_teams.v2",
			seed: func(w *world) {
				w.discovered[workItemDiscovery+"|jira:ABC-1"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
			},
			calls: []string{"discover(org_1,work_item_repositories,jira:ABC-1)"},
			// The team is decided fail closed until CHAOS-7227.
			restrictedServed: false,
		},
		{
			kind: contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, values: []string{grantedID, "dep:1", "INC-1", "native"}, query: "deployment_incident_edges.v2",
			seed: func(w *world) {
				w.repos[grantedID] = grantedRep
				w.discovered[incidentDiscovery+"|INC-1"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
			},
			calls:            []string{"repository_by_id(org_1," + grantedID + ")", "discover(org_1,incident_repositories,INC-1)"},
			restrictedServed: true,
		},
	}
}

func TestEncodedKindsReadTheirRow(t *testing.T) {
	for _, tc := range encodedCases() {
		t.Run(string(tc.kind), func(t *testing.T) {
			ref, id := mustMint(t, tc.kind, tc.values...)
			for _, principal := range []storage.Principal{unrestricted, universal, restricted} {
				w := newWorld()
				tc.seed(w)
				w.addRow(grantedID, tc.query, ref, catalogRow(tc.query, ref, entityTypes[tc.kind], id, grantedRep))
				expanded, decision := resolver(t, w).ResolveSourceRow(context.Background(), principal, string(tc.kind), id)
				want := tc.restrictedServed || principal.RepositoryScopes == nil || principal.RepositoryScopes[0] == "*"
				if got := decision.Reason == contextfabric.SourceRowServed; got != want {
					t.Fatalf("%v: served %v, want %v (%+v, calls %v)", principal.RepositoryScopes, got, want, decision, w.calls)
				}
				wantCalls := append([]string(nil), tc.calls...)
				if want {
					wantCalls = append(wantCalls, fmt.Sprintf("row(%s,%s,%s,%s,)", orgID, grantedID, tc.query, ref))
				}
				if !reflect.DeepEqual(w.calls, wantCalls) {
					t.Fatalf("%v: calls = %v, want %v", principal.RepositoryScopes, w.calls, wantCalls)
				}
				if !want {
					if decision.Reason != contextfabric.SourceRowNoRow {
						t.Fatalf("refusal = %+v", decision)
					}
					continue
				}
				if !reflect.DeepEqual(w.reads[0].Components, tc.values) || decision.Grammar != contextfabric.SourceRowGrammarEncoded || decision.Rows != 1 {
					t.Fatalf("read %+v, decision %+v", w.reads[0], decision)
				}
				assertSourceRowExpansion(t, expanded, ref, string(tc.kind), grantedID, grantedRep, tc.query)
			}
		})
	}
}

// The edge gate, per kind: the row's own repository AND every other subject
// it names must be readable. Each case is a world and the caller granted
// acme/api only.
func TestEncodedKindsRefuseAnUnreadableEndpoint(t *testing.T) {
	dependency := contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2
	hierarchy := contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2
	deploymentIncident := contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2
	cases := []struct {
		name   string
		kind   contractsv1.ContextFabricEvidenceEntityType
		values []string
		query  string
		seed   func(w *world)
		// served for the restricted caller, and for an unrestricted one.
		restrictedServed, unrestrictedServed bool
	}{
		{"dependency: target in another repository", dependency, []string{"s", "t", "blocks:fwd"}, "work_item_dependencies.v2", func(w *world) {
			w.discovered[workItemDiscovery+"|s"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
			w.discovered[workItemDiscovery+"|t"] = []contractsv1.ResolvedScope{scope(secretID, secretRep)}
		}, false, true},
		{"dependency: source in another repository", dependency, []string{"s", "t", "blocks:fwd"}, "work_item_dependencies.v2", func(w *world) {
			w.discovered[workItemDiscovery+"|s"] = []contractsv1.ResolvedScope{scope(secretID, secretRep)}
			w.discovered[workItemDiscovery+"|t"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
		}, false, true},
		{"dependency: an unresolved target is authorized by the source (the projector's stub)", dependency, []string{"s", "EXT-1", "relates_to:fwd"}, "work_item_dependencies.v2", func(w *world) {
			w.discovered[workItemDiscovery+"|s"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
		}, true, true},
		{"dependency: a target in a slug-less (Linear) repository is not the stub", dependency, []string{"s", "linear:X", "blocks:fwd"}, "work_item_dependencies.v2", func(w *world) {
			w.discovered[workItemDiscovery+"|s"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
			w.discovered[workItemDiscovery+"|linear:X"] = []contractsv1.ResolvedScope{scope(zeroRepoID, "")}
		}, false, true},
		{"dependency: a source in a slug-less repository has nowhere to be read", dependency, []string{"linear:S", "t", "blocks:fwd"}, "work_item_dependencies.v2", func(w *world) {
			w.discovered[workItemDiscovery+"|linear:S"] = []contractsv1.ResolvedScope{scope(zeroRepoID, "")}
			w.discovered[workItemDiscovery+"|t"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
		}, false, false},
		{"dependency: a target in two repositories, one readable", dependency, []string{"s", "t", "blocks:fwd"}, "work_item_dependencies.v2", func(w *world) {
			w.discovered[workItemDiscovery+"|s"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
			w.discovered[workItemDiscovery+"|t"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep), scope(secretID, secretRep)}
		}, true, true},
		{"hierarchy: parent in another repository", hierarchy, []string{grantedID, "c", "p"}, "work_item_hierarchy.v2", func(w *world) {
			w.repos[grantedID] = grantedRep
			w.discovered[workItemDiscovery+"|p"] = []contractsv1.ResolvedScope{scope(secretID, secretRep)}
		}, false, true},
		{"hierarchy: a parent that is no work item refuses every caller", hierarchy, []string{grantedID, "c", "p"}, "work_item_hierarchy.v2", func(w *world) {
			w.repos[grantedID] = grantedRep
		}, false, false},
		{"hierarchy: child repository not granted", hierarchy, []string{secretID, "c", "p"}, "work_item_hierarchy.v2", func(w *world) {
			w.repos[secretID] = secretRep
			w.discovered[workItemDiscovery+"|p"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
		}, false, true},
		{"deployment-incident: incident mapped only elsewhere", deploymentIncident, []string{grantedID, "d", "INC", "native"}, "deployment_incident_edges.v2", func(w *world) {
			w.repos[grantedID] = grantedRep
			w.discovered[incidentDiscovery+"|INC"] = []contractsv1.ResolvedScope{scope(secretID, secretRep)}
		}, false, true},
		{"deployment-incident: an incident no service maps refuses every caller", deploymentIncident, []string{grantedID, "d", "INC", "native"}, "deployment_incident_edges.v2", func(w *world) {
			w.repos[grantedID] = grantedRep
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, id := mustMint(t, tc.kind, tc.values...)
			for _, principal := range []storage.Principal{restricted, unrestricted} {
				w := newWorld()
				tc.seed(w)
				for _, repo := range []string{grantedID, secretID} {
					w.addRow(repo, tc.query, ref, catalogRow(tc.query, ref, entityTypes[tc.kind], id, "slug"))
				}
				_, decision := resolver(t, w).ResolveSourceRow(context.Background(), principal, string(tc.kind), id)
				want := tc.unrestrictedServed
				if principal.RepositoryScopes != nil {
					want = tc.restrictedServed
				}
				if got := decision.Reason == contextfabric.SourceRowServed; got != want {
					t.Fatalf("%v: served %v, want %v (%+v, calls %v)", principal.RepositoryScopes, got, want, decision, w.calls)
				}
				if !want && decision.Reason != contextfabric.SourceRowNoRow {
					t.Fatalf("%v: refusal %+v", principal.RepositoryScopes, decision)
				}
				for _, call := range w.calls {
					if strings.HasPrefix(call, "row(") && !want {
						t.Fatalf("%v: a refused row was read: %v", principal.RepositoryScopes, w.calls)
					}
					if strings.HasPrefix(call, "row(") && principal.RepositoryScopes != nil && !strings.Contains(call, grantedID) {
						t.Fatalf("read in a repository the caller may not read: %v", w.calls)
					}
				}
			}
		})
	}
}

// Equal refusal (P2) for every encoded kind: a row the restricted caller may
// not read and the same id naming nothing run the same reads with the same
// arguments and end in the same decision.
func TestEncodedRefusalRunsTheSameReadsAsAbsence(t *testing.T) {
	for _, tc := range encodedCases() {
		t.Run(string(tc.kind), func(t *testing.T) {
			secretValues := append([]string(nil), tc.values...)
			grammar, _ := evidenceref.Lookup(tc.kind)
			for index, segment := range grammar.Segments {
				if segment.Form == evidenceref.UUID && secretValues[index] == grantedID {
					secretValues[index] = secretID
				}
			}
			ref, id := mustMint(t, tc.kind, secretValues...)
			present, absent := newWorld(), newWorld()
			// Every subject of the row lives in other-org/secret.
			present.repos[secretID] = secretRep
			for _, discovery := range []string{workItemDiscovery, incidentDiscovery} {
				for _, value := range secretValues {
					present.discovered[discovery+"|"+value] = []contractsv1.ResolvedScope{scope(secretID, secretRep)}
				}
			}
			present.addRow(secretID, tc.query, ref, catalogRow(tc.query, ref, entityTypes[tc.kind], id, secretRep))
			if _, decision := resolver(t, present).ResolveSourceRow(context.Background(), unrestricted, string(tc.kind), id); decision.Reason != contextfabric.SourceRowServed {
				t.Fatalf("fixture row not served to an unrestricted caller: %+v, calls %v", decision, present.calls)
			}
			present.calls = nil
			refusedExpansion, refused := resolver(t, present).ResolveSourceRow(context.Background(), restricted, string(tc.kind), id)
			missingExpansion, missing := resolver(t, absent).ResolveSourceRow(context.Background(), restricted, string(tc.kind), id)
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

// An id is decided from its text before any read: a retired bare-':' id, a
// segment the codec never emits, a wrong segment count or a non-canonical
// repository UUID is malformed and reads nothing.
func TestEncodedMalformedIdsReadNothing(t *testing.T) {
	cases := []struct {
		kind contractsv1.ContextFabricEvidenceEntityType
		id   string
	}{
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, "jira:A:B:jira:C:blocks:fwd"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, grantedID + ":jira%3AA:jira%3AC:blocks%3Afwd"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, "%:b:c"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, "a:%3a:c"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, "A" + grantedID[1:] + ":c:p"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, grantedID + ":jira:P:Q:jira:R"},
		// The retired three-segment shape under the new kind: three segments
		// where the grammar has four.
		{contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, grantedID + ":jira%3AABC-1:team-a"},
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, "edge-1"},
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, " " + grantedID + ":d:i:native"},
	}
	for _, tc := range cases {
		w := newWorld()
		_, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, string(tc.kind), tc.id)
		if decision.Reason != contextfabric.SourceRowIDMalformed || len(w.calls) != 0 {
			t.Fatalf("%s %q: %+v, calls %v", tc.kind, tc.id, decision, w.calls)
		}
	}
}

// A saturated endpoint list and two rows for one id are refused as
// ambiguous, never answered from a partial list or a pick.
func TestEncodedAmbiguityIsRefused(t *testing.T) {
	kind := contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2
	ref, id := mustMint(t, kind, "s", "t", "blocks:fwd")
	saturated := newWorld()
	saturated.discovered[workItemDiscovery+"|s"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
	for i := 0; i <= contextpacket.MaxSourceRowRepositories(); i++ {
		saturated.discovered[workItemDiscovery+"|t"] = append(saturated.discovered[workItemDiscovery+"|t"], scope(fmt.Sprintf("40000000-0000-4000-8000-%012d", i), fmt.Sprintf("acme/r%03d", i)))
	}
	if _, decision := resolver(t, saturated).ResolveSourceRow(context.Background(), unrestricted, string(kind), id); decision.Reason != contextfabric.SourceRowAmbiguous {
		t.Fatalf("saturated: %+v", decision)
	}
	two := newWorld()
	two.discovered[workItemDiscovery+"|s"] = []contractsv1.ResolvedScope{scope(grantedID, grantedRep)}
	two.addRow(grantedID, "work_item_dependencies.v2", ref, catalogRow("work_item_dependencies.v2", ref, entityTypes[kind], id, grantedRep))
	two.addRow(grantedID, "work_item_dependencies.v2", ref, catalogRow("work_item_dependencies.v2", ref, entityTypes[kind], id, grantedRep))
	if _, decision := resolver(t, two).ResolveSourceRow(context.Background(), unrestricted, string(kind), id); decision.Reason != contextfabric.SourceRowAmbiguous {
		t.Fatalf("two rows: %+v", decision)
	}
}

// project-team.v2 is minted but its source row is a separate change: it
// reads nothing and the persisted record answers it.
func TestProjectTeamV2StaysOnTheRecord(t *testing.T) {
	w := newWorld()
	_, id := mustMint(t, contractsv1.ContextFabricEvidenceEntityProjectTeamV2, "gitlab", "org:gitlab:1", "gl:team", "native")
	_, decision := resolver(t, w).ResolveSourceRow(context.Background(), unrestricted, string(contractsv1.ContextFabricEvidenceEntityProjectTeamV2), id)
	if decision.Reason != contextfabric.SourceRowKindOnRecord || len(w.calls) != 0 {
		t.Fatalf("%+v, calls %v", decision, w.calls)
	}
}

// Every routed ".v2" plan has a resolver spec, and every spec a routed plan:
// a route without a spec would read nothing, a spec without a route is dead.
func TestEncodedSpecsMatchThePlans(t *testing.T) {
	specs := map[contractsv1.ContextFabricEvidenceEntityType]bool{}
	for _, kind := range sourcerow.EncodedKinds() {
		specs[kind] = true
	}
	routed := 0
	for kind, plan := range contextfabric.SourceRowPlans() {
		if plan.Route != contextfabric.SourceRowRouteEncoded {
			continue
		}
		routed++
		if !specs[kind] {
			t.Fatalf("%s is routed encoded with no resolver spec", kind)
		}
		if _, ok := evidenceref.Lookup(kind); !ok {
			t.Fatalf("%s is routed encoded with no grammar", kind)
		}
	}
	if routed != len(specs) || routed != 4 {
		t.Fatalf("routed %d, specs %d", routed, len(specs))
	}
}
