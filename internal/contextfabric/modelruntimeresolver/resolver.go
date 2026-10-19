// Package modelruntimeresolver resolves a per-request
// contextfabric.ModelRuntime from the authenticated principal's
// organization (CHAOS-3775): an organization with a stored BYO LLM
// configuration gets its own runtime; an organization with none uses the
// deployment default; an organization whose stored configuration cannot be
// used (a broken credential, a decryption failure) gets ErrModelUnavailable
// scoped to that organization alone -- it NEVER falls back to the
// deployment default's credential (TRD §19.3.3's explicit prohibition,
// restated as AC-3775-3).
//
// Resolver itself implements contextfabric.ModelRuntime, so it drops into
// exactly the same seam the deployment-default runtime already occupies
// (RuntimeQuestionInterpreter.Runtime / RuntimeAnswerSynthesizer.Runtime) --
// no caller outside this package needs to know per-organization resolution
// exists.
//
// Caching (AC-3775-2, AC-3775-5): constructing a contextfabric.ModelRuntime
// means initializing a genkit.Genkit instance, which is not free, so
// Resolver caches the constructed runtime (or construction failure) per
// organization, keyed by the stored configuration's Generation -- a
// table-wide monotonic sequence value from the store, not a wall-clock
// timestamp (Codex round-1 finding F3: two upserts landing in the same
// clock tick, or a clock stepping backward, would be indistinguishable
// under a timestamp key and could pin a stale runtime). Every request
// still does one cheap config lookup+decrypt (via
// contextfabric.OrgModelConfigResolver) to compare Generation; only a
// *change* triggers a rebuild. This gives AC-3775-5 ("changing the
// configuration ... affects the next request without a restart") without
// paying genkit-init cost on every request. A cached construction FAILURE
// is also kept -- rebuilding a broken credential's runtime on every request
// would turn one org's outage into a genkit-init cost multiplied by its
// request volume -- and is invalidated the same way, by Generation: an
// operator fixing the credential and re-saving is what clears it, not time.
// A canceled/deadline-exceeded build is the one exception: see
// runtimeFor's isTransientBuildError guard (Codex round-2 finding M1).
//
// Cache writes are guarded by the generation-monotonic rule. Two builds for
// DIFFERENT generations of the same organization can finish out of order
// (e.g. an UPDATE lands mid-build, so an old-generation build finishes after
// a newer-generation build has already cached its result). A write is
// refused whenever the entry already cached for this organization has a
// Generation strictly newer than the one this build was for. Without it the
// stale write is harmless from a SERVED-answer perspective (Generation still
// mismatches on the next read, forcing a fresh rebuild), but it is a wasted
// rebuild every time it happens.
package modelruntimeresolver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"golang.org/x/sync/singleflight"
)

// Build constructs a contextfabric.ModelRuntime from a resolved
// per-organization configuration. See BuildFunc in provider.go for the
// production implementation over modelprovider.New; tests supply a fake.
type Build func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error)

type cacheEntry struct {
	generation int64
	runtime    contextfabric.ModelRuntime
	err        error
}

type buildResult struct {
	runtime contextfabric.ModelRuntime
	err     error
}

// Resolver implements contextfabric.ModelRuntime by resolving a
// per-organization runtime on every call. Default may be nil (no
// deployment-default provider configured); Configs may be nil (no
// per-organization configuration support wired at all, e.g. no encryption
// key configured -- see internal/runtime/hosted), in which case every
// organization uses Default, unchanged from pre-CHAOS-3775 behavior.
type Resolver struct {
	Default contextfabric.ModelRuntime
	Configs contextfabric.OrgModelConfigResolver
	Build   Build
	// Logger (CHAOS-5638) carries the one Warn line a failed per-sample
	// assertion emits -- see chaos5638_sampled.go. Optional: nil falls back
	// to slog.Default(), the same convention NewSlogEngineTelemetry uses,
	// so a composition that never sets it still gets the line rather than
	// losing it.
	Logger *slog.Logger

	mu sync.Mutex
	// cache holds only entries currently considered valid to SERVE. A
	// build's result reaches here only if it wins the write-time generation
	// guard (see runtimeFor's package-doc comment).
	cache map[string]cacheEntry
	// group coalesces concurrent cold-cache Build calls for the SAME
	// organization AND generation into one construction (Codex round-1
	// finding F6): without this, N concurrent first requests for a
	// newly-configured organization each raced to construct their own
	// genkit.Genkit instance before any of them finished and populated the
	// cache -- a thundering herd that multiplied init cost by concurrent
	// request count for exactly the moment (a cold org) latency matters
	// most. Keyed by generation, not just orgID, so a request racing a
	// configuration change is never coalesced with a build for the
	// superseded configuration.
	group singleflight.Group
}

