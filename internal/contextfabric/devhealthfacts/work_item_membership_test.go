package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const workItemMembershipTestOrg = "org-chaos-5752"

type workItemMembershipFakeClient struct {
	rows         [][]any
	queryErr     error
	scanErrAt    int
	iterationErr error
	queries      []workItemMembershipCapturedQuery
	scanner      *workItemMembershipFakeScanner
}

type workItemMembershipCapturedQuery struct {
	statement string
	bindings  []contextpacket.ClickHouseBinding
}

func (c *workItemMembershipFakeClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.queries = append(c.queries, workItemMembershipCapturedQuery{statement: statement, bindings: bindings})
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	c.scanner = &workItemMembershipFakeScanner{rows: c.rows, scanErrAt: c.scanErrAt, iterationErr: c.iterationErr}
	return c.scanner, nil
}

type workItemMembershipFakeScanner struct {
	rows         [][]any
	index        int
	nextCalls    int
	scanCalls    int
	scanErrAt    int
	iterationErr error
}

// Next advances to the current row before Scan, matching the driver contract.
// A Scan failure must not leave Next returning the same row forever.
func (s *workItemMembershipFakeScanner) Next() bool {
	s.nextCalls++
	if s.index >= len(s.rows) {
		return false
	}
	s.index++
	return true
}

func (s *workItemMembershipFakeScanner) Scan(dest ...any) error {
	s.scanCalls++
	rowIndex := s.index - 1
	if rowIndex == s.scanErrAt {
		return errors.New("synthetic scan failure")
	}
	row := s.rows[rowIndex]
	if len(row) != len(dest) {
		return errors.New("synthetic scan width failure")
	}
	for i, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[i].(string)
		case *uint8:
			*value = row[i].(uint8)
		case *uint64:
			*value = row[i].(uint64)
		default:
			return errors.New("synthetic scan destination failure")
		}
	}
	return nil
}

func (s *workItemMembershipFakeScanner) Err() error   { return s.iterationErr }
func (s *workItemMembershipFakeScanner) Close() error { return nil }

type workItemMembershipTelemetrySpy struct {
	s1    []contextfabric.WorkItemMembershipS1Event
	gates []contextfabric.WorkItemMembershipGateEvent
}

func (s *workItemMembershipTelemetrySpy) RecordWorkItemMembershipS1(_ context.Context, _ storage.Principal, event contextfabric.WorkItemMembershipS1Event) {
	s.s1 = append(s.s1, event)
}

func (s *workItemMembershipTelemetrySpy) RecordWorkItemMembershipGate(_ context.Context, _ storage.Principal, event contextfabric.WorkItemMembershipGateEvent) {
	s.gates = append(s.gates, event)
}

func workItemMembershipTestAnchor(t *testing.T, provider, projectID string) contextfabric.WorkItemMembershipAnchor {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, projectID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive project anchor: id=%q omitted=%t err=%v", canonicalID, omitted, err)
	}
	return contextfabric.WorkItemMembershipAnchor{Subject: contextfabric.SubjectRef{
		Kind:        contextfabric.SubjectProject,
		CanonicalID: canonicalID,
	}}
}

func workItemMembershipTestRow(t *testing.T, repoID, workItemID string, authorized uint8, scoped, allowed, denied uint64) []any {
	t.Helper()
	canonicalID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive work-item identity: id=%q omitted=%t err=%v", canonicalID, omitted, err)
	}
	if authorized == 0 {
		canonicalID, repoID, workItemID = "", "", ""
	}
	return []any{canonicalID, repoID, workItemID, "acme/api", authorized, scoped, allowed, denied, uint64(0), uint64(0), uint8(0), uint8(1)}
}

func workItemMembershipTestSentinelRow() []any {
	return []any{"", "", "", "", uint8(0), uint64(0), uint64(0), uint64(0), uint64(0), uint64(0), uint8(1), uint8(1)}
}

func workItemMembershipRowsWithSentinel(rows ...workItemMembershipS1Row) []workItemMembershipS1Row {
	withSentinel := make([]workItemMembershipS1Row, len(rows)+1)
	copy(withSentinel, rows)
	for i := range rows {
		withSentinel[i].AnchorResolution = workItemMembershipAnchorResolved
	}
	withSentinel[len(rows)] = workItemMembershipS1Row{RowKind: workItemMembershipRowAnchorSentinel, AnchorResolution: workItemMembershipAnchorResolved}
	return withSentinel
}

func newWorkItemMembershipTestReader(t *testing.T, client contextpacket.ClickHouseQueryClient, telemetry contextfabric.WorkItemMembershipTelemetry) (*WorkItemMembershipReader, *contextfabric.WorkItemMembershipGate) {
	t.Helper()
	gate, err := contextfabric.NewWorkItemMembershipGate(2, 1)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}
	reader, err := NewWorkItemMembershipReader(client, WorkItemMembershipReaderOptions{
		Gate:      gate,
		Telemetry: telemetry,
		Now:       func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewWorkItemMembershipReader: %v", err)
	}
	return reader, gate
}

