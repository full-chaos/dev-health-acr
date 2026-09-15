package devhealthfacts

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestWorkItemMembershipS1AgainstActualDDL runs the new port against the
// production-shaped DDL and a real ClickHouse client. It exercises one
// atomic census over the view plus work_items/repos/projects, including the
// transition move-out rule, a denied masked row, and a future boundary.
func TestWorkItemMembershipS1AgainstActualDDL(t *testing.T) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"CLICKHOUSE_USER":     "acr",
				"CLICKHOUSE_PASSWORD": "acr",
				"CLICKHOUSE_DB":       "default",
			},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ClickHouse: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate ClickHouse: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("ClickHouse host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("ClickHouse port: %v", err)
	}
	address := net.JoinHostPort(host, port.Port())
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr:        []string{address},
		Auth:        clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open ClickHouse: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if err := direct.Ping(ctx); err == nil {
			break
		} else if time.Now().After(pingDeadline) {
			t.Fatalf("ping ClickHouse: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN:         "clickhouse://acr:acr@" + address + "/default",
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })

	for _, statement := range devhealthschema.DDL("repos", "work_items", "projects", "project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create fixture table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create membership view: %v", err)
	}

	const (
		orgID       = "chaos-5752-live"
		projectID   = "P1"
		otherID     = "P2"
		allowedRepo = "20000000-0000-4000-8000-000000000001"
		deniedRepo  = "20000000-0000-4000-8000-000000000002"
	)
	at := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	seed := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed fixture: %v", err)
		}
	}
	seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)`,
		allowedRepo, orgID, "acme/allowed", "linear", at,
		deniedRepo, orgID, "acme/denied", "linear", at)
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, orgID, "linear", "P1", "Project 1", uint8(1), "active", "https://linear.app/p1", at,
		otherID, orgID, "linear", "P2", "Project 2", uint8(1), "active", "https://linear.app/p2", at)
	seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"allowed", allowedRepo, orgID, "allowed", "open", "", at, "", "linear", "",
		"denied", deniedRepo, orgID, "denied", "open", "", at, "", "linear", "",
		"moved", allowedRepo, orgID, "moved", "open", "", at, "", "linear", "",
		"self", allowedRepo, orgID, "self", "open", "", at, "", "linear", "")
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, allowedRepo, "work_item", "allowed", "linear", "", projectID, "", "P1", "fixture", at.Add(time.Hour), at, "allowed-add",
		orgID, deniedRepo, "work_item", "denied", "linear", "", projectID, "", "P1", "fixture", at.Add(-time.Hour), at, "denied-add",
		orgID, allowedRepo, "work_item", "moved", "linear", "", projectID, "", "P1", "fixture", at, at, "moved-add",
		orgID, allowedRepo, "work_item", "self", "linear", projectID, projectID, "P1", "P1", "fixture", at.Add(-2*time.Hour), at, "self-touch")
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, allowedRepo, "work_item", "moved", "linear", projectID, otherID, "P1", "P2", "fixture", at.Add(time.Minute), at, "moved-out")

	gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
	if err != nil {
		t.Fatalf("create gate: %v", err)
	}
	telemetry := &workItemMembershipTelemetrySpy{}
	recordingClient := &workItemMembershipRecordingQueryClient{delegate: query}
	reader, err := NewWorkItemMembershipReader(recordingClient, WorkItemMembershipReaderOptions{
		Gate:      gate,
		Telemetry: telemetry,
	})
	if err != nil {
		t.Fatalf("create membership reader: %v", err)
	}
	request := contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", projectID),
		RequestedRepositoryScope: []string{"acme/allowed"},
		S1Instant:                at.Add(30 * time.Minute),
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lease, result, err := reader.BeginWorkItemMembership(deadlineCtx, storage.Principal{OrgID: orgID}, request)
	if err != nil || lease == nil {
		t.Fatalf("BeginWorkItemMembership lease=%v err=%v", lease, err)
	}
	defer lease.Release()
	if result.Census.State != contextfabric.WorkItemMembershipCensusExact || result.Census.AuthorizedPopulation != 2 || result.Census.DeniedPopulation != 1 || result.Census.FutureBoundaryCount != 1 || result.Census.TransitionAssertionCount != 3 {
		t.Fatalf("live census = %+v, want exact 2 authorized / 1 denied / 1 future boundary / 3 transition assertions", result.Census)
	}
	if len(result.Members) != 2 {
		t.Fatalf("live members = %#v, want the future and first-self transition members", result.Members)
	}
	memberIDs := map[string]bool{result.Members[0].WorkItemID: true, result.Members[1].WorkItemID: true}
	if !memberIDs["allowed"] || !memberIDs["self"] || memberIDs["denied"] || memberIDs["moved"] {
		t.Fatalf("live members = %#v, want allowed/self only (denied masked and moved out)", result.Members)
	}
	// S1's lease is held through the response. Release it here before starting
	// the independent floor probe; the test's deferred release remains
	// idempotent and protects the first path if an assertion fails above.
	lease.Release()
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"P-empty", orgID, "linear", "", "Empty Project", uint8(1), "active", "", at)
	emptyCtx, cancelEmpty := context.WithTimeout(ctx, 30*time.Second)
	emptyLease, emptyResult, err := reader.BeginWorkItemMembership(emptyCtx, storage.Principal{OrgID: orgID}, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", "P-empty"),
		RequestedRepositoryScope: []string{"acme/allowed"},
		S1Instant:                at.Add(30 * time.Minute),
	})
	cancelEmpty()
	if err != nil || emptyLease == nil {
		t.Fatalf("valid empty BeginWorkItemMembership lease=%v err=%v", emptyLease, err)
	}
	if emptyResult.Census.State != contextfabric.WorkItemMembershipCensusExact || !emptyResult.Census.PopulationMeasured || emptyResult.Census.AuthorizedPopulation != 0 || len(emptyResult.Members) != 0 {
		emptyLease.Release()
		t.Fatalf("valid empty census = %+v, want measured exact zero", emptyResult.Census)
	}
	emptyLease.Release()

	// A no-history column arm is an independent completed-query control for
	// the C+1 floor. It uses the production work_items.project_id column and
	// the real presence view, but no transition rows. The transition-heavy
	// C+2 fixture below remains an explicit physical-read failure under the
	// fixed 8192 ceiling; this control proves that a completed stream still
	// reports its logical floor rather than treating every large population as
	// unmeasured.
	const (
		columnOrgID      = "chaos-5752-column-floor"
		columnProjectID  = "P-column-floor"
		columnRepoID     = "20000000-0000-4000-8000-000000000003"
		columnRepoSlug   = "acme/column-floor"
		columnFloorRows  = contextfabric.WorkItemMembershipCensusLimit + 1
		columnInsertSize = 250
	)
	seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		columnRepoID, columnOrgID, columnRepoSlug, "linear", at)
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		columnProjectID, columnOrgID, "linear", columnProjectID, "Column floor", uint8(1), "active", "", at)
	for start := 0; start < columnFloorRows; start += columnInsertSize {
		end := start + columnInsertSize
		if end > columnFloorRows {
			end = columnFloorRows
		}
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*10)
		for index := start; index < end; index++ {
			workID := fmt.Sprintf("column-floor-%04d", index)
			values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, workID, columnRepoID, columnOrgID, workID, "open", "", at, "", "linear", columnProjectID)
		}
		seed("INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES "+strings.Join(values, ", "), args...)
	}
	columnCtx, cancelColumn := context.WithTimeout(ctx, 60*time.Second)
	columnLease, columnResult, err := reader.BeginWorkItemMembership(columnCtx, storage.Principal{OrgID: columnOrgID}, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", columnProjectID),
		RequestedRepositoryScope: []string{columnRepoSlug},
		S1Instant:                at.Add(30 * time.Minute),
	})
	cancelColumn()
	if err != nil || columnLease == nil {
		t.Fatalf("column floor BeginWorkItemMembership lease=%v err=%v", columnLease, err)
	}
	if columnResult.Census.State != contextfabric.WorkItemMembershipCensusFloor || !columnResult.Census.PopulationMeasured || columnResult.Census.PopulationComplete || !columnResult.Census.PopulationIncomplete || columnResult.Census.CappedPopulation != columnFloorRows || columnResult.Census.AuthorizedPopulation != columnFloorRows || columnResult.Census.DeniedPopulation != 0 || columnResult.Census.FutureBoundaryCount != 0 || columnResult.Census.TransitionAssertionCount != 0 || columnResult.Census.ServedMembers != contextfabric.WorkItemMembershipServeLimit || len(columnResult.Members) != contextfabric.WorkItemMembershipServeLimit {
		columnLease.Release()
		t.Fatalf("column floor census = %+v, want completed C+1 authorized floor with no transition metadata", columnResult.Census)
	}
	if columnResult.Members[0].CanonicalID >= columnResult.Members[len(columnResult.Members)-1].CanonicalID {
		columnLease.Release()
		t.Fatalf("column floor members are not in canonical order: first=%q last=%q", columnResult.Members[0].CanonicalID, columnResult.Members[len(columnResult.Members)-1].CanonicalID)
	}
	t.Logf("live no-history column fixture: state=%s capped=%d authorized=%d denied=%d future=%d assertions=%d served=%d", columnResult.Census.State, columnResult.Census.CappedPopulation, columnResult.Census.AuthorizedPopulation, columnResult.Census.DeniedPopulation, columnResult.Census.FutureBoundaryCount, columnResult.Census.TransitionAssertionCount, columnResult.Census.ServedMembers)
	columnLease.Release()

	// Isolate a single future self assertion from any earlier history. The
	// presence view must keep the member, while the metadata relation must
	// classify the (P,P) event as non-boundary.
	const (
		selfOrgID     = "chaos-5752-self"
		selfProjectID = "P-self"
		selfRepoID    = "20000000-0000-4000-8000-000000000005"
		selfRepoSlug  = "acme/self"
		selfWorkID    = "self-only"
	)
	seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		selfRepoID, selfOrgID, selfRepoSlug, "linear", at)
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		selfProjectID, selfOrgID, "linear", selfProjectID, "Self transition", uint8(1), "active", "", at)
	seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		selfWorkID, selfRepoID, selfOrgID, selfWorkID, "open", "", at, "", "linear", "")
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		selfOrgID, selfRepoID, "work_item", selfWorkID, "linear", selfProjectID, selfProjectID, selfProjectID, selfProjectID, "fixture", at.Add(time.Hour), at, "self-only-future")
	selfCtx, cancelSelf := context.WithTimeout(ctx, 30*time.Second)
	selfLease, selfResult, err := reader.BeginWorkItemMembership(selfCtx, storage.Principal{OrgID: selfOrgID}, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", selfProjectID),
		RequestedRepositoryScope: []string{selfRepoSlug},
		S1Instant:                at.Add(30 * time.Minute),
	})
	cancelSelf()
	if err != nil || selfLease == nil {
		t.Fatalf("self transition BeginWorkItemMembership lease=%v err=%v", selfLease, err)
	}
	if selfResult.Census.State != contextfabric.WorkItemMembershipCensusExact || selfResult.Census.AuthorizedPopulation != 1 || selfResult.Census.TransitionAssertionCount != 1 || selfResult.Census.FutureBoundaryCount != 0 || len(selfResult.Members) != 1 || selfResult.Members[0].WorkItemID != selfWorkID {
		selfLease.Release()
		t.Fatalf("live self transition census = %+v members=%#v, want one member, one assertion, and zero future boundaries", selfResult.Census, selfResult.Members)
	}
	t.Logf("live self transition fixture: state=%s authorized=%d future=%d assertions=%d members=%d", selfResult.Census.State, selfResult.Census.AuthorizedPopulation, selfResult.Census.FutureBoundaryCount, selfResult.Census.TransitionAssertionCount, len(selfResult.Members))
	selfLease.Release()

	// Two future ADD assertions for one project exercise the real duplicate
	// continuation rule. The latest asserted row remains a member, but it is
	// not a new future boundary.
	const (
		duplicateOrgID     = "chaos-5752-duplicate"
		duplicateProjectID = "P-duplicate"
		duplicateRepoID    = "20000000-0000-4000-8000-000000000006"
		duplicateRepoSlug  = "acme/duplicate"
		duplicateWorkID    = "duplicate-only"
	)
	seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		duplicateRepoID, duplicateOrgID, duplicateRepoSlug, "linear", at)
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		duplicateProjectID, duplicateOrgID, "linear", duplicateProjectID, "Duplicate transition", uint8(1), "active", "", at)
	seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		duplicateWorkID, duplicateRepoID, duplicateOrgID, duplicateWorkID, "open", "", at, "", "linear", "")
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		duplicateOrgID, duplicateRepoID, "work_item", duplicateWorkID, "linear", "", duplicateProjectID, "", duplicateProjectID, "fixture", at.Add(time.Hour), at, "duplicate-first",
		duplicateOrgID, duplicateRepoID, "work_item", duplicateWorkID, "linear", "", duplicateProjectID, "", duplicateProjectID, "fixture", at.Add(2*time.Hour), at, "duplicate-second")
	duplicateCtx, cancelDuplicate := context.WithTimeout(ctx, 30*time.Second)
	duplicateLease, duplicateResult, err := reader.BeginWorkItemMembership(duplicateCtx, storage.Principal{OrgID: duplicateOrgID}, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", duplicateProjectID),
		RequestedRepositoryScope: []string{duplicateRepoSlug},
		S1Instant:                at.Add(30 * time.Minute),
	})
	cancelDuplicate()
	if err != nil || duplicateLease == nil {
		t.Fatalf("duplicate transition BeginWorkItemMembership lease=%v err=%v", duplicateLease, err)
	}
	if duplicateResult.Census.State != contextfabric.WorkItemMembershipCensusExact || duplicateResult.Census.AuthorizedPopulation != 1 || duplicateResult.Census.TransitionAssertionCount != 1 || duplicateResult.Census.FutureBoundaryCount != 0 || len(duplicateResult.Members) != 1 || duplicateResult.Members[0].WorkItemID != duplicateWorkID {
		duplicateLease.Release()
		t.Fatalf("live duplicate transition census = %+v members=%#v, want one latest member, one assertion, and zero future boundaries", duplicateResult.Census, duplicateResult.Members)
	}
	t.Logf("live duplicate transition fixture: state=%s authorized=%d future=%d assertions=%d members=%d", duplicateResult.Census.State, duplicateResult.Census.AuthorizedPopulation, duplicateResult.Census.FutureBoundaryCount, duplicateResult.Census.TransitionAssertionCount, len(duplicateResult.Members))
	duplicateLease.Release()

	// Provider is part of the membership identity. A transition from another
	// provider must not authorize a work-item row that happens to reuse the
	// same repository and subject identifiers.
	seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"provider-mismatch", allowedRepo, orgID, "provider mismatch", "open", "", at, "", "jira", "")
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, allowedRepo, "work_item", "provider-mismatch", "linear", "", projectID, "", "P1", "fixture", at.Add(-time.Hour), at, "provider-mismatch-add")
	mismatchCtx, cancelMismatch := context.WithTimeout(ctx, 30*time.Second)
	mismatchLease, mismatchResult, err := reader.BeginWorkItemMembership(mismatchCtx, storage.Principal{OrgID: orgID}, request)
	cancelMismatch()
	if err != nil || mismatchLease == nil {
		t.Fatalf("provider mismatch BeginWorkItemMembership lease=%v err=%v", mismatchLease, err)
	}
	if mismatchResult.Census.AuthorizedPopulation != 2 || mismatchResult.Census.CappedPopulation != 3 {
		mismatchLease.Release()
		t.Fatalf("provider mismatch census = %+v, want unchanged 2 authorized of 3 scoped rows", mismatchResult.Census)
	}
	for _, member := range mismatchResult.Members {
		if member.WorkItemID == "provider-mismatch" {
			mismatchLease.Release()
			t.Fatalf("provider mismatch member was authorized: census=%+v members=%#v", mismatchResult.Census, mismatchResult.Members)
		}
	}
	mismatchLease.Release()

	// A same-project self-transition remains an assertion but creates no new
	// membership boundary. The real view still returns the latest assertion;
	// the same-statement history metadata keeps the future counter at one.
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, allowedRepo, "work_item", "self", "linear", projectID, projectID, "P1", "P1", "fixture", at.Add(time.Hour), at, "self-future")
	futureCtx, cancelFuture := context.WithTimeout(ctx, 30*time.Second)
	futureLease, futureResult, err := reader.BeginWorkItemMembership(futureCtx, storage.Principal{OrgID: orgID}, request)
	cancelFuture()
	if err != nil || futureLease == nil {
		t.Fatalf("future self BeginWorkItemMembership lease=%v err=%v", futureLease, err)
	}
	if futureResult.Census.FutureBoundaryCount != 1 {
		futureLease.Release()
		t.Fatalf("future self census = %+v, want one actual future boundary", futureResult.Census)
	}
	futureLease.Release()

	// Exercise the producer's history classification in the same live view:
	// a real move-in is a boundary, a duplicate add is a continuation, and a
	// dangling remove suppresses the asserted row without becoming a boundary.
	seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"move-in", allowedRepo, orgID, "move in", "open", "", at, "", "linear", "",
		"duplicate", allowedRepo, orgID, "duplicate", "open", "", at, "", "linear", "",
		"dangling", allowedRepo, orgID, "dangling", "open", "", at, "", "linear", "")
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, allowedRepo, "work_item", "move-in", "linear", otherID, projectID, "P2", "P1", "fixture", at.Add(2*time.Hour), at, "move-in-boundary",
		orgID, allowedRepo, "work_item", "duplicate", "linear", "", projectID, "", "P1", "fixture", at.Add(2*time.Hour), at, "duplicate-first",
		orgID, allowedRepo, "work_item", "duplicate", "linear", "", projectID, "", "P1", "fixture", at.Add(3*time.Hour), at, "duplicate-second",
		orgID, allowedRepo, "work_item", "dangling", "linear", projectID, "", "P1", "", "fixture", at.Add(4*time.Hour), at, "dangling-remove")
	classificationCtx, cancelClassification := context.WithTimeout(ctx, 30*time.Second)
	classificationLease, classificationResult, err := reader.BeginWorkItemMembership(classificationCtx, storage.Principal{OrgID: orgID}, request)
	cancelClassification()
	if err != nil || classificationLease == nil {
		t.Fatalf("history classification BeginWorkItemMembership lease=%v err=%v", classificationLease, err)
	}
	if classificationResult.Census.AuthorizedPopulation != 4 || classificationResult.Census.DeniedPopulation != 1 || classificationResult.Census.FutureBoundaryCount != 2 || classificationResult.Census.TransitionAssertionCount != 5 {
		classificationLease.Release()
		t.Fatalf("history classification census = %+v, want 4 authorized / 1 denied / 2 actual future boundaries / 5 assertions", classificationResult.Census)
	}
	classificationMembers := map[string]bool{}
	for _, member := range classificationResult.Members {
		classificationMembers[member.WorkItemID] = true
	}
	for _, workItemID := range []string{"allowed", "self", "move-in", "duplicate"} {
		if !classificationMembers[workItemID] {
			classificationLease.Release()
			t.Fatalf("history classification members = %#v, missing %q", classificationResult.Members, workItemID)
		}
	}
	for _, workItemID := range []string{"moved", "dangling", "provider-mismatch", "denied"} {
		if classificationMembers[workItemID] {
			classificationLease.Release()
			t.Fatalf("history classification members = %#v, unexpectedly contains %q", classificationResult.Members, workItemID)
		}
	}
	classificationLease.Release()

	// The excluded provider policy is also exercised through the live view.
	// Transition assertions remain observable for gitlab and legacy github,
	// while column-only rows remain unmeasured with no invented zero.
	const (
		gitlabPresentRepo = "20000000-0000-4000-8000-000000000011"
		gitlabAbsentRepo  = "20000000-0000-4000-8000-000000000012"
		githubPresentRepo = "20000000-0000-4000-8000-000000000013"
		githubAbsentRepo  = "20000000-0000-4000-8000-000000000014"
		gitlabPresentProj = "GLP"
		gitlabAbsentProj  = "GLA"
		githubPresentProj = "GHP"
		githubAbsentProj  = "GHA"
	)
	seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)`,
		gitlabPresentRepo, orgID, "acme/gitlab-present", "gitlab", at,
		gitlabAbsentRepo, orgID, "acme/gitlab-absent", "gitlab", at,
		githubPresentRepo, orgID, "acme/github-present", "github", at,
		githubAbsentRepo, orgID, "acme/github-absent", "github", at)
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gitlabPresentProj, orgID, "gitlab", "group/present", "GitLab present", uint8(1), "active", "", at,
		gitlabAbsentProj, orgID, "gitlab", "group/absent", "GitLab absent", uint8(1), "active", "", at,
		githubPresentProj, orgID, "github", "owner/present", "GitHub present", uint8(1), "active", "", at,
		githubAbsentProj, orgID, "github", "owner/absent", "GitHub absent", uint8(1), "active", "", at)
	seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gitlab-present", gitlabPresentRepo, orgID, "gitlab present", "open", "", at, "", "gitlab", "",
		"gitlab-absent", gitlabAbsentRepo, orgID, "gitlab absent", "open", "", at, "", "gitlab", gitlabAbsentProj,
		"github-present", githubPresentRepo, orgID, "github present", "open", "", at, "", "github", "",
		"github-absent", githubAbsentRepo, orgID, "github absent", "open", "", at, "", "github", githubAbsentProj)
	seed(`INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, gitlabPresentRepo, "work_item", "gitlab-present", "gitlab", "", gitlabPresentProj, "", "group/present", "fixture", at.Add(time.Hour), at, "gitlab-present-add",
		orgID, githubPresentRepo, "work_item", "github-present", "github", "", githubPresentProj, "", "owner/present", "fixture", at.Add(time.Hour), at, "github-present-add")
	for _, tc := range []struct {
		name              string
		provider          string
		projectID         string
		wantAssertions    int
		wantFutureCounter int
	}{
		{name: "gitlab_transition_present", provider: "gitlab", projectID: gitlabPresentProj, wantAssertions: 1, wantFutureCounter: 1},
		{name: "gitlab_transition_absent", provider: "gitlab", projectID: gitlabAbsentProj},
		{name: "github_legacy_transition_present", provider: "github", projectID: githubPresentProj, wantAssertions: 1, wantFutureCounter: 1},
		{name: "github_legacy_transition_absent", provider: "github", projectID: githubAbsentProj},
	} {
		t.Run(tc.name, func(t *testing.T) {
			excludedCtx, cancelExcluded := context.WithTimeout(ctx, 30*time.Second)
			excludedLease, excludedResult, err := reader.BeginWorkItemMembership(excludedCtx, storage.Principal{OrgID: orgID}, contextfabric.WorkItemMembershipRequest{
				Anchor:    workItemMembershipTestAnchor(t, tc.provider, tc.projectID),
				S1Instant: at.Add(30 * time.Minute),
			})
			cancelExcluded()
			if err != nil || excludedLease == nil {
				t.Fatalf("excluded provider BeginWorkItemMembership lease=%v err=%v", excludedLease, err)
			}
			defer excludedLease.Release()
			if excludedResult.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || excludedResult.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredExcludedProvider || excludedResult.Census.PopulationMeasured || excludedResult.Census.TransitionAssertionCount != tc.wantAssertions || excludedResult.Census.FutureBoundaryCount != tc.wantFutureCounter || len(excludedResult.Members) != 0 {
				t.Fatalf("excluded provider result = %+v, want unmeasured with assertions=%d future=%d and no members", excludedResult.Census, tc.wantAssertions, tc.wantFutureCounter)
			}
		})
	}

	// Add C+2 authorized subjects in bounded insert batches, then run the same
	// S1 statement with its production settings. The inner C+1 probe must yield
	// a floor while the outer K=200 result remains bounded.
	const floorRows = contextfabric.WorkItemMembershipCensusLimit + 2
	for start := 0; start < floorRows; start += 250 {
		end := start + 250
		if end > floorRows {
			end = floorRows
		}
		workValues := make([]string, 0, end-start)
		workArgs := make([]any, 0, (end-start)*10)
		transitionValues := make([]string, 0, end-start)
		transitionArgs := make([]any, 0, (end-start)*13)
		for index := start; index < end; index++ {
			workID := fmt.Sprintf("floor-%04d", index)
			workValues = append(workValues, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			workArgs = append(workArgs, workID, allowedRepo, orgID, workID, "open", "", at, "", "linear", "")
			transitionValues = append(transitionValues, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			transitionArgs = append(transitionArgs, orgID, allowedRepo, "work_item", workID, "linear", "", projectID, "", "P1", "fixture", at.Add(-time.Hour), at, fmt.Sprintf("floor-add-%04d", index))
		}
		seed("INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES "+strings.Join(workValues, ", "), workArgs...)
		seed("INSERT INTO project_membership_transitions (org_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id) VALUES "+strings.Join(transitionValues, ", "), transitionArgs...)
	}
	floorCtx, cancelFloor := context.WithTimeout(ctx, 60*time.Second)
	defer cancelFloor()
	floorLease, floorResult, err := reader.BeginWorkItemMembership(floorCtx, storage.Principal{OrgID: orgID}, request)
	if err != nil || floorLease == nil {
		t.Fatalf("floor BeginWorkItemMembership lease=%v err=%v", floorLease, err)
	}
	defer floorLease.Release()
	if floorResult.Census.State == contextfabric.WorkItemMembershipCensusUnmeasured {
		// The retained 8192 real-DDL receipt records ClickHouse code 158 for
		// this transition-heavy C+2 fixture. Assert the backend mechanism so
		// this branch cannot silently turn a semantic failure into a passing
		// unmeasured result. The rows are intentionally discarded by S1.
		if floorResult.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || floorResult.Census.PopulationMeasured || len(floorResult.Members) != 0 || !strings.Contains(recordingClient.lastErrorText(), "code: 158") {
			floorLease.Release()
			t.Fatalf("live C+2 resource result = %+v backend=%q, want code-158 unmeasured result with no members", floorResult.Census, recordingClient.lastErrorText())
		}
		t.Logf("live C+2 transition fixture is intentionally unmeasured at max_rows_to_read=%d: backend=%s", workItemMembershipMaxRowsToRead, recordingClient.lastErrorText())
	} else {
		if floorResult.Census.State != contextfabric.WorkItemMembershipCensusFloor || !floorResult.Census.PopulationIncomplete || floorResult.Census.CappedPopulation != contextfabric.WorkItemMembershipCensusLimit+1 || floorResult.Census.AuthorizedPopulation != contextfabric.WorkItemMembershipCensusLimit+1 || floorResult.Census.DeniedPopulation != 0 || floorResult.Census.ServedMembers != contextfabric.WorkItemMembershipServeLimit || len(floorResult.Members) != contextfabric.WorkItemMembershipServeLimit {
			floorLease.Release()
			t.Fatalf("live floor census = %+v, telemetry = %#v, want C+1 capped/authorized, no denied row in authorized-first probe, incomplete, and K served", floorResult.Census, telemetry.s1)
		}
		if floorResult.Members[0].CanonicalID >= floorResult.Members[len(floorResult.Members)-1].CanonicalID {
			floorLease.Release()
			t.Fatalf("live floor members are not in canonical order: first=%q last=%q", floorResult.Members[0].CanonicalID, floorResult.Members[len(floorResult.Members)-1].CanonicalID)
		}
	}
	floorLease.Release()

	// A separate small organization keeps the duplicate-key sentinel proof
	// independent from the transition-heavy C+2 resource fixture above. Two
	// projects share the same provider-qualified key, while one column-only
	// work item points at that key. The completed one-statement stream must
	// carry an unresolved sentinel instead of collapsing to exact zero.
	const (
		ambiguousOrgID      = "chaos-5752-ambiguous"
		ambiguousProjectA   = "P-ambiguous-a"
		ambiguousProjectB   = "P-ambiguous-b"
		ambiguousProjectKey = "P-ambiguous"
		ambiguousRepoID     = "20000000-0000-4000-8000-000000000004"
		ambiguousRepoSlug   = "acme/ambiguous"
		ambiguousWorkItemID = "ambiguous-column"
	)
	seed(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		ambiguousRepoID, ambiguousOrgID, ambiguousRepoSlug, "linear", at)
	seed(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ambiguousProjectA, ambiguousOrgID, "linear", ambiguousProjectKey, "Ambiguous A", uint8(1), "active", "", at,
		ambiguousProjectB, ambiguousOrgID, "linear", ambiguousProjectKey, "Ambiguous B", uint8(1), "active", "", at)
	seed(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id, provider, project_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ambiguousWorkItemID, ambiguousRepoID, ambiguousOrgID, ambiguousWorkItemID, "open", "", at, "", "linear", ambiguousProjectKey)
	ambiguousCtx, cancelAmbiguous := context.WithTimeout(ctx, 30*time.Second)
	ambiguousLease, ambiguousResult, err := reader.BeginWorkItemMembership(ambiguousCtx, storage.Principal{OrgID: ambiguousOrgID}, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", ambiguousProjectKey),
		RequestedRepositoryScope: []string{ambiguousRepoSlug},
		S1Instant:                at.Add(30 * time.Minute),
	})
	cancelAmbiguous()
	if err != nil || ambiguousLease == nil {
		t.Fatalf("ambiguous project BeginWorkItemMembership lease=%v err=%v", ambiguousLease, err)
	}
	if ambiguousResult.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || ambiguousResult.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || ambiguousResult.Census.PopulationMeasured || len(ambiguousResult.Members) != 0 {
		t.Fatalf("ambiguous project census = %+v members=%#v, want unmeasured with no members", ambiguousResult.Census, ambiguousResult.Members)
	}
	t.Logf("live ambiguous project fixture: state=%s reason=%s measured=%t members=%d", ambiguousResult.Census.State, ambiguousResult.Census.UnmeasuredReason, ambiguousResult.Census.PopulationMeasured, len(ambiguousResult.Members))
	ambiguousLease.Release()
}

var _ contextpacket.ClickHouseQueryClient = (*runtimeclickhouse.Client)(nil)

// workItemMembershipRecordingQueryClient keeps the real backend error for
// the one intentionally over-budget integration fixture. The production
// reader still receives the unmodified ClickHouse client contract; this
// wrapper only makes the test distinguish native resource refusal from a
// semantic unmeasured result.
type workItemMembershipRecordingQueryClient struct {
	delegate contextpacket.ClickHouseQueryClient
	lastErr  error
}

func (c *workItemMembershipRecordingQueryClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	rows, err := c.delegate.Query(ctx, statement, bindings)
	if err != nil {
		c.lastErr = err
		return nil, err
	}
	return &workItemMembershipRecordingRowScanner{owner: c, delegate: rows}, nil
}

func (c *workItemMembershipRecordingQueryClient) lastErrorText() string {
	if c == nil || c.lastErr == nil {
		return ""
	}
	return c.lastErr.Error()
}

type workItemMembershipRecordingRowScanner struct {
	owner    *workItemMembershipRecordingQueryClient
	delegate contextpacket.ClickHouseRowScanner
}

func (s *workItemMembershipRecordingRowScanner) Next() bool {
	return s.delegate.Next()
}

func (s *workItemMembershipRecordingRowScanner) Scan(dest ...any) error {
	return s.delegate.Scan(dest...)
}

func (s *workItemMembershipRecordingRowScanner) Err() error {
	err := s.delegate.Err()
	if err != nil {
		s.owner.lastErr = err
	}
	return err
}

func (s *workItemMembershipRecordingRowScanner) Close() error {
	return s.delegate.Close()
}

var _ contextpacket.ClickHouseQueryClient = (*workItemMembershipRecordingQueryClient)(nil)