var _ contextfabric.ModelRuntime = (*Resolver)(nil)

// New constructs a Resolver. build must be non-nil whenever configs is
// non-nil; a nil configs is valid (falls straight through to default on
// every call).
func New(deploymentDefault contextfabric.ModelRuntime, configs contextfabric.OrgModelConfigResolver, build Build) *Resolver {
	return &Resolver{Default: deploymentDefault, Configs: configs, Build: build, cache: make(map[string]cacheEntry)}
}

func (r *Resolver) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	runtime, err := r.runtimeFor(ctx, principal.OrgID)
	if err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, err
	}
	if runtime == nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
	}
	return runtime.InterpretQuestion(ctx, principal, request)
}

func (r *Resolver) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	runtime, err := r.runtimeFor(ctx, principal.OrgID)
	if err != nil {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, err
	}
	if runtime == nil {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, contextfabric.ErrModelUnavailable
	}
	return runtime.SynthesizeAnswer(ctx, principal, input)
}

// PhraseStructureOffers implements contextfabric.OfferPhrasingModelRuntime
// through the same per-organization resolution as interpretation and
// synthesis: the organization's own model first, the deployment default only
// when the turn may use it.
func (r *Resolver) PhraseStructureOffers(ctx context.Context, principal storage.Principal, input contextfabric.StructureOfferPhrasingInput) (contextfabric.StructureOfferPhrasingDraft, contextfabric.ModelExecutionReceipt, error) {
	runtime, err := r.runtimeFor(ctx, principal.OrgID)
	if err != nil {
		return contextfabric.StructureOfferPhrasingDraft{}, contextfabric.ModelExecutionReceipt{}, err
	}
	phraser, ok := runtime.(contextfabric.OfferPhrasingModelRuntime)
	if !ok || isNilRuntime(runtime) {
		logger := r.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.WarnContext(ctx, "context fabric offer phrasing withheld: no model runtime", "org_id", contextfabric.SanitizeLogAttr(principal.OrgID), "basis", "no_model_configured", "request_id", contextfabric.SanitizeLogAttr(input.RequestID))
		return contextfabric.StructureOfferPhrasingDraft{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("%w: %w: no model runtime configured", contextfabric.ErrModelUnavailable, contextfabric.ErrPlatformModelWithheld)
	}
	return phraser.PhraseStructureOffers(ctx, principal, input)
}

// runtimeFor is the resolution decision AC-3775-1..3 describe. It returns
// (nil, nil) only when there is genuinely no runtime to use and no error to
// report -- i.e. no org, no per-org support, and no deployment default;
// InterpretQuestion/SynthesizeAnswer both convert that into
// ErrModelUnavailable, matching the pre-CHAOS-3775 nil-runtime behavior.
func (r *Resolver) runtimeFor(ctx context.Context, orgID string) (contextfabric.ModelRuntime, error) {
	if r.Configs == nil || orgID == "" || contextfabric.DeploymentModelBound(ctx) {
		return r.deploymentDefault(ctx, orgID)
	}

	resolved, ok, err := r.Configs.ResolveOrgModelConfig(ctx, orgID)
	if err != nil {
		// The organization HAS a configuration but it could not be read
		// (e.g. its credential ciphertext no longer decrypts). Scoped to
		// this organization only -- Default is never consulted here. This
		// is the AC-3775-3 "broken credential does not fall back to the
		// deployment credential" prohibition; it applies even before a
		// provider call is ever attempted.
		return nil, wrapUnavailable(orgID, err)
	}
	if !ok {
		// AC-3775-3: no configuration at all -> deployment default.
		return r.deploymentDefault(ctx, orgID)
	}

	r.mu.Lock()
	entry, cached := r.cache[orgID]
	r.mu.Unlock()
	if cached && entry.generation == resolved.Generation {
		return entry.runtime, entry.err
	}

	// Coalesced via singleflight, keyed by orgID+generation so a request
	// racing a configuration change is never coalesced with a build for a
	// superseded configuration (see the group field's doc comment for why
	// this exists -- F6). Only the FIRST caller to reach Do for a given key
	// actually executes this closure -- every other concurrent caller for the
	// same key just waits and shares this result. The key intentionally
	// does NOT bind ctx: only the first caller's context actually drives
	// construction, and any concurrent followers share its result even if
	// their own context has a different deadline -- the standard, accepted
	// singleflight trade-off, and correct here because genkit construction
	// is a bounded local operation, not a per-caller network call.
	key := orgID + "\x00" + strconv.FormatInt(resolved.Generation, 10)
	value, _, _ := r.group.Do(key, func() (any, error) {
		runtime, buildErr := r.Build(ctx, resolved)
		if buildErr != nil {
			buildErr = wrapUnavailable(orgID, buildErr)
		}

		// M1: a canceled or deadline-exceeded build reflects the CALLER's
		// context, not a genuine construction failure -- caching it would
		// poison every later request at this generation with an error
		// that has nothing to do with whether the organization's
		// provider/credential actually works. Leave the cache exactly as
		// it was (whatever it held before this attempt, likely nothing,
		// since this path only runs on a cache miss) so the next request
		// gets a real attempt.
		if isTransientBuildError(buildErr) {
			return buildResult{runtime: runtime, err: buildErr}, nil
		}

		r.mu.Lock()
		// Refuse to regress an already-cached NEWER generation (see the
		// package doc comment).
		existing, hasExisting := r.cache[orgID]
		if !hasExisting || existing.generation <= resolved.Generation {
			r.cache[orgID] = cacheEntry{generation: resolved.Generation, runtime: runtime, err: buildErr}
		}
		r.mu.Unlock()
		// The result (success or failure) is carried inside buildResult,
		// never as this function's own error return: singleflight.Do's
		// error return is shared verbatim with every waiter, and encoding
		// the outcome in the value keeps that sharing explicit and typed
		// rather than relying on singleflight's own error-propagation
		// semantics.
		return buildResult{runtime: runtime, err: buildErr}, nil
	})
	result := value.(buildResult)
	return result.runtime, result.err
}

// errDeploymentModelWithheld refuses the deployment default to a turn that may
// not run on it: the turn was admitted on the organization's own model, and
// that configuration is gone by the time the runtime is resolved.
var errDeploymentModelWithheld = fmt.Errorf("%w: %w: deployment model withheld for this turn", contextfabric.ErrModelUnavailable, contextfabric.ErrPlatformModelWithheld)

// deploymentDefault is the one place a request falls through to the
// deployment's own model, so it is the one place that honors a withheld turn.
func (r *Resolver) deploymentDefault(ctx context.Context, orgID string) (contextfabric.ModelRuntime, error) {
	if contextfabric.PlatformModelWithheld(ctx) {
		logger := r.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.WarnContext(ctx, "context fabric deployment model withheld at resolution", "org_id", contextfabric.SanitizeLogAttr(orgID), "org_config_support", r.Configs != nil, "basis", contextfabric.SanitizeLogAttr(contextfabric.PlatformModelWithheldBasis(ctx)))
		return nil, errDeploymentModelWithheld
	}
	return r.Default, nil
}

// isNilRuntime reports a nil interface or an interface holding a nil pointer,
// so a typed-nil runtime is treated as absent rather than called.
func isNilRuntime(runtime any) bool {
	if runtime == nil {
		return true
	}
	value := reflect.ValueOf(runtime)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return value.IsNil()
	default:
		return false
	}
}

// isTransientBuildError reports whether err reflects the CALLER's context
// being canceled or its deadline expiring, rather than a genuine
// construction failure (a bad credential, an unreachable provider, ...).
// wrapUnavailable already leaves a context error unwrapped (bare
// context.Canceled/context.DeadlineExceeded, not joined into
// ErrModelUnavailable), so checking with errors.Is here correctly ignores
// any ErrModelUnavailable-wrapped error this is not.
func isTransientBuildError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func wrapUnavailable(orgID string, err error) error {
	if isTransientBuildError(err) {
		return err
	}
	return fmt.Errorf("%w: organization %s model runtime: %v", contextfabric.ErrModelUnavailable, orgID, err)
}
