package falkorgraph

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The keyset predicate must not be index-answerable (see the comment at its
// construction): a bare `r.relationship_id > $after` turned a 60 ms page
// into a timeout on real data. The live tests cannot see a plan, so the text
// is pinned here.
func TestChaos7074KeysetPredicateStaysOffTheRelationshipIndex(t *testing.T) {
	cypher, params := directEdgePageCypher("org", directread.EdgePageQuery{
		Origins: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T"}},
		After:   &directread.EdgeKey{RelationshipID: "rel-1"}, Limit: 5, ValidAt: time.Unix(1, 0),
	})
	if !strings.Contains(cypher, "toString(r.relationship_id) > $after") || strings.Contains(cypher, " r.relationship_id > $after") {
		t.Fatalf("keyset predicate: %s", cypher)
	}
	if params["lim"] != int64(6) || params["after"] != "rel-1" {
		t.Fatalf("params: %v", params)
	}
}
