package contextfabric

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkItemMembershipCensusLimit is the S1 census ceiling. S1 reads one row
// past this value so it can distinguish an exact population from a floor.
// It is deliberately independent of the response member cap.
const WorkItemMembershipCensusLimit = 2000

// WorkItemMembershipServeLimit is the largest member set this port serves.
// A request may narrow it further, but cannot widen it.
const WorkItemMembershipServeLimit = 200

// WorkItemMembershipMaxMemoryUsage is the proposed per-statement memory
// ceiling. It follows the shared ClickHouse reader default (64 MiB) while
// this PR keeps the value internal to the inactive port; no public setting is
// inferred from it.
const WorkItemMembershipMaxMemoryUsage uint64 = 64 << 20

// WorkItemMembershipCensusState says what S1 proved about the authorized
// population. Exact includes a measured zero. Floor means the census reached
// the cap and only proves a lower bound. Unmeasured means no population claim
// is safe.
type WorkItemMembershipCensusState string

const (
	WorkItemMembershipCensusExact      WorkItemMembershipCensusState = "exact"
	WorkItemMembershipCensusFloor      WorkItemMembershipCensusState = "floor"
	WorkItemMembershipCensusUnmeasured WorkItemMembershipCensusState = "unmeasured"
)

// WorkItemMembershipUnmeasuredReason is a closed server-side vocabulary. It
// never carries a ClickHouse error or request text.
type WorkItemMembershipUnmeasuredReason string

const (
	WorkItemMembershipUnmeasuredNone WorkItemMembershipUnmeasuredReason = ""
	// WorkItemMembershipUnmeasuredS1Error is the residual backend-error
	// reason: the S1 statement failed for a cause that is neither a query
	// resource budget (see WorkItemMembershipUnmeasuredReadLimitExceeded)
	// nor context cancellation (see WorkItemMembershipUnmeasuredCancelled).
	// It never carries the underlying error's own text.
	WorkItemMembershipUnmeasuredS1Error WorkItemMembershipUnmeasuredReason = "s1_error"
	// WorkItemMembershipUnmeasuredReadLimitExceeded (CHAOS-5991): the S1
	// statement's own query-resource budget (MaxRowsToRead/MaxMemoryUsage)
	// was exceeded before the statement could finish -- distinguished from
	// the generic s1_error so an operator can tell "the census outgrew its
	// own bound" from any other backend fault without reading the swallowed
	// ClickHouse exception text, which this vocabulary never carries.
	WorkItemMembershipUnmeasuredReadLimitExceeded WorkItemMembershipUnmeasuredReason = "read_limit_exceeded"
	// WorkItemMembershipUnmeasuredCancelled: the request's own
	// context ended (cancelled or its deadline expired) while S1 was in
	// flight -- a caller-side/deadline story, not a backend fault.
	WorkItemMembershipUnmeasuredCancelled              WorkItemMembershipUnmeasuredReason = "cancelled"
	WorkItemMembershipUnmeasuredExcludedProvider       WorkItemMembershipUnmeasuredReason = "excluded_provider"
	WorkItemMembershipUnmeasuredZeroAuthorizedOverflow WorkItemMembershipUnmeasuredReason = "zero_authorized_overflow"
	WorkItemMembershipUnmeasuredIdentityOmitted        WorkItemMembershipUnmeasuredReason = "identity_omitted"
)

// WorkItemMembershipAnchor is the project subject that supplies the
// provider-qualified project identity for the S1 join.
type WorkItemMembershipAnchor struct {
	Subject SubjectRef
}

// WorkItemMembershipRequest carries only resolved inputs. Question text is
// intentionally absent: natural-language interpretation ends before this
// bounded read port.
type WorkItemMembershipRequest struct {
	Anchor WorkItemMembershipAnchor

	// RequestedRepositoryScope is the raw, request-owned repository selector.
	// The adapter must derive the typed library scope from the current
	// authenticated Principal and this value through the one 5751 translation
	// seam. A caller cannot inject a stale or mismatched AuthorizationScope.
	RequestedRepositoryScope []string

	// PlanMaxMembers and RequestMaxMembers are resolved caps. Positive values
	// narrow the fixed serving limit; zero means that dimension added no cap.
	PlanMaxMembers    int
	RequestMaxMembers int

	// S1Instant is the census instant used for the future-boundary counter.
	// A zero value is filled by the adapter's clock before the statement runs.
	S1Instant time.Time
}