func TestWorkItemMembershipS1UsesAtomicMaskAndCanonicalOrder(t *testing.T) {
	firstRepo := "00000000-0000-0000-0000-000000000001"
	secondRepo := "00000000-0000-0000-0000-000000000002"
	client := &workItemMembershipFakeClient{scanErrAt: -1, rows: append([][]any{
		workItemMembershipTestRow(t, secondRepo, "WI-2", 1, 3, 2, 1),
		workItemMembershipTestRow(t, firstRepo, "WI-1", 1, 3, 2, 1),
		workItemMembershipTestRow(t, "00000000-0000-0000-0000-000000000003", "WI-3", 0, 3, 2, 1),
	}, workItemMembershipTestSentinelRow())}
	telemetry := &workItemMembershipTelemetrySpy{}
	reader, gate := newWorkItemMembershipTestReader(t, client, telemetry)
	lease, result, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", "P1"),
		RequestedRepositoryScope: []string{"acme/api"},
		PlanMaxMembers:           2,
		RequestMaxMembers:        3,
	})
	if err != nil {
		t.Fatalf("BeginWorkItemMembership: %v", err)
	}
	if lease == nil {
		t.Fatal("BeginWorkItemMembership returned a nil lease")
	}
	defer lease.Release()
	if result.Census.State != contextfabric.WorkItemMembershipCensusExact || !result.Census.PopulationMeasured || result.Census.AuthorizedPopulation != 2 || result.Census.DeniedPopulation != 1 {
		t.Fatalf("census = %+v, want exact measured 2 authorized / 1 denied", result.Census)
	}
	if len(result.Members) != 2 {
		t.Fatalf("members = %#v, want 2 after K=min(200,2,3)", result.Members)
	}
	if result.Members[0].CanonicalID >= result.Members[1].CanonicalID {
		t.Fatalf("members are not in canonical identity order: %#v", result.Members)
	}
	if len(telemetry.s1) != 1 || telemetry.s1[0].State != contextfabric.WorkItemMembershipCensusExact {
		t.Fatalf("S1 telemetry = %#v, want one exact event", telemetry.s1)
	}
	if got := gate.Stats().InFlight; got != 1 {
		t.Fatalf("in-flight permits after S1 = %d, want 1 until lease release", got)
	}
	statement := client.queries[0].statement
	for _, fragment := range []string{
		"project_membership_presence AS m",
		"work_items AS w FINAL",
		"m.org_id = w.org_id AND m.repo_id = w.repo_id AND m.subject_id = w.work_item_id AND m.provider = w.provider",
		"LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id",
		"to_project_id != from_project_id",
		"lagInFrame(is_add, 1, 2)",
		"AND NOT dup_flag",
		"anchor_resolved",
		"row_kind",
		"authorized_repo_all",
		"requested_repo_slugs",
		"ORDER BY authorized_flag DESC, canonical_key ASC",
		"LIMIT 2001",
		"LIMIT {serve_limit:UInt32}",
		"max_threads = 1",
		"max_execution_time = ",
		"timeout_overflow_mode = 'throw'",
		"max_rows_to_read = 2000000",
		"read_overflow_mode = 'throw'",
		"max_memory_usage = 67108864",
		"max_result_rows = 3",
		"result_overflow_mode = 'throw'",
	} {
		if !strings.Contains(statement, fragment) {
			t.Errorf("S1 statement missing %q: %s", fragment, statement)
		}
	}
	var serveLimit uint32
	for _, binding := range client.queries[0].bindings {
		if binding.Name == "serve_limit" {
			value, ok := binding.Value.(uint32)
			if !ok {
				t.Fatalf("serve_limit binding type = %T, want uint32", binding.Value)
			}
			serveLimit = value
		}
	}
	if serveLimit != 2 {
		t.Fatalf("serve_limit binding = %d, want K=2", serveLimit)
	}
}

func TestWorkItemMembershipS1AnchorSentinelDistinguishesEmptyFromUnresolved(t *testing.T) {
	anchor := workItemMembershipTestAnchor(t, "linear", "P1")
	valid := finalizeWorkItemMembershipS1([]workItemMembershipS1Row{{RowKind: workItemMembershipRowAnchorSentinel, AnchorResolution: workItemMembershipAnchorResolved}}, anchor, 200)
	if valid.Census.State != contextfabric.WorkItemMembershipCensusExact || !valid.Census.PopulationMeasured || valid.Census.AuthorizedPopulation != 0 || valid.Census.Limitation != "" {
		t.Fatalf("valid sentinel result = %+v, want measured exact zero", valid)
	}
	missing := finalizeWorkItemMembershipS1(nil, anchor, 200)
	if missing.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || missing.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || missing.Census.PopulationMeasured || len(missing.Members) != 0 {
		t.Fatalf("missing sentinel result = %+v, want unmeasured S1 error", missing)
	}
	duplicate := finalizeWorkItemMembershipS1([]workItemMembershipS1Row{
		{RowKind: workItemMembershipRowAnchorSentinel, AnchorResolution: workItemMembershipAnchorResolved},
		{RowKind: workItemMembershipRowAnchorSentinel, AnchorResolution: workItemMembershipAnchorResolved},
	}, anchor, 200)
	if duplicate.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || duplicate.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || duplicate.Census.PopulationMeasured || len(duplicate.Members) != 0 {
		t.Fatalf("duplicate sentinel result = %+v, want unmeasured S1 error", duplicate)
	}
	malformed := finalizeWorkItemMembershipS1([]workItemMembershipS1Row{{CanonicalID: "work_item.v2:forbidden", RowKind: workItemMembershipRowAnchorSentinel, AnchorResolution: workItemMembershipAnchorResolved}}, anchor, 200)
	if malformed.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || malformed.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || malformed.Census.PopulationMeasured || len(malformed.Members) != 0 {
		t.Fatalf("malformed sentinel result = %+v, want unmeasured S1 error", malformed)
	}
	contradictoryMember := finalizeWorkItemMembershipS1([]workItemMembershipS1Row{
		{CanonicalID: "work_item.v2:repo:item", RepoID: "repo", WorkItemID: "item", Authorized: 1, ScopedPopulation: 1, AuthorizedPopulation: 1, AnchorResolution: 0},
		{RowKind: workItemMembershipRowAnchorSentinel, AnchorResolution: workItemMembershipAnchorResolved},
	}, anchor, 200)
	if contradictoryMember.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || contradictoryMember.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || contradictoryMember.Census.PopulationMeasured || len(contradictoryMember.Members) != 0 {
		t.Fatalf("contradictory member metadata result = %+v, want unmeasured S1 error", contradictoryMember)
	}
	invalid := finalizeWorkItemMembershipS1([]workItemMembershipS1Row{{RowKind: workItemMembershipRowAnchorSentinel}}, anchor, 200)
	if invalid.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || invalid.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || invalid.Census.PopulationMeasured || len(invalid.Members) != 0 {
		t.Fatalf("unresolved sentinel result = %+v, want unmeasured S1 error", invalid)
	}
}

