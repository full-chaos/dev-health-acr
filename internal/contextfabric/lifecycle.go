package contextfabric

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// This file is CHAOS-3898 S2a's domain surface: the per-org graph lifecycle
// state machine (design brief v4.1 §3.1, §3.5), its durable per-epoch
// retire records (v4.1 F3), per-source build completion (§3.3), and the
// KeyResolver/telemetry ports the falkorgraph adapter and the (future,
// S2/conversion-slice) retire scheduler consume. pglifecycle is the
// production Postgres implementation; see that package's doc comment for
// the CAS SQL shape.
//
// Nothing in this file changes production behavior by itself: every
// existing organization has no row in the backing store, and every port
// here is optional/nil-safe at its consumer (falkorgraph.Config.EpochResolver),
// degrading to the pre-CHAOS-3898 single, unsuffixed graph key -- see
// LifecycleStatusServing's doc comment.

var (
	// ErrLifecycleConflict identifies a lifecycle (or retire-record) CAS
	// transition that lost a race: the row's (status, active_epoch) --
	// or, for a retire record, its state -- no longer matched what the
	// caller expected. Exactly one concurrent transition wins any race
	// (design brief §3.5); every loser observes this and must re-read
	// before retrying, never blindly retry the same expectation.
	ErrLifecycleConflict = errors.New("context fabric graph lifecycle transition conflict")
	// ErrLifecycleNotFound identifies a lookup for an organization with no
	// lifecycle row -- the correct, common state for every organization
	// that has never begun a build (see OrgGraphLifecycle's own doc
	// comment: absence means "serving epoch 0, the legacy key", not an
	// error condition for a resolver, but Get itself reports it plainly
	// rather than synthesizing a row, so callers that DO need to
	// distinguish "never migrated" from "found" can.
	ErrLifecycleNotFound = errors.New("context fabric graph lifecycle: organization has no lifecycle row")
	// ErrLifecycleTransitionRefused identifies a transition that is
	// structurally illegal regardless of race -- e.g. rollback attempted
	// outside grace, or begin_retire attempted before the grace deadline
	// without an explicit force. Distinct from ErrLifecycleConflict: a
	// conflict means "retry against fresh state might succeed"; a refusal
	// means "this call can never succeed as shaped".
	ErrLifecycleTransitionRefused = errors.New("context fabric graph lifecycle transition refused")
)

// LifecycleStatus is the org lifecycle row's own state (design brief §3.5).
// Deliberately only three values: epoch disposal after grace ends (by
// either rollback or begin_retire) is tracked entirely by EpochRetirement
// records, never by a fourth org-level "retiring" status -- the org
// resumes ordinary serving the instant grace ends either way.
type LifecycleStatus string

const (
	// LifecycleStatusServing is the steady state: reads and writes both
	// resolve ActiveEpoch. An organization with NO lifecycle row is
	// equivalent to LifecycleStatusServing at ActiveEpoch 0 (the legacy,
	// unsuffixed graphKey(prefix, orgID)) -- this is what makes adopting
	// the pointer require zero migration for an existing organization.
	LifecycleStatusServing LifecycleStatus = "serving"
	// LifecycleStatusBuilding: a build is open at TargetEpoch. ActiveEpoch
	// still serves every read; ordinary projection ticks write the FULL
	// snapshot into TargetEpoch's own key and checkpoint set. The old
	// graph is untouched and complete throughout.
	LifecycleStatusBuilding LifecycleStatus = "building"
	// LifecycleStatusGrace: the flip already happened -- ActiveEpoch is
	// the NEW epoch, GraceEpoch is the old one, retained until
	// GraceDeadline. Rollback is legal ONLY in this state.
	LifecycleStatusGrace LifecycleStatus = "grace"
)

// BuildCompletionMode is the per-source completion shape a source reports
// for one (org, epoch) build (design brief §3.3/item 5: the four shapes a
// coordinator's ProjectionRun enumeration flags must surface). "pending" is
// the default a required source starts in when begin_build freezes
// RequiredSources -- a source that can never report exhaustion stays
// "pending" forever, which is deliberate: the flip gate requires every
// required source to reach a non-pending mode, so a source that cannot
// prove it MUST fail the gate rather than silently pass (design brief §9
// item 3).
type BuildCompletionMode string

