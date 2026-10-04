package devhealthfacts_test

// A read that matches more rows than the row limit must serve the same rows on
// every call, whatever order the store holds them in. The library readers cut
// at the limit, so without a total order on the cut ClickHouse may return a
// different subset per call and the answer, and the client synthesis digest,
// differ for the same question.
//
// Two orgs hold the same 260 work items written as three parts each, in
// opposite part orders. The status read asks for all 260, so it reaches the
// limit. Both orgs must serve the same facts, and every served fact must come
// from the rows a total order keeps (the first limit+1 by repo and item id,
// the rows the reader fetches before this package drops the probe row).

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	rowLimitOrderItems = 260
	rowLimitOrderRepos = 5
)

func rowLimitOrderRepo(n int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", n%rowLimitOrderRepos)
}

func rowLimitOrderItem(n int) string { return fmt.Sprintf("WI-%03d", n) }

func TestRowLimitReadServesTheSameRowsForAnyStoreOrder(t *testing.T) {
	ctx := context.Background()
	baseOrg := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)

	seed := func(org string, spans [][3]int) {
		t.Helper()
		for _, span := range spans {
			direction := "ASC"
			if span[2] == 1 {
				direction = "DESC"
			}
			statement := fmt.Sprintf(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, project_id, updated_at)
SELECT concat('WI-', leftPad(toString(number), 3, '0')), concat('00000000-0000-0000-0000-', leftPad(toString(number %% %d), 12, '0')), '%s', 't', 'open', '', '', '', now()
FROM numbers(%d, %d)
ORDER BY number %s`, rowLimitOrderRepos, org, span[0], span[1], direction)
			if err := direct.Exec(ctx, statement); err != nil {
				t.Fatalf("seed %s %v: %v", org, span, err)
			}
		}
	}
	orgAscending, orgDescending := baseOrg+"-asc", baseOrg+"-desc"
	seed(orgAscending, [][3]int{{0, 100, 0}, {100, 100, 0}, {200, 60, 0}})
	seed(orgDescending, [][3]int{{200, 60, 1}, {0, 100, 1}, {100, 100, 1}})

	subjects := make([]contextfabric.SubjectRef, 0, rowLimitOrderItems)
	keys := make([]string, 0, rowLimitOrderItems)
	canonicalByKey := make(map[string]string, rowLimitOrderItems)
	for n := 0; n < rowLimitOrderItems; n++ {
		subject := workItemSubject(rowLimitOrderRepo(n), rowLimitOrderItem(n))
		subjects = append(subjects, subject)
		key := rowLimitOrderRepo(n) + ":" + rowLimitOrderItem(n)
		keys = append(keys, key)
		canonicalByKey[key] = subject.CanonicalID
	}
	sort.Strings(keys)
	kept := make(map[string]bool, readers.ProbeRowLimit)
	for _, key := range keys[:readers.ProbeRowLimit] {
		kept[canonicalByKey[key]] = true
	}

	served := func(org string) []string {
		t.Helper()
		provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactStatus)
		result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: org}, contextfabric.FactQuery{
			Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind:     contextfabric.FactStatus,
			Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("%s: ReadFacts: %v", org, err)
		}
		if !result.Truncated {
			t.Fatalf("%s: Truncated = false for %d matching rows over a limit of %d; the read did not reach the limit, so the test measured nothing", org, rowLimitOrderItems, readers.DefaultRowLimit)
		}
		if len(result.Facts) != readers.DefaultRowLimit {
			t.Fatalf("%s: served %d facts, want %d", org, len(result.Facts), readers.DefaultRowLimit)
		}
		ids := make([]string, 0, len(result.Facts))
		for _, fact := range result.Facts {
			ids = append(ids, fact.Subject.CanonicalID)
		}
		sort.Strings(ids)
		return ids
	}

	ascending, descending := served(orgAscending), served(orgDescending)
	for i := range ascending {
		if ascending[i] != descending[i] {
			t.Fatalf("the two store orders served different rows at position %d: %q vs %q", i, ascending[i], descending[i])
		}
		if !kept[ascending[i]] {
			t.Fatalf("served %q, which is outside the first %d rows by repo and item id", ascending[i], readers.ProbeRowLimit)
		}
	}
}
