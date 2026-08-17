package devhealthsource

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// identityUniverseKinds is the CHAOS-3884 Option C source-table coverage:
// the four registered entityTables query functions that ALREADY populate
// Aliases (repository: this ticket's Part A; project/team: native_key/
// project_key; work_item: ticketKeyAlias) -- the identical kind set
// graphrank.isAliasLookupScopedKind names as the counting scope. Sharing
// this list with that registry is enforced by a dedicated test
// (TestIdentityUniverseKindsMatchAliasLookupScope) rather than a compile-time
// coupling, since the two lists live in different packages by design
// (devhealthsource owns the SOURCE side, graphrank owns the RESOLUTION
// side) and must never silently drift apart.
var identityUniverseKinds = []entityTable{
	{name: "repos", query: queryRepositories},
	{name: "projects", query: queryProjects},
	{name: "teams", query: queryTeams},
	{name: "work_items", query: queryWorkItems},
}

// identityUniverseRowBudget bounds the per-KIND row count IdentityUniverse
// will accumulate before reporting incomplete for the WHOLE call (CHAOS-3884
// HIGH-3's per-term cap, recast for Option C's complete-enumeration shape:
// there are no per-term SQL queries here, so the analogous risk is "can
// this kind's population actually be read in full and held in Go memory",
// not "did a ranked search truncate a claimant"). 20,000 is generous for
// the small, per-org-enumerable repository/project/team populations the
// identity-fast-path's own soundness argument depends on, and still bounds
// a large org's work_item population to something Go can hold and iterate
// without concern.
const identityUniverseRowBudget = 20000

// identityUniversePageSize is the per-call SELECT LIMIT the exhausting
// loop below uses -- larger than snapshotPerQueryCap (150, tuned for
// ordinary incremental projection batches) since this is a ONE-SHOT
// complete read, not a paced batch sequence a caller polls repeatedly.
const identityUniversePageSize = 2000

// IdentityUniverse (CHAOS-3884, Option C) is the complete, keyed
// identity-claimant read graphrank.ResolveDeps.AliasLookup's own doc
// comment describes: every repository/project/team/work_item this
// organization has, enumerated in full by repeatedly draining each
// registered query function's OWN cursor-paginated SELECT (the identical,
// already-verified-live SQL the ordinary projection pipeline uses -- no
// new query is written for this path) until each reports !truncated, not a
// ranked/truncatable relevance search. Returns:
//   - rows: every entity-shaped candidate converted to graphrank.IdentityRow
//     (a relationship/episode/tombstone/progress-marker candidate, which
//     these four query functions can still emit alongside their entity --
//     e.g. work_items' BELONGS_TO_REPOSITORY edge -- is skipped; only
//     entity.Subject identity/alias data feeds identity matching).
//   - observedAt: the MAX entity.ObservedAt across every row read --
//     IdentityObservationTime's own per-call input.
//   - complete: false if ANY kind's accumulated row count would exceed
//     identityUniverseRowBudget -- the WHOLE call reports incomplete, not
//     just that one kind, mirroring HIGH-3's "no cross-term shared
//     budget, but a hit on ANY one disables the whole call" shape.
func IdentityUniverse(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string) (rows []graphrank.IdentityRow, observedAt time.Time, complete bool, err error) {
	complete = true
	for _, table := range identityUniverseKinds {
		kindRows, kindObservedAt, kindComplete, kindErr := fetchIdentityKind(ctx, client, orgID, table.query)
		if kindErr != nil {
			return nil, time.Time{}, false, kindErr
		}
		rows = append(rows, kindRows...)
		if kindObservedAt.After(observedAt) {
			observedAt = kindObservedAt
		}
		if !kindComplete {
			complete = false
		}
	}
	return rows, observedAt, complete, nil
}

// fetchIdentityKind drains ONE registered query function's cursor to
// exhaustion (or until identityUniverseRowBudget is exceeded), converting
// every entity-shaped candidate it returns into a graphrank.IdentityRow.
func fetchIdentityKind(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, query func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error)) ([]graphrank.IdentityRow, time.Time, bool, error) {
	var rows []graphrank.IdentityRow
	var observedAt time.Time
	cursor := cursorState{}
	for {
		page, truncated, err := query(ctx, client, orgID, cursor, identityUniversePageSize)
		if err != nil {
			return nil, time.Time{}, false, err
		}
		for _, c := range page {
			if c.entity == nil {
				continue // relationship/episode/tombstone/progress-marker candidate
			}
			if c.entity.ObservedAt.After(observedAt) {
				observedAt = c.entity.ObservedAt
			}
			rows = append(rows, graphrank.IdentityRow{
				Kind: c.entity.Subject.Kind, CanonicalID: c.entity.Subject.CanonicalID, Label: c.entity.Subject.Label,
				Aliases: c.entity.Aliases, ProviderAliases: c.entity.ProviderAliases, ObservedAt: c.entity.ObservedAt,
			})
		}
		if len(rows) > identityUniverseRowBudget {
			return rows, observedAt, false, nil
		}
		if !truncated || len(page) == 0 {
			return rows, observedAt, true, nil
		}
		last := page[len(page)-1]
		cursor = cursorState{Since: last.observedAt, After: last.sortKey}
	}
}
