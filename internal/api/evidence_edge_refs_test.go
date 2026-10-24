package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

const edgeZeroRepoID = "00000000-0000-0000-0000-000000000000"

func edgeScope(id, slug string) contractsv1.ResolvedScope {
	return contractsv1.ResolvedScope{RepoID: id, RepoSlug: slug, Resolution: contractsv1.ScopeRepoFallback, FallbackReasons: []string{}}
}

func (s *sourceRowTables) discover(discovery contextpacket.SourceRowDiscovery, id string, scopes ...contractsv1.ResolvedScope) *sourceRowTables {
	if s.discoveries == nil {
		s.discoveries = map[string][]contractsv1.ResolvedScope{}
	}
	s.discoveries[string(discovery)+"|"+id] = scopes
	return s
}

// edgeRow seeds the row a ".v2" ref names in repository repoID, read by its
// full ref.
func (s *sourceRowTables) edgeRow(repoID, slug, ref, query, entityType, id string) *sourceRowTables {
	return s.withKindRow(repoID, slug, ref, query, entityType, id)
}

type edgeArm struct {
	name string
	// ref is the ref the arm asks for; empty means the kind's own.
	ref   string
	build func() *sourceRowTables
}

// edgeKindCase is one ".v2" kind: its ref is minted by the production
// evidenceref.Mint, never spelled by hand.
type edgeKindCase struct {
	kind         contractsv1.ContextFabricEvidenceEntityType
	ref          string
	unrestricted bool
	served       func() *sourceRowTables
	refused      []edgeArm
}

func mustMintEdge(t *testing.T, kind contractsv1.ContextFabricEvidenceEntityType, values ...string) (ref, id string) {
	t.Helper()
	ref, fits := evidenceref.Mint(kind, values...)
	if !fits {
		t.Fatalf("%s %q does not fit", kind, values)
	}
	return ref, strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix+string(kind)+":")
}