const (
	BuildCompletionPending          BuildCompletionMode = "pending"
	BuildCompletionPagedFinal       BuildCompletionMode = "paged_final"
	BuildCompletionEmptyFirstTick   BuildCompletionMode = "empty_first_tick"
	BuildCompletionDisabledAtFreeze BuildCompletionMode = "disabled_at_freeze"
	BuildCompletionCursorExhausted  BuildCompletionMode = "cursor_exhausted"
)

// RetireReason names why a durable EpochRetirement record exists (design
// brief v4.1 F3): the two -- and only two -- paths that create one.
type RetireReason string

const (
	// RetireReasonGraceExpired: begin_retire created this record for the
	// OLD epoch a normal flip left behind, once its grace window ended
	// without a rollback.
	RetireReasonGraceExpired RetireReason = "grace_expired"
	// RetireReasonRollbackAbandoned: rollback created this record for the
	// NEWER epoch it just abandoned. Rollback is permanently disabled for
	// an epoch holding one of these records -- it can never be
	// re-activated or re-allocated (design brief §3.1 step 1's monotonic
	// allocator is what guarantees the epoch NUMBER is never reused
	// either).
	RetireReasonRollbackAbandoned RetireReason = "rollback_abandoned"
)

// RetireRecordState is one EpochRetirement's own small state machine,
// draining -> deleting -> deleted, each transition its own CAS. The retire
// executor is the only writer that ever advances it, and the only caller
// permitted to issue GRAPH.DELETE for the epoch's key.
type RetireRecordState string

const (
	RetireRecordDraining RetireRecordState = "draining"
	RetireRecordDeleting RetireRecordState = "deleting"
	RetireRecordDeleted  RetireRecordState = "deleted"
)

// OrgGraphLifecycle is one organization's durable lifecycle row (design
// brief §3.1's pointer + §3.5's lifecycle row -- the same row).
type OrgGraphLifecycle struct {
	OrgID string
	// ActiveEpoch is the epoch every read resolves today, regardless of
	// Status. 0 is the legacy, unsuffixed graph key.
	ActiveEpoch int64
	// LastAllocatedEpoch is the monotonic allocator (design brief P3):
	// independent of ActiveEpoch, bumped by BeginBuild, NEVER decremented
	// or reused. A build/rollback/build cycle always yields
	// ActiveEpoch+2 relative to where it started.
	LastAllocatedEpoch int64
	Status             LifecycleStatus
	// TargetEpoch is set only while Status == LifecycleStatusBuilding.
	TargetEpoch *int64
	// GraceEpoch is set only while Status == LifecycleStatusGrace: the
	// OLD epoch retained pending rollback or begin_retire.
	GraceEpoch *int64
	// RequiredSources is frozen by BeginBuild from the coordinator's
	// configured source set; the flip gate checks every name here against
	// BuildSourceProgress for TargetEpoch. Set only while Status ==
	// LifecycleStatusBuilding.
	RequiredSources []string
	// GraceDeadline is set only while Status == LifecycleStatusGrace.
	GraceDeadline *time.Time
	UpdatedAt     time.Time
}

