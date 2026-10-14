package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	pullRequestStatesMemoryOrg   = "5c1e8a37-94d2-4b60-8f13-7a2d0e6b9c41"
	pullRequestStatesOwnedPRs    = 400
	pullRequestStatesFillerSmall = 50_000
	pullRequestStatesFillerMore  = 150_000
	pullRequestStatesSlope       = 1.15
	// The bound is frozen at the measured read plus 25%: 27201 bytes at both
	// sizes on CI (400 owned pull requests, 50k and 200k organization ones).
	// The statements read the owned repositories' own pull requests, so the
	// bytes follow those and never the organization.
	pullRequestStatesReadBytesBound = 34_000
	pullRequestStatesWarmReads      = 3
)

// The pull_requests_by_state statements read the pull requests of the team's
// repositories only. Their read_bytes follow those pull requests, not the
// organization's other pull requests. Three warm reads per size: the slope
// compares the lowest read per size, the absolute bound holds the highest
// read, and a planted whole-organization read must break the slope.
func TestTeamPullRequestStatesReadFollowsTeamRepositoriesNotOrganizationSizeOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	org := pullRequestStatesMemoryOrg
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "git_pull_requests", "work_graph_issue_pr", "work_items",
		"projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	const teamID = "team-states"
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("states-owned"), org, "acme/states-owned", "github", at)
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID("states-filler"), org, "acme/states-filler", "github", at)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		org, "github", teamID, repoUUID("states-owned"), "acme/states-owned", "exact", "native", uint8(1), uint16(1), int32(1), at.Add(-24*time.Hour), nil, at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + 1), 'pr', if(number % 2 = 0, 'open', 'merged'), ?, ?, NULL, NULL, 'feat', '' FROM numbers(?)`,
		repoUUID("states-owned"), org, at, at, uint64(pullRequestStatesOwnedPRs))
	addFiller := func(offset, items int) {
		t.Helper()
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT ?, ?, toUInt32(number + ?), 'pr', 'open', ?, ?, NULL, NULL, 'feat', '' FROM numbers(?)`,
			repoUUID("states-filler"), org, uint64(offset), at, at, uint64(items))
	}
	addFiller(1, pullRequestStatesFillerSmall)

	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactPullRequests)
	measure := func() (lowest, highest uint64) {
		for read := 0; read < pullRequestStatesWarmReads+1; read++ {
			mark := serverNowMicros(t, ctx, direct)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org, RepositoryScopes: []string{"*"}}, contextfabric.FactQuery{
				Time: contractsv1TimeContext(ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 30, 23, 59, 59)), Kind: contextfabric.FactPullRequests,
				Subjects: []contextfabric.SubjectRef{teamSubject(teamID)},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
			}
			if read == 0 {
				continue // cold read
			}
			wantTableTotal(t, result, teamSubject(teamID))
			bytes := pullRequestStatesReadBytes(t, ctx, direct, mark)
			if bytes == 0 {
				t.Fatalf("read_bytes measurement did not happen: no pull_requests_by_state statement logged")
			}
			if lowest == 0 || bytes < lowest {
				lowest = bytes
			}
			if bytes > highest {
				highest = bytes
			}
		}
		return lowest, highest
	}
	smallLow, smallHigh := measure()
	addFiller(1+pullRequestStatesFillerSmall, pullRequestStatesFillerMore)
	largeLow, largeHigh := measure()
	t.Logf("pull_requests_by_state read_bytes: small lowest=%d highest=%d, large lowest=%d highest=%d, slope=%.2f, bound=%d",
		smallLow, smallHigh, largeLow, largeHigh, pullRequestStatesSlope, pullRequestStatesReadBytesBound)
	if float64(largeLow) > pullRequestStatesSlope*float64(smallLow) {
		t.Errorf("read_bytes grew with the organization's pull requests: lowest %d at the larger size, %d at the smaller, over the %.2fx slope", largeLow, smallLow, pullRequestStatesSlope)
	}
	if largeHigh > pullRequestStatesReadBytesBound {
		t.Errorf("highest warm read %d exceeds the frozen bound %d", largeHigh, pullRequestStatesReadBytesBound)
	}
	// Planted control: a state count over every pull request of the
	// organization is the defect the slope exists to catch; it must break it.
	var controlBytes uint64
	mark := serverNowMicros(t, ctx, direct)
	var n uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM git_pull_requests FINAL WHERE org_id = ? AND state = 'open'`, org).Scan(&n); err != nil {
		t.Fatalf("control read: %v", err)
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	if err := direct.QueryRow(ctx, `SELECT max(read_bytes) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select' AND position(query, 'git_pull_requests') > 0`, mark).Scan(&controlBytes); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	if float64(controlBytes) <= pullRequestStatesSlope*float64(smallLow) {
		t.Fatalf("planted whole-organization read did not break the slope (%d bytes, small lowest %d): the guard cannot fail", controlBytes, smallLow)
	}
}

func wantTableTotal(t *testing.T, result contextfabric.FactProviderResult, subject contextfabric.SubjectRef) {
	t.Helper()
	value, ok := factFor(t, result, subject).Fields["pull_requests_by_state"]
	if !ok || value.Table == nil {
		t.Fatalf("pull_requests_by_state table missing: %#v", value)
	}
	if err := value.Validate(); err != nil {
		t.Fatalf("pull_requests_by_state fails FactValue.Validate: %v", err)
	}
	counts := map[string]int64{}
	for _, row := range value.Rows {
		state, count := row.Fields["state"].String, row.Fields["pull_request_count"].Integer
		if state == nil || count == nil {
			t.Fatalf("row without state or count: %#v", row)
		}
		counts[*state] += *count
	}
	if counts["open"] != pullRequestStatesOwnedPRs/2 || counts["merged"] != pullRequestStatesOwnedPRs/2 || len(counts) != 2 {
		t.Fatalf("counts by state = %v, want %d open and %d merged", counts, pullRequestStatesOwnedPRs/2, pullRequestStatesOwnedPRs/2)
	}
}

func pullRequestStatesReadBytes(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, markMicros int64) uint64 {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	var peak uint64
	if err := direct.QueryRow(ctx, `SELECT max(read_bytes) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND position(query, 'arraySort(groupArray((st, c)))') > 0`, markMicros).Scan(&peak); err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	return peak
}
