package devhealthfacts_test

// CHAOS-5751: the repository selector must be evaluated by the real
// work_items/repositories statement used by each content reader. A fake
// client can assert that a selector binding was emitted, but it never lets
// ClickHouse join current repository metadata, apply the grant/request
// intersection, or select the current ReplacingMergeTree version after a
// metadata update.

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	chaos5751LiveRepoA      = "ca198fbc-1945-3717-05d8-eb78866b4e90"
	chaos5751LiveRepoB      = "cb198fbc-1945-3717-05d8-eb78866b4e91"
	chaos5751LiveRepoC      = "cc198fbc-1945-3717-05d8-eb78866b4e92"
	chaos5751LiveOrphanRepo = "cd198fbc-1945-3717-05d8-eb78866b4e93"
	chaos5751LiveZeroRepo   = "00000000-0000-0000-0000-000000000000"
)

type chaos5751CountingClickHouseClient struct {
	inner      contextpacket.ClickHouseQueryClient
	calls      int
	statements []string
}

func (c *chaos5751CountingClickHouseClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.calls++
	c.statements = append(c.statements, statement)
	return c.inner.Query(ctx, statement, bindings)
}

// TestChaos5751WorkItemProvidersUseCurrentRepositoryMetadata runs all three
// content readers against one production-shaped ClickHouse schema. The
// assertions deliberately use the same work-item subjects for each arm, so
// a reader that silently drops its selector or performs a second metadata
// lookup cannot satisfy the result and query-count checks.
func TestChaos5751WorkItemProvidersUseCurrentRepositoryMetadata(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	seedChaos5751WorkItems(t, ctx, direct, orgID, at)

	subjects := []contextfabric.SubjectRef{
		workItemSubject(chaos5751LiveRepoA, "WI-A"),
		workItemSubject(chaos5751LiveRepoB, "WI-B-EARLY"),
		workItemSubject(chaos5751LiveRepoB, "WI-B-LATE"),
		workItemSubject(chaos5751LiveRepoC, "WI-C"),
		workItemSubject(chaos5751LiveZeroRepo, "WI-ZERO"),
		workItemSubject(chaos5751LiveOrphanRepo, "WI-ORPHAN"),
	}
	asOf := at.Add(time.Hour)
	completionAtAsOf := map[string]bool{
		workItemSubject(chaos5751LiveRepoA, "WI-A").CanonicalID:           false,
		workItemSubject(chaos5751LiveRepoB, "WI-B-EARLY").CanonicalID:     true,
		workItemSubject(chaos5751LiveRepoB, "WI-B-LATE").CanonicalID:      false,
		workItemSubject(chaos5751LiveRepoC, "WI-C").CanonicalID:           true,
		workItemSubject(chaos5751LiveZeroRepo, "WI-ZERO").CanonicalID:     true,
		workItemSubject(chaos5751LiveOrphanRepo, "WI-ORPHAN").CanonicalID: true,
	}

	readAll := func(t *testing.T, principal storage.Principal, requested []string) map[contextfabric.FactKind]map[string]bool {
		t.Helper()
		got := make(map[contextfabric.FactKind]map[string]bool, 3)
		for _, kind := range []contextfabric.FactKind{contextfabric.FactStatus, contextfabric.FactWork, contextfabric.FactActualCompletion} {
			client := &chaos5751CountingClickHouseClient{inner: query}
			provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
			timeContext := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
			if kind == contextfabric.FactActualCompletion {
				bound := asOf
				timeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &bound}
			}
			result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
				Time: timeContext, Kind: kind, Subjects: subjects,
				RequestedRepositoryScope: requested,
			})
			if err != nil {
				t.Fatalf("%s ReadFacts() error = %v", kind, err)
			}
			if client.calls != 1 {
				t.Fatalf("%s query calls = %d, want exactly one content query and no metadata lookup", kind, client.calls)
			}
			statement := client.statements[0]
			for _, fragment := range []string{
				"LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id",
				"authorized_repo_slugs",
				"max_rows_to_read = 8192",
				"max_memory_usage = 67108864",
				"max_threads = 1",
				"read_overflow_mode = 'throw'",
				"result_overflow_mode = 'throw'",
			} {
				if !strings.Contains(statement, fragment) {
					t.Fatalf("%s statement = %q, want %q", kind, statement, fragment)
				}
			}
			if len(requested) > 0 && !strings.Contains(statement, "requested_repo_slugs") {
				t.Fatalf("%s statement = %q, want the requested selector binding", kind, statement)
			}
			if len(requested) == 0 && strings.Contains(statement, "requested_repo_slugs") {
				t.Fatalf("%s statement = %q, want no requested selector for nil/empty ACR request", kind, statement)
			}
			if kind == contextfabric.FactActualCompletion && !strings.Contains(statement, "{time_end:DateTime64(6,'UTC')}") {
				t.Fatalf("%s statement = %q, want the active time bound", kind, statement)
			}
			ids := make(map[string]bool, len(result.Facts))
			for _, fact := range result.Facts {
				ids[fact.Subject.CanonicalID] = true
				if kind == contextfabric.FactActualCompletion {
					wantCompleted, ok := completionAtAsOf[fact.Subject.CanonicalID]
					if !ok {
						t.Fatalf("actual completion returned unexpected subject %q", fact.Subject.CanonicalID)
					}
					completed, ok := fact.Fields["completed"]
					if !ok || completed.Boolean == nil || *completed.Boolean != wantCompleted {
						t.Fatalf("actual completion for %q = %#v, want completed=%v at %v", fact.Subject.CanonicalID, fact.Fields, wantCompleted, asOf)
					}
				}
			}
			got[kind] = ids
		}
		return got
	}

	want := func(pairs ...[]string) map[string]bool {
		t.Helper()
		result := make(map[string]bool, len(pairs))
		for _, pair := range pairs {
			result[workItemSubject(pair[0], pair[1]).CanonicalID] = true
		}
		return result
	}
	assertAll := func(t *testing.T, got map[contextfabric.FactKind]map[string]bool, expected map[string]bool) {
		t.Helper()
		for kind, ids := range got {
			if len(ids) != len(expected) {
				t.Errorf("%s facts = %d, want %d: %#v", kind, len(ids), len(expected), ids)
			}
			for id := range expected {
				if !ids[id] {
					t.Errorf("%s missing authorized fact %q: %#v", kind, id, ids)
				}
			}
			for id := range ids {
				if !expected[id] {
					t.Errorf("%s returned unauthorized fact %q: %#v", kind, id, ids)
				}
			}
		}
	}

	t.Run("grant and request selectors are intersected", func(t *testing.T) {
		got := readAll(t,
			storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/alpha", "acme/beta"}},
			[]string{"acme/beta", "other/gamma"})
		assertAll(t, got, want([]string{chaos5751LiveRepoB, "WI-B-EARLY"}, []string{chaos5751LiveRepoB, "WI-B-LATE"}))
	})

	t.Run("the current request moves from A to B", func(t *testing.T) {
		principal := storage.Principal{OrgID: orgID}
		assertAll(t, readAll(t, principal, []string{"acme/alpha"}), want([]string{chaos5751LiveRepoA, "WI-A"}))
		assertAll(t, readAll(t, principal, []string{"acme/beta"}), want([]string{chaos5751LiveRepoB, "WI-B-EARLY"}, []string{chaos5751LiveRepoB, "WI-B-LATE"}))
	})

	t.Run("organization-wide absent and empty requests retain sentinel rows", func(t *testing.T) {
		principal := storage.Principal{OrgID: orgID}
		expected := want(
			[]string{chaos5751LiveRepoA, "WI-A"},
			[]string{chaos5751LiveRepoB, "WI-B-EARLY"},
			[]string{chaos5751LiveRepoB, "WI-B-LATE"},
			[]string{chaos5751LiveRepoC, "WI-C"},
			[]string{chaos5751LiveZeroRepo, "WI-ZERO"},
			[]string{chaos5751LiveOrphanRepo, "WI-ORPHAN"},
		)
		assertAll(t, readAll(t, principal, nil), expected)
		assertAll(t, readAll(t, principal, []string{}), expected)
	})

	t.Run("explicit requested wildcard requires same-org metadata", func(t *testing.T) {
		got := readAll(t, storage.Principal{OrgID: orgID}, []string{"*"})
		assertAll(t, got, want(
			[]string{chaos5751LiveRepoA, "WI-A"},
			[]string{chaos5751LiveRepoB, "WI-B-EARLY"},
			[]string{chaos5751LiveRepoB, "WI-B-LATE"},
			[]string{chaos5751LiveRepoC, "WI-C"},
		))
	})

	t.Run("same repository id follows metadata rename and deletion", func(t *testing.T) {
		// A later ReplacingMergeTree version changes the slug under the same
		// repository id. The subject key stays stable while authorization uses
		// the current metadata row in this statement.
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, chaos5751LiveRepoA, orgID, "renamed/alpha", "github", at.Add(time.Minute)); err != nil {
			t.Fatalf("rename repo: %v", err)
		}
		assertAll(t, readAll(t, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/alpha"}}, nil), map[string]bool{})
		assertAll(t, readAll(t, storage.Principal{OrgID: orgID, RepositoryScopes: []string{"renamed/alpha"}}, nil), want([]string{chaos5751LiveRepoA, "WI-A"}))

		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, chaos5751LiveRepoA, orgID, "", "github", at.Add(2*time.Minute)); err != nil {
			t.Fatalf("delete repo metadata: %v", err)
		}
		// A grant wildcard still admits work_items whose repo metadata is
		// absent; an explicit request wildcard requires a usable same-org row.
		allExpected := want(
			[]string{chaos5751LiveRepoA, "WI-A"},
			[]string{chaos5751LiveRepoB, "WI-B-EARLY"},
			[]string{chaos5751LiveRepoB, "WI-B-LATE"},
			[]string{chaos5751LiveRepoC, "WI-C"},
			[]string{chaos5751LiveZeroRepo, "WI-ZERO"},
			[]string{chaos5751LiveOrphanRepo, "WI-ORPHAN"},
		)
		assertAll(t, readAll(t, storage.Principal{OrgID: orgID}, nil), allExpected)
		assertAll(t, readAll(t, storage.Principal{OrgID: orgID}, []string{"*"}), want(
			[]string{chaos5751LiveRepoB, "WI-B-EARLY"},
			[]string{chaos5751LiveRepoB, "WI-B-LATE"},
			[]string{chaos5751LiveRepoC, "WI-C"},
		))
	})
}