func TestWorkItemMembershipReadersWithNilGateShareProcessDefault(t *testing.T) {
	const readersCount = contextfabric.DefaultWorkItemMembershipMaxInFlight + 1
	readers := make([]*WorkItemMembershipReader, 0, readersCount)
	for i := 0; i < readersCount; i++ {
		reader, err := NewWorkItemMembershipReader(&workItemMembershipFakeClient{}, WorkItemMembershipReaderOptions{
			Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{},
		})
		if err != nil {
			t.Fatalf("reader %d: %v", i, err)
		}
		readers = append(readers, reader)
	}
	if readers[0].gate != defaultWorkItemMembershipProcessGate {
		t.Fatalf("first nil-gate reader uses %p, want process gate %p", readers[0].gate, defaultWorkItemMembershipProcessGate)
	}
	for i, reader := range readers[1:] {
		if reader.gate != readers[0].gate {
			t.Fatalf("reader %d uses gate %p, want shared gate %p", i+1, reader.gate, readers[0].gate)
		}
	}

	active := make([]*contextfabric.WorkItemMembershipLease, 0, contextfabric.DefaultWorkItemMembershipMaxInFlight)
	for i := 0; i < contextfabric.DefaultWorkItemMembershipMaxInFlight; i++ {
		lease, err := readers[0].gate.Acquire(context.Background())
		if err != nil {
			t.Fatalf("fill permit %d: %v", i, err)
		}
		active = append(active, lease)
	}
	queued := make(chan *contextfabric.WorkItemMembershipLease, contextfabric.DefaultWorkItemMembershipQueueCapacity)
	queueErrs := make(chan error, contextfabric.DefaultWorkItemMembershipQueueCapacity)
	for i := 0; i < contextfabric.DefaultWorkItemMembershipQueueCapacity; i++ {
		go func() {
			lease, err := readers[0].gate.Acquire(context.Background())
			if err != nil {
				queueErrs <- err
				return
			}
			queued <- lease
		}()
	}
	deadline := time.Now().Add(time.Second)
	for readers[0].gate.Stats().Queued != contextfabric.DefaultWorkItemMembershipQueueCapacity && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := readers[0].gate.Stats().Queued; got != contextfabric.DefaultWorkItemMembershipQueueCapacity {
		t.Fatalf("shared default queue occupancy = %d, want %d", got, contextfabric.DefaultWorkItemMembershipQueueCapacity)
	}
	if _, err := readers[readersCount-1].gate.Acquire(context.Background()); !errors.Is(err, contextfabric.ErrWorkItemMembershipQueueFull) {
		t.Fatalf("33rd nil-gate reader admission = %v, want queue-full shared-bound refusal", err)
	}
	for _, lease := range active {
		lease.Release()
	}
	queuedLeases := make([]*contextfabric.WorkItemMembershipLease, 0, contextfabric.DefaultWorkItemMembershipQueueCapacity)
	for i := 0; i < contextfabric.DefaultWorkItemMembershipQueueCapacity; i++ {
		select {
		case err := <-queueErrs:
			t.Fatalf("queued reader admission: %v", err)
		case lease := <-queued:
			queuedLeases = append(queuedLeases, lease)
		case <-time.After(time.Second):
			t.Fatal("queued reader admission did not receive a permit")
		}
	}
	for _, lease := range queuedLeases {
		lease.Release()
	}
	if stats := readers[0].gate.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
		t.Fatalf("shared default gate after cleanup = %+v, want empty", stats)
	}
}

func TestWorkItemMembershipS1UsesLiveRawScopeAndRefusesTooShortDeadline(t *testing.T) {
	client := &workItemMembershipFakeClient{scanErrAt: -1, rows: [][]any{workItemMembershipTestSentinelRow()}}
	telemetry := &workItemMembershipTelemetrySpy{}
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}
	reader, err := NewWorkItemMembershipReader(client, WorkItemMembershipReaderOptions{
		Gate:      gate,
		Telemetry: telemetry,
	})
	if err != nil {
		t.Fatalf("NewWorkItemMembershipReader: %v", err)
	}
	shortCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	principal := storage.Principal{OrgID: "live-org", RepositoryScopes: []string{"acme/granted"}}
	lease, _, err := reader.BeginWorkItemMembership(shortCtx, principal, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", "P1"),
		RequestedRepositoryScope: []string{"acme/api"},
	})
	if !errors.Is(err, contextfabric.ErrWorkItemMembershipDeadlineTooShort) || lease != nil {
		t.Fatalf("short deadline lease=%v err=%v, want refusal before S1", lease, err)
	}
	if len(client.queries) != 0 || gate.Stats().InFlight != 0 {
		t.Fatalf("short deadline started query or retained permit: queries=%d stats=%+v", len(client.queries), gate.Stats())
	}
	if len(telemetry.gates) != 1 || telemetry.gates[0].Outcome != "deadline_too_short" {
		t.Fatalf("short deadline gate telemetry = %#v, want deadline_too_short", telemetry.gates)
	}

	longCtx, cancelLong := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelLong()
	var result contextfabric.WorkItemMembershipResult
	lease, result, err = reader.BeginWorkItemMembership(longCtx, principal, contextfabric.WorkItemMembershipRequest{
		Anchor:                   workItemMembershipTestAnchor(t, "linear", "P1"),
		RequestedRepositoryScope: []string{"acme/api"},
	})
	if err != nil || lease == nil {
		t.Fatalf("long deadline lease=%v err=%v", lease, err)
	}
	defer lease.Release()
	if result.Census.State != contextfabric.WorkItemMembershipCensusExact || !result.Census.PopulationMeasured {
		t.Fatalf("long deadline result = %+v, want measured empty result", result)
	}
	var authorizedSlugs, requestedSlugs []string
	for _, binding := range client.queries[0].bindings {
		switch binding.Name {
		case "authorized_repo_slugs":
			var ok bool
			authorizedSlugs, ok = binding.Value.([]string)
			if !ok {
				t.Fatalf("authorized_repo_slugs binding type = %T, want []string", binding.Value)
			}
		case "requested_repo_slugs":
			var ok bool
			requestedSlugs, ok = binding.Value.([]string)
			if !ok {
				t.Fatalf("requested_repo_slugs binding type = %T, want []string", binding.Value)
			}
		}
	}
	if len(authorizedSlugs) != 1 || authorizedSlugs[0] != "acme/granted" || len(requestedSlugs) != 1 || requestedSlugs[0] != "acme/api" {
		t.Fatalf("authorization bindings authorized=%#v requested=%#v, want current principal grant and raw request selector", authorizedSlugs, requestedSlugs)
	}
}

