package devhealthfacts_test

// A team with no work unit in the window is one of three states: it owns no
// repository, it has pull requests and none has an admitted link to tracked
// work, or it is quiet / not yet computed. The middle one is a real state (an
// organization can hold such a team for months) and gets its own reason.
// EXECUTED against a real ClickHouse.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type prLinkSeed struct {
	t      *testing.T
	ctx    context.Context
	direct clickhousedriver.Conn
	org    string
	at     time.Time
}

func newPRLinkSeed(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, org string) prLinkSeed {
	t.Helper()
	createCHAOS5930Tables(t, ctx, direct)
	for _, statement := range devhealthschema.DDL("git_pull_requests", "work_graph_issue_pr") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	return prLinkSeed{t: t, ctx: ctx, direct: direct, org: org, at: ts(2026, 9, 18, 0, 0, 0)}
}

func (s prLinkSeed) exec(statement string, args ...any) {
	s.t.Helper()
	if err := s.direct.Exec(s.ctx, statement, args...); err != nil {
		s.t.Fatalf("seed: %v\n%s", err, statement)
	}
}

func (s prLinkSeed) team(team, repo string) {
	s.exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(repo), s.org, "acme/"+repo, "github", s.at)
	s.exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.org, "github", team, repoUUID(repo), "acme/"+repo, "exact", "native", uint8(1), uint16(100), int32(0), s.at, nil, s.at)
}

func (s prLinkSeed) pullRequest(repo string, number uint32, created time.Time, merged any) {
	s.exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,?,NULL,?,?)`,
		repoUUID(repo), s.org, number, "pr", "open", s.at, created, merged, "feature", "")
}

func (s prLinkSeed) workItem(id, typ string) {
	s.exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, '', 'linear', '', NULL, ?)`,
		id, repoUUID("repo-any"), s.org, "title "+id, typ, "open", s.at, s.at, s.at)
}

func (s prLinkSeed) link(repo, workItem string, number uint32, provenance string) {
	s.exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoUUID(repo), workItem, number, float32(1), provenance, "", s.at, s.org)
}

func TestTeamWithPullRequestsAndNoAdmittedLinkGetsItsOwnReasonAtTheReadFactsBoundary(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	const orgID = "org-pr-link-state"
	s := newPRLinkSeed(t, ctx, direct, orgID)
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 25, 0, 0, 0)
	inWindow := ts(2026, 9, 10, 0, 0, 0)
	beforeWindow := ts(2026, 7, 1, 0, 0, 0)

	s.workItem("issue-1", "issue")
	s.workItem("pr-typed", "pr")

	s.team("team-nolink", "repo-nolink")
	for n := uint32(1); n <= 3; n++ {
		s.pullRequest("repo-nolink", n, inWindow, nil)
	}
	s.team("team-partial", "repo-partial")
	s.pullRequest("repo-partial", 1, inWindow, nil)
	s.pullRequest("repo-partial", 2, inWindow, nil)
	s.link("repo-partial", "issue-1", 1, "native")
	s.team("team-quiet", "repo-quiet")
	s.team("team-oldpr", "repo-oldpr")
	s.pullRequest("repo-oldpr", 1, beforeWindow, ts(2026, 7, 5, 0, 0, 0))
	s.team("team-prtyped", "repo-prtyped")
	s.pullRequest("repo-prtyped", 1, inWindow, nil)
	s.link("repo-prtyped", "pr-typed", 1, "native")
	s.team("team-badtier", "repo-badtier")
	s.pullRequest("repo-badtier", 1, inWindow, nil)
	s.link("repo-badtier", "issue-1", 1, "unknown_tier")
	s.team("team-open", "repo-open")
	s.pullRequest("repo-open", 1, beforeWindow, nil)
	s.team("team-staleonly", "repo-staleonly")
	s.link("repo-staleonly", "issue-1", 99, "native")

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	reader := directread.NewFactsReader(directread.NewSubjectGate(admitAllGraph{}, nil), directread.NewFactReader(seam), nil)
	teams := []string{"team-nolink", "team-partial", "team-quiet", "team-oldpr", "team-prtyped", "team-badtier", "team-open", "team-staleonly"}
	requested := make([]directread.RequestSubject, 0, len(teams))
	for _, team := range teams {
		requested = append(requested, directread.RequestSubject{Kind: "team", CanonicalID: teamSubject(team).CanonicalID})
	}
	response, err := reader.Read(observability.WithRequestID(ctx, "req_00000000000000000000000000009602"),
		storage.Principal{OrgID: orgID, Subject: "t9602", CredentialID: "t9602"}, directread.FactsRequest{
			Kinds: []string{"investment"}, Subjects: requested,
			Window: &directread.RequestWindow{Mode: directread.WindowRange, Start: &start, End: &end},
		})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	if len(response.Facts) != 0 {
		t.Fatalf("no team has a work unit, so no fact is served: %+v", response.Facts)
	}
	const token, old = "investment_team_prs_without_linked_work", "investment_team_no_unit_in_window"
	want := map[string]struct {
		token, count string
	}{
		"team-nolink":    {token, "3 pull request"},
		"team-prtyped":   {token, "1 pull request"},
		"team-badtier":   {token, "1 pull request"},
		"team-partial":   {old, ""},
		"team-quiet":     {old, ""},
		"team-oldpr":     {old, ""},
		"team-open":      {token, "1 pull request"},
		"team-staleonly": {old, ""},
	}
	reasons := map[string]string{}
	for _, row := range response.Coverage {
		reasons[row.Subject.CanonicalID] = row.Reason
	}
	for _, team := range teams {
		reason := reasons[teamSubject(team).CanonicalID]
		w := want[team]
		other := old
		if w.token == old {
			other = token
		}
		// The per-team reason of the old state is its sentence; the token name
		// is in the request note.
		if w.token == old {
			if !strings.Contains(reason, "none of them has persisted work unit effort") || strings.Contains(reason, token) {
				t.Errorf("%s coverage reason = %q, want the no-unit sentence and not %s", team, reason, token)
			}
			continue
		}
		if !strings.Contains(reason, w.token) || strings.Contains(reason, other) || (w.count != "" && !strings.Contains(reason, w.count)) {
			t.Errorf("%s coverage reason = %q, want %s (%s) and not %s", team, reason, w.token, w.count, other)
		}
	}
	for _, team := range []string{"team-nolink"} {
		reason := reasons[teamSubject(team).CanonicalID]
		for _, claim := range []string{"no pull request in the window has an admitted link to tracked work", "work_graph_issue_pr"} {
			if !strings.Contains(reason, claim) {
				t.Errorf("%s reason = %q, missing the stated rule %q", team, reason, claim)
			}
		}
	}
}