// EpochRetirement is one durable per-epoch retire record (design brief
// v4.1 F3).
type EpochRetirement struct {
	OrgID      string
	Epoch      int64
	Reason     RetireReason
	DrainStart time.Time
	State      RetireRecordState
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// BuildSourceProgress is one (org, epoch, source)'s completion report
// (design brief §5b cf_build_source_progress).
type BuildSourceProgress struct {
	OrgID          string
	Epoch          int64
	Source         string
	CompletionMode BuildCompletionMode
	RowsProjected  int64
	UpdatedAt      time.Time
}

// GraphLifecycleStore is the CHAOS-3898 S2a lifecycle CAS machinery's
// Postgres-backed port. pglifecycle.Store is the production implementation.
// Every mutating method is a compare-and-swap: it fails with
// ErrLifecycleConflict when the row no longer matches the caller's stated
// expectation, and with ErrLifecycleTransitionRefused when the request is
// structurally illegal regardless of the row's current state (e.g. a
// rollback requested outside grace).
type GraphLifecycleStore interface {
	// Get reads orgID's lifecycle row. found=false with a nil error means
	// no row exists -- equivalent to LifecycleStatusServing at ActiveEpoch
	// 0 (see OrgGraphLifecycle's doc comment); it is the normal state for
	// an organization that has never begun a build, not an error.
	Get(ctx context.Context, orgID string) (row OrgGraphLifecycle, found bool, err error)

	// BeginBuild is the serving -> building CAS transition (design brief
	// §3.1 step 1-2). It allocates target = LastAllocatedEpoch + 1,
	// durably bumps LastAllocatedEpoch to match, freezes requiredSources,
	// and creates the row if none exists yet (an absent row is
	// LifecycleStatusServing at ActiveEpoch 0 -- BeginBuild's implicit
	// "expected" state). Refuses (ErrLifecycleTransitionRefused) if a
	// build or grace window is already open for this organization.
	BeginBuild(ctx context.Context, orgID string, requiredSources []string, now time.Time) (OrgGraphLifecycle, error)

	// RecordSourceProgress upserts one (org, epoch, source)'s completion
	// report. Never itself a lifecycle transition -- Flip is what reads
	// this table to decide whether every required source has completed.
	RecordSourceProgress(ctx context.Context, orgID string, epoch int64, source string, mode BuildCompletionMode, rowsProjected int64, now time.Time) error

	// SourceProgress reads every recorded (org, epoch, *) row, for the
	// flip gate and for cf_build_source_progress telemetry.
	SourceProgress(ctx context.Context, orgID string, epoch int64) ([]BuildSourceProgress, error)

	// Flip is the building -> grace CAS transition (design brief §3.1
	// step 3), gated by per-source completion: it refuses
	// (ErrLifecycleTransitionRefused) unless every name in the row's
	// RequiredSources has a BuildSourceProgress entry for TargetEpoch
	// whose CompletionMode is not BuildCompletionPending. On success,
	// ActiveEpoch becomes the (pre-flip) TargetEpoch, GraceEpoch becomes
	// the pre-flip ActiveEpoch, GraceDeadline is now+graceWindow, and
	// TargetEpoch/RequiredSources are cleared.
	Flip(ctx context.Context, orgID string, expectedTargetEpoch int64, graceWindow time.Duration, now time.Time) (OrgGraphLifecycle, error)

	// Rollback is the grace -> serving CAS transition restoring
	// GraceEpoch (design brief §3.1 step 4). Legal ONLY while
	// Status == LifecycleStatusGrace; refuses
	// (ErrLifecycleTransitionRefused) otherwise. In the SAME durable
	// write it creates a RetireReasonRollbackAbandoned EpochRetirement
	// for the epoch just abandoned (the pre-rollback ActiveEpoch), with
	// DrainStart = now (v4.1 F3: the drain clock starts at rollback, not
	// at the original flip).
	Rollback(ctx context.Context, orgID string, expectedActiveEpoch int64, now time.Time) (OrgGraphLifecycle, error)

	// BeginRetire is the grace -> serving CAS transition that forecloses
	// rollback (design brief §3.1 step 5, §3.5): the point of no return.
	// Legal ONLY while Status == LifecycleStatusGrace AND
	// (now >= GraceDeadline OR force); refuses
	// (ErrLifecycleTransitionRefused) otherwise. Creates a
	// RetireReasonGraceExpired EpochRetirement for GraceEpoch, with
	// DrainStart = now.
	BeginRetire(ctx context.Context, orgID string, expectedActiveEpoch int64, now time.Time, force bool) (OrgGraphLifecycle, EpochRetirement, error)

	// DrainingRetirements lists every EpochRetirement in
	// RetireRecordDraining state whose DrainStart is at or before cutoff
	// (i.e. drain_start + lease + deadline <= now, computed by the
	// caller) -- the retire executor's work queue. Never itself mutates
	// anything.
	DrainingRetirements(ctx context.Context, cutoff time.Time) ([]EpochRetirement, error)

	// AdvanceRetirement is one EpochRetirement's own CAS
	// (draining -> deleting -> deleted). The retire executor is the only
	// caller; RetireRecordDeleted must be written only after the actual
	// GRAPH.DELETE (and checkpoint-set delete) succeeded.
	AdvanceRetirement(ctx context.Context, orgID string, epoch int64, expected, next RetireRecordState, now time.Time) (EpochRetirement, error)
}

// OrgEpochResolver is the KeyResolver port the falkorgraph adapter consumes
// at its six graph-key call sites (design brief §3.1's "KeyResolver...
// replaces the six inline derivations"). It resolves EPOCH NUMBERS only --
// the falkorgraph package alone knows how to turn an epoch into the actual
// graphKey() string (prefix + org digest + epoch suffix), so that format
// never leaks into this backend-neutral port.
//
// nil is a valid, common configuration (falkorgraph.Config.EpochResolver's
// own doc comment): every call site falls back to epoch 0 -- byte-identical
// to pre-CHAOS-3898 behavior -- exactly the same "optional dependency,
// absent means degrade" convention IdentityUniverse/CensusFunc already use
// on that Config.
type OrgEpochResolver interface {
	// ResolveActiveEpoch returns orgID's current ActiveEpoch (0 for an
	// organization with no lifecycle row). Used by every read call site
	// (ResolveSubjects, DiscoverContext) and by steady-state writes
	// (ApplyProjectionBatch, ProjectionWatermark, PurgeOrganization) when
	// no build is open.
	ResolveActiveEpoch(ctx context.Context, orgID string) (epoch int64, err error)
	// ResolveBuildEpoch returns orgID's open TargetEpoch and ok=true only
	// while Status == LifecycleStatusBuilding; ok=false (with a nil
	// error) means no build is open, and the caller must fall back to
	// ResolveActiveEpoch.
	ResolveBuildEpoch(ctx context.Context, orgID string) (epoch int64, ok bool, err error)
}

// GraphKeyRole is the CHAOS-3898 §2.0 (v4.1 F2) role vocabulary a resolved
// graph key is stamped with: divergence is defined as two DIFFERENT keys
// observed for the SAME (org, epoch, role) -- not merely "two keys exist
// for one org", which is the normal shape of a healthy build (active-epoch
// reads coexisting with target-epoch writes).
type GraphKeyRole string

const (
	GraphKeyRoleInvestigationRead GraphKeyRole = "investigation_read"
	GraphKeyRoleProjectionWrite   GraphKeyRole = "projection_write"
	GraphKeyRoleReuseRecheck      GraphKeyRole = "reuse_recheck"
	GraphKeyRoleRetireTarget      GraphKeyRole = "retire_target"
)

// RetireGuardVerdict is the CHAOS-3898 §5b cf_epoch_retire guard-verdict
// enum: every reason the retire executor's final safety check (the
// isSweepTargetSafe shape, plus the drain-bound check) can refuse a
// GRAPH.DELETE, plus "ok" for the one case it proceeds.
type RetireGuardVerdict string

const (
	RetireGuardOK                  RetireGuardVerdict = "ok"
	RetireGuardRefusedActiveKey    RetireGuardVerdict = "refused_active_key"
	RetireGuardRefusedState        RetireGuardVerdict = "refused_state"
	RetireGuardRefusedDrainPending RetireGuardVerdict = "refused_drain_pending"
	RetireGuardRefusedUnderivable  RetireGuardVerdict = "refused_underivable"
)

// LifecycleTransition names one of the five lifecycle-row/retire-record CAS
// transitions, for cf_lifecycle_cas_conflict's losing-transition field
// (design brief v4.1 F6).
type LifecycleTransition string

const (
	LifecycleTransitionBeginBuild  LifecycleTransition = "begin_build"
	LifecycleTransitionFlip        LifecycleTransition = "flip"
	LifecycleTransitionRollback    LifecycleTransition = "rollback"
	LifecycleTransitionBeginRetire LifecycleTransition = "begin_retire"
	LifecycleTransitionRetireDone  LifecycleTransition = "retire_done"
)

// CheckpointEpochState is cf_checkpoint_epoch_state's per-(org, epoch)
// enum (design brief §3.4/§5b).
type CheckpointEpochState string

const (
	CheckpointEpochActive    CheckpointEpochState = "active"
	CheckpointEpochBuilding  CheckpointEpochState = "building"
	CheckpointEpochFrozen    CheckpointEpochState = "frozen"
	CheckpointEpochAbandoned CheckpointEpochState = "abandoned"
)

// GraphLifecycleTelemetry is the CHAOS-3898 §5b signal sink: every
// lifecycle transition and every guard emits a signal, live BEFORE any
// organization's first flip (design brief v4.1 F4's instrument-before-flip
// deploy sub-order). Required-method interface, same discipline as
// falkorgraph.GraphTelemetry's own doc comment: NoopGraphLifecycleTelemetry
// exists for callers that genuinely want no signals, so declining is
// explicit, never an accidental nil.
//
// STANDING RULE (design brief §5b): every field is a count, enum, id,
// hash, or bool -- never corpus text, never unhashed term identity. org_id
// is an opaque internal identifier (the same convention every existing
// Context Fabric telemetry sink already uses), not a credential or
// evidence body.
type GraphLifecycleTelemetry interface {
	// RecordResolvedGraphKey is cf_resolved_graph_key: the opaque derived
	// key stamped on every projection write batch and every investigation
	// graph read, keyed (org, epoch, role).
	RecordResolvedGraphKey(ctx context.Context, orgID string, epoch int64, role GraphKeyRole, key string)
	// RecordGraphKeyDivergence is cf_graph_key_divergence: fired when TWO
	// DIFFERENT keys are observed for the SAME (org, epoch, role) -- see
	// GraphKeyRole's doc comment for why that is the definition, not
	// merely "two keys exist for one org".
	RecordGraphKeyDivergence(ctx context.Context, orgID string, epoch int64, role GraphKeyRole)
	// RecordStartupPrefixAssertion is the §2.0 startup/config assertion's
	// own signal: ok=false means the resolved prefix was empty (a
	// deployment misconfiguration caught before any graph call).
	RecordStartupPrefixAssertion(ctx context.Context, ok bool)
	// RecordEpochFlip is cf_epoch_flip.
	RecordEpochFlip(ctx context.Context, orgID string, fromEpoch, toEpoch int64, buildDuration time.Duration, sourcesCompleted int)
	// RecordEpochRollback is cf_epoch_rollback, including the
	// projection-policy replay outcome (design brief §3.4).
	RecordEpochRollback(ctx context.Context, orgID string, fromEpoch, toEpoch int64, graceRemaining time.Duration)
	// RecordEpochRetire is cf_epoch_retire.
	RecordEpochRetire(ctx context.Context, orgID string, epoch int64, verdict RetireGuardVerdict, drainWait time.Duration)
	// RecordLifecycleCASConflict is cf_lifecycle_cas_conflict (v4.1 F6):
	// the LOSING transition plus the winner/current-state enum observed
	// at failure -- both together are what identify the colliding
	// transition PAIR, not just that someone lost.
	RecordLifecycleCASConflict(ctx context.Context, orgID string, losing LifecycleTransition, observedStatus LifecycleStatus)
	// RecordCheckpointEpochState is cf_checkpoint_epoch_state, per (org,
	// epoch).
	RecordCheckpointEpochState(ctx context.Context, orgID string, epoch int64, state CheckpointEpochState, cursorAge time.Duration)
	// RecordBuildSourceProgress is cf_build_source_progress, per (org,
	// epoch, source).
	RecordBuildSourceProgress(ctx context.Context, orgID string, epoch int64, source string, mode BuildCompletionMode, rowsProjected int64)
}

// EpochGraphDeleter is the retire executor's graph-deletion port (design
// brief §3.5's terminal GRAPH.DELETE action). falkorgraph.Adapter is the
// production implementation. epoch and activeEpoch are both required
// explicitly -- never re-derived internally -- so a caller cannot forget
// the safety comparison: implementations MUST refuse (without deleting
// anything) whenever epoch == activeEpoch (the isSweepTargetSafe shape:
// the key to delete must not be the currently active/serving key).
// Epoch 0 (the legacy, unsuffixed key) is NOT otherwise special: an
// organization's first-ever flip (0 -> 1) legitimately retires epoch 0's
// graph exactly like any later abandoned epoch, once it is no longer
// ActiveEpoch. Because graphKeyForEpoch is injective in epoch (the "-eN"
// suffix, or its absence, never collides across distinct epoch values for
// the same org), epoch != activeEpoch is exactly the string-key-inequality
// check isSweepTargetSafe performs, without needing to expose derived key
// text outside the falkorgraph package.
type EpochGraphDeleter interface {
	DeleteEpochGraph(ctx context.Context, orgID string, epoch, activeEpoch int64) error
}

// EpochCheckpointDeleter is the retire executor's checkpoint-set deletion
// port (design brief §3.4's "delete-together promise" -- an epoch's graph
// and its checkpoint set are torn down together). pgprojection.CheckpointStore
// is the production implementation.
type EpochCheckpointDeleter interface {
	DeleteEpochCheckpoints(ctx context.Context, orgID string, epoch int64) error
}

// NoopGraphLifecycleTelemetry discards every signal.
type NoopGraphLifecycleTelemetry struct{}

func (NoopGraphLifecycleTelemetry) RecordResolvedGraphKey(context.Context, string, int64, GraphKeyRole, string) {
}
func (NoopGraphLifecycleTelemetry) RecordGraphKeyDivergence(context.Context, string, int64, GraphKeyRole) {
}
func (NoopGraphLifecycleTelemetry) RecordStartupPrefixAssertion(context.Context, bool) {}
func (NoopGraphLifecycleTelemetry) RecordEpochFlip(context.Context, string, int64, int64, time.Duration, int) {
}
func (NoopGraphLifecycleTelemetry) RecordEpochRollback(context.Context, string, int64, int64, time.Duration) {
}
func (NoopGraphLifecycleTelemetry) RecordEpochRetire(context.Context, string, int64, RetireGuardVerdict, time.Duration) {
}
func (NoopGraphLifecycleTelemetry) RecordLifecycleCASConflict(context.Context, string, LifecycleTransition, LifecycleStatus) {
}
func (NoopGraphLifecycleTelemetry) RecordCheckpointEpochState(context.Context, string, int64, CheckpointEpochState, time.Duration) {
}
func (NoopGraphLifecycleTelemetry) RecordBuildSourceProgress(context.Context, string, int64, string, BuildCompletionMode, int64) {
}

var _ GraphLifecycleTelemetry = NoopGraphLifecycleTelemetry{}

// SlogGraphLifecycleTelemetry is the production GraphLifecycleTelemetry:
// structured operational logs through log/slog, mirroring
// falkorgraph.SlogTelemetry's own conventions exactly (same package, same
// "log the org_id, an internal identifier -- never text, never a
// credential" posture; see that type's doc comment).
//
// Design brief v4.1 F4's instrument-before-flip deploy sub-order is why
// this exists and is wired into every composition root NOW, in this
// slice, even though nothing in S2a yet drives a real BeginBuild/Flip:
// cf_resolved_graph_key already fires on every graph call today (stamped
// at epoch 0 for every organization, since Config.EpochResolver stays nil
// in every production composition root this slice ships) -- so the signal
// pipeline is proven live and observable in production BEFORE the
// follow-up slice wires the first organization's actual build/flip.
type SlogGraphLifecycleTelemetry struct{ Logger *slog.Logger }

func (t SlogGraphLifecycleTelemetry) logger() *slog.Logger {
	if t.Logger != nil {
		return t.Logger
	}
	return slog.Default()
}

func (t SlogGraphLifecycleTelemetry) RecordResolvedGraphKey(_ context.Context, orgID string, epoch int64, role GraphKeyRole, key string) {
	t.logger().Debug("context_fabric: resolved graph key", "org_id", orgID, "epoch", epoch, "role", string(role), "key", key)
}

func (t SlogGraphLifecycleTelemetry) RecordGraphKeyDivergence(_ context.Context, orgID string, epoch int64, role GraphKeyRole) {
	t.logger().Error("context_fabric: graph key divergence detected -- two different keys observed for the same (org, epoch, role)",
		"org_id", orgID, "epoch", epoch, "role", string(role))
}

func (t SlogGraphLifecycleTelemetry) RecordStartupPrefixAssertion(_ context.Context, ok bool) {
	if ok {
		t.logger().Debug("context_fabric: startup graph key prefix assertion passed")
		return
	}
	t.logger().Error("context_fabric: startup graph key prefix assertion FAILED -- resolved prefix is empty")
}

func (t SlogGraphLifecycleTelemetry) RecordEpochFlip(_ context.Context, orgID string, fromEpoch, toEpoch int64, buildDuration time.Duration, sourcesCompleted int) {
	t.logger().Info("context_fabric: graph epoch flip",
		"org_id", orgID, "from_epoch", fromEpoch, "to_epoch", toEpoch,
		"build_duration_ms", buildDuration.Milliseconds(), "sources_completed", sourcesCompleted)
}

func (t SlogGraphLifecycleTelemetry) RecordEpochRollback(_ context.Context, orgID string, fromEpoch, toEpoch int64, graceRemaining time.Duration) {
	t.logger().Warn("context_fabric: graph epoch rollback",
		"org_id", orgID, "from_epoch", fromEpoch, "to_epoch", toEpoch, "grace_remaining_ms", graceRemaining.Milliseconds())
}

// RecordEpochRetire logs at Warn for any refused_* verdict (the guard is
// the race being caught, worth an operator's attention) and Info for ok
// (routine, expected teardown).
func (t SlogGraphLifecycleTelemetry) RecordEpochRetire(_ context.Context, orgID string, epoch int64, verdict RetireGuardVerdict, drainWait time.Duration) {
	fields := []any{"org_id", orgID, "epoch", epoch, "verdict", string(verdict), "drain_wait_ms", drainWait.Milliseconds()}
	if verdict == RetireGuardOK {
		t.logger().Info("context_fabric: graph epoch retired", fields...)
		return
	}
	t.logger().Warn("context_fabric: graph epoch retire guard refused", fields...)
}

func (t SlogGraphLifecycleTelemetry) RecordLifecycleCASConflict(_ context.Context, orgID string, losing LifecycleTransition, observedStatus LifecycleStatus) {
	t.logger().Info("context_fabric: lifecycle CAS conflict",
		"org_id", orgID, "losing_transition", string(losing), "observed_status", string(observedStatus))
}

func (t SlogGraphLifecycleTelemetry) RecordCheckpointEpochState(_ context.Context, orgID string, epoch int64, state CheckpointEpochState, cursorAge time.Duration) {
	t.logger().Debug("context_fabric: checkpoint epoch state",
		"org_id", orgID, "epoch", epoch, "state", string(state), "cursor_age_seconds", cursorAge.Seconds())
}

func (t SlogGraphLifecycleTelemetry) RecordBuildSourceProgress(_ context.Context, orgID string, epoch int64, source string, mode BuildCompletionMode, rowsProjected int64) {
	t.logger().Debug("context_fabric: build source progress",
		"org_id", orgID, "epoch", epoch, "source", source, "completion_mode", string(mode), "rows_projected", rowsProjected)
}

var _ GraphLifecycleTelemetry = SlogGraphLifecycleTelemetry{}
