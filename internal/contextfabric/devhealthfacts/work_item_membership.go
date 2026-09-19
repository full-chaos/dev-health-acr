package devhealthfacts

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

// classifyWorkItemMembershipS1Error maps an S1 query failure onto the
// closed WorkItemMembershipUnmeasuredReason vocabulary WITHOUT ever
// carrying the underlying error's own text -- that text is a
// ClickHouse exception message, which is never safe to log or persist
// verbatim (query fragments, backend detail). Precedence: a query-resource
// budget failure (the census's own MaxRowsToRead/MaxMemoryUsage bound) is
// checked FIRST via the shared, already-certified
// runtimeclickhouse.IsQueryBudgetExceeded classifier (the SAME one
// devhealthsource/assemble.go's error taxonomy already uses -- reused, not
// re-derived) so a caller can tell "the census outgrew its own bound" from
// any other backend fault; context cancellation/deadline is checked next,
// because that is the CALLER's story, not the backend's; anything else
// stays the pre-existing generic s1_error.
func classifyWorkItemMembershipS1Error(err error) contextfabric.WorkItemMembershipUnmeasuredReason {
	if runtimeclickhouse.IsQueryBudgetExceeded(err) {
		return contextfabric.WorkItemMembershipUnmeasuredReadLimitExceeded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contextfabric.WorkItemMembershipUnmeasuredCancelled
	}
	return contextfabric.WorkItemMembershipUnmeasuredS1Error
}

// WorkItemMembershipReader is the dormant PR2 S1 port. No registry or
// Engine constructor installs it yet; a later wiring change owns that switch.
// The reader owns one atomic membership census statement and returns a lease
// that the response owner must release after the complete answer finishes.
type WorkItemMembershipReader struct {
	client    contextpacket.ClickHouseQueryClient
	gate      *contextfabric.WorkItemMembershipGate
	telemetry contextfabric.WorkItemMembershipTelemetry
	now       func() time.Time
}

// WorkItemMembershipReaderOptions keeps the new port local and testable. The
// gate and telemetry are process-owned dependencies; no public runtime config
// is added by PR2.
type WorkItemMembershipReaderOptions struct {
	Gate      *contextfabric.WorkItemMembershipGate
	Telemetry contextfabric.WorkItemMembershipTelemetry
	Now       func() time.Time
}

