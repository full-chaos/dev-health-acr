package falkorgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12300: resolveEdge's authorization refusals of a project-only caller
// are classified by the predicate that decided them; other classes, other
// resolutions and other reasons add nothing to the basis.
func TestNoteAuthzBasisClassifiesEachProjectOnlyRefusal(t *testing.T) {
	projectOnly := storage.Principal{OrgID: "o", ProjectScopes: []string{"project.v2:linear:P-1"}}
	edge := func(name string, attrs map[string]interface{}) graphrank.CandidateEdge {
		return graphrank.CandidateEdge{Name: name, Attributes: attrs}
	}
	var c edgeFilterCounts
	scope := contextfabric.RequestedScope{}
	c.noteAuthzBasis(projectOnly, scope, edge("DOCUMENTED_BY", nil), edgeFiltered, edgeFilterReasonAuthz)
	c.noteAuthzBasis(projectOnly, scope, edge("OWNED_BY_TEAM", map[string]interface{}{"authorization_projects": []string{"x"}}), edgeFiltered, edgeFilterReasonAuthz)
	c.noteAuthzBasis(projectOnly, scope, edge("OWNED_BY_TEAM", map[string]interface{}{"authorization_projects": []string{"x"}}), edgeFiltered, edgeFilterReasonAuthz)
	c.noteAuthzBasis(projectOnly, scope, edge("BELONGS_TO_PROJECT", map[string]interface{}{}), edgeFiltered, edgeFilterReasonAuthz) // an endpoint refused it
	c.noteAuthzBasis(projectOnly, contextfabric.RequestedScope{ProjectIDs: []string{"P-1"}}, edge("BELONGS_TO_PROJECT", nil), edgeFiltered, edgeFilterReasonAuthz)
	c.noteAuthzBasis(projectOnly, scope, edge("DOCUMENTED_BY", nil), edgeFiltered, edgeFilterReasonTemporalWindow)
	c.noteAuthzBasis(projectOnly, scope, edge("DOCUMENTED_BY", nil), edgeAdmitted, edgeFilterReasonNone)
	c.noteAuthzBasis(storage.Principal{OrgID: "o", RepositoryScopes: []string{"acme/a"}}, scope, edge("DOCUMENTED_BY", nil), edgeFiltered, edgeFilterReasonAuthz)
	want := EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1, AttributionRelation: 1, ProjectList: 2, ProjectNarrowing: 1}
	if c.Basis != want {
		t.Fatalf("basis %+v, want %+v", c.Basis, want)
	}
}

func edgeFilterLine(t *testing.T, authz int, basis EdgeAuthzBasis) map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	SlogTelemetry{Logger: slog.New(slog.NewJSONHandler(&buffer, nil))}.RecordEdgesFilteredByReason(context.Background(), "org_1", authz, 1, 0, basis)
	var line map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &line); err != nil {
		t.Fatalf("decode %q: %v", buffer.String(), err)
	}
	return line
}

// CHAOS-12300: a project-only caller's edge-filter line carries the E1/E2
// basis of its authz count, and the unclassified rest (tree-walk refusals)
// so the parts add up; every other class keeps the three counts alone.
func TestTheEdgeFilterLineCarriesTheProjectOnlyBasis(t *testing.T) {
	line := edgeFilterLine(t, 9, EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1, AttributionRelation: 2, ProjectList: 3, ProjectNarrowing: 0})
	for key, want := range map[string]float64{"authz": 9, "authz_endpoint": 1, "authz_attribution_relation": 2, "authz_project_list": 3, "authz_project_narrowing": 0, "authz_unclassified": 3} {
		if got, ok := line[key].(float64); !ok || got != want {
			t.Errorf("%s = %v, want %v", key, line[key], want)
		}
	}
	if line["grant_class"] != "project_only" {
		t.Errorf("grant_class = %v", line["grant_class"])
	}
	other := edgeFilterLine(t, 4, EdgeAuthzBasis{})
	for _, key := range []string{"grant_class", "authz_endpoint", "authz_attribution_relation", "authz_project_list", "authz_project_narrowing", "authz_unclassified"} {
		if _, ok := other[key]; ok {
			t.Errorf("a non-project-only line carries %s: %v", key, other)
		}
	}
	if len(other) != 7 { // time, level, msg, org_id, authz, temporal_window, self_loop
		t.Errorf("a non-project-only line changed shape: %v", other)
	}
}