func edgeKindCases(t *testing.T) []edgeKindCase {
	g, gs, x, xs := sourceRowGrantedRepoID, hostedTestRepository, sourceRowSecretRepoID, sourceRowSecretSlug
	workItem, incident := contextpacket.SourceRowDiscoveryWorkItem, contextpacket.SourceRowDiscoveryIncident

	depRef, depID := mustMintEdge(t, contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, "jira:A:B", "jira:C", "blocks:fwd")
	dep := func(source, target []contractsv1.ResolvedScope, rowRepo, rowSlug string) *sourceRowTables {
		tables := newSourceRowTables().edgeRow(rowRepo, rowSlug, depRef, "work_item_dependencies.v2", "work_item_dependency", depID)
		return tables.discover(workItem, "jira:A:B", source...).discover(workItem, "jira:C", target...)
	}
	var tooMany []contractsv1.ResolvedScope
	for index := 0; index <= contextpacket.MaxSourceRowRepositories(); index++ {
		tooMany = append(tooMany, edgeScope(fmt.Sprintf("40000000-0000-4000-8000-%012d", index), gs))
	}

	hierRef := func(child string) (string, string) {
		return mustMintEdge(t, contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, child, "jira:P:Q", "jira:R")
	}
	hierGranted, hierGrantedID := hierRef(g)
	hierSecret, hierSecretID := hierRef(x)
	hier := func(ref, id, childRepo, childSlug string, parent ...contractsv1.ResolvedScope) *sourceRowTables {
		return newSourceRowTables().edgeRow(childRepo, childSlug, ref, "work_item_hierarchy.v2", "work_item_hierarchy", id).discover(workItem, "jira:R", parent...)
	}

	teamRef, teamID := mustMintEdge(t, contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, edgeZeroRepoID, "jira:ABC-1", "ari:cloud:identity::team/1", "native_team")
	workItemTeam := func(item []contractsv1.ResolvedScope, teamOwnedBy string) *sourceRowTables {
		tables := newSourceRowTables().edgeRow(g, gs, teamRef, "work_item_teams.v2", "work_item_team", teamID).discover(workItem, "jira:ABC-1", item...)
		tables.gate = directread.NewSubjectGate(&ownershipGraph{owned: map[string][]string{contextfabric.TeamCanonicalID("ari:cloud:identity::team/1"): {teamOwnedBy}}}, nil)
		return tables
	}

	diRef := func(repo string) (string, string) {
		return mustMintEdge(t, contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, repo, "dep:1", "INC-1", "native")
	}
	diGranted, diGrantedID := diRef(g)
	diSecret, diSecretID := diRef(x)
	di := func(ref, id, repo, slug string, mapped ...contractsv1.ResolvedScope) *sourceRowTables {
		return newSourceRowTables().edgeRow(repo, slug, ref, "deployment_incident_edges.v2", "deployment_incident_edge", id).discover(incident, "INC-1", mapped...)
	}

	revGranted, revGrantedID := mustMintEdge(t, contractsv1.ContextFabricEvidenceEntityReviewV2, g, "1042", "approval-42")
	revSecret, revSecretID := mustMintEdge(t, contractsv1.ContextFabricEvidenceEntityReviewV2, x, "1042", "approval-42")

	ptValues := []string{"gitlab", "org:gitlab:1", "gl:team", "native"}
	ptRef, ptID := mustMintEdge(t, contractsv1.ContextFabricEvidenceEntityProjectTeamV2, ptValues...)
	projectTeam := func() *sourceRowTables {
		tables := newSourceRowTables()
		tables.orgRows = map[string][]contextpacket.OrganizationRowReference{"project_teams.v2|" + ptRef: {{Key: ptValues, Reference: contextpacket.EvidenceReference{Excerpt: "gitlab", Evidence: contractsv1.EvidenceRef{
			SchemaVersion: contractsv1.EvidenceRefSchema, EvidenceRefID: ptRef, SourceVersion: "project_teams.v2",
			Source:     contractsv1.EvidenceSource{System: "dev_health", EntityType: "project_team", EntityID: ptID, DisplayLabel: "group"},
			Provenance: "native", Confidence: 1, Citation: "gitlab", ObservedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Availability: contractsv1.EvidenceAvailable,
		}}}}}
		return tables
	}

	return []edgeKindCase{
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, ref: depRef,
			served: func() *sourceRowTables {
				return dep([]contractsv1.ResolvedScope{edgeScope(g, gs)}, []contractsv1.ResolvedScope{edgeScope(g, gs)}, g, gs)
			},
			refused: []edgeArm{
				{"source in an ungranted repository", "", func() *sourceRowTables {
					return dep([]contractsv1.ResolvedScope{edgeScope(x, xs)}, []contractsv1.ResolvedScope{edgeScope(g, gs)}, x, xs).edgeRow(g, gs, depRef, "work_item_dependencies.v2", "work_item_dependency", depID)
				}},
				{"target in an ungranted repository", "", func() *sourceRowTables {
					return dep([]contractsv1.ResolvedScope{edgeScope(g, gs)}, []contractsv1.ResolvedScope{edgeScope(x, xs)}, g, gs)
				}},
				{"target mapped to more repositories than the bound", "", func() *sourceRowTables {
					return dep([]contractsv1.ResolvedScope{edgeScope(g, gs)}, tooMany, g, gs)
				}},
			}},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, ref: hierGranted,
			served: func() *sourceRowTables { return hier(hierGranted, hierGrantedID, g, gs, edgeScope(g, gs)) },
			refused: []edgeArm{
				{"child repository not granted", hierSecret, func() *sourceRowTables {
					return hier(hierSecret, hierSecretID, x, xs, edgeScope(g, gs))
				}},
				{"parent in an ungranted repository", "", func() *sourceRowTables {
					return hier(hierGranted, hierGrantedID, g, gs, edgeScope(x, xs))
				}},
				{"parent that is no work item", "", func() *sourceRowTables { return hier(hierGranted, hierGrantedID, g, gs) }},
			}},
		{kind: contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, ref: teamRef,
			served: func() *sourceRowTables { return workItemTeam([]contractsv1.ResolvedScope{edgeScope(g, gs)}, gs) },
			refused: []edgeArm{
				{"work item in an ungranted repository", "", func() *sourceRowTables {
					return workItemTeam([]contractsv1.ResolvedScope{edgeScope(x, xs)}, gs)
				}},
				{"team refused by the subject gate", "", func() *sourceRowTables {
					return workItemTeam([]contractsv1.ResolvedScope{edgeScope(g, gs)}, xs)
				}},
			}},
		{kind: contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, ref: diGranted,
			served: func() *sourceRowTables { return di(diGranted, diGrantedID, g, gs, edgeScope(g, gs)) },
			refused: []edgeArm{
				{"edge repository not granted", diSecret, func() *sourceRowTables {
					return di(diSecret, diSecretID, x, xs, edgeScope(g, gs))
				}},
				{"incident mapped only to an ungranted repository", "", func() *sourceRowTables {
					return di(diGranted, diGrantedID, g, gs, edgeScope(x, xs))
				}},
				{"incident no service maps", "", func() *sourceRowTables { return di(diGranted, diGrantedID, g, gs) }},
			}},
		{kind: contractsv1.ContextFabricEvidenceEntityReviewV2, ref: revGranted,
			served: func() *sourceRowTables {
				return newSourceRowTables().edgeRow(g, gs, revGranted, "pull_request_reviews.v2", "pull_request_review", revGrantedID)
			},
			refused: []edgeArm{
				{"review repository not granted", revSecret, func() *sourceRowTables {
					return newSourceRowTables().edgeRow(x, xs, revSecret, "pull_request_reviews.v2", "pull_request_review", revSecretID)
				}},
			}},
		{kind: contractsv1.ContextFabricEvidenceEntityProjectTeamV2, ref: ptRef, unrestricted: true,
			served: projectTeam,
			refused: []edgeArm{
				{"restricted caller", "", projectTeam},
			}},
	}
}