// TestChaos5751SubsecondDeadlineUsesClientContextWithoutRounding proves the
// boundary the readers API can represent. A remaining deadline below one
// second must not be rounded up into a one-second SETTINGS ceiling, so each
// production content provider refuses before issuing a statement. The real
// client still receives the caller context, so a separately issued
// deliberately slow server query is canceled before it can finish.
func TestChaos5751SubsecondDeadlineUsesClientContextWithoutRounding(t *testing.T) {
	query, direct := sharedClickHouseFixture(t)
	orgID := sharedTestOrgID(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	seedChaos5751WorkItems(t, context.Background(), direct, orgID, at)

	client := &chaos5751CountingClickHouseClient{inner: query}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactStatus)
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	_, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     contextfabric.FactStatus,
		Subjects: []contextfabric.SubjectRef{workItemSubject(chaos5751LiveRepoA, "WI-A")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("status ReadFacts() error = %v, want SourceUnavailable FactReadFailure", err)
	}
	if client.calls != 0 {
		t.Fatalf("status query calls = %d, want zero because no positive whole-second server ceiling fits", client.calls)
	}
	t.Logf("sub-second production provider control: provider_query_started=%t refusal=%T", client.calls != 0, err)

	serverStatement := readers.WithSettings(
		"SELECT sleepEachRow(1) FROM numbers(3) SETTINGS max_block_size = 1",
		readers.Settings{MaxExecutionTimeSeconds: 1},
	)
	serverStarted := time.Now()
	serverRows, serverErr := query.Query(context.Background(), serverStatement, nil)
	if serverErr == nil {
		for serverRows.Next() {
		}
		serverErr = serverRows.Err()
		if closeErr := serverRows.Close(); closeErr != nil && serverErr == nil {
			serverErr = closeErr
		}
	}
	if serverErr == nil {
		t.Fatal("whole-second real-server query unexpectedly completed")
	}
	var serverException *clickhousedriver.Exception
	if !errors.As(serverErr, &serverException) {
		t.Fatalf("whole-second real-server query error = %v, want a native ClickHouse exception", serverErr)
	}
	if serverException.Code != 159 {
		t.Fatalf("whole-second real-server query native error code = %d, want 159 (TIMEOUT_EXCEEDED)", serverException.Code)
	}
	t.Logf("whole-second server deadline control: native_error_code=%d query_error=%v elapsed=%s", serverException.Code, serverErr, time.Since(serverStarted))

	probeCtx, probeCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer probeCancel()
	probeDeadline, deadlineSet := probeCtx.Deadline()
	if !deadlineSet {
		t.Fatal("sub-second probe context has no deadline")
	}
	started := time.Now()
	rows, err := query.Query(probeCtx, "SELECT sleepEachRow(1) FROM numbers(3) SETTINGS max_block_size = 1", nil)
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if err == nil {
		t.Fatal("sub-second real-client query unexpectedly completed")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("sub-second real-client query error = %v, want context.DeadlineExceeded or os.ErrDeadlineExceeded", err)
	}
	elapsed := time.Since(started)
	t.Logf("sub-second deadline control: query_error_type=%T query_error=%v elapsed=%s", err, err, elapsed)
	if elapsed < probeDeadline.Sub(started) {
		t.Fatalf("sub-second real-client query stopped after %s, before its context deadline (%s)", elapsed, probeDeadline.Sub(started))
	}
	if elapsed >= time.Second {
		t.Fatalf("sub-second real-client query took %s, want cancellation before one second", elapsed)
	}
}

