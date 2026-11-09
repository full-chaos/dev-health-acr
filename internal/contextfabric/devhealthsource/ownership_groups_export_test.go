package devhealthsource

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// OwnershipCandidateForTest is one project -> team ownership candidate: its
// keyset place and what it writes (an edge, a tombstone, or nothing).
type OwnershipCandidateForTest struct {
	SortKey      string
	Position     time.Time
	Relationship *contractsv1.ContextFabricRelationshipProjection
	Tombstone    *contractsv1.ContextFabricProjectionTombstone
}

func ownershipCandidatesForTest(rows []candidate) []OwnershipCandidateForTest {
	out := make([]OwnershipCandidateForTest, 0, len(rows))
	for _, c := range rows {
		out = append(out, OwnershipCandidateForTest{SortKey: c.sortKey, Position: c.position(), Relationship: c.relationship, Tombstone: c.tombstone})
	}
	return out
}

// OwnershipPageForTest reads one page of the project -> team ownership
// producer, on the ingest cursor, after (since, after).
func OwnershipPageForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, since time.Time, after string, limit int) ([]OwnershipCandidateForTest, bool, error) {
	rows, more, err := queryProjectTeams(ctx, client, orgID, cursorState{Since: since, After: after}, limit, &ambiguityLedger{}, true)
	if err != nil {
		return nil, false, err
	}
	return ownershipCandidatesForTest(rows), more, nil
}

// OwnershipPageStatementForTest renders the ownership page statement and its
// bindings at a cursor.
func OwnershipPageStatementForTest(orgID string, since time.Time, after string, limit int) (string, []contextpacket.ClickHouseBinding) {
	cursor := cursorState{Since: since, After: after}
	return projectTeamsStatementFor(cursor, true), rowLimitBindings(orgID, cursor, limit)
}

// LegacyOwnershipCandidatesForTest walks every group of the organization
// through the per-organization group statement (the oracle), page by page.
func LegacyOwnershipCandidatesForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string) ([]OwnershipCandidateForTest, error) {
	var out []OwnershipCandidateForTest
	cursor := cursorState{}
	for {
		rows, more, err := queryProjectTeams(ctx, client, orgID, cursor, 997, &ambiguityLedger{}, true)
		if err != nil {
			return nil, err
		}
		out = append(out, ownershipCandidatesForTest(rows)...)
		if !more || len(rows) == 0 {
			return out, nil
		}
		last := rows[len(rows)-1]
		cursor = cursorState{Since: last.position(), After: last.sortKey}
	}
}

// OwnershipGroupsOracleSQLForTest selects every group of {org_id} from the
// per-organization group statement as (row_key, content), content in the
// same spelling OwnershipGroupsCurrentContentSQLForTest gives a stored group.
func OwnershipGroupsOracleSQLForTest() string {
	return `SELECT ` + projectTeamsRowKeyOf("g") + ` AS row_key, ` + ownershipGroupContentSQL("g") + ` AS content FROM (` + projectTeamsGroupSQL(true) + `) AS g`
}

// OwnershipGroupsCurrentContentSQLForTest selects every live stored group of
// {org_id} from project_team_ownership_groups_current as (row_key, content).
func OwnershipGroupsCurrentContentSQLForTest() string {
	return `SELECT g.row_key AS row_key, ` + ownershipGroupContentSQL("g") + ` AS content FROM project_team_ownership_groups_current AS g FINAL WHERE g.org_id = {org_id:String} AND g.is_deleted = 0`
}

func projectTeamsRowKeyOf(alias string) string {
	return rowKeySQL(alias+".provider", alias+".project_id", alias+".team_id", alias+".source_name")
}

func ownershipGroupContentSQL(alias string) string {
	return `toString(tuple(` + alias + `.project_id, ` + alias + `.team_id, ` + alias + `.source_name, ` + alias + `.provider, toUnixTimestamp64Milli(` + alias + `.first_valid_from), toUInt8(` + alias + `.latest_is_open), toUnixTimestamp64Milli(` + alias + `.latest_valid_to), toUnixTimestamp64Milli(` + alias + `.observed_at), toUInt8(` + alias + `.edge_suppressed), arraySort(` + alias + `.conflict_identities), toUInt8(` + alias + `.conflicting_identity_present)))`
}

// OwnershipCandidateCounts counts the edges and tombstones a walk leaves.
type OwnershipCandidateCounts struct{ Relationships, Tombstones int }
