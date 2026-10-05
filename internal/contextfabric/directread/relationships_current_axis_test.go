package directread

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Every edge page of a read without as_of (hop 1, the depth-2 frontier scan
// and hop 2) asks the graph for the current-axis rule; every page of an as_of
// read asks for the strict window.
func TestRelationshipsCurrentAxisRuleOnEveryPage(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		asOf    string
		current bool
	}{
		{"current axis", "", true},
		{"as_of axis", now.Add(-48 * time.Hour).Format(time.RFC3339Nano), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := &fakeEdgeGraph{fakeGraph: graphOfOrgA()}
			graph.nodes[graphrank.SubjectKey(workA)] = repos("acme/a")
			graph.edges = []EdgeCandidate{
				edgeBetween("e1", "OWNED_BY_TEAM", repoA, teamT, nil),
				edgeBetween("e2", "BELONGS_TO_REPOSITORY", workA, repoA, nil),
			}
			reader, _ := newRelReader(graph, &now)
			_, ids := readAll(t, reader, unrestricted, RelationshipsRequest{
				Subject: RelationshipsSubject{Kind: string(contractsv1.ContextFabricSubjectTeam), CanonicalID: teamT.CanonicalID}, Depth: 2, AsOf: tc.asOf,
			})
			if len(ids) != 2 {
				t.Fatalf("ids = %v, want the hop-1 and the hop-2 edge", ids)
			}
			if len(graph.queries) < 3 {
				t.Fatalf("%d edge pages read, want hop 1, the frontier scan and hop 2", len(graph.queries))
			}
			for i, q := range graph.queries {
				if q.Current != tc.current {
					t.Fatalf("page %d (origins %v): Current = %v, want %v", i, q.Origins, q.Current, tc.current)
				}
			}
		})
	}
}

// A cursor issued under the strict current-axis rule (request digest tag v1)
// does not continue a read under the current rule: it is refused as stale,
// and the graph is not read. The forged cursor carries the digest the earlier
// build computed for the same request.
func TestRelationshipsCursorFromTheStrictRuleIsRefused(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	graph := hubGraph()
	reader, _ := newRelReader(graph, &now)
	request := RelationshipsRequest{Subject: RelationshipsSubject{Kind: "team", CanonicalID: teamT.CanonicalID}, Direction: "in", Limit: 100}
	first, err := reader.Read(relCtx("strict-p0"), unrestricted, request)
	if err != nil || first.Page.NextCursor == "" {
		t.Fatalf("first page: %v %+v", err, first.Page)
	}
	cursor := decodeCursorForTest(t, reader.sealer, first.Page.NextCursor)
	old := sha256.Sum256([]byte(strings.Join([]string{
		"read_relationships.v1", "team", teamT.CanonicalID, "", "in", "1", "",
	}, "\x00")))
	if cursor.RequestDigest == hex.EncodeToString(old[:16]) {
		t.Fatal("the request digest still equals the strict rule's digest")
	}
	cursor.RequestDigest = hex.EncodeToString(old[:16])
	token, err := encodeRelationshipsCursor(reader.sealer, cursor)
	if err != nil {
		t.Fatal(err)
	}
	calls := graph.pageCalls
	request.Cursor = token
	_, err = reader.Read(relCtx("strict-p1"), unrestricted, request)
	var requestError *RelationshipsRequestError
	if !errors.As(err, &requestError) || requestError.Reason != RelationshipsRefusalInvalidCursor || requestError.Cursor != CursorStale {
		t.Fatalf("err = %v (%+v), want an invalid_cursor refusal with outcome stale", err, requestError)
	}
	if graph.pageCalls != calls {
		t.Fatal("a refused cursor read the graph")
	}
}
