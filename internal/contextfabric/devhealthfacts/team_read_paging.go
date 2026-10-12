package devhealthfacts

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// The runtime ClickHouse client runs max_result_rows = 1,000 with
// result_overflow_mode = throw, so one statement that returns more rows fails
// the read instead of truncating. Two reads of a team's repositories can
// reach that: the ownership-shaped reads (one row per team and repository,
// bound maxTeamOwnedRepositoryRows) are paged by a keyset cursor, and the
// per-repository reads over the owned key set are issued in key chunks.
const (
	// teamReadPageRows is the rows one ownership page consumes; the statement
	// asks for one more as the continuation probe. Both stay under the cap.
	teamReadPageRows = 500
	// teamRepoKeyChunk is the repository keys one per-repository statement
	// carries. Each key returns at most one row, so a chunk never exceeds
	// maxFactRowsPerQuery rows, the strictest per-statement cap in use (the
	// work-item reader settings run max_result_rows = maxFactRowsProbe).
	teamRepoKeyChunk = maxFactRowsPerQuery
)

// teamRepoCursor names the (team, repository) key a paged ownership read is
// ordered by: SQL expressions for the two grouped columns, and the ClickHouse
// type of the repository expression so the cursor compares in the column's
// own order (a UUID orders by value, not by its string form).
type teamRepoCursor struct {
	teamExpr string
	repoExpr string
	repoType string
}

// havingAfter is the keyset predicate of every page after the first. It
// belongs between GROUP BY and ORDER BY of the statement.
func (c teamRepoCursor) havingAfter() string {
	return "HAVING (" + c.teamExpr + ", " + c.repoExpr + ") > ({after_team:String}, {after_repo:" + c.repoType + "})"
}

// pagedTeamRepoRead runs one ownership-shaped read to completion in pages.
// build renders the statement for a page: having is empty on the first page
// and the cursor predicate afterwards, limit is the page probe size. run
// executes a rendered statement with the cursor bindings in extra and calls
// scan once per row; scan reports the row's (team id, repository key) so the
// next page can resume after it. The total across pages is bounded by
// maxTeamOwnedRepositoryRows: a larger set is still a loud named failure,
// because a rollup over a partial repository set would be a wrong total.
func pagedTeamRepoRead(
	cursor teamRepoCursor,
	build func(having string, limit int) string,
	run func(statement string, extra []readers.Binding, scan func(contextpacket.ClickHouseRowScanner) error) error,
	scan func(contextpacket.ClickHouseRowScanner) (teamID, repoKey string, err error),
) error {
	total := 0
	var afterTeam, afterRepo string
	first := true
	for {
		having := ""
		var extra []readers.Binding
		if !first {
			having = cursor.havingAfter()
			extra = []readers.Binding{{Name: "after_team", Value: afterTeam}, {Name: "after_repo", Value: afterRepo}}
		}
		got := 0
		err := run(build(having, teamReadPageRows+1), extra, func(row contextpacket.ClickHouseRowScanner) error {
			got++
			if got > teamReadPageRows {
				return nil
			}
			teamID, repoKey, err := scan(row)
			if err != nil {
				return err
			}
			afterTeam, afterRepo = teamID, repoKey
			return nil
		})
		if err != nil {
			return err
		}
		total += min(got, teamReadPageRows)
		if total > maxTeamOwnedRepositoryRows {
			return fmt.Errorf("%s", teamOwnedRepositoriesOverflowReason)
		}
		if got <= teamReadPageRows {
			return nil
		}
		if total == maxTeamOwnedRepositoryRows {
			return fmt.Errorf("%s", teamOwnedRepositoriesOverflowReason)
		}
		first = false
	}
}

// pagedOrgScopedTeamRepoRead is pagedTeamRepoRead over readers.QueryOrgScopedNamed
// with the read's own extra bindings carried on every page.
func pagedOrgScopedTeamRepoRead(
	ctx context.Context,
	client contextpacket.ClickHouseQueryClient,
	reader, orgID string,
	teamIDs []string,
	cursor teamRepoCursor,
	extra []readers.Binding,
	build func(having string, limit int) string,
	scan func(contextpacket.ClickHouseRowScanner) (teamID, repoKey string, err error),
) error {
	return pagedTeamRepoRead(cursor, build, func(statement string, page []readers.Binding, each func(contextpacket.ClickHouseRowScanner) error) error {
		bindings := append(append([]readers.Binding(nil), extra...), page...)
		return readers.QueryOrgScopedNamed(ctx, client, reader, statement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
			return each(row)
		}, bindings...)
	}, scan)
}

// forEachRepoKeyChunk calls fn with consecutive chunks of keys, each at most
// teamRepoKeyChunk long, in order. A per-repository read merges its chunks:
// the keys are disjoint, so no repository is read twice.
func forEachRepoKeyChunk(keys []string, fn func(chunk []string) error) error {
	for start := 0; start < len(keys); start += teamRepoKeyChunk {
		if err := fn(keys[start:min(start+teamRepoKeyChunk, len(keys))]); err != nil {
			return err
		}
	}
	return nil
}

// forEachTeamIDChunk calls fn with consecutive chunks of team ids, each at
// most teamRepoKeyChunk long, in order. A per-team grouped read emits at most
// one row per team, so a chunk never exceeds the work-item reader's row cap.
func forEachTeamIDChunk(teamIDs []string, fn func(chunk []string) error) error {
	for start := 0; start < len(teamIDs); start += teamRepoKeyChunk {
		if err := fn(teamIDs[start:min(start+teamRepoKeyChunk, len(teamIDs))]); err != nil {
			return err
		}
	}
	return nil
}