// WorkItemMembershipMember is one authorized canonical work-item identity.
// Repository slug is metadata for a later content read and is empty when the
// repository has no name.
type WorkItemMembershipMember struct {
	CanonicalID string
	RepoID      string
	WorkItemID  string
	RepoSlug    string
}

// WorkItemMembershipCensus is the server-side S1 measurement. Counts are
// meaningful only when PopulationMeasured is true, except CappedPopulation
// which records the rows observed before a floor or an unmeasured failure.
type WorkItemMembershipCensus struct {
	State                WorkItemMembershipCensusState
	PopulationMeasured   bool
	PopulationComplete   bool
	PopulationIncomplete bool
	CappedPopulation     int
	AuthorizedPopulation int
	DeniedPopulation     int
	// Paths splits the measured population by the authorization path that
	// admits each member and by the repo-less and project-less subsets of
	// the denied members. Meaningful exactly when the other counts are.
	Paths                    WorkItemMembershipPathCensus
	ServedMembers            int
	CensusLimit              int
	FutureBoundaryCount      int
	TransitionAssertionCount int
	UnmeasuredReason         WorkItemMembershipUnmeasuredReason
	Limitation               string
}

// WorkItemMembershipPathCensus is S1's per-path census over the same capped
// population as AuthorizedPopulation. A member two paths admit counts under
// both, so the four path counts may sum past AuthorizedPopulation; each is
// the population that path alone admits. RepoLess is every member with no
// real repository, RepoLessDenied the denied part of it, and
// DeniedProjectLess the denied members whose own row names no project --
// the population no project path can ever reach.
type WorkItemMembershipPathCensus struct {
	OrganizationGrant int
	DirectRepository  int
	ProjectOwnership  int
	PullRequestLink   int
	RepoLess          int
	RepoLessDenied    int
	DeniedProjectLess int
	// ExcludedExplicitTextLink and ExcludedHeuristicLink count the members
	// with a link to a granted repository that is evidence, not a grant: an
	// issue key found in pull-request text, and a time-window guess. Only a
	// provider-recorded (native) link authorizes; these disclose the rest.
	ExcludedExplicitTextLink int
	ExcludedHeuristicLink    int
}

// WorkItemMembershipGrantShape is the pre-entry shape of the repository
// grant S1 evaluates: organization-wide or not, how many exact and owner
// selectors, and whether the request narrowed it. Counts only; selector
// values never reach telemetry.
type WorkItemMembershipGrantShape struct {
	OrganizationWide   bool
	ExactSelectors     int
	OwnerSelectors     int
	RequestedSelectors bool
}

// WorkItemMembershipResult is the inactive PR2 seam result. A successful
// empty query returns State=exact and AuthorizedPopulation=0. A query or scan
// failure returns State=unmeasured with no members and the existing Context
// Fabric limitation string.
type WorkItemMembershipResult struct {
	Members []WorkItemMembershipMember
	Census  WorkItemMembershipCensus
}

// WorkItemMembershipLease must remain held until the complete future answer
// response finishes. The S1 read does not release it automatically.
type WorkItemMembershipLease struct {
	once    sync.Once
	release func()
}

// Release returns one admission permit. It is idempotent so an HTTP or MCP
// completion path can safely defer it while also releasing on a terminal
// response callback.
func (l *WorkItemMembershipLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
}

// WorkItemMembershipPort is the future wiring boundary. PR2 defines it but
// does not install it into Engine, the fact registry, or old callers.
type WorkItemMembershipPort interface {
	BeginWorkItemMembership(context.Context, storage.Principal, WorkItemMembershipRequest) (*WorkItemMembershipLease, WorkItemMembershipResult, error)
}

