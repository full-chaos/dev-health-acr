package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// membershipPageRow is one scanned row of the membership statement, rendered
// whole, with the keyset position it hands the next page.
type membershipPageRow struct {
	text  string
	since time.Time
	after string
	krc   uint64
	dup   uint8
	bad   uint8
}

func readMembershipPage(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding, ingest bool) []membershipPageRow {
	t.Helper()
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		t.Fatalf("run statement: %v", err)
	}
	defer rows.Close()
	var out []membershipPageRow
	for rows.Next() {
		var subjectKind, repoID, subjectID, repoSlug, eventID, source, provider, projectID, resolvedProjectID string
		var observedAt, validToValue, ingestAt time.Time
		var keyResolutionCount uint64
		var validToPresent, isMalformed, isDuplicateAdd uint8
		dest := []any{&subjectKind, &repoID, &subjectID, &repoSlug, &observedAt, &eventID, &source, &provider, &projectID, &resolvedProjectID, &keyResolutionCount, &validToPresent, &validToValue, &isMalformed, &isDuplicateAdd}
		if ingest {
			dest = append(dest, &ingestAt)
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		position := observedAt.UTC()
		if ingest {
			position = ingestAt.UTC()
		}
		out = append(out, membershipPageRow{
			text: fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%d|%d|%s|%d|%d|%s", subjectKind, repoID, subjectID, repoSlug, observedAt.UTC().Format(time.RFC3339Nano), eventID, source, provider, projectID, resolvedProjectID,
				keyResolutionCount, validToPresent, validToValue.UTC().Format(time.RFC3339Nano), isMalformed, isDuplicateAdd, ingestAt.UTC().Format(time.RFC3339Nano)),
			since: position,
			after: identity.JoinSegments(subjectKind, repoID, subjectID, provider, projectID, eventID),
			krc:   keyResolutionCount, dup: isDuplicateAdd, bad: isMalformed,
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedMembershipResolutionEdges adds the project-resolution cases the
// changed-subjects store lacks: an ambiguous key, a key equal to its own id,
// an unresolved id, the same id under another provider, a duplicate add and a
// dangling remove. Several share one ingest stamp so the row key decides
// their order.
func seedMembershipResolutionEdges(t *testing.T, seed changedSeeder) {
	t.Helper()
	at := time.Date(2026, 9, 1, 0, 3, 0, 0, time.UTC)
	for _, p := range []struct{ id, key, provider string }{
		{"AMB-A", "AMB", "linear"}, {"AMB-B", "AMB", "linear"}, {"SAME", "SAME", "linear"}, {changedProjectID(0), "", "github"},
	} {
		seed("edge project", `INSERT INTO projects (id, org_id, name, project_key, provider, state, url, is_active, updated_at) VALUES (?, ?, ?, ?, ?, 'backlog', '', 1, ?)`,
			p.id, changedOrg, p.id, p.key, p.provider, at)
	}
	for i, tr := range []struct{ subject, from, to string }{
		{"linear:E-amb", "", "AMB"},
		{"linear:E-same", "", "SAME"},
		{"linear:E-ghost", "", "GHOST"},
		{"linear:E-dup", "", changedProjectID(1)},
		{"linear:E-dup", "", changedProjectID(1)},
		{"linear:E-dangling", changedProjectID(2), ""},
	} {
		seed("edge transition", `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at)
VALUES (?, NULL, toUUID(?), 'work_item', ?, 'linear', ?, ?, '', '', '', ?, ?, ?, ?)`,
			changedOrg, changedRepo, tr.subject, tr.from, tr.to, at.Add(time.Duration(i)*time.Second), at, fmt.Sprintf("edge-%d", i), at)
	}
}

// TestLiveMembershipPageEqualsTheOracleOnEveryCursor walks the membership
// statement page by page and, at every cursor the walk reaches, runs the
// oracle (the statement as it was before the page was cut ahead of the
// project resolution) with the same bindings: same rows, same columns, same
// order. Ingest and legacy cursor spaces, with and without the
// changed-subject pruning. The edge count over the whole row source is
// compared too.
func TestLiveMembershipPageEqualsTheOracleOnEveryCursor(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v", err)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	seed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	seedChangedSubjectsStore(t, seed, 60)
	seedMembershipResolutionEdges(t, seed)

	const limit = 7
	for _, ingest := range []bool{true, false} {
		for _, pruned := range []bool{true, false} {
			t.Run(fmt.Sprintf("ingest=%v/pruned=%v", ingest, pruned), func(t *testing.T) {
				var since time.Time
				after := ""
				pages, rowsSeen := 0, 0
				var ambiguous, unresolved, duplicate, malformed int
				for {
					if pages > 1000 {
						t.Fatal("the walk did not end")
					}
					current, bindings := devhealthsource.MembershipStatementForTest(changedOrg, since, after, limit, ingest, pruned)
					legacy, legacyBindings := devhealthsource.LegacyMembershipStatementForTest(changedOrg, since, after, limit, ingest, pruned)
					if current == legacy {
						t.Fatal("the oracle and the producer render the same text: the cell compares nothing")
					}
					got := readMembershipPage(t, ctx, query, current, bindings, ingest)
					want := readMembershipPage(t, ctx, query, legacy, legacyBindings, ingest)
					if len(got) != len(want) {
						t.Fatalf("page %d (since=%v after=%q): %d rows, oracle %d", pages, since, after, len(got), len(want))
					}
					for i := range want {
						if got[i].text != want[i].text {
							t.Fatalf("page %d row %d (since=%v after=%q):\ngot    %s\noracle %s", pages, i, since, after, got[i].text, want[i].text)
						}
					}
					pages++
					kept := got
					if len(kept) > limit {
						kept = kept[:limit]
					}
					for _, r := range kept {
						rowsSeen++
						switch {
						case r.krc == 0:
							unresolved++
						case r.krc > 1:
							ambiguous++
						}
						if r.dup != 0 {
							duplicate++
						}
						if r.bad != 0 {
							malformed++
						}
					}
					if len(got) <= limit {
						break
					}
					last := got[limit-1]
					since, after = last.since, last.after
				}
				if pages < 10 || ambiguous == 0 || unresolved == 0 || duplicate == 0 || malformed == 0 {
					t.Fatalf("non-vacuity: pages=%d rows=%d ambiguous=%d unresolved=%d duplicate=%d malformed=%d -- every case must be walked across many pages", pages, rowsSeen, ambiguous, unresolved, duplicate, malformed)
				}
				t.Logf("%d pages, %d rows equal to the oracle (ambiguous %d, unresolved %d, duplicate %d, malformed %d)", pages, rowsSeen, ambiguous, unresolved, duplicate, malformed)
			})
		}
		t.Run(fmt.Sprintf("ingest=%v/edge count", ingest), func(t *testing.T) {
			current, legacy := devhealthsource.MembershipEdgeCountSQLForTest(ingest)
			bindings := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: changedOrg}}
			got, want := edgeCountRows(t, ctx, query, current, bindings), edgeCountRows(t, ctx, query, legacy, bindings)
			if len(want) == 0 || strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("edge count differs from the oracle:\ngot    %v\noracle %v", got, want)
			}
		})
	}
}

func edgeCountRows(t *testing.T, ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding) []string {
	t.Helper()
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		t.Fatalf("run edge count: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var source string
		var count uint64
		if err := rows.Scan(&source, &count); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s=%d", source, count))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}
