package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// noReposGraph is the route graph with no repository the caller may read.
type noReposGraph struct{ chaos7126Graph }

func (noReposGraph) ListSubjectsByKind(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, string, string, int) (directread.LookupPage, error) {
	return directread.LookupPage{}, nil
}

// numericMembers walks a decoded JSON value and returns the paths of every
// number and of every member whose name suggests a count.
func numericMembers(path string, value any, out *[]string) {
	switch v := value.(type) {
	case float64:
		*out = append(*out, path)
	case map[string]any:
		for key, member := range v {
			switch key {
			case "count", "total", "total_known", "returned", "population", "rows", "matches":
				*out = append(*out, path+"."+key)
			}
			numericMembers(path+"."+key, member, out)
		}
	case []any:
		for _, member := range v {
			numericMembers(path+"[]", member, out)
		}
	}
}

// CHAOS-7160 r1 (the reviewer's route case, permanent): a restricted
// credential with NO readable repository asks for a work-item handle through
// the real route and the production census and anchor support. The state to
// reach: 400 invalid_request, reason scope_required, and a refusal body in
// which no member is a number or a count.
func TestReviewRouteRestrictedWorkItemHandleWithNoReadableRepositories(t *testing.T) {
	graph := noReposGraph{chaos7126Graph{chaos7074Graph: chaos7074RouteGraph()}}
	censusCalls := 0
	production := devhealthsource.NewCensusFunc(nil)
	census := func(ctx context.Context, org string, kind graphrank.CensusKind, value string, bound bool, anchorKind contextfabric.SubjectKind, anchor string, anchorBound bool) (graphrank.CensusOutcome, error) {
		censusCalls++
		return production(ctx, org, kind, value, bound, anchorKind, anchor, anchorBound)
	}
	h := newChaos7071Harness(t, 100, func(deps *RuntimeDependencies) {
		gate := directread.NewSubjectGate(graph, nil)
		deps.DirectReadGate = gate
		deps.DataSubjects = directread.NewSubjectLookup(graph, gate, nil).
			WithOwnershipAndHandles(graph, census, graph).
			WithCensusAnchorSupport(devhealthsource.CensusAnchorSupported)
	})
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	response := h.postSubjects(token, `{"handle":"CHAOS-4322"}`)
	assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Details["reason"] != "scope_required" {
		t.Fatalf("reason = %v", envelope.Error.Details)
	}
	var detailsJSON any
	raw, _ := json.Marshal(envelope.Error.Details)
	_ = json.Unmarshal(raw, &detailsJSON)
	var numeric []string
	numericMembers("details", detailsJSON, &numeric)
	if len(numeric) != 0 {
		t.Fatalf("refusal details carry numbers or counts: %v (%s)", numeric, raw)
	}
	if censusCalls != 0 {
		t.Fatalf("the refusal ran %d census statements", censusCalls)
	}
}
