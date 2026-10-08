package directread

import "testing"

// owned_by reads ownership on the current axis: an owned project whose own
// validity ended (an archived project) is still owned, so the edge page must
// not window the end nodes.
func TestOwnedByReadsTheEdgePageOnTheCurrentAxis(t *testing.T) {
	g := ownershipGraph()
	if _, err := newModesLookup(g, nil).Find(relCtx("owned-current-axis"), unrestricted, FindRequest{OwnedBy: teamT.CanonicalID}); err != nil {
		t.Fatal(err)
	}
	if len(g.queries) == 0 {
		t.Fatal("owned_by read no edge page")
	}
	for _, q := range g.queries {
		if !q.Current {
			t.Fatalf("owned_by edge page query = %+v, want Current", q)
		}
	}
}