// WorkItemMembershipS1Event is the safe telemetry projection of one S1
// attempt. It carries counts and closed states only; it never carries query
// text, question text, canonical IDs, labels, or raw backend errors.
type WorkItemMembershipS1Event struct {
	State                    WorkItemMembershipCensusState
	Reason                   WorkItemMembershipUnmeasuredReason
	CappedPopulation         int
	AuthorizedPopulation     int
	DeniedPopulation         int
	Grant                    WorkItemMembershipGrantShape
	Paths                    WorkItemMembershipPathCensus
	ServedMembers            int
	CensusLimit              int
	FutureBoundaryCount      int
	TransitionAssertionCount int
	PopulationMeasured       bool
	PopulationComplete       bool
	MaxExecutionTimeSeconds  uint64
	MaxRowsToRead            uint64
	MaxMemoryUsage           uint64
	MaxResultRows            uint64
}

// WorkItemMembershipGateEvent records admission without content identity.
type WorkItemMembershipGateEvent struct {
	Outcome       string
	InFlight      int
	Queued        int
	MaxInFlight   int
	QueueCapacity int
}

// WorkItemMembershipTelemetry is optional at the type boundary, but the
// production adapter must receive the configured logger-backed implementation
// so required Info records reach the real collection path.
type WorkItemMembershipTelemetry interface {
	RecordWorkItemMembershipS1(context.Context, storage.Principal, WorkItemMembershipS1Event)
	RecordWorkItemMembershipGate(context.Context, storage.Principal, WorkItemMembershipGateEvent)
}

// NoopWorkItemMembershipTelemetry is explicit for tests and dormant wiring.
// Production composition should use NewSlogWorkItemMembershipTelemetry with
// the configured request logger.
type NoopWorkItemMembershipTelemetry struct{}

func (NoopWorkItemMembershipTelemetry) RecordWorkItemMembershipS1(context.Context, storage.Principal, WorkItemMembershipS1Event) {
}

func (NoopWorkItemMembershipTelemetry) RecordWorkItemMembershipGate(context.Context, storage.Principal, WorkItemMembershipGateEvent) {
}

// SlogWorkItemMembershipTelemetry sends the required Info records through a
// caller-provided logger. A nil logger is a deliberate no-op for dormant
// tests; it does not silently substitute a process-global logger.
type SlogWorkItemMembershipTelemetry struct {
	Logger *slog.Logger
}

func NewSlogWorkItemMembershipTelemetry(logger *slog.Logger) SlogWorkItemMembershipTelemetry {
	return SlogWorkItemMembershipTelemetry{Logger: logger}
}

func (t SlogWorkItemMembershipTelemetry) RecordWorkItemMembershipS1(ctx context.Context, principal storage.Principal, event WorkItemMembershipS1Event) {
	if t.Logger == nil {
		return
	}
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"state", SanitizeLogAttr(sanitizeWorkItemMembershipState(event.State)),
		"reason", SanitizeLogAttr(sanitizeWorkItemMembershipReason(event.Reason)),
		"population_measured", event.PopulationMeasured,
		"population_complete", event.PopulationComplete,
		"capped_population", event.CappedPopulation,
		"authorized_population", event.AuthorizedPopulation,
		"denied_population", event.DeniedPopulation,
		"grant_organization_wide", event.Grant.OrganizationWide,
		"grant_exact_selectors", event.Grant.ExactSelectors,
		"grant_owner_selectors", event.Grant.OwnerSelectors,
		"grant_requested_selectors", event.Grant.RequestedSelectors,
		"organization_grant_population", event.Paths.OrganizationGrant,
		"direct_repo_population", event.Paths.DirectRepository,
		"project_ownership_population", event.Paths.ProjectOwnership,
		"pr_link_population", event.Paths.PullRequestLink,
		"repo_less_population", event.Paths.RepoLess,
		"repo_less_denied_population", event.Paths.RepoLessDenied,
		"denied_project_less_population", event.Paths.DeniedProjectLess,
		"excluded_explicit_text_link_population", event.Paths.ExcludedExplicitTextLink,
		"excluded_heuristic_link_population", event.Paths.ExcludedHeuristicLink,
		"served_members", event.ServedMembers,
		"census_limit", event.CensusLimit,
		"future_boundary_count", event.FutureBoundaryCount,
		"transition_assertion_count", event.TransitionAssertionCount,
		"max_execution_time_seconds", event.MaxExecutionTimeSeconds,
		"max_rows_to_read", event.MaxRowsToRead,
		"max_memory_usage", event.MaxMemoryUsage,
		"max_result_rows", event.MaxResultRows,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.Logger.InfoContext(ctx, "context fabric work item membership s1", args...)
}

