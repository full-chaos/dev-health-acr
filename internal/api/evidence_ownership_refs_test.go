package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/limits"
)

type ownershipArm struct {
	name  string
	build func() *sourceRowTables
}

type ownershipKindCase struct {
	kind         contractsv1.ContextFabricEvidenceEntityType
	ref          string
	unrestricted bool
	served       func() *sourceRowTables
	refused      []ownershipArm
}

func ownershipKindCases(t *testing.T) []ownershipKindCase {
	team := func(owner string) *sourceRowTables {
		graph := &ownershipGraph{owned: map[string][]string{contextfabric.TeamCanonicalID("team-widget"): {owner}}}
		return newSourceRowTables().withTeam("team-widget", graph)
	}
	projectCanonical := projectCanonicalID(t, "jira", "WID")
	ownerTeam := contextfabric.TeamCanonicalID("team-widget")
	project := func(owner string) *sourceRowTables {
		graph := &ownershipGraph{owned: map[string][]string{ownerTeam: {owner}}, ownedBy: map[string][]string{projectCanonical: {ownerTeam}}}
		return newSourceRowTables().withProject("jira", "WID", graph)
	}
	return []ownershipKindCase{
		{kind: contractsv1.ContextFabricEvidenceEntityTeam, ref: contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, "team-widget"),
			served: func() *sourceRowTables { return team(hostedTestRepository) },
			refused: []ownershipArm{
				{"team that owns only an ungranted repository", func() *sourceRowTables { return team(sourceRowSecretSlug) }},
				{"team the graph has no node for", func() *sourceRowTables {
					return newSourceRowTables().withTeam("team-widget", &ownershipGraph{})
				}},
				{"no subject gate composed", func() *sourceRowTables {
					tables := team(hostedTestRepository)
					tables.gate = nil
					return tables
				}},
			}},
		{kind: contractsv1.ContextFabricEvidenceEntityProject, ref: contractsv1.EvidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, "jira:WID"),
			served: func() *sourceRowTables { return project(hostedTestRepository) },
			refused: []ownershipArm{
				{"project owned only by a team of an ungranted repository", func() *sourceRowTables { return project(sourceRowSecretSlug) }},
				{"project no team owns", func() *sourceRowTables {
					graph := &ownershipGraph{owned: map[string][]string{ownerTeam: {hostedTestRepository}}, ownedBy: map[string][]string{projectCanonical: {}}}
					return newSourceRowTables().withProject("jira", "WID", graph)
				}},
				{"project the graph has no node for", func() *sourceRowTables {
					return newSourceRowTables().withProject("jira", "WID", &ownershipGraph{})
				}},
			}},
	}
}

func TestEvidenceRouteOwnershipRefsExpandWithoutResultID(t *testing.T) {
	for _, tc := range ownershipKindCases(t) {
		store := memoryinvestigation.NewStore()
		seedResult3355(t, store, "org_1", citingStoredResult("result_ownership_"+string(tc.kind), tc.ref))
		serve := func(tables *sourceRowTables, resultID string) *httptest.ResponseRecorder {
			app, token := newParityHostedAppForRepositories(t, nil, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, &bytes.Buffer{}, []string{hostedTestRepository})
			app.runtime.SourceRows = testSourceRows(t, tables)
			rec := httptest.NewRecorder()
			request := scopedEvidenceRequest(t, token, tc.ref, resultID)
			request.Header.Set("X-Request-ID", "req_7228000000000000000000000000beef")
			app.Handler().ServeHTTP(rec, request)
			return rec
		}
		absentTables := func() *sourceRowTables {
			if tc.kind == contractsv1.ContextFabricEvidenceEntityTeam {
				return newSourceRowTables().withTeam("other-team", &ownershipGraph{})
			}
			return newSourceRowTables().withProject("jira", "OTHER", &ownershipGraph{})
		}
		t.Run(string(tc.kind)+"/served", func(t *testing.T) {
			rec := serve(tc.served(), "")
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
		t.Run(string(tc.kind)+"/absent", func(t *testing.T) {
			if rec := serve(absentTables(), ""); rec.Code != http.StatusNotFound {
				t.Fatalf("absent ref without result_id: status %d, want 404 (no stored-result fallback): %s", rec.Code, rec.Body.String())
			}
			if rec := serve(absentTables(), "result_ownership_"+string(tc.kind)); rec.Code != http.StatusOK {
				t.Fatalf("scoped ref with an absent row: status %d, want 200 from the stored result: %s", rec.Code, rec.Body.String())
			}
		})
		for _, arm := range tc.refused {
			t.Run(string(tc.kind)+"/"+arm.name, func(t *testing.T) {
				tables := arm.build()
				got := serve(tables, "")
				absent := serve(absentTables(), "")
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

func TestEvidenceRouteOwnershipRefsServeAWildcardCaller(t *testing.T) {
	for _, tc := range ownershipKindCases(t) {
		t.Run(string(tc.kind), func(t *testing.T) {
			app, token := newParityHostedAppForRepositories(t, nil, memoryinvestigation.NewStore(), limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, &bytes.Buffer{}, []string{"*"})
			app.runtime.SourceRows = testSourceRows(t, tc.served())
			rec := httptest.NewRecorder()
			app.Handler().ServeHTTP(rec, scopedEvidenceRequest(t, token, tc.ref, ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