func TestWorkItemMembershipS1DoesNotSaturateOverflowedCounts(t *testing.T) {
	row := workItemMembershipS1Row{
		CanonicalID:          "work_item.v2:repo:item",
		RepoID:               "repo",
		WorkItemID:           "item",
		Authorized:           1,
		ScopedPopulation:     ^uint64(0),
		AuthorizedPopulation: ^uint64(0),
	}
	result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(row), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
	if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || result.Census.PopulationMeasured || len(result.Members) != 0 {
		t.Fatalf("overflowed census = %+v, want unmeasured with no members", result)
	}
}

func TestWorkItemMembershipSettingsUsesAPositiveWholeSecondBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	settings, err := workItemMembershipSettings(ctx, 200)
	if err != nil {
		t.Fatalf("workItemMembershipSettings: %v", err)
	}
	if settings.MaxExecutionTimeSeconds < 1 || settings.MaxExecutionTimeSeconds > 1 {
		t.Fatalf("MaxExecutionTimeSeconds = %d, want the one whole second available in this bounded context", settings.MaxExecutionTimeSeconds)
	}
	expiredCtx, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancelExpired()
	if _, err := workItemMembershipSettings(expiredCtx, 200); !errors.Is(err, contextfabric.ErrWorkItemMembershipDeadlineTooShort) {
		t.Fatalf("expired settings error = %v, want ErrWorkItemMembershipDeadlineTooShort", err)
	}
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancelShort()
	if _, err := workItemMembershipSettings(shortCtx, 200); !errors.Is(err, contextfabric.ErrWorkItemMembershipDeadlineTooShort) {
		t.Fatalf("subsecond settings error = %v, want ErrWorkItemMembershipDeadlineTooShort", err)
	}
}

func TestWorkItemMembershipSettingsUsesTheFixedDefaultTimeout(t *testing.T) {
	settings, err := workItemMembershipSettings(context.Background(), 200)
	if err != nil {
		t.Fatalf("workItemMembershipSettings: %v", err)
	}
	if settings.MaxExecutionTimeSeconds != 5 {
		t.Fatalf("default MaxExecutionTimeSeconds = %d, want 5", settings.MaxExecutionTimeSeconds)
	}
}

func TestWorkItemMembershipSettingsUsesSharedResourceClass(t *testing.T) {
	for _, k := range []int{1, 17, contextfabric.WorkItemMembershipServeLimit} {
		settings, err := workItemMembershipSettings(context.Background(), k)
		if err != nil {
			t.Fatal(err)
		}
		// MaxMemoryUsage/MaxThreads stay the shared content-reader policy;
		// MaxRowsToRead does NOT -- S1 is a whole-population census (window
		// count() OVER() aggregates over every relation it joins before any
		// cap applies), not a bounded per-id-list page read, so it needs its
		// OWN, much larger row-read bound: a real project's census read
		// that shares the too-small page bound exceeds it and fails closed.
		if settings.MaxRowsToRead == workItemReaderMaxRowsToRead {
			t.Fatalf("K=%d S1 MaxRowsToRead must NOT equal the shared per-page bound %d -- S1 needs its own, larger census bound", k, workItemReaderMaxRowsToRead)
		}
		if settings.MaxRowsToRead != workItemMembershipMaxRowsToRead {
			t.Fatalf("K=%d S1 MaxRowsToRead = %d, want its own dedicated bound %d", k, settings.MaxRowsToRead, workItemMembershipMaxRowsToRead)
		}
		if settings.MaxMemoryUsage != workItemReaderMaxMemoryUsage || settings.MaxThreads != workItemReaderMaxThreads {
			t.Fatalf("K=%d S1 memory/thread class differs from the shared policy it DOES still share: %+v", k, settings)
		}
		if settings.MaxResultRows != uint64(k+1) {
			t.Fatalf("K=%d result probe=%d, want K+1", k, settings.MaxResultRows)
		}
		rendered := readers.WithSettings("SELECT 1", settings)
		if strings.Count(rendered, "max_threads = 1") != 1 || strings.Count(rendered, "SETTINGS ") != 1 {
			t.Fatalf("K=%d typed settings must render one thread bound: %s", k, rendered)
		}
		for _, mode := range []string{"read_overflow_mode = 'throw'", "result_overflow_mode = 'throw'", "timeout_overflow_mode = 'throw'"} {
			if !strings.Contains(rendered, mode) {
				t.Fatalf("K=%d missing %s: %s", k, mode, rendered)
			}
		}
	}
}

func TestWorkItemMembershipS1QueryFailureDiscardsRowsAndMeasuresNothing(t *testing.T) {
	client := &workItemMembershipFakeClient{queryErr: errors.New("synthetic query failure")}
	telemetry := &workItemMembershipTelemetrySpy{}
	reader, _ := newWorkItemMembershipTestReader(t, client, telemetry)
	lease, result, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
		Anchor: workItemMembershipTestAnchor(t, "linear", "P1"),
	})
	if err != nil || lease == nil {
		t.Fatalf("BeginWorkItemMembership returned lease=%v err=%v, want held lease and no public backend error", lease, err)
	}
	defer lease.Release()
	if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.PopulationMeasured || len(result.Members) != 0 || result.Census.Limitation == "" {
		t.Fatalf("query failure result = %+v, want unmeasured with no members and limitation", result)
	}
	if len(telemetry.s1) != 1 || telemetry.s1[0].Reason != contextfabric.WorkItemMembershipUnmeasuredS1Error {
		t.Fatalf("query failure telemetry = %#v, want s1_error", telemetry.s1)
	}
}

