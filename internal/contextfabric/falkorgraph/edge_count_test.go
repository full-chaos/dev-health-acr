package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestEdgeCountCypherPerRelationAndArm(t *testing.T) {
	t.Parallel()
	project := contractsv1.ContextFabricRelationshipBelongsToProject
	for name, tc := range map[string]struct {
		key     contextfabric.EdgeCountKey
		want    []string
		notWant []string
		wantErr string
	}{
		"relation without arms": {
			key:     contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest},
			want:    []string{"MATCH (a:" + labelSubject + " {" + propOrgID + ":$org})-[r:" + labelRelation + "]->()", "r." + propRelationType + " = $rel", "count(DISTINCT r." + propRelationshipID + ")"},
			notWant: []string{propValidFromNs},
		},
		"column arm has no validity start": {
			key:  contextfabric.EdgeCountKey{Relation: project, Arm: contextfabric.EdgeArmWorkItemColumn},
			want: []string{"r." + propValidFromNs + " IS NULL"},
		},
		"transition arm has a validity start": {
			key:     contextfabric.EdgeCountKey{Relation: project, Arm: contextfabric.EdgeArmTransition},
			want:    []string{"r." + propValidFromNs + " IS NOT NULL"},
			notWant: []string{propValidFromNs + " IS NULL"},
		},
		"unknown arm is refused":            {key: contextfabric.EdgeCountKey{Relation: project, Arm: "mystery"}, wantErr: `has no arm "mystery"`},
		"an arm on a relation without arms": {key: contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest, Arm: contextfabric.EdgeArmTransition}, wantErr: "has no arm"},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cypher, err := edgeCountCypher(tc.key)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error %q, got %q, %v", tc.wantErr, cypher, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(cypher, want) {
					t.Fatalf("cypher lacks %q:\n%s", want, cypher)
				}
			}
			for _, banned := range tc.notWant {
				if strings.Contains(cypher, banned) {
					t.Fatalf("cypher must not contain %q:\n%s", banned, cypher)
				}
			}
		})
	}
}

func TestCountEdgesBindsOrgAndNormalizedRelationAndReadsOneScalar(t *testing.T) {
	t.Parallel()
	var params map[string]interface{}
	var cypher string
	var readOnly bool
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, p map[string]interface{}, ro bool) ([]row, error) {
		if strings.Contains(q, "relation_type") {
			cypher, params, readOnly = q, p, ro
		}
		return []row{{"total": int64(1997)}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	got, err := adapter.CountEdges(context.Background(), "org-1", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmTransition})
	if err != nil || got != 1997 {
		t.Fatalf("CountEdges = %d, %v; want 1997", got, err)
	}
	if !readOnly || params["org"] != "org-1" || params["rel"] == "" || strings.Contains(cypher, "org-1") || strings.Contains(cypher, "BELONGS_TO_PROJECT") {
		t.Fatalf("values are bound, never interpolated; read only: ro=%v params=%v\n%s", readOnly, params, cypher)
	}
}

func TestCountEdgesFailsLoudlyOnAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	for name, rows := range map[string][]row{
		"no row":       {},
		"two rows":     {{"total": int64(1)}, {"total": int64(2)}},
		"non numeric":  {{"total": "many"}},
		"missing cell": {{"other": int64(1)}},
	} {
		rows := rows
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
				if strings.Contains(q, "relation_type") {
					return rows, nil
				}
				return []row{{"total": int64(0)}}, nil
			}}
			got, err := newFakeAdapter(t, fake).CountEdges(context.Background(), "org-1", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest})
			if err == nil || got != 0 {
				t.Fatalf("an unreadable answer is an error, never a zero: %d, %v", got, err)
			}
		})
	}
	if _, err := newFakeAdapter(t, &fakeConn{}).CountEdges(context.Background(), " ", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject}); err == nil {
		t.Fatal("a blank organization is refused")
	}
}