// A ".v2" edge ref is expanded from its source row without a result_id when
// every gate admits it; each gate clause refuses with the bytes of an absent
// ref and reads no row; a stored result citing the ref is never served in
// place of the row, and still serves with its result_id.
func TestEvidenceRouteEdgeRefsExpandWithoutResultID(t *testing.T) {
	for _, tc := range edgeKindCases(t) {
		store := memoryinvestigation.NewStore()
		seedResult3355(t, store, "org_1", citingStoredResult("result_edge_"+string(tc.kind), tc.ref))
		serve := func(tables *sourceRowTables, ref string, unrestricted bool, resultID string) *httptest.ResponseRecorder {
			repos := []string{hostedTestRepository}
			if unrestricted {
				repos = []string{"*"}
			}
			app, token := newParityHostedAppForRepositories(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, &bytes.Buffer{}, repos)
			app.runtime.SourceRows = testSourceRows(t, tables)
			rec := httptest.NewRecorder()
			request := scopedEvidenceRequest(t, token, ref, resultID)
			request.Header.Set("X-Request-ID", "req_6180000000000000000000000000beef")
			app.Handler().ServeHTTP(rec, request)
			return rec
		}
		t.Run(string(tc.kind)+"/served", func(t *testing.T) {
			rec := serve(tc.served(), tc.ref, tc.unrestricted, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var expanded contractsv1.ExpandedEvidence
			if err := json.Unmarshal(rec.Body.Bytes(), &expanded); err != nil {
				t.Fatal(err)
			}
			if expanded.Evidence.EvidenceRefID != tc.ref || expanded.Evidence.Source.System != "dev_health" || expanded.Evidence.Metadata["record"] != "source_row" {
				t.Fatalf("not the source row: %+v", expanded.Evidence)
			}
		})
		absent := serve(newSourceRowTables(), tc.ref, tc.unrestricted, "")
		t.Run(string(tc.kind)+"/absent", func(t *testing.T) {
			if absent.Code != http.StatusNotFound {
				t.Fatalf("absent ref without result_id: status %d, want 404 (no stored-result fallback): %s", absent.Code, absent.Body.String())
			}
			if rec := serve(newSourceRowTables(), tc.ref, tc.unrestricted, "result_edge_"+string(tc.kind)); rec.Code != http.StatusOK {
				t.Fatalf("scoped ref with an absent row: status %d, want 200 from the stored result: %s", rec.Code, rec.Body.String())
			}
		})
		for _, arm := range tc.refused {
			t.Run(string(tc.kind)+"/"+arm.name, func(t *testing.T) {
				tables := arm.build()
				ref := arm.ref
				if ref == "" {
					ref = tc.ref
				}
				got := serve(tables, ref, false, "")
				absent := serve(newSourceRowTables(), ref, false, "")
				if got.Code != http.StatusNotFound {
					t.Fatalf("status %d, want 404: %s", got.Code, got.Body.String())
				}
				if !bytes.Equal(got.Body.Bytes(), absent.Body.Bytes()) || fmt.Sprint(got.Header()) != fmt.Sprint(absent.Header()) {
					t.Fatalf("differs from an absent ref:\n %s %v\n %s %v", got.Body.String(), got.Header(), absent.Body.String(), absent.Header())
				}
				for _, read := range tables.reads {
					if strings.HasPrefix(read, "row:") || strings.HasPrefix(read, "org_row:") {
						t.Fatalf("a refused row was read: %v", tables.reads)
					}
				}
			})
		}
	}
}