// TestWorkItemMembershipS1ClassifiesQueryBudgetExceededDistinctly pins
// CHAOS-5991: a ClickHouse query-resource-budget exception (the census's own
// MaxRowsToRead/MaxMemoryUsage bound, the exact shape a real project's
// census hit live) must classify as read_limit_exceeded, NOT the generic
// s1_error -- so an operator can tell "the census outgrew its own bound"
// from any other backend fault without ever seeing the underlying
// ClickHouse exception text, which this vocabulary never carries.
func TestWorkItemMembershipS1ClassifiesQueryBudgetExceededDistinctly(t *testing.T) {
	client := &workItemMembershipFakeClient{queryErr: fmt.Errorf("wrapped: %w", &proto.Exception{Code: 158, Name: "TOO_MANY_ROWS", Message: "Limit for rows or bytes to read exceeded"})}
	telemetry := &workItemMembershipTelemetrySpy{}
	reader, _ := newWorkItemMembershipTestReader(t, client, telemetry)
	lease, result, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
		Anchor: workItemMembershipTestAnchor(t, "linear", "P1"),
	})
	if err != nil || lease == nil {
		t.Fatalf("BeginWorkItemMembership returned lease=%v err=%v, want held lease and no public backend error", lease, err)
	}
	defer lease.Release()
	if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured {
		t.Fatalf("query-budget failure state = %v, want unmeasured", result.Census.State)
	}
	if len(telemetry.s1) != 1 || telemetry.s1[0].Reason != contextfabric.WorkItemMembershipUnmeasuredReadLimitExceeded {
		t.Fatalf("query-budget failure telemetry = %#v, want read_limit_exceeded", telemetry.s1)
	}
}

// TestWorkItemMembershipS1ClassifiesCancellationDistinctly pins the third
// closed-vocabulary arm: a context cancellation/deadline mid-query is the
// CALLER's story, not a backend fault, and must not be reported as
// read_limit_exceeded or the generic s1_error.
func TestWorkItemMembershipS1ClassifiesCancellationDistinctly(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"canceled", fmt.Errorf("wrapped: %w", context.Canceled)},
		{"deadline exceeded", fmt.Errorf("wrapped: %w", context.DeadlineExceeded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &workItemMembershipFakeClient{queryErr: tc.err}
			telemetry := &workItemMembershipTelemetrySpy{}
			reader, _ := newWorkItemMembershipTestReader(t, client, telemetry)
			lease, result, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
				Anchor: workItemMembershipTestAnchor(t, "linear", "P1"),
			})
			if err != nil || lease == nil {
				t.Fatalf("BeginWorkItemMembership returned lease=%v err=%v, want held lease and no public backend error", lease, err)
			}
			defer lease.Release()
			if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured {
				t.Fatalf("%s state = %v, want unmeasured", tc.name, result.Census.State)
			}
			if len(telemetry.s1) != 1 || telemetry.s1[0].Reason != contextfabric.WorkItemMembershipUnmeasuredCancelled {
				t.Fatalf("%s telemetry = %#v, want cancelled", tc.name, telemetry.s1)
			}
		})
	}
}

// TestClassifyWorkItemMembershipS1Error exercises the classifier directly
// over its whole input domain: a budget exception, a budget exception
// wrapped through fmt.Errorf (errors.As must unwrap it), cancellation, a
// deadline, and a plain backend error that is none of those.
func TestClassifyWorkItemMembershipS1Error(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want contextfabric.WorkItemMembershipUnmeasuredReason
	}{
		{"bare budget exception (rows)", &proto.Exception{Code: 158}, contextfabric.WorkItemMembershipUnmeasuredReadLimitExceeded},
		{"bare budget exception (bytes)", &proto.Exception{Code: 307}, contextfabric.WorkItemMembershipUnmeasuredReadLimitExceeded},
		{"wrapped budget exception", fmt.Errorf("query: %w", &proto.Exception{Code: 158}), contextfabric.WorkItemMembershipUnmeasuredReadLimitExceeded},
		{"unrelated exception code", &proto.Exception{Code: 999}, contextfabric.WorkItemMembershipUnmeasuredS1Error},
		{"cancelled", context.Canceled, contextfabric.WorkItemMembershipUnmeasuredCancelled},
		{"deadline exceeded", context.DeadlineExceeded, contextfabric.WorkItemMembershipUnmeasuredCancelled},
		{"wrapped cancelled", fmt.Errorf("op: %w", context.Canceled), contextfabric.WorkItemMembershipUnmeasuredCancelled},
		{"plain backend error", errors.New("connection reset"), contextfabric.WorkItemMembershipUnmeasuredS1Error},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWorkItemMembershipS1Error(tc.err); got != tc.want {
				t.Fatalf("classifyWorkItemMembershipS1Error(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestWorkItemMembershipS1ScanAndIterationFailuresDiscardScannedRows(t *testing.T) {
	cases := []struct {
		name         string
		scanErrAt    int
		iterationErr error
	}{
		{name: "scan-first", scanErrAt: 0},
		{name: "scan", scanErrAt: 1},
		{name: "iteration", scanErrAt: -1, iterationErr: errors.New("synthetic iteration failure")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &workItemMembershipFakeClient{
				rows: append([][]any{
					workItemMembershipTestRow(t, "00000000-0000-0000-0000-000000000001", "WI-1", 1, 2, 2, 0),
					workItemMembershipTestRow(t, "00000000-0000-0000-0000-000000000002", "WI-2", 1, 2, 2, 0),
				}, workItemMembershipTestSentinelRow()),
				scanErrAt: tc.scanErrAt, iterationErr: tc.iterationErr,
			}
			telemetry := &workItemMembershipTelemetrySpy{}
			reader, _ := newWorkItemMembershipTestReader(t, client, telemetry)
			lease, result, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
				Anchor: workItemMembershipTestAnchor(t, "linear", "P1"),
			})
			if err != nil || lease == nil {
				t.Fatalf("BeginWorkItemMembership returned lease=%v err=%v", lease, err)
			}
			defer lease.Release()
			if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.PopulationMeasured || len(result.Members) != 0 {
				t.Fatalf("%s result = %+v, want unmeasured with no members", tc.name, result)
			}
			if len(telemetry.s1) != 1 || telemetry.s1[0].Reason != contextfabric.WorkItemMembershipUnmeasuredS1Error {
				t.Fatalf("%s telemetry = %#v, want one s1_error event", tc.name, telemetry.s1)
			}
			if tc.scanErrAt >= 0 {
				wantCalls := tc.scanErrAt + 1
				if client.scanner == nil {
					t.Fatalf("%s scanner = nil, want scanner observation", tc.name)
				}
				if client.scanner.nextCalls != wantCalls || client.scanner.scanCalls != wantCalls {
					t.Fatalf("%s scanner calls = next:%d scan:%d, want %d each after scan error", tc.name, client.scanner.nextCalls, client.scanner.scanCalls, wantCalls)
				}
			}
		})
	}
}

