package devhealthfacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// TestChaos5405_TheProjectionMaskAndOrderAreOBSERVED replaces the substring
// guards that stood in for them (codex r3 P3).
//
// The old guards read the statement with strings.Contains, and a statement
// that emits every identity column BARE while parking the masked forms in an
// SQL comment satisfied all of them:
//
//	against a statement that masks NOTHING and orders by repo_id, the existing
//	guards report masked=5/5 order=true limit=true
//
// A guard that inspects text instead of behaviour is a finding by this repo's
// own rule, and the file those guards lived in argued at length that it was an
// exception. It was not. So the statement is EXECUTED here against a real
// ClickHouse and the assertions read the RETURNED ROWS -- a comment cannot
// satisfy a row.
//
// This is the only venue where these two properties are decidable at all. A
// denied row is dropped by the Go-side gate either way, so nothing downstream
// can tell a masked projection from an unmasked one; what changes is whether
// an unauthorized work item's identity crossed the database boundary, which is
// the half of the gate that matters exactly when the Go half is what broke.
func TestChaos5405_TheProjectionMaskAndOrderAreOBSERVED(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Second)
	// 3 repo-backed + 1 repo-less + 1 orphan + the ambiguous decoy.
	seedChaos5405Fixture(t, ctx, direct, orgID, at, 3)
	_ = direct

	// A REPOSITORY-RESTRICTED principal, so the relation carries BOTH admitted
	// and denied rows. With an organization-wide principal every row is
	// authorized and the mask has nothing to mask -- the assertions below
	// would pass on an unmasked statement, which is the vacuity this setup
	// exists to avoid.
	rows, err := query.Query(ctx, devhealthfacts.ProjectWorkItemSelectionSQLForTest(200), []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "authorized_repository_slugs", Value: []string{chaos5405LiveRepoSlug}},
		{Name: "authorized_repository_owners", Value: []string{}},
		{Name: "project_ids", Value: []string{chaos5405LiveProjectProvider + ":" + chaos5405LiveProjectID}},
	})
	if err != nil {
		t.Fatalf("execute the selection: %v", err)
	}
	defer rows.Close()

	type row struct {
		repoID, workItemID, repoSlug, originID, attributionSource string
		authorized, repoLess                                      uint8
	}
	var got []row
	for rows.Next() {
		var r row
		var scoped, authorizedPop, repoLessPop, repoLessDenied, orphaned uint64
		if err := rows.Scan(&r.repoID, &r.workItemID, &r.repoSlug, &r.originID, &r.attributionSource,
			&r.authorized, &r.repoLess, &scoped, &authorizedPop, &repoLessPop, &repoLessDenied, &orphaned); err != nil {
			t.Fatalf("scan: %v -- the twelve-column shape %s and the scanner have drifted", err, devhealthfacts.WorkItemScopeSelectionColumnsForTest)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	// NON-VACUITY FIRST. Without both populations present, neither assertion
	// below is testing anything.
	admitted, denied := 0, 0
	for _, r := range got {
		if r.authorized == 1 {
			admitted++
		} else {
			denied++
		}
	}
	t.Logf("returned rows=%d admitted=%d denied=%d", len(got), admitted, denied)
	if admitted == 0 || denied == 0 {
		t.Fatalf("the page carries admitted=%d denied=%d -- this test needs BOTH, or the mask and the ordering have nothing to decide", admitted, denied)
	}

	// THE MASK, observed: a denied row carries no identity at all.
	for i, r := range got {
		if r.authorized == 1 {
			continue
		}
		for _, column := range []struct {
			name  string
			value string
		}{
			{"repo_id", r.repoID},
			{"work_item_id", r.workItemID},
			{"repo_slug", r.repoSlug},
			{"origin_id", r.originID},
			{"attribution_source", r.attributionSource},
		} {
			if column.value != "" {
				t.Errorf("row %d is DENIED and still carried %s=%q -- an unauthorized work item's identity crossed the database boundary", i, column.name, column.value)
			}
		}
	}

	// THE ORDERING, observed: every admitted row precedes every denied one, so
	// a bounded page is spent on rows that can be served.
	seenDenied := false
	for i, r := range got {
		if r.authorized != 1 {
			seenDenied = true
			continue
		}
		if seenDenied {
			t.Fatalf("row %d is ADMITTED and follows a denied row -- the page is not ordered admissible-first, so a bounded page can be filled with rows that carry nothing", i)
		}
	}

	// AND THE ADMITTED ROWS DO CARRY THEIR IDENTITY, which is what stops all
	// of the above from being satisfied by a statement that masks everything.
	for i, r := range got {
		if r.authorized != 1 {
			continue
		}
		if r.repoID == "" || r.workItemID == "" {
			t.Errorf("row %d is ADMITTED and carries no identity (repo_id=%q work_item_id=%q) -- the mask is applied to rows it must not touch", i, r.repoID, r.workItemID)
		}
	}
}
