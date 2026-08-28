package devhealthsource

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// identityUniverseKinds is the CHAOS-3884 Option C source-table coverage:
// the registered entityTables query functions that ALREADY populate Aliases
// (repository: this ticket's Part A; project/team: native_key/project_key)
// -- the identical kind set graphrank.isAliasLookupScopedKind names as the
// counting scope. Sharing this list with that registry is enforced by a
// dedicated test (TestIdentityUniverseCoversExactlyTheAliasLookupScopedKinds)
// rather than a compile-time coupling, since the two lists live in
// different packages by design (devhealthsource owns the SOURCE side,
// graphrank owns the RESOLUTION side) and must never silently drift apart.
//
// This reader (IdentityUniverse/fetchIdentityKind, below) lives IN
// devhealthsource, not in graphrank or falkorgraph, and that is not
// stylistic (adjustment 3, team-lead amendment 2026-08-17): the
// authorization builders each query function calls (projectAuthorizationScope,
// repoAuthorization) and their shared helpers (authorizationValue,
// scopeContainsAttr) are UNEXPORTED here and in their own packages -- "call
// the same function [the ordinary projection pipeline uses]" only works
// in-package. Nothing is exported to make this reader live elsewhere; it is
// a natural sibling of the projection queries reading the SAME rows.
//
// work_items is DELIBERATELY ABSENT for slice 1 (team-lead amendment,
// 2026-08-17, settled decision 2) -- queryWorkItems already populates
// Aliases (ticketKeyAlias) and could be re-added here in one line, but
// work_item rows dwarf repository/project/team combined (3288 vs. handfuls
// in the trial org) and would trip identityUniverseRowBudget's per-branch
// cap on any larger org, permanently disabling the identity fast path via
// aliasIdentityComplete=false. See graphrank.aliasLookupScopedKinds' own
// doc comment for the full residual argument (ticket-key terms essentially
// cannot collide with repository bare-name slugs).
// devhealthschema:not-a-production-replica this is a SUBSET of entityTables (tables.go), the same
// producer registry, reusing its own query functions verbatim -- it mirrors
// no column type, engine or sort key of its own, so it cannot drift from
// production the way a rival schema declaration would; devhealthschema
// remains the only physical source. No new ingest path or query is
// introduced here.
var identityUniverseKinds = []entityTable{
	{name: "repos", query: queryRepositories},
	{name: "projects", query: queryProjects},
	// CHAOS-4390: queryTeams now takes a *teamAuthorizationLedger; this
	// reader is not a projection run and has no per-organization ledger to
	// thread through (teamsQuery/queryTeams' own ledger.record is a nil-safe
	// no-op, matching ambiguityLedger/presenceTelemetryLedger's convention).
	{name: "teams", query: teamsQuery(nil)},
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
// comment describes: every repository/project/team this organization has
// (slice-1 counting scope -- see identityUniverseKinds' own doc comment for
// why work_item is deliberately absent), enumerated in full by repeatedly
// draining each registered query function's OWN cursor-paginated SELECT
// (the identical, already-verified-live SQL the ordinary projection
// pipeline uses -- no new query is written for this path) until each
// reports !truncated, not a ranked/truncatable relevance search. Returns:
//   - rows: every entity-shaped candidate converted to graphrank.IdentityRow
//     (a relationship/episode/tombstone/progress-marker candidate, which
//     these query functions can still emit alongside their entity, is
//     skipped; only entity.Subject identity/alias data feeds identity
//     matching).
//   - observedAt: the MAX entity.ObservedAt across every row read --
//     IdentityObservationTime's own per-call input.
//   - complete: false if ANY kind's accumulated row count would exceed
//     identityUniverseRowBudget -- PER BRANCH (one independent budget per
//     registered kind, fetchIdentityKind's own local `rows`), but a hit on
//     ANY ONE branch disables the WHOLE call, never just that branch,
//     mirroring HIGH-3's "no cross-term shared budget, but a hit on ANY one
//     disables the whole call" shape (team-lead amendment, 2026-08-17,
//     settled decision 2 -- pinned by
//     TestIdentityUniverse_AnyShortBranchPoisonsCompletenessGlobally).
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
		if len(page) == 0 {
			// Codex xhigh review (chaos-pivot-p1, first round), finding 6:
			// an empty page is only a legitimate "done" signal when the
			// adapter also says truncated=false. truncated=true with an
			// empty page is an inconsistent adapter response (it claims
			// more rows exist but returns none to advance the cursor from)
			// -- treat it as an incomplete read and fail closed, matching
			// this same read's err!=nil handling, rather than the
			// unconditional complete=true a bare len(page)==0 check would
			// have returned. Still must not index page[len(page)-1] below.
			return rows, observedAt, !truncated, nil
		}
		if !truncated {
			return rows, observedAt, true, nil
		}
		last := page[len(page)-1]
		cursor = cursorState{Since: last.observedAt, After: last.sortKey}
	}
}
