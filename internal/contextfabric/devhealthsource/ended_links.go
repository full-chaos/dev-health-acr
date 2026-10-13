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
	// keys holds one element per ended link as relationcurrent.EndedLinksSQL
	// builds it: hex(source), hex(target), hex(type) and the end time in unix
	// microseconds, dot separated. Hex keeps any work item id (a backslash
	// included) inside the binding encoding the client accepts.
	keys []string
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
	links := &endedLinks{keys: []string{}}
	for rows.Next() {
		if err := rows.Scan(&links.keys); err != nil {
			return nil, err
		}
	}
	if links.keys == nil {
		links.keys = []string{}
	}
	return links, rows.Err()
}

func (l *endedLinks) bindings() []contextpacket.ClickHouseBinding {
	return []contextpacket.ClickHouseBinding{{Name: "ended_links", Value: l.keys}}
}

// endedLinksJoinSQL joins a dependency row d to its entry in the set, as rr:
// rr.ended_at is set where the link ended.
const endedLinksJoinSQL = `LEFT JOIN (SELECT unhex(p[1]) AS s, unhex(p[2]) AS t, unhex(p[3]) AS rt, fromUnixTimestamp64Micro(toInt64(p[4]), 'UTC') AS ended_at, 1 AS ended
  FROM (SELECT splitByChar('.', e) AS p FROM (SELECT arrayJoin({ended_links:Array(String)}) AS e))) AS rr
  ON rr.s = d.source_work_item_id AND rr.t = d.target_work_item_id AND rr.rt = d.relationship_type`