// TestChaos5751WorkItemReadersReadK200WithIndependentPhysicalCeilings proves
// the fixed reader settings against the production tables and client. The
// first arm returns exactly the 200-row fact budget; the second scans the same
// 200-row source population while a repository selector admits only half of
// it. A WHERE predicate and a result LIMIT therefore cannot be mistaken for
// the separate max_rows_to_read ceiling.
func TestChaos5751WorkItemReadersReadK200WithIndependentPhysicalCeilings(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := sharedClickHouseFixture(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	const (
		repoA = "da198fbc-1945-3717-05d8-eb78866b4e90"
		repoB = "db198fbc-1945-3717-05d8-eb78866b4e91"
	)
	for _, repo := range []struct{ id, slug string }{
		{id: repoA, slug: "acme/k200-a"},
		{id: repoB, slug: "acme/k200-b"},
	} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo.id, orgID, repo.slug, "github", at); err != nil {
			t.Fatalf("seed repo %s: %v", repo.slug, err)
		}
	}

	const rowCount = 200
	subjects := make([]contextfabric.SubjectRef, 0, rowCount)
	placeholders := make([]string, 0, rowCount)
	args := make([]any, 0, rowCount*10)
	for i := 0; i < rowCount; i++ {
		id := "K-" + strconv.Itoa(i)
		repoID := repoA
		if i >= rowCount/2 {
			repoID = repoB
		}
		subjects = append(subjects, workItemSubject(repoID, id))
		placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
		args = append(args, id, repoID, orgID, "github", "K200 "+id, "open", at, at, at, at)
	}
	statement := `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, last_synced) VALUES ` + strings.Join(placeholders, ", ")
	if err := direct.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("seed %d work items: %v", rowCount, err)
	}

	read := func(t *testing.T, requested []string, want int) {
		t.Helper()
		for _, kind := range []contextfabric.FactKind{contextfabric.FactStatus, contextfabric.FactWork, contextfabric.FactActualCompletion} {
			client := &chaos5751CountingClickHouseClient{inner: query}
			provider := findProvider(t, devhealthfacts.NewProviders(client), kind)
			result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: kind,
				Subjects: subjects, RequestedRepositoryScope: requested,
			})
			if err != nil {
				t.Fatalf("%s ReadFacts() error = %v", kind, err)
			}
			if len(result.Facts) != want {
				t.Fatalf("%s facts = %d, want %d from %d production work_items rows", kind, len(result.Facts), want, rowCount)
			}
			if result.Truncated {
				t.Fatalf("%s Truncated = true for %d admitted rows under the 201-row probe", kind, want)
			}
			if client.calls != 1 {
				t.Fatalf("%s query calls = %d, want exactly one content query", kind, client.calls)
			}
			statement := client.statements[0]
			for _, fragment := range []string{
				"max_rows_to_read = 8192",
				"read_overflow_mode = 'throw'",
				"max_memory_usage = 67108864",
				"max_threads = 1",
				"max_result_rows = 201",
				"result_overflow_mode = 'throw'",
			} {
				if !strings.Contains(statement, fragment) {
					t.Fatalf("%s statement = %q, want %q", kind, statement, fragment)
				}
			}
		}
	}

	t.Run("exactly K200 rows are served without truncation", func(t *testing.T) {
		read(t, nil, rowCount)
	})
	t.Run("the same K200 source rows can be narrowed independently", func(t *testing.T) {
		read(t, []string{"acme/k200-a"}, rowCount/2)
	})
	t.Run("a result limit throws a native error for every reader", func(t *testing.T) {
		scope := readers.AuthorizationScope{
			RepositorySelectors: &readers.RepositorySelectorScope{
				Granted: readers.RepositorySelectorSet{All: true},
			},
		}
		settings := readers.Settings{MaxResultRows: 1}
		ids := chaos5751SubjectIDs(subjects)
		for _, kind := range []contextfabric.FactKind{contextfabric.FactStatus, contextfabric.FactWork, contextfabric.FactActualCompletion} {
			var err error
			switch kind {
			case contextfabric.FactStatus:
				_, err = readers.ReadWorkItemStatusWithScopeAndRowLimit(ctx, query, orgID, ids, scope, settings, rowCount+1)
			case contextfabric.FactWork:
				_, err = readers.ReadWorkItemTitleWithScopeAndRowLimit(ctx, query, orgID, ids, scope, settings, rowCount+1)
			case contextfabric.FactActualCompletion:
				_, err = readers.ReadWorkItemCompletionWithScopeAndRowLimit(ctx, query, orgID, ids, readers.TimeBound{}, scope, settings, rowCount+1)
			}
			if err == nil {
				t.Errorf("%s returned no error with max_result_rows=1 and %d rows", kind, rowCount)
				continue
			}
			var serverError *clickhousedriver.Exception
			if !errors.As(err, &serverError) {
				t.Errorf("%s error = %v, want a native ClickHouse exception", kind, err)
				continue
			}
			if serverError.Code != 396 {
				t.Errorf("%s native error code = %d, want 396 (TOO_MANY_ROWS_OR_BYTES)", kind, serverError.Code)
			}
		}
	})
}