// CHAOS-12300, wiring: a project-only DiscoverContext walk reports the E1/E2
// basis of each refused edge through GraphTelemetry: an attribution relation
// (E2), a foreign project list (E1) and an edge refused on its unstated
// work-item endpoint.
func TestAProjectOnlyWalkReportsEachEdgeBasisThroughTelemetry(t *testing.T) {
	const granted = "project.v2:linear:P-1"
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: granted, Label: "Origin"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != granted {
				return nil, nil
			}
			return []row{
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "DOCUMENTED_BY", propRelationshipID: "rel_doc"}},
					"srcKind": "project", "srcId": granted, "dstKind": "document", "dstId": "doc_1"},
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_team", "authorization_projects": []string{"project.v2:linear:OTHER"}}},
					"srcKind": "project", "srcId": granted, "dstKind": "team", "dstId": "team_1"},
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "BELONGS_TO_PROJECT", propRelationshipID: "rel_member"}},
					"srcKind": "work_item", "srcId": "work_unstated", "dstKind": "project", "dstId": granted},
			}, nil
		default: // nodeByKindID
			switch params["id"] {
			case granted:
				return []row{fakeSubjectNodeRow("project", granted, "Origin")}, nil
			case "work_unstated":
				return []row{fakeSubjectNodeRow("work_item", "work_unstated", "Unstated")}, nil
			}
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", ProjectScopes: []string{granted}}
	if _, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 10)); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	want := EdgeAuthzBasis{ProjectOnly: true, Endpoint: 1, AttributionRelation: 1, ProjectList: 1}
	if telemetry.edgesFilteredBasis != want || telemetry.edgesFilteredAuthz != 3 {
		t.Fatalf("authz %d basis %+v, want 3 and %+v", telemetry.edgesFilteredAuthz, telemetry.edgesFilteredBasis, want)
	}
}

// CHAOS-12300, wiring: the edges of a full-text hit (DiscoverContext's
// text-adjacent loop) are classified too, so the basis covers both loops.
func TestAProjectOnlyFullTextHitReportsItsEdgeBasis(t *testing.T) {
	const textHit = "project.v2:linear:P-2"
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return []row{fulltextRow("project", textHit, "Text hit", "struggling", nil)}, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != textHit {
				return nil, nil
			}
			return []row{
				{"r": &edge{Properties: map[string]interface{}{propRelationType: "OWNED_BY_TEAM", propRelationshipID: "rel_text_team", "authorization_projects": []string{"project.v2:linear:OTHER"}}},
					"srcKind": "project", "srcId": textHit, "dstKind": "team", "dstId": "team_2"},
			}, nil
		default:
			if params["id"] == textHit {
				return []row{fakeSubjectNodeRow("project", textHit, "Text hit")}, nil
			}
			return nil, nil
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", ProjectScopes: []string{textHit}}
	request := fakeDiscoveryRequest(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project.v2:linear:ABSENT"}, 10)
	if _, err := adapter.DiscoverContext(context.Background(), principal, request); err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	want := EdgeAuthzBasis{ProjectOnly: true, ProjectList: 1}
	if telemetry.edgesFilteredBasis != want || telemetry.edgesFilteredAuthz != 1 {
		t.Fatalf("authz %d basis %+v, want 1 and %+v", telemetry.edgesFilteredAuthz, telemetry.edgesFilteredBasis, want)
	}
}