func TestWorkItemMembershipS1FloorDedupsAndZeroAuthorizedOverflowIsUnmeasured(t *testing.T) {
	anchor := workItemMembershipTestAnchor(t, "linear", "P1")
	first := workItemMembershipS1Row{CanonicalID: "work_item.v2:r:one", RepoID: "r", WorkItemID: "one", Authorized: 1, ScopedPopulation: 2001, AuthorizedPopulation: 2001}
	duplicate := first
	second := workItemMembershipS1Row{CanonicalID: "work_item.v2:r:two", RepoID: "r", WorkItemID: "two", Authorized: 1, ScopedPopulation: 2001, AuthorizedPopulation: 2001}
	floor := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(first, duplicate, second), anchor, 200)
	if floor.Census.State != contextfabric.WorkItemMembershipCensusFloor || !floor.Census.PopulationIncomplete || floor.Census.AuthorizedPopulation != 2001 || len(floor.Members) != 2 {
		t.Fatalf("floor result = %+v, want floor, 2001 authorized, 2 deduped members", floor)
	}
	denied := make([]workItemMembershipS1Row, 2)
	for i := range denied {
		denied[i] = workItemMembershipS1Row{Authorized: 0, ScopedPopulation: 2001, AuthorizedPopulation: 0, DeniedPopulation: 2001}
	}
	overflow := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(denied...), anchor, 200)
	if overflow.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || overflow.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredZeroAuthorizedOverflow || overflow.Census.PopulationMeasured || len(overflow.Members) != 0 {
		t.Fatalf("zero-authorized overflow = %+v, want unmeasured denial with no members", overflow)
	}
}

func TestWorkItemMembershipS1ExcludedProviderIsUnmeasuredWithTransitionCount(t *testing.T) {
	for _, tc := range []struct {
		name              string
		provider          string
		projectID         string
		rows              []workItemMembershipS1Row
		wantAssertions    int
		wantFutureCounter int
	}{
		{
			name:              "gitlab_transition_present",
			provider:          "gitlab",
			projectID:         "group/project",
			rows:              []workItemMembershipS1Row{{Authorized: 1, ScopedPopulation: 1, AuthorizedPopulation: 1, TransitionAssertionCount: 2, FutureBoundaryCount: 1}},
			wantAssertions:    2,
			wantFutureCounter: 1,
		},
		{
			name:              "gitlab_transition_absent",
			provider:          "gitlab",
			projectID:         "group/project",
			wantAssertions:    0,
			wantFutureCounter: 0,
		},
		{
			name:              "github_legacy_transition_present",
			provider:          "github",
			projectID:         "owner/repo",
			rows:              []workItemMembershipS1Row{{Authorized: 1, ScopedPopulation: 1, AuthorizedPopulation: 1, TransitionAssertionCount: 3, FutureBoundaryCount: 1}},
			wantAssertions:    3,
			wantFutureCounter: 1,
		},
		{
			name:              "github_legacy_transition_absent",
			provider:          "github",
			projectID:         "owner/repo",
			wantAssertions:    0,
			wantFutureCounter: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(tc.rows...), workItemMembershipTestAnchor(t, tc.provider, tc.projectID), 200)
			if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredExcludedProvider || result.Census.TransitionAssertionCount != tc.wantAssertions || result.Census.FutureBoundaryCount != tc.wantFutureCounter || len(result.Members) != 0 {
				t.Fatalf("excluded provider result = %+v, want unmeasured with %d transition assertions, %d future boundaries, and no members", result, tc.wantAssertions, tc.wantFutureCounter)
			}
		})
	}
}

func TestWorkItemMembershipColumnArmExclusionUsesProviderAndProjectPrefix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		project  string
		excluded bool
	}{
		{name: "linear column", provider: "linear", project: "P1", excluded: false},
		{name: "gitlab always excluded", provider: "gitlab", project: "group/project", excluded: true},
		{name: "github legacy excluded", provider: "github", project: "owner/project", excluded: true},
		{name: "github v2 column allowed", provider: "github", project: "ghprojv2:owner/project", excluded: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := membershipColumnArmExcluded(tc.provider, tc.project); got != tc.excluded {
				t.Fatalf("membershipColumnArmExcluded(%q, %q) = %t, want %t", tc.provider, tc.project, got, tc.excluded)
			}
		})
	}
}

func TestWorkItemMembershipS1SelfTransitionDoesNotCreateAFutureBoundary(t *testing.T) {
	row := workItemMembershipS1Row{
		CanonicalID:              "work_item.v2:repo:self",
		RepoID:                   "repo",
		WorkItemID:               "self",
		Authorized:               1,
		ScopedPopulation:         1,
		AuthorizedPopulation:     1,
		TransitionAssertionCount: 1,
		FutureBoundaryCount:      0,
	}
	result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(row), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
	if result.Census.State != contextfabric.WorkItemMembershipCensusExact || result.Census.FutureBoundaryCount != 0 || result.Census.TransitionAssertionCount != 1 || len(result.Members) != 1 {
		t.Fatalf("first self-transition result = %+v, want one member, one transition assertion, and an explicit zero future-boundary count", result)
	}
}