func chaos5751SubjectIDs(subjects []contextfabric.SubjectRef) []string {
	ids := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		const prefix = "work_item.v2:"
		ids = append(ids, strings.TrimPrefix(subject.CanonicalID, prefix))
	}
	return ids
}

func seedChaos5751WorkItems(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}, orgID string, at time.Time) {
	t.Helper()
	for _, repo := range []struct {
		id   string
		slug string
	}{
		{chaos5751LiveRepoA, "acme/alpha"},
		{chaos5751LiveRepoB, "acme/beta"},
		{chaos5751LiveRepoC, "other/gamma"},
	} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repo.id, orgID, repo.slug, "github", at); err != nil {
			t.Fatalf("seed repo %s: %v", repo.slug, err)
		}
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (repo_id, work_item_id, org_id, title, status, created_at, updated_at, completed_at, last_synced) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chaos5751LiveRepoA, "WI-A", orgID, "A", "open", at, at, at.Add(2*time.Hour), at,
		chaos5751LiveRepoB, "WI-B-EARLY", orgID, "B early", "open", at, at, at.Add(30*time.Minute), at,
		chaos5751LiveRepoB, "WI-B-LATE", orgID, "B late", "open", at, at, at.Add(2*time.Hour), at,
		chaos5751LiveRepoC, "WI-C", orgID, "C", "open", at, at, at.Add(30*time.Minute), at,
		chaos5751LiveZeroRepo, "WI-ZERO", orgID, "zero", "open", at, at, at.Add(30*time.Minute), at,
		chaos5751LiveOrphanRepo, "WI-ORPHAN", orgID, "orphan", "open", at, at, at.Add(30*time.Minute), at,
	); err != nil {
		t.Fatalf("seed work items: %v", err)
	}
}
