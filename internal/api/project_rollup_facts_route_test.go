package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// projectRollupRowsClient answers the two project rollup statements with one
// canned row each and every other statement with no rows.
type projectRollupRowsClient struct{}

func (projectRollupRowsClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	collapsed := strings.Join(strings.Fields(statement), " ")
	switch {
	case strings.Contains(collapsed, "lp.project_key"):
		return &projectRollupRows{rows: [][]any{{"github:P1", int64(3), int64(4), int64(1), int64(2), int64(0)}}}, nil
	case strings.Contains(collapsed, "SELECT project_key, toInt64(uniqExactIf(tgt, is_open = 1))"):
		return &projectRollupRows{rows: [][]any{{"github:P1", int64(2), int64(2), int64(1)}}}, nil
	}
	return &projectRollupRows{}, nil
}

type projectRollupRows struct {
	rows [][]any
	next int
}

func (r *projectRollupRows) Next() bool { return r.next < len(r.rows) }

func (r *projectRollupRows) Scan(dest ...any) error {
	row := r.rows[r.next]
	r.next++
	for i, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[i].(string)
		case *int64:
			*value = row[i].(int64)
		default:
			return errors.New("project rollup rows: unsupported scan destination")
		}
	}
	return nil
}

func (r *projectRollupRows) Err() error   { return nil }
func (r *projectRollupRows) Close() error { return nil }

// A client model ranking projects by open pull requests or blockers reads both
// kinds for a set of project subjects in one read_facts call; the catalogue
// names the route.
func TestReadFactsRouteServesProjectPullRequestAndBlockerCounts(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	// The work-item reader refuses a deadline under one second; the harness
	// default request budget is exactly one.
	h.app.config.RequestTimeout = 30 * time.Second
	token := h.issueFor(t, []string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"}, nil).Token
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(projectRollupRowsClient{}), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.app.runtime.DirectReadGate = directread.NewSubjectGate(admitAllGraph{}, nil)
	h.app.runtime.DirectFactReader = directread.NewFactReader(registry.WithoutScopeExpansion())
	project, omitted, err := identity.Derive(identity.KindProject, []string{"github", "P1"}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project id: %v", err)
	}

	response := h.postFacts(token, `{"kinds":["pull_requests","blockers"],"subjects":[{"kind":"project","canonical_id":"`+project+`"}]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("read_facts status %d: %s", response.Code, response.Body.String())
	}
	var body directread.FactsResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Coverage) != 2 {
		t.Fatalf("coverage rows = %d, want one per kind: %s", len(body.Coverage), response.Body.String())
	}
	// The rollup is served; the per-item expansion beside it is off on the
	// direct path and disclosed, as for a team root, so the row may read
	// truncated, never unavailable.
	for _, row := range body.Coverage {
		if row.Outcome != directread.OutcomeFactServed && row.Outcome != directread.OutcomeTruncated {
			t.Fatalf("%s over a project is %s (%s), want the rollup served: %s", row.Kind, row.Outcome, row.Reason, response.Body.String())
		}
	}
	want := map[string]string{"pull_requests": "pull_requests_open_current", "blockers": "blocked_work_items_current"}
	for _, fact := range body.Facts {
		field := want[fact.Kind]
		if field == "" {
			continue
		}
		if _, ok := fact.Fields[field]; !ok {
			t.Fatalf("%s fact lacks %s: %s", fact.Kind, field, response.Body.String())
		}
		delete(want, fact.Kind)
	}
	if len(want) != 0 {
		t.Fatalf("no fact served for %v: %s", want, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, ContextFabricDataCatalogPath+"?sections=facts", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	catalog := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(catalog, request)
	if catalog.Code != http.StatusOK {
		t.Fatalf("catalog status %d: %s", catalog.Code, catalog.Body.String())
	}
	var document struct {
		Facts directread.CatalogFacts `json:"facts"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document.Facts.Note, "kinds pull_requests and blockers with project subjects") || !strings.Contains(document.Facts.Note, "linked pull request rows") {
		t.Fatalf("catalogue note does not name the project route: %q", document.Facts.Note)
	}
	for _, entry := range document.Facts.Kinds {
		if entry.Kind != "pull_requests" && entry.Kind != "blockers" {
			continue
		}
		found := false
		for _, kind := range entry.SubjectKinds {
			found = found || kind == "project"
		}
		if !found {
			t.Fatalf("catalogue %s subject kinds = %v, want project", entry.Kind, entry.SubjectKinds)
		}
	}
}