func TestWorkItemMembershipS1SuccessfulEmptyIsMeasuredZero(t *testing.T) {
	client := &workItemMembershipFakeClient{scanErrAt: -1, rows: [][]any{workItemMembershipTestSentinelRow()}}
	telemetry := &workItemMembershipTelemetrySpy{}
	reader, _ := newWorkItemMembershipTestReader(t, client, telemetry)
	lease, result, err := reader.BeginWorkItemMembership(context.Background(), storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{
		Anchor: workItemMembershipTestAnchor(t, "linear", "P1"),
	})
	if err != nil || lease == nil {
		t.Fatalf("BeginWorkItemMembership returned lease=%v err=%v", lease, err)
	}
	defer lease.Release()
	if result.Census.State != contextfabric.WorkItemMembershipCensusExact || !result.Census.PopulationMeasured || result.Census.AuthorizedPopulation != 0 || result.Census.Limitation != "" {
		t.Fatalf("empty result = %+v, want measured exact zero without limitation", result)
	}
}

func TestWorkItemMembershipServeLimitUsesOnlyPositiveLowerCaps(t *testing.T) {
	for _, tc := range []struct {
		name       string
		planMax    int
		requestMax int
		want       int
	}{
		{name: "no caps", planMax: 0, requestMax: 0, want: contextfabric.WorkItemMembershipServeLimit},
		{name: "negative caps do not narrow", planMax: -1, requestMax: -1, want: contextfabric.WorkItemMembershipServeLimit},
		{name: "plan lower", planMax: 2, requestMax: 0, want: 2},
		{name: "request lower", planMax: 0, requestMax: 3, want: 3},
		{name: "request lower than plan", planMax: 7, requestMax: 3, want: 3},
		{name: "caps at fixed limit", planMax: contextfabric.WorkItemMembershipServeLimit, requestMax: contextfabric.WorkItemMembershipServeLimit, want: contextfabric.WorkItemMembershipServeLimit},
		{name: "caps above fixed limit", planMax: contextfabric.WorkItemMembershipServeLimit + 1, requestMax: contextfabric.WorkItemMembershipServeLimit + 2, want: contextfabric.WorkItemMembershipServeLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workItemMembershipServeLimit(tc.planMax, tc.requestMax); got != tc.want {
				t.Fatalf("workItemMembershipServeLimit(%d, %d) = %d, want %d", tc.planMax, tc.requestMax, got, tc.want)
			}
		})
	}
}

func TestWorkItemMembershipCountsFitAcceptsEachIntMaximum(t *testing.T) {
	maxInt := uint64(^uint(0) >> 1)
	for _, tc := range []struct {
		name string
		set  func(*workItemMembershipS1Row)
	}{
		{name: "scoped population", set: func(row *workItemMembershipS1Row) { row.ScopedPopulation = maxInt }},
		{name: "authorized population", set: func(row *workItemMembershipS1Row) { row.AuthorizedPopulation = maxInt }},
		{name: "denied population", set: func(row *workItemMembershipS1Row) { row.DeniedPopulation = maxInt }},
		{name: "future boundary count", set: func(row *workItemMembershipS1Row) { row.FutureBoundaryCount = maxInt }},
		{name: "transition assertion count", set: func(row *workItemMembershipS1Row) { row.TransitionAssertionCount = maxInt }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := workItemMembershipS1Row{}
			tc.set(&row)
			if !workItemMembershipCountsFit(row) {
				t.Fatalf("workItemMembershipCountsFit(%s) = false at the largest representable int", tc.name)
			}
		})
	}
}

func TestWorkItemMembershipCountsFitRejectsEachUintOverflow(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*workItemMembershipS1Row)
	}{
		{name: "scoped population", set: func(row *workItemMembershipS1Row) { row.ScopedPopulation = ^uint64(0) }},
		{name: "authorized population", set: func(row *workItemMembershipS1Row) { row.AuthorizedPopulation = ^uint64(0) }},
		{name: "denied population", set: func(row *workItemMembershipS1Row) { row.DeniedPopulation = ^uint64(0) }},
		{name: "future boundary count", set: func(row *workItemMembershipS1Row) { row.FutureBoundaryCount = ^uint64(0) }},
		{name: "transition assertion count", set: func(row *workItemMembershipS1Row) { row.TransitionAssertionCount = ^uint64(0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := workItemMembershipS1Row{}
			tc.set(&row)
			if workItemMembershipCountsFit(row) {
				t.Fatalf("workItemMembershipCountsFit(%s) = true for uint64 overflow", tc.name)
			}
		})
	}
}

func TestWorkItemMembershipS1RejectsMismatchedCountersIndividually(t *testing.T) {
	base := workItemMembershipS1Row{
		CanonicalID:              "work_item.v2:r:one",
		RepoID:                   "r",
		WorkItemID:               "one",
		Authorized:               1,
		ScopedPopulation:         2,
		AuthorizedPopulation:     2,
		DeniedPopulation:         0,
		FutureBoundaryCount:      1,
		TransitionAssertionCount: 2,
	}
	for _, tc := range []struct {
		name string
		set  func(*workItemMembershipS1Row)
	}{
		{name: "scoped population", set: func(row *workItemMembershipS1Row) { row.ScopedPopulation++ }},
		{name: "authorized population", set: func(row *workItemMembershipS1Row) { row.AuthorizedPopulation++ }},
		{name: "denied population", set: func(row *workItemMembershipS1Row) { row.DeniedPopulation++ }},
		{name: "future boundary count", set: func(row *workItemMembershipS1Row) { row.FutureBoundaryCount++ }},
		{name: "transition assertion count", set: func(row *workItemMembershipS1Row) { row.TransitionAssertionCount++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			second := base
			second.CanonicalID = "work_item.v2:r:two"
			second.WorkItemID = "two"
			tc.set(&second)
			result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(base, second), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
			if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.PopulationMeasured || len(result.Members) != 0 {
				t.Fatalf("counter mismatch %s result = %+v, want unmeasured with no members", tc.name, result)
			}
		})
	}
}

func TestWorkItemMembershipS1RejectsInvalidAuthorizedFlag(t *testing.T) {
	row := workItemMembershipS1Row{
		CanonicalID:          "work_item.v2:r:invalid-flag",
		RepoID:               "r",
		WorkItemID:           "invalid-flag",
		Authorized:           2,
		ScopedPopulation:     1,
		AuthorizedPopulation: 1,
	}
	result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(row), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
	if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || result.Census.PopulationMeasured || len(result.Members) != 0 {
		t.Fatalf("invalid authorized flag result = %+v, want s1_error and no members", result)
	}
}

