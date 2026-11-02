package devhealthsource

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// workItemKeyPrefixPredicate matches a work item whose key, the text after the
// first ':' of work_item_id (ticketKeyAlias), starts with prefix. prefix ends in
// "-", so OPS- never matches OPS2-1.
func workItemKeyPrefixPredicate(prefix string) CensusPredicate {
	return CensusPredicate{
		SQL:      "position(w.work_item_id, ':') > 0 AND startsWith(substring(w.work_item_id, position(w.work_item_id, ':') + 1), {census_key_prefix:String})",
		Bindings: []contextpacket.ClickHouseBinding{{Name: "census_key_prefix", Value: prefix}},
	}
}

// NewWorkItemKeyPrefixFunc is graphrank's key prefix read (CHAOS-12463): does
// one work item the caller can read have a key of this prefix? It reads under the
// grant bound on ctx, through the same link arm as a work item census, and reads
// at most one row.
func NewWorkItemKeyPrefixFunc(client contextpacket.ClickHouseQueryClient) graphrank.WorkItemKeyPrefixFunc {
	return func(ctx context.Context, orgID, prefix string) (graphrank.WorkItemKeyPrefixOutcome, error) {
		if client == nil {
			return graphrank.WorkItemKeyPrefixOutcome{}, fmt.Errorf("devhealthsource: key prefix read requires a ClickHouseQueryClient")
		}
		candidates := workItemKeyPrefixPredicate(prefix)
		predicate := candidates
		grant, grantBound := contextfabric.CensusGrantFrom(ctx)
		if grantBound {
			var applied bool
			predicate, applied = withWorkItemGrantAmong(orgID, candidates, grant, candidates)
			if !applied {
				return graphrank.WorkItemKeyPrefixOutcome{}, graphrank.ErrCensusGrantNotApplied
			}
		}
		bindings := append([]contextpacket.ClickHouseBinding{{Name: "census_org_id", Value: orgID}}, predicate.Bindings...)
		statement := "SELECT toString(1) FROM work_items AS w FINAL WHERE w.org_id = {census_org_id:String} AND " + predicate.SQL + " LIMIT 1" + predicate.rowBound(" SETTINGS ")
		rows, err := runCensusRow(ctx, client, statement, bindings)
		if err != nil {
			return graphrank.WorkItemKeyPrefixOutcome{}, err
		}
		return graphrank.WorkItemKeyPrefixOutcome{Seen: len(rows) > 0, GrantApplied: grantBound}, nil
	}
}
