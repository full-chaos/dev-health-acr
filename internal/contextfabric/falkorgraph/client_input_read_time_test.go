package falkorgraph

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestTwoReadsOfOneGraphGiveOneClientInput(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			return []row{{
				"r": &edge{Properties: map[string]interface{}{
					propRelationType: "BLOCKS", propRelationshipID: "relationship_read_time",
					propEvidenceRefs:             []string{"evidence_read_time_1234"},
					"authorization_repositories": []string{"full-chaos/dev-health-acr"},
				}},
				"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_target",
			}}, nil
		default:
			switch params["id"] {
			case "p1":
				originRow := fakeSubjectNodeRow("project", "p1", "Origin")
				originRow["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
				return []row{originRow}, nil
			case "work_target":
				targetRow := fakeSubjectNodeRow("work_item", "work_target", "Target")
				targetRow["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
				return []row{targetRow}, nil
			default:
				return nil, nil
			}
		}
	}}
	adapter := newFakeAdapter(t, fake)
	clock := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	adapter.now = func() time.Time {
		clock = clock.Add(2317 * time.Millisecond)
		return clock
	}
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}

	asOf := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for name, timeContext := range map[string]contextfabric.TimeContext{
		"current":          {Axis: contextfabric.TemporalCurrent},
		"historical as-of": {Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
	} {
		t.Run(name, func(t *testing.T) {
			twoReadsGiveOneClientInput(t, adapter, principal, origin, timeContext)
		})
	}
}

func twoReadsGiveOneClientInput(t *testing.T, adapter *Adapter, principal storage.Principal, origin contextfabric.SubjectRef, timeContext contextfabric.TimeContext) {
	t.Helper()
	read := func() (client, server []byte) {
		t.Helper()
		request := fakeDiscoveryRequest(origin, 10)
		request.Interpretation.TimeContext = timeContext
		graph, err := adapter.DiscoverContext(context.Background(), principal, request)
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		stamped := 0
		for _, source := range graph.Coverage.Sources {
			if source.ObservedAt != nil {
				stamped++
			}
		}
		wantStamped := 1
		if timeContext.Axis != contextfabric.TemporalCurrent {
			wantStamped = 2
		}
		if stamped != wantStamped || findFakeEdge(graph.Paths, "BLOCKS") == nil {
			t.Fatalf("the read returned %d stamped sources (want %d) or no path: sources=%+v", stamped, wantStamped, graph.Coverage.Sources)
		}
		input := contextfabric.SynthesisInput{
			Request:        contextfabric.InvestigationRequest{Question: "what does it depend on"},
			Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, TimeContext: timeContext},
			Graph:          graph,
		}
		client, err = synthesisprompt.ClientPayload(principal.OrgID, input, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		server, err = synthesisprompt.UserPayload(principal.OrgID, input, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return client, server
	}
	firstClient, firstServer := read()
	secondClient, secondServer := read()
	if bytes.Equal(firstServer, secondServer) {
		t.Fatal("the two reads gave one service model input: the reader no longer stamps its read time, so this test proves nothing")
	}
	if !bytes.Equal(firstClient, secondClient) {
		t.Fatalf("two reads of one graph gave two client inputs:\n%s\n%s", firstClient, secondClient)
	}
	if bytes.Contains(firstClient, []byte(`"observed_at":"2026-08-12T12:00`)) {
		t.Fatalf("the client input carries a read time: %s", firstClient)
	}
}
