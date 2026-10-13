package devhealthsource

import (
	"context"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/relationcurrent"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// endedLinks is the set of blocks relations the provider no longer reports,
// with the time each ended. The rule needs the read times of every blocker
// endpoint of the organization, so it is evaluated once per projection call
// and the page statements receive only its result (the removed links), never
// the endpoint tables.
type endedLinks struct {
	source, target, relationshipType []string
	endedAtMicros                    []int64
}

type endedLinksKey struct{}

type endedLinkMemo struct {
	mu    sync.Mutex
	byOrg map[string]*endedLinks
}

func withEndedLinkMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, endedLinksKey{}, &endedLinkMemo{byOrg: map[string]*endedLinks{}})
}

// endedLinksFor reads the set at most once per memo; a context without a memo
// reads it for the one call.
func endedLinksFor(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string) (*endedLinks, error) {
	memo, _ := ctx.Value(endedLinksKey{}).(*endedLinkMemo)
	if memo == nil {
		return readEndedLinks(ctx, client, orgID)
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if links, ok := memo.byOrg[orgID]; ok {
		return links, nil
	}
	links, err := readEndedLinks(ctx, client, orgID)
	if err != nil {
		return nil, err
	}
	memo.byOrg[orgID] = links
	return links, nil
}

func readEndedLinks(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string) (*endedLinks, error) {
	rows, err := client.Query(ctx, relationcurrent.EndedLinksSQL(), []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := &endedLinks{source: []string{}, target: []string{}, relationshipType: []string{}, endedAtMicros: []int64{}}
	for rows.Next() {
		var source, target, relationshipType string
		var at int64
		if err := rows.Scan(&source, &target, &relationshipType, &at); err != nil {
			return nil, err
		}
		links.source = append(links.source, source)
		links.target = append(links.target, target)
		links.relationshipType = append(links.relationshipType, relationshipType)
		links.endedAtMicros = append(links.endedAtMicros, at)
	}
	return links, rows.Err()
}

func (l *endedLinks) bindings() []contextpacket.ClickHouseBinding {
	return []contextpacket.ClickHouseBinding{
		{Name: "ended_source", Value: l.source}, {Name: "ended_target", Value: l.target},
		{Name: "ended_type", Value: l.relationshipType}, {Name: "ended_at_us", Value: l.endedAtMicros},
	}
}

// endedLinksJoinSQL joins a dependency row d to its entry in the set, as rr:
// rr.ended_at is set where the link ended.
const endedLinksJoinSQL = `LEFT JOIN (SELECT z.1 AS s, z.2 AS t, z.3 AS rt, fromUnixTimestamp64Micro(z.4, 'UTC') AS ended_at, 1 AS ended
  FROM (SELECT arrayJoin(arrayZip({ended_source:Array(String)}, {ended_target:Array(String)}, {ended_type:Array(String)}, {ended_at_us:Array(Int64)})) AS z)) AS rr
  ON rr.s = d.source_work_item_id AND rr.t = d.target_work_item_id AND rr.rt = d.relationship_type`