func TestWorkItemMembershipS1RejectsMissingAuthorizedIdentityParts(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  workItemMembershipS1Row
	}{
		{
			name: "repo id",
			row: func() workItemMembershipS1Row {
				canonicalID, _, err := identity.Derive(identity.KindWorkItem, []string{"", "item"}, nil)
				if err != nil {
					t.Fatalf("derive empty-repo identity: %v", err)
				}
				return workItemMembershipS1Row{CanonicalID: canonicalID, WorkItemID: "item", Authorized: 1, ScopedPopulation: 1, AuthorizedPopulation: 1}
			}(),
		},
		{
			name: "work item id",
			row: func() workItemMembershipS1Row {
				canonicalID, _, err := identity.Derive(identity.KindWorkItem, []string{"repo", ""}, nil)
				if err != nil {
					t.Fatalf("derive empty-work-item identity: %v", err)
				}
				return workItemMembershipS1Row{CanonicalID: canonicalID, RepoID: "repo", Authorized: 1, ScopedPopulation: 1, AuthorizedPopulation: 1}
			}(),
		},
		{
			name: "canonical id",
			row:  workItemMembershipS1Row{RepoID: "repo", WorkItemID: "item", Authorized: 1, ScopedPopulation: 1, AuthorizedPopulation: 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(tc.row), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
			if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || result.Census.PopulationMeasured || len(result.Members) != 0 {
				t.Fatalf("missing %s result = %+v, want s1_error and no members", tc.name, result)
			}
		})
	}
}

func TestWorkItemMembershipS1DistinguishesIdentityOmissionFromMismatch(t *testing.T) {
	row := workItemMembershipS1Row{
		CanonicalID:          "work_item.v2:placeholder",
		RepoID:               strings.Repeat("r", identity.MaxNaturalKeyBytes),
		WorkItemID:           "item",
		Authorized:           1,
		ScopedPopulation:     1,
		AuthorizedPopulation: 1,
	}
	result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(row), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
	if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredIdentityOmitted || result.Census.PopulationMeasured || len(result.Members) != 0 {
		t.Fatalf("omitted identity result = %+v, want identity_omitted and no members", result)
	}
}

func TestWorkItemMembershipS1RejectsCanonicalIdentityMismatch(t *testing.T) {
	row := workItemMembershipS1Row{
		CanonicalID:          "work_item.v2:r:wrong",
		RepoID:               "r",
		WorkItemID:           "item",
		Authorized:           1,
		ScopedPopulation:     1,
		AuthorizedPopulation: 1,
	}
	result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(row), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
	if result.Census.State != contextfabric.WorkItemMembershipCensusUnmeasured || result.Census.UnmeasuredReason != contextfabric.WorkItemMembershipUnmeasuredS1Error || result.Census.PopulationMeasured || len(result.Members) != 0 {
		t.Fatalf("mismatched identity result = %+v, want s1_error and no members", result)
	}
}

func TestWorkItemMembershipS1KeepsExactCensusAtTheLimit(t *testing.T) {
	row := workItemMembershipS1Row{
		CanonicalID:          "work_item.v2:r:item",
		RepoID:               "r",
		WorkItemID:           "item",
		Authorized:           1,
		ScopedPopulation:     contextfabric.WorkItemMembershipCensusLimit,
		AuthorizedPopulation: contextfabric.WorkItemMembershipCensusLimit,
	}
	result := finalizeWorkItemMembershipS1(workItemMembershipRowsWithSentinel(row), workItemMembershipTestAnchor(t, "linear", "P1"), 200)
	if result.Census.State != contextfabric.WorkItemMembershipCensusExact || !result.Census.PopulationComplete || result.Census.PopulationIncomplete || result.Census.CappedPopulation != contextfabric.WorkItemMembershipCensusLimit || result.Census.AuthorizedPopulation != contextfabric.WorkItemMembershipCensusLimit {
		t.Fatalf("exact-limit result = %+v, want complete exact census at C", result)
	}
}

func TestWorkItemMembershipFixedBoundsRemainFinite(t *testing.T) {
	if contextfabric.WorkItemMembershipCensusLimit != 2000 {
		t.Fatalf("census limit = %d, want 2000", contextfabric.WorkItemMembershipCensusLimit)
	}
	if contextfabric.WorkItemMembershipServeLimit != 200 {
		t.Fatalf("serve limit = %d, want 200", contextfabric.WorkItemMembershipServeLimit)
	}
	if contextfabric.WorkItemMembershipMaxMemoryUsage != 64<<20 {
		t.Fatalf("memory limit = %d, want 64 MiB", contextfabric.WorkItemMembershipMaxMemoryUsage)
	}
}

func TestWorkItemMembershipAnchorSegmentsRejectsInvalidShape(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor contextfabric.WorkItemMembershipAnchor
	}{
		{
			name: "wrong subject kind",
			anchor: contextfabric.WorkItemMembershipAnchor{Subject: contextfabric.SubjectRef{
				Kind:        contextfabric.SubjectKind("member"),
				CanonicalID: "project.v2:linear:P1",
			}},
		},
		{
			name: "legacy id",
			anchor: contextfabric.WorkItemMembershipAnchor{Subject: contextfabric.SubjectRef{
				Kind:        contextfabric.SubjectProject,
				CanonicalID: "project-linear-P1",
			}},
		},
		{
			name: "wrong segment count",
			anchor: contextfabric.WorkItemMembershipAnchor{Subject: contextfabric.SubjectRef{
				Kind:        contextfabric.SubjectProject,
				CanonicalID: "project.v2:linear",
			}},
		},
		{
			name: "empty provider",
			anchor: contextfabric.WorkItemMembershipAnchor{Subject: contextfabric.SubjectRef{
				Kind:        contextfabric.SubjectProject,
				CanonicalID: "project.v2::P1",
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, projectID, err := contextfabric.WorkItemMembershipAnchorSegments(tc.anchor)
			if err == nil || provider != "" || projectID != "" {
				t.Fatalf("invalid anchor result = provider=%q project=%q err=%v, want empty segments and an error", provider, projectID, err)
			}
		})
	}
}