// NewWorkItemMembershipReader builds the bounded reader against the actual
// ClickHouse client boundary used by devhealthfacts. Hosted tuple reuse uses
// this reader; fresh tuple dispatch remains a separate integration.
func NewWorkItemMembershipReader(client contextpacket.ClickHouseQueryClient, options WorkItemMembershipReaderOptions) (*WorkItemMembershipReader, error) {
	if client == nil {
		return nil, errors.New("work item membership reader: clickhouse query client is required")
	}
	gate := options.Gate
	if gate == nil {
		gate = defaultWorkItemMembershipProcessGate
	}
	if options.Telemetry == nil {
		return nil, errors.New("work item membership reader: telemetry is required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &WorkItemMembershipReader{
		client:    client,
		gate:      gate,
		telemetry: options.Telemetry,
		now:       now,
	}, nil
}

// BeginWorkItemMembership executes S1. Backend query, scan, and row-iteration
// failures discard every scanned member and return an unmeasured result. The
// returned lease is still held so the caller can keep the per-process bound
// through the future response completion.
func (r *WorkItemMembershipReader) BeginWorkItemMembership(ctx context.Context, principal storage.Principal, request contextfabric.WorkItemMembershipRequest) (returnedLease *contextfabric.WorkItemMembershipLease, membership contextfabric.WorkItemMembershipResult, returnedErr error) {
	if r == nil || r.client == nil || r.gate == nil {
		return nil, contextfabric.WorkItemMembershipResult{}, errors.New("work item membership reader is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	provider, projectID, err := contextfabric.WorkItemMembershipAnchorSegments(request.Anchor)
	if err != nil {
		return nil, contextfabric.WorkItemMembershipResult{}, err
	}

	lease, acquireErr := r.gate.Acquire(ctx)
	if acquireErr != nil {
		stats := r.gate.Stats()
		outcome := "refused"
		if errors.Is(acquireErr, context.Canceled) || errors.Is(acquireErr, context.DeadlineExceeded) {
			outcome = "context_canceled"
		}
		r.telemetry.RecordWorkItemMembershipGate(ctx, principal, contextfabric.WorkItemMembershipGateEvent{
			Outcome:       outcome,
			InFlight:      stats.InFlight,
			Queued:        stats.Queued,
			MaxInFlight:   stats.MaxInFlight,
			QueueCapacity: stats.QueueCapacity,
		})
		return nil, contextfabric.WorkItemMembershipResult{}, acquireErr
	}

	// Transfer admission before any S1 work or telemetry can panic. Raw
	// port callers own a returned lease; abnormal exits never hand one off.
	owner, owned := contextfabric.WorkItemResponseOwnerFromContext(ctx)
	if owned {
		if err := owner.Retain(lease); err != nil {
			stats := r.gate.Stats()
			outcome := "owner_lease_conflict"
			if errors.Is(err, contextfabric.ErrWorkItemResponseOwnerClosed) {
				outcome = "owner_closed"
			}
			r.telemetry.RecordWorkItemMembershipGate(ctx, principal, contextfabric.WorkItemMembershipGateEvent{
				Outcome: outcome, InFlight: stats.InFlight, Queued: stats.Queued,
				MaxInFlight: stats.MaxInFlight, QueueCapacity: stats.QueueCapacity,
			})
			return nil, contextfabric.WorkItemMembershipResult{}, err
		}
	}
	release := func() {
		if owned {
			owner.Release(lease)
		} else {
			lease.Release()
		}
	}
	defer func() {
		if !owned && returnedLease == nil {
			release()
		}
	}()

	s1Instant := request.S1Instant
	if s1Instant.IsZero() {
		s1Instant = r.now().UTC()
	} else {
		s1Instant = s1Instant.UTC()
	}
	k := workItemMembershipServeLimit(request.PlanMaxMembers, request.RequestMaxMembers)
	settings, settingsErr := workItemMembershipSettings(ctx, k)
	if settingsErr != nil {
		release()
		stats := r.gate.Stats()
		r.telemetry.RecordWorkItemMembershipGate(ctx, principal, contextfabric.WorkItemMembershipGateEvent{
			Outcome:       "deadline_too_short",
			InFlight:      stats.InFlight,
			Queued:        stats.Queued,
			MaxInFlight:   stats.MaxInFlight,
			QueueCapacity: stats.QueueCapacity,
		})
		return nil, contextfabric.WorkItemMembershipResult{}, settingsErr
	}
	requestedScope := append([]string(nil), request.RequestedRepositoryScope...)
	// 5751 owns the only raw ACR selector translation point. Derive the
	// library scope from the live principal and this request-owned selector at
	// S1 time; no caller can inject a stale typed grant. The same resulting
	// scope shape is used by the later S2/S3 readers on the stacked tip.
	authorization := workItemRepositoryAuthorization(principal, requestedScope)
	grant := workItemMembershipGrantShape(authorization)
	statement, extraBindings := workItemMembershipS1Statement(authorization, k)
	extraBindings = append(extraBindings,
		readers.Binding{Name: "anchor_provider", Value: provider},
		readers.Binding{Name: "anchor_project_id", Value: projectID},
		readers.Binding{Name: "s1_instant", Value: s1Instant},
		readers.Binding{Name: "serve_limit", Value: uint32(k)},
	)
	statement = readers.WithSettings(statement, settings)

	rows := make([]workItemMembershipS1Row, 0, k+1)
	queryErr := readers.QueryOrgScopedNamed(ctx, r.client, "ReadWorkItemMembershipS1", statement, principal.OrgID, nil, func(scanner readers.RowScanner) error {
		var row workItemMembershipS1Row
		if scanErr := scanner.Scan(
			&row.CanonicalID,
			&row.RepoID,
			&row.WorkItemID,
			&row.RepoSlug,
			&row.Authorized,
			&row.ScopedPopulation,
			&row.AuthorizedPopulation,
			&row.DeniedPopulation,
			&row.Paths.OrganizationGrant,
			&row.Paths.DirectRepository,
			&row.Paths.ProjectOwnership,
			&row.Paths.PullRequestLink,
			&row.Paths.RepoLess,
			&row.Paths.RepoLessDenied,
			&row.Paths.DeniedProjectLess,
			&row.Paths.ExcludedExplicitTextLink,
			&row.Paths.ExcludedHeuristicLink,
			&row.FutureBoundaryCount,
			&row.TransitionAssertionCount,
			&row.RowKind,
			&row.AnchorResolution,
		); scanErr != nil {
			return scanErr
		}
		rows = append(rows, row)
		return nil
	}, extraBindings...)
	if queryErr != nil {
		result := unmeasuredWorkItemMembershipResult(classifyWorkItemMembershipS1Error(queryErr))
		r.recordS1(ctx, principal, result, settings, grant)
		return lease, result, nil
	}

	result := finalizeWorkItemMembershipS1(rows, request.Anchor, k)
	r.recordS1(ctx, principal, result, settings, grant)
	return lease, result, nil
}

type workItemMembershipS1Row struct {
	CanonicalID              string
	RepoID                   string
	WorkItemID               string
	RepoSlug                 string
	Authorized               uint8
	ScopedPopulation         uint64
	AuthorizedPopulation     uint64
	DeniedPopulation         uint64
	Paths                    workItemMembershipPathPopulations
	FutureBoundaryCount      uint64
	TransitionAssertionCount uint64
	RowKind                  uint8
	AnchorResolution         uint8
}

// workItemMembershipPathPopulations are S1's per-path window counts, in the
// statement's column order (the library path order, then the three
// repo-less/project-less splits).
type workItemMembershipPathPopulations struct {
	OrganizationGrant uint64
	DirectRepository  uint64
	ProjectOwnership  uint64
	PullRequestLink   uint64
	RepoLess          uint64
	RepoLessDenied    uint64
	DeniedProjectLess uint64
	// The members with a non-authorizing link to a granted repository, by
	// link kind (an issue key in pull-request text; a time-window guess).
	ExcludedExplicitTextLink uint64
	ExcludedHeuristicLink    uint64
}

func (p workItemMembershipPathPopulations) fit() bool {
	const maxInt = int(^uint(0) >> 1)
	max := uint64(maxInt)
	return p.OrganizationGrant <= max && p.DirectRepository <= max && p.ProjectOwnership <= max &&
		p.PullRequestLink <= max && p.RepoLess <= max && p.RepoLessDenied <= max && p.DeniedProjectLess <= max &&
		p.ExcludedExplicitTextLink <= max && p.ExcludedHeuristicLink <= max
}

func (p workItemMembershipPathPopulations) census() contextfabric.WorkItemMembershipPathCensus {
	return contextfabric.WorkItemMembershipPathCensus{
		OrganizationGrant: boundedInt(p.OrganizationGrant),
		DirectRepository:  boundedInt(p.DirectRepository),
		ProjectOwnership:  boundedInt(p.ProjectOwnership),
		PullRequestLink:   boundedInt(p.PullRequestLink),
		RepoLess:          boundedInt(p.RepoLess),
		RepoLessDenied:    boundedInt(p.RepoLessDenied),
		DeniedProjectLess: boundedInt(p.DeniedProjectLess),

		ExcludedExplicitTextLink: boundedInt(p.ExcludedExplicitTextLink),
		ExcludedHeuristicLink:    boundedInt(p.ExcludedHeuristicLink),
	}
}

const (
	workItemMembershipRowMember         uint8 = 0
	workItemMembershipRowAnchorSentinel uint8 = 1
	workItemMembershipAnchorResolved    uint8 = 1
)

func workItemMembershipAnchorSentinelIsValid(row workItemMembershipS1Row) bool {
	return row.AnchorResolution == workItemMembershipAnchorResolved &&
		row.CanonicalID == "" &&
		row.RepoID == "" &&
		row.WorkItemID == "" &&
		row.RepoSlug == "" &&
		row.Authorized == 0 &&
		row.ScopedPopulation == 0 &&
		row.AuthorizedPopulation == 0 &&
		row.DeniedPopulation == 0 &&
		row.Paths == (workItemMembershipPathPopulations{}) &&
		row.FutureBoundaryCount == 0 &&
		row.TransitionAssertionCount == 0
}

const (
	workItemMembershipDefaultTimeout = 5 * time.Second
	// S1 shares the content readers' private resource class for threads and
	// memory, but NOT for MaxRowsToRead -- see workItemMembershipMaxRowsToRead
	// below, which is deliberately its own, much larger bound.
	workItemMembershipMaxThreads     = workItemReaderMaxThreads
	workItemMembershipMaxMemoryUsage = workItemReaderMaxMemoryUsage
)

// workItemMembershipMaxRowsToRead is S1's OWN row-read bound, not the shared
// workItemReaderMaxRowsToRead (8192, sized for a bounded per-page content
// read keyed by an explicit id list, single table, no JOIN -- confirmed from
// dev-health-go/readers.workItemReadStatement's own source). S1's statement
// is structurally a WHOLE-POPULATION CENSUS: every relation it joins
// (project_membership_presence, work_items FINAL, the transition-metadata
// window subquery, resolved_projects) is read in full before WHERE/LIMIT
// narrow it, and ClickHouse's MaxRowsToRead counts that PRE-FILTER,
// PRE-FINAL-merge physical read (workitem_scope.go's own comment already
// says this).
//
// CHAOS-5991, root cause proven live against the trial store (EXPLAIN
// indexes=1 + system.query_log, never inferred): org/project-scoped
// primary-key pruning is already applied correctly at every base table
// (EXPLAIN's PrimaryKey Condition binds org_id exactly), but the read still
// exceeded 8192 -- the shared bound, which happens to equal every touched
// table's own index_granularity -- because (a) a table smaller than one
// granule reads as a whole regardless of predicate selectivity, and (b) the
// query plan reads several relations MORE THAN ONCE (a
// ClickHouse-optimizer-introduced anti-join pass, a runtime-filter build,
// and the join itself, each a separate ReadFromMergeTree).
//
// DERIVED, not an arbitrary constant picked by feel. The REAL production statement
// (workItemMembershipS1Statement), run against the trial store for a real
// 1675-work-item project, measured via system.query_log: 32,504 rows read,
// across 17 ReadFromMergeTree passes (EXPLAIN indexes=1), with the library
// authorization relation's two organization-wide aggregates joined in
// (project ownership and linked pull requests; 14,063 rows and 9 passes
// before them). Every touched table is smaller than one 8192-row granule
// there, so each pass reads its table whole -- the granule floor. Scaling that measured cost linearly with
// population (primary-key pruning already proven correct, so a well-pruned
// org-scoped read grows with THAT org's own row count, not the shared
// table's) to a large real organization's project (100,000 work items, ~60x
// this trial project's size): 32,504 x 60 = 1,950,240 rows; doubled for
// safety margin (multiple projects sharing the touched tables, plan
// variance) = 3,900,480, rounded up to the clean value below. Re-derive by the same method (real
// statement, real system.query_log, a real project near the new headroom
// target) if this table's shape or the query's own pass count ever changes.
//
// A package-level VAR, not a const, so an integration test can lower it
// (restored via t.Cleanup) to prove -- against a REAL ClickHouse container,
// not a mock -- that exceeding it emits the read_limit_exceeded reason on
// the real trace line. Production composition (NewWorkItemMembershipReader)
// never overrides it.
var workItemMembershipMaxRowsToRead = uint64(4_000_000)

func workItemMembershipSettings(ctx context.Context, k int) (readers.Settings, error) {
	seconds := uint64(workItemMembershipDefaultTimeout / time.Second)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < time.Second {
			return readers.Settings{}, contextfabric.ErrWorkItemMembershipDeadlineTooShort
		}
		seconds = uint64(remaining / time.Second)
	}
	return readers.Settings{
		MaxExecutionTimeSeconds: seconds,
		MaxRowsToRead:           workItemMembershipMaxRowsToRead,
		MaxMemoryUsage:          workItemMembershipMaxMemoryUsage,
		MaxThreads:              workItemMembershipMaxThreads,
		MaxResultRows:           uint64(k + 1),
	}, nil
}

func workItemMembershipServeLimit(planMax, requestMax int) int {
	k := contextfabric.WorkItemMembershipServeLimit
	if planMax > 0 && planMax < k {
		k = planMax
	}
	if requestMax > 0 && requestMax < k {
		k = requestMax
	}
	return k
}

func unmeasuredWorkItemMembershipResult(reason contextfabric.WorkItemMembershipUnmeasuredReason) contextfabric.WorkItemMembershipResult {
	return contextfabric.WorkItemMembershipResult{
		Census: contextfabric.WorkItemMembershipCensus{
			State:              contextfabric.WorkItemMembershipCensusUnmeasured,
			PopulationMeasured: false,
			CensusLimit:        contextfabric.WorkItemMembershipCensusLimit,
			UnmeasuredReason:   reason,
			Limitation:         contextfabric.WorkItemMembershipLimitation(),
		},
	}
}

func finalizeWorkItemMembershipS1(rows []workItemMembershipS1Row, anchor contextfabric.WorkItemMembershipAnchor, k int) contextfabric.WorkItemMembershipResult {
	provider, projectID, err := contextfabric.WorkItemMembershipAnchorSegments(anchor)
	if err != nil {
		return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
	}
	// The statement appends one identity-free row that carries the anchor
	// resolution state. It is deliberately separate from membership rows so a
	// valid empty result remains measurable while an ambiguous or missing
	// project cannot collapse into an exact zero. The sentinel is part of the
	// result protocol: a stream without exactly one valid sentinel is not a
	// successful S1 result.
	actualRows := make([]workItemMembershipS1Row, 0, len(rows))
	anchorSentinelSeen := false
	for _, row := range rows {
		switch row.RowKind {
		case workItemMembershipRowMember:
			if row.AnchorResolution != workItemMembershipAnchorResolved {
				return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
			}
			actualRows = append(actualRows, row)
		case workItemMembershipRowAnchorSentinel:
			if anchorSentinelSeen || !workItemMembershipAnchorSentinelIsValid(row) {
				return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
			}
			anchorSentinelSeen = true
		default:
			return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
		}
	}
	if !anchorSentinelSeen {
		return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
	}
	rows = actualRows

	if membershipColumnArmExcluded(provider, projectID) {
		return unmeasuredWorkItemMembershipResultWithAssertions(contextfabric.WorkItemMembershipUnmeasuredExcludedProvider, rows)
	}

	result := contextfabric.WorkItemMembershipResult{
		Census: contextfabric.WorkItemMembershipCensus{
			State:              contextfabric.WorkItemMembershipCensusExact,
			PopulationMeasured: true,
			PopulationComplete: true,
			CensusLimit:        contextfabric.WorkItemMembershipCensusLimit,
		},
	}
	if len(rows) == 0 {
		return result
	}

	first := rows[0]
	if !workItemMembershipCountsFit(first) {
		return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
	}
	result.Census.CappedPopulation = boundedInt(first.ScopedPopulation)
	result.Census.AuthorizedPopulation = boundedInt(first.AuthorizedPopulation)
	result.Census.DeniedPopulation = boundedInt(first.DeniedPopulation)
	result.Census.Paths = first.Paths.census()
	result.Census.FutureBoundaryCount = boundedInt(first.FutureBoundaryCount)
	result.Census.TransitionAssertionCount = boundedInt(first.TransitionAssertionCount)
	for _, row := range rows {
		if row.Authorized > 1 || row.ScopedPopulation != first.ScopedPopulation || row.AuthorizedPopulation != first.AuthorizedPopulation || row.DeniedPopulation != first.DeniedPopulation || row.Paths != first.Paths || row.FutureBoundaryCount != first.FutureBoundaryCount || row.TransitionAssertionCount != first.TransitionAssertionCount {
			return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
		}
	}
	if result.Census.CappedPopulation > contextfabric.WorkItemMembershipCensusLimit {
		result.Census.CappedPopulation = contextfabric.WorkItemMembershipCensusLimit + 1
	}
	if result.Census.AuthorizedPopulation > contextfabric.WorkItemMembershipCensusLimit {
		result.Census.AuthorizedPopulation = contextfabric.WorkItemMembershipCensusLimit + 1
	}

	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Authorized == 0 {
			continue
		}
		if row.RepoID == "" || row.WorkItemID == "" || row.CanonicalID == "" {
			return unmeasuredWorkItemMembershipResult(contextfabric.WorkItemMembershipUnmeasuredS1Error)
		}
		canonicalID, omitted, deriveErr := identity.Derive(identity.KindWorkItem, []string{row.RepoID, row.WorkItemID}, nil)
		if deriveErr != nil || omitted || canonicalID != row.CanonicalID {
			reason := contextfabric.WorkItemMembershipUnmeasuredS1Error
			if omitted {
				reason = contextfabric.WorkItemMembershipUnmeasuredIdentityOmitted
			}
			return unmeasuredWorkItemMembershipResult(reason)
		}
		if _, duplicate := seen[canonicalID]; duplicate {
			continue
		}
		seen[canonicalID] = struct{}{}
		result.Members = append(result.Members, contextfabric.WorkItemMembershipMember{
			CanonicalID: canonicalID,
			RepoID:      row.RepoID,
			WorkItemID:  row.WorkItemID,
			RepoSlug:    row.RepoSlug,
		})
	}
	sort.Slice(result.Members, func(i, j int) bool {
		return result.Members[i].CanonicalID < result.Members[j].CanonicalID
	})
	if len(result.Members) > k {
		result.Members = result.Members[:k]
	}
	result.Census.ServedMembers = len(result.Members)
	if result.Census.AuthorizedPopulation >= contextfabric.WorkItemMembershipCensusLimit+1 {
		result.Census.State = contextfabric.WorkItemMembershipCensusFloor
		result.Census.PopulationComplete = false
		result.Census.PopulationIncomplete = true
	}
	if result.Census.AuthorizedPopulation == 0 && result.Census.CappedPopulation >= contextfabric.WorkItemMembershipCensusLimit+1 {
		return unmeasuredWorkItemMembershipResultWithAssertions(contextfabric.WorkItemMembershipUnmeasuredZeroAuthorizedOverflow, rows)
	}
	return result
}

func unmeasuredWorkItemMembershipResultWithAssertions(reason contextfabric.WorkItemMembershipUnmeasuredReason, rows []workItemMembershipS1Row) contextfabric.WorkItemMembershipResult {
	result := unmeasuredWorkItemMembershipResult(reason)
	for _, row := range rows {
		if row.RowKind != workItemMembershipRowMember {
			continue
		}
		result.Census.TransitionAssertionCount = boundedInt(row.TransitionAssertionCount)
		result.Census.FutureBoundaryCount = boundedInt(row.FutureBoundaryCount)
		result.Census.CappedPopulation = boundedInt(row.ScopedPopulation)
		result.Census.DeniedPopulation = boundedInt(row.DeniedPopulation)
		result.Census.Paths = row.Paths.census()
		break
	}
	return result
}

func boundedInt(value uint64) int {
	const maxInt = int(^uint(0) >> 1)
	if value > uint64(maxInt) {
		return 0
	}
	return int(value)
}

func workItemMembershipCountsFit(row workItemMembershipS1Row) bool {
	const maxInt = int(^uint(0) >> 1)
	max := uint64(maxInt)
	return row.ScopedPopulation <= max &&
		row.AuthorizedPopulation <= max &&
		row.DeniedPopulation <= max &&
		row.FutureBoundaryCount <= max &&
		row.TransitionAssertionCount <= max &&
		row.Paths.fit()
}

func membershipColumnArmExcluded(provider, projectID string) bool {
	if provider == "gitlab" {
		return true
	}
	return provider == "github" && !strings.HasPrefix(projectID, "ghprojv2:")
}

// workItemMembershipGrantShape is the pre-entry shape of the grant S1
// evaluates: whether it is organization-wide and how many exact and owner
// selectors it carries, plus whether the request narrowed it. Counts only;
// the selector values never reach the trace.
func workItemMembershipGrantShape(scope readers.AuthorizationScope) contextfabric.WorkItemMembershipGrantShape {
	if scope.RepositorySelectors == nil {
		return contextfabric.WorkItemMembershipGrantShape{}
	}
	granted := scope.RepositorySelectors.Granted
	return contextfabric.WorkItemMembershipGrantShape{
		OrganizationWide:   granted.All,
		ExactSelectors:     len(granted.ExactSlugs),
		OwnerSelectors:     len(granted.Owners),
		RequestedSelectors: scope.RepositorySelectors.Requested != nil,
	}
}

func (r *WorkItemMembershipReader) recordS1(ctx context.Context, principal storage.Principal, result contextfabric.WorkItemMembershipResult, settings readers.Settings, grant contextfabric.WorkItemMembershipGrantShape) {
	event := contextfabric.WorkItemMembershipS1Event{
		State:                    result.Census.State,
		Reason:                   result.Census.UnmeasuredReason,
		CappedPopulation:         result.Census.CappedPopulation,
		AuthorizedPopulation:     result.Census.AuthorizedPopulation,
		DeniedPopulation:         result.Census.DeniedPopulation,
		Grant:                    grant,
		Paths:                    result.Census.Paths,
		ServedMembers:            result.Census.ServedMembers,
		CensusLimit:              result.Census.CensusLimit,
		FutureBoundaryCount:      result.Census.FutureBoundaryCount,
		TransitionAssertionCount: result.Census.TransitionAssertionCount,
		PopulationMeasured:       result.Census.PopulationMeasured,
		PopulationComplete:       result.Census.PopulationComplete,
		MaxExecutionTimeSeconds:  settings.MaxExecutionTimeSeconds,
		MaxRowsToRead:            settings.MaxRowsToRead,
		MaxMemoryUsage:           settings.MaxMemoryUsage,
		MaxResultRows:            settings.MaxResultRows,
	}
	r.telemetry.RecordWorkItemMembershipS1(ctx, principal, event)
}

// workItemMembershipTransitionMetadataSQL derives the boundary status of the
// latest transition assertion for each current (subject, project) row. The
// presence view remains the asserted-member source; this relation only adds
// metadata for its future-boundary counter in the same statement.
//
// It mirrors the established transition history rule: self transitions are
// retained as additions for alignment with the presence view but are kept in
// a separate history partition, a duplicate ADD is not a new boundary, and a
// dangling REMOVE cannot create one. One ARRAY JOIN unpivots both transition
// sides from one FINAL scan. The outer is_add=1 filter retains independent
// additions (including self and duplicate rows) while excluding every REMOVE;
// therefore a dangling_flag term in the boundary expression would be
// redundant and is deliberately absent.
const workItemMembershipTransitionMetadataSQL = `(
  WITH unpivoted AS (
    SELECT org_id, subject_kind, repo_id, subject_id, provider,
      occurred_at, event_id,
      touch.1 AS project_id,
      touch.2 AS is_add,
      touch.3 AS boundary_candidate
    FROM project_membership_transitions FINAL
    ARRAY JOIN arrayFilter(
      pair -> pair.4 = 1,
      [
        (to_project_id, toUInt8(1), toUInt8(to_project_id != from_project_id), toUInt8(to_project_id != '')),
        (from_project_id, toUInt8(0), toUInt8(1), toUInt8(from_project_id != '' AND from_project_id != to_project_id))
      ]
    ) AS touch
    WHERE org_id = {org_id:String} AND subject_kind = 'work_item'
  ),
  classified AS (
    SELECT org_id, subject_kind, repo_id, subject_id, provider, project_id,
      occurred_at, event_id, is_add, boundary_candidate,
      (boundary_candidate = 1 AND is_add = 1 AND lagInFrame(is_add, 1, 2) OVER history = 1) AS dup_flag
    FROM unpivoted
    WINDOW history AS (
      PARTITION BY org_id, subject_kind, repo_id, subject_id, provider, project_id, boundary_candidate
      ORDER BY occurred_at, event_id
      ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING
    )
  ),
  SELECT org_id, subject_kind, repo_id, subject_id, project_id,
    argMax(provider, (occurred_at, event_id)) AS provider,
    argMax(occurred_at, (occurred_at, event_id)) AS observed_at,
    argMax(toUInt8(boundary_candidate = 1 AND NOT dup_flag), (occurred_at, event_id)) AS is_boundary
  FROM classified
  WHERE is_add = 1
  GROUP BY org_id, subject_kind, repo_id, subject_id, project_id
)`

// workItemMembershipS1Statement is the one atomic census and selection
// statement. The library-owned WorkItemScopeSQL result is inserted in the
// same relation for the S1 authorization mask. The later S2/S3 carrier will
// invoke the same library renderer from the stacked content-reader change.
func workItemMembershipS1Statement(scope readers.AuthorizationScope, k int) (string, []readers.Binding) {
	rendered := readers.WorkItemScopeSQL(scope)
	resolvedProjects := `(
  SELECT provider, id, join_key, count() OVER (PARTITION BY provider, join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT provider, id, join_key
    FROM (
      SELECT provider, id, id AS join_key
      FROM projects FINAL
      WHERE org_id = {org_id:String}
      UNION ALL
      SELECT provider, id, ifNull(project_key, '') AS join_key
      FROM projects FINAL
      WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
  LIMIT 1 BY provider, join_key
)`
	// Keep anchor resolution inside the atomic S1 statement. The sentinel row
	// below is the only result for a valid empty census or an unresolved
	// anchor, so finalization can distinguish measured zero from fail-closed
	// ambiguity without a second query.
	anchorResolution := `(
  SELECT toUInt8(if(countIf(
    provider = {anchor_provider:String}
    AND join_key = {anchor_project_id:String}
    AND id = {anchor_project_id:String}
    AND key_resolution_count = 1
  ) = 1, 1, 0)) AS anchor_resolved
  FROM ` + resolvedProjects + `
)`
	from := `FROM project_membership_presence AS m
INNER JOIN work_items AS w FINAL
  ON m.org_id = w.org_id AND m.repo_id = w.repo_id AND m.subject_id = w.work_item_id AND m.provider = w.provider`
	if rendered.JoinSQL != "" {
		from += "\n" + rendered.JoinSQL
	} else {
		// The ID-only scope has no selector join in the library result. S1
		// still projects the repository slug for the later content seam, so
		// retain the same organization-qualified metadata relation here.
		from += "\nLEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id"
	}
	from += `
INNER JOIN ` + resolvedProjects + ` AS p ON p.provider = m.provider AND p.join_key = m.project_id
LEFT JOIN ` + workItemMembershipTransitionMetadataSQL + ` AS tm
  ON tm.org_id = m.org_id
  AND tm.subject_kind = m.subject_kind
  AND tm.repo_id = m.repo_id
  AND tm.subject_id = m.subject_id
  AND tm.provider = m.provider
  AND tm.project_id = m.project_id
  AND tm.observed_at = m.observed_at`

	canonicalKey := `concat('work_item.v2:', replaceAll(replaceAll(toString(w.repo_id), '%', '%25'), ':', '%3A'), ':', replaceAll(replaceAll(w.work_item_id, '%', '%25'), ':', '%3A'))`
	memberRows := `
  SELECT
    ` + canonicalKey + ` AS canonical_key,
    toString(w.repo_id) AS repo_id,
    w.work_item_id AS work_item_id,
    ifNull(r.repo, '') AS repo_slug,
    toUInt8(if(` + rendered.AuthorizationExpr + `, 1, 0)) AS authorized_flag,
` + workItemMembershipPathFlagsSQL(rendered) + `
    toUInt8(NOT (toString(w.repo_id) != '' AND toString(w.repo_id) != '` + zeroRepositoryID + `')) AS repo_less,
    toUInt8(w.project_id = '') AS project_less,
    toUInt8(has(` + workItemMembershipExcludedLinksSQL(rendered) + `, 'explicit_text')) AS excluded_explicit_text_link,
    toUInt8(has(` + workItemMembershipExcludedLinksSQL(rendered) + `, 'heuristic')) AS excluded_heuristic_link,
    countIf(m.source = 'transition') AS transition_assertions,
    countIf(m.source = 'transition' AND m.observed_at > {s1_instant:DateTime64(6, 'UTC')} AND ifNull(tm.is_boundary, toUInt8(0)) = 1) AS future_boundaries
  ` + from + `
  WHERE m.org_id = {org_id:String}
    AND m.subject_kind = 'work_item'
    AND p.provider = {anchor_provider:String}
    AND p.id = {anchor_project_id:String}
    AND p.key_resolution_count = 1
  GROUP BY canonical_key, repo_id, work_item_id, repo_slug, authorized_flag,
    ` + workItemMembershipPathColumns("path_") + `, repo_less, project_less, excluded_explicit_text_link, excluded_heuristic_link`

	selectedRows := `(
  SELECT
    if(authorized_flag = 1, canonical_key, '') AS canonical_id,
    if(authorized_flag = 1, repo_id, '') AS repo_id,
    if(authorized_flag = 1, work_item_id, '') AS work_item_id,
    if(authorized_flag = 1, repo_slug, '') AS repo_slug,
    authorized_flag,
    scoped_population,
    authorized_population,
    denied_population,
    ` + workItemMembershipPathColumns("", "_population") + `,
    repo_less_population,
    repo_less_denied_population,
    denied_project_less_population,
    excluded_explicit_text_link_population,
    excluded_heuristic_link_population,
    future_boundary_count,
    transition_assertion_count
  FROM (
    SELECT
      canonical_key,
      repo_id,
      work_item_id,
      repo_slug,
      authorized_flag,
      ` + workItemMembershipPathColumns("path_") + `,
      repo_less,
      project_less,
      excluded_explicit_text_link,
      excluded_heuristic_link,
      transition_assertions,
      future_boundaries,
      count() OVER () AS scoped_population,
      countIf(authorized_flag = 1) OVER () AS authorized_population,
      countIf(authorized_flag = 0) OVER () AS denied_population,
` + workItemMembershipPathWindowsSQL() + `
      countIf(repo_less = 1) OVER () AS repo_less_population,
      countIf(repo_less = 1 AND authorized_flag = 0) OVER () AS repo_less_denied_population,
      countIf(project_less = 1 AND authorized_flag = 0) OVER () AS denied_project_less_population,
      countIf(excluded_explicit_text_link = 1) OVER () AS excluded_explicit_text_link_population,
      countIf(excluded_heuristic_link = 1) OVER () AS excluded_heuristic_link_population,
      sum(future_boundaries) OVER () AS future_boundary_count,
      sum(transition_assertions) OVER () AS transition_assertion_count
    FROM (
      SELECT
        canonical_key,
        repo_id,
        work_item_id,
        repo_slug,
        authorized_flag,
        ` + workItemMembershipPathColumns("path_") + `,
        repo_less,
        project_less,
        excluded_explicit_text_link,
        excluded_heuristic_link,
        transition_assertions,
        future_boundaries
      FROM (` + memberRows + `)
      ORDER BY authorized_flag DESC, canonical_key ASC
      LIMIT 2001
    ) AS capped_members
  )
  ORDER BY authorized_flag DESC, canonical_id ASC
  LIMIT {serve_limit:UInt32}
)`

	memberAndSentinelRows := `(
	  SELECT selected.canonical_id,
	    selected.repo_id,
	    selected.work_item_id,
	    selected.repo_slug,
	    selected.authorized_flag,
	    selected.scoped_population,
	    selected.authorized_population,
	    selected.denied_population,
	    ` + workItemMembershipPathColumns("selected.", "_population") + `,
	    selected.repo_less_population,
	    selected.repo_less_denied_population,
	    selected.denied_project_less_population,
	    selected.excluded_explicit_text_link_population,
	    selected.excluded_heuristic_link_population,
	    selected.future_boundary_count,
	    selected.transition_assertion_count,
	    toUInt8(0) AS row_kind
	  FROM ` + selectedRows + ` AS selected
	  UNION ALL
	  SELECT
	    '' AS canonical_id,
	    '' AS repo_id,
	    '' AS work_item_id,
	    '' AS repo_slug,
	    toUInt8(0) AS authorized_flag,
	    toUInt64(0) AS scoped_population,
	    toUInt64(0) AS authorized_population,
	    toUInt64(0) AS denied_population,
	    ` + workItemMembershipPathZeroColumnsSQL() + `,
	    toUInt64(0) AS repo_less_population,
	    toUInt64(0) AS repo_less_denied_population,
	    toUInt64(0) AS denied_project_less_population,
	    toUInt64(0) AS excluded_explicit_text_link_population,
	    toUInt64(0) AS excluded_heuristic_link_population,
	    toUInt64(0) AS future_boundary_count,
	    toUInt64(0) AS transition_assertion_count,
	    toUInt8(1) AS row_kind
 )`

	// Join the anchor state after the member/sentinel UNION so one successful
	// stream carries exactly one shared resolution value and one sentinel.
	statement := `SELECT
	  result_rows.canonical_id,
	  result_rows.repo_id,
	  result_rows.work_item_id,
	  result_rows.repo_slug,
	  result_rows.authorized_flag,
	  result_rows.scoped_population,
	  result_rows.authorized_population,
	  result_rows.denied_population,
	  ` + workItemMembershipPathColumns("result_rows.", "_population") + `,
	  result_rows.repo_less_population,
	  result_rows.repo_less_denied_population,
	  result_rows.denied_project_less_population,
	  result_rows.excluded_explicit_text_link_population,
	  result_rows.excluded_heuristic_link_population,
	  result_rows.future_boundary_count,
	  result_rows.transition_assertion_count,
	  result_rows.row_kind,
	  anchor_state.anchor_resolved AS anchor_resolution
FROM ` + memberAndSentinelRows + ` AS result_rows
CROSS JOIN ` + anchorResolution + ` AS anchor_state
ORDER BY row_kind ASC, authorized_flag DESC, canonical_id ASC`
	return statement, rendered.Bindings
}

// workItemMembershipPathFlagsSQL projects one 0/1 flag per authorization
// path, in the library's fixed path order, from the same rendered scope
// whose AuthorizationExpr decides authorized_flag. A scope with no path
// expressions (the ID-only mode) projects zero for every path, so the census
// never reports a path it did not evaluate.
func workItemMembershipPathFlagsSQL(rendered readers.WorkItemScopeSQLResult) string {
	exprs := make(map[string]string, len(rendered.Provenance))
	for _, path := range rendered.Provenance {
		exprs[path.Path] = path.Expr
	}
	var b strings.Builder
	for _, path := range readers.WorkItemAuthorizationPaths() {
		expr, ok := exprs[path]
		if !ok {
			expr = "0"
		}
		b.WriteString("    toUInt8(if(" + expr + ", 1, 0)) AS path_" + path + ",\n")
	}
	return b.String()
}

// workItemMembershipPathColumns lists one column per authorization path as
// prefix+path+suffix, comma-separated, in the library's fixed order.
func workItemMembershipPathColumns(prefix string, suffix ...string) string {
	tail := strings.Join(suffix, "")
	names := make([]string, 0, len(readers.WorkItemAuthorizationPaths()))
	for _, path := range readers.WorkItemAuthorizationPaths() {
		names = append(names, prefix+path+tail)
	}
	return strings.Join(names, ", ")
}

// workItemMembershipPathWindowsSQL counts, over the whole capped census,
// the members each path authorizes. A member admitted by two paths counts
// in both, so the path counts may sum past authorized_population; each is
// the population that path alone would admit.
func workItemMembershipPathWindowsSQL() string {
	var b strings.Builder
	for _, path := range readers.WorkItemAuthorizationPaths() {
		b.WriteString("      countIf(path_" + path + " = 1) OVER () AS " + path + "_population,\n")
	}
	return b.String()
}

// workItemMembershipExcludedLinksSQL is the library's excluded-link
// disclosure for the row: the kinds of non-authorizing links that name a
// granted repository for a repo-less item. A scope without one (ID-only)
// discloses nothing.
func workItemMembershipExcludedLinksSQL(rendered readers.WorkItemScopeSQLResult) string {
	if rendered.ExcludedLinkProvenancesExpr == "" {
		return "CAST([], 'Array(String)')"
	}
	return rendered.ExcludedLinkProvenancesExpr
}

func workItemMembershipPathZeroColumnsSQL() string {
	names := make([]string, 0, len(readers.WorkItemAuthorizationPaths()))
	for _, path := range readers.WorkItemAuthorizationPaths() {
		names = append(names, "toUInt64(0) AS "+path+"_population")
	}
	return strings.Join(names, ", ")
}

var _ contextfabric.WorkItemMembershipPort = (*WorkItemMembershipReader)(nil)