// The pull request link state is one bounded statement: its reads follow the
// requested repositories, not the organization. The row reads at several
// organization sizes, the lowest of five warm reads each, must not grow, and
// the highest peak of those reads stays under the ceiling the package other rows use.
func TestTeamPullRequestLinkStateCostIsFlatInTheOrganizationOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	const orgID = "org-pr-link-cost"
	s := newPRLinkSeed(t, ctx, direct, orgID)
	inWindow := ts(2026, 9, 10, 0, 0, 0)
	s.workItem("issue-1", "issue")
	s.team("team-cost", "repo-cost")
	for n := uint32(1); n <= 20; n++ {
		s.pullRequest("repo-cost", n, inWindow, nil)
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 25, 0, 0, 0)
	measure := func() (lowRows, lowPeak, highPeak uint64) {
		t.Helper()
		first := true
		for run := 0; run < 5; run++ {
			mark := serverNowMicros(t, ctx, direct)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
				Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-cost")},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			if served := contextfabric.CombineRequestNote(result.Reason, result.RequestNote); !strings.Contains(served, "investment_team_prs_without_linked_work") || !strings.Contains(served, "20 pull request") {
				t.Fatalf("reason = %q, want the new token with 20 pull requests", served)
			}
			if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
				t.Fatalf("flush logs: %v", err)
			}
			var rows, memory uint64
			if err := direct.QueryRow(ctx, `SELECT max(read_rows), max(memory_usage) FROM system.query_log
WHERE `+queryLogSinceMark+` AND type = 'QueryFinish' AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.', 'git_pull_requests')) AND position(query, 'toUUIDOrZero') > 0`, mark).Scan(&rows, &memory); err != nil {
				t.Fatalf("read query_log: %v", err)
			}
			if rows == 0 {
				t.Fatalf("measurement did not happen: no logged link state statement")
			}
			if first || rows < lowRows {
				lowRows = rows
			}
			if first || memory < lowPeak {
				lowPeak = memory
			}
			if memory > highPeak {
				highPeak = memory
			}
			first = false
		}
		return lowRows, lowPeak, highPeak
	}
	baseRows, baseLowPeak, baseHighPeak := measure()
	const ceiling = 64 << 20
	for _, step := range []int{50_000, 150_000} {
		s.exec(fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT toUUID(concat('00000000-0000-4000-8000-', leftPad(toString(number %% 400), 12, '0'))), '%s', toUInt32(number + 100000), 'pr', 'open', now(), now() - INTERVAL 20 DAY, NULL, NULL, 'feature', '' FROM numbers(%d)`, orgID, step))
		s.exec(fmt.Sprintf(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT toUUID(concat('00000000-0000-4000-8000-', leftPad(toString(number %% 400), 12, '0'))), concat('other-', toString(number)), toUInt32(number + 100000), 1, 'native', '', now(), '%s' FROM numbers(%d)`, orgID, step))
		s.exec(fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('other-', toString(number)), toUUID(concat('00000000-0000-4000-8000-', leftPad(toString(number %% 400), 12, '0'))), '%s', 'title', 'issue', 'open', '', now(), now(), '', 'linear', '', NULL, now() FROM numbers(%d)`, orgID, step))
		rows, lowPeak, highPeak := measure()
		t.Logf("unrelated rows added %d: read_rows lowest=%d (base %d, bound %d); peak lowest=%d highest=%d (base lowest %d highest %d, ceiling %d)", step, rows, baseRows, baseRows+baseRows/10+64, lowPeak, highPeak, baseLowPeak, baseHighPeak, ceiling)
		if rows > baseRows+baseRows/10+64 {
			t.Errorf("read_rows %d with %d unrelated pull requests and links, base %d: the read follows the organization", rows, step, baseRows)
		}
		if highPeak > ceiling {
			t.Errorf("highest peak memory %d bytes with %d unrelated rows, over the %d-byte ceiling", highPeak, step, ceiling)
		}
	}
}
