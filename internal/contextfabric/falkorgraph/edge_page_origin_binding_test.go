package falkorgraph

import (
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// The in arm binds the origin before it matches the edge, on both axes and
// for a frontier; with EndKinds it keeps the single pattern. The out arm is
// one pattern that starts at the origin.
func TestDirectEdgePageInArmBindsTheOriginFirst(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	origin := []contextfabric.SubjectRef{{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T"}}
	const (
		bound   = "UNWIND $origins AS o MATCH (b:Subject {org_id:$org, subject_kind:o.k, canonical_id:o.i}) WITH b MATCH (a:Subject {org_id:$org})-[r:Relates]->(b) WHERE "
		single  = "UNWIND $origins AS o MATCH (a:Subject {org_id:$org})-[r:Relates]->(b:Subject {org_id:$org, subject_kind:o.k, canonical_id:o.i}) WHERE "
		outward = "UNWIND $origins AS o MATCH (a:Subject {org_id:$org, subject_kind:o.k, canonical_id:o.i})-[r:Relates]->(b:Subject {org_id:$org}) WHERE "
	)
	for name, c := range map[string]struct {
		query directread.EdgePageQuery
		want  []string
		not   []string
	}{
		"current both":   {directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: at, Current: true}, []string{bound, outward}, []string{single}},
		"as_of both":     {directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: at}, []string{bound, outward}, []string{single}},
		"current in":     {directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: at, Current: true, Direction: directread.EdgeDirectionIn}, []string{bound}, []string{single, outward}},
		"current out":    {directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: at, Current: true, Direction: directread.EdgeDirectionOut}, []string{outward}, []string{single, bound}},
		"end kinds in":   {directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: at, Direction: directread.EdgeDirectionIn, EndKinds: []string{"repository"}}, []string{single}, []string{bound, outward}},
		"end kinds both": {directread.EdgePageQuery{Origins: origin, Limit: 5, ValidAt: at, EndKinds: []string{"repository"}}, []string{single, outward}, []string{bound}},
	} {
		cypher, _ := directEdgePageCypher("org", c.query)
		for _, want := range c.want {
			if !strings.Contains(cypher, want) {
				t.Errorf("%s: statement lacks %q:\n%s", name, want, cypher)
			}
		}
		for _, not := range c.not {
			if strings.Contains(cypher, not) {
				t.Errorf("%s: statement holds %q:\n%s", name, not, cypher)
			}
		}
	}
}
