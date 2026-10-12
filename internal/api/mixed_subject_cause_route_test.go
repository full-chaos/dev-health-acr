package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestFactsRouteMixedSubjectProjectRowCarriesItsOwnCause(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7073Reader(t, h, reachingGraph{}, deploymentsRouteProvider{})
	const subjects = `"subjects":[{"kind":"repository","canonical_id":"` + hostedTestRepository + `"},{"kind":"project","canonical_id":"project:a"}]`
	cases := []struct{ name, body, wantState, wantCause string }{
		{"current window", `{"kinds":["deployments"],` + subjects + `}`, directread.KindStateScopeExpansionUnavailable, "scope expansion is not available on this read path"},
		{"trailing window", `{"kinds":["deployments"],` + subjects + `,"window":{"mode":"trailing","days":30}}`, directread.KindStateWindowUnsupported, "supports the current window only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := h.postFacts(token, tc.body)
			if response.Code != http.StatusOK {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			var document directread.FactsResponse
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			var project, repo *directread.CoverageRow
			for i := range document.Coverage {
				switch document.Coverage[i].Subject.Kind {
				case "project":
					project = &document.Coverage[i]
				case "repository":
					repo = &document.Coverage[i]
				}
			}
			if repo == nil || repo.Outcome != directread.OutcomeReadNoFact || repo.KindState != "truncated" || !strings.Contains(repo.Reason, tc.wantCause) || !strings.HasPrefix(repo.Reason, "narrowed:subject_kind_unsupported: 1 subject(s) were not queried") {
				t.Fatalf("repository row changed: %+v", repo)
			}
			if project == nil {
				t.Fatalf("no project row: %s", response.Body.String())
			}
			if project.Outcome != directread.OutcomeUnavailable || project.KindState != tc.wantState || !strings.Contains(project.Reason, tc.wantCause) || strings.Contains(project.Reason, "narrowed:") {
				t.Fatalf("project row %+v, want unavailable/%s naming %q", *project, tc.wantState, tc.wantCause)
			}
		})
	}
}

func TestFactsRouteProjectOnlyRowIsUnchangedByMixedSubjectFix(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7073Reader(t, h, reachingGraph{}, deploymentsRouteProvider{})
	response := h.postFacts(token, `{"kinds":["deployments"],"subjects":[{"kind":"project","canonical_id":"project:a"}]}`)
	var document directread.FactsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Coverage) != 1 || document.Coverage[0].Outcome != directread.OutcomeUnavailable || document.Coverage[0].KindState != directread.KindStateScopeExpansionUnavailable {
		t.Fatalf("project-only row: %+v", document.Coverage)
	}
}