func (t SlogWorkItemMembershipTelemetry) RecordWorkItemMembershipGate(ctx context.Context, principal storage.Principal, event WorkItemMembershipGateEvent) {
	if t.Logger == nil {
		return
	}
	args := []any{
		"org_id", SanitizeLogAttr(principal.OrgID),
		"outcome", SanitizeLogAttr(event.Outcome),
		"in_flight", event.InFlight,
		"queued", event.Queued,
		"max_in_flight", event.MaxInFlight,
		"queue_capacity", event.QueueCapacity,
	}
	args = append(args, requestIDLogAttrs(ctx)...)
	t.Logger.InfoContext(ctx, "context fabric work item membership gate", args...)
}

func sanitizeWorkItemMembershipState(state WorkItemMembershipCensusState) string {
	switch state {
	case WorkItemMembershipCensusExact, WorkItemMembershipCensusFloor, WorkItemMembershipCensusUnmeasured:
		return string(state)
	default:
		return "unclassified"
	}
}

func sanitizeWorkItemMembershipReason(reason WorkItemMembershipUnmeasuredReason) string {
	switch reason {
	case WorkItemMembershipUnmeasuredNone,
		WorkItemMembershipUnmeasuredS1Error,
		WorkItemMembershipUnmeasuredReadLimitExceeded,
		WorkItemMembershipUnmeasuredCancelled,
		WorkItemMembershipUnmeasuredExcludedProvider,
		WorkItemMembershipUnmeasuredZeroAuthorizedOverflow,
		WorkItemMembershipUnmeasuredIdentityOmitted:
		return string(reason)
	default:
		return "unclassified"
	}
}

var (
	// ErrWorkItemMembershipQueueFull is returned before a query starts when
	// both permits and the bounded waiting queue are full.
	ErrWorkItemMembershipQueueFull = errors.New("work item membership admission queue is full")
	// ErrWorkItemMembershipGateInvalid identifies an invalid local gate
	// construction, never a request-derived refusal.
	ErrWorkItemMembershipGateInvalid = errors.New("work item membership gate configuration is invalid")
	// ErrWorkItemMembershipDeadlineTooShort is returned before S1 when the
	// remaining context deadline is below ClickHouse's one-second setting
	// granularity. No query is started in that state.
	ErrWorkItemMembershipDeadlineTooShort = errors.New("work item membership deadline is too short for a bounded S1 query")
)

// WorkItemMembershipLimitation returns the existing answer-facing limitation
// string. It does not mint a second limitation token for PR2.
func WorkItemMembershipLimitation() string {
	return factScopeUnexpandedLimitation
}

// WorkItemMembershipAnchorSegments validates and decodes an anchor without
// exposing any partial identity to a query or log.
func WorkItemMembershipAnchorSegments(anchor WorkItemMembershipAnchor) (provider, projectID string, err error) {
	if anchor.Subject.Kind != SubjectProject {
		return "", "", errors.New("work item membership anchor must be a project")
	}
	segments, ok := identity.Segments(identity.KindProject, anchor.Subject.CanonicalID)
	if !ok || len(segments) != 2 || strings.TrimSpace(segments[0]) == "" || strings.TrimSpace(segments[1]) == "" {
		return "", "", errors.New("work item membership anchor identity is invalid")
	}
	return segments[0], segments[1], nil
}
