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
// Cache-write fencing has two INDEPENDENT guards, checked together at
// write time, each closing a different race:
//
//  1. Eviction fence (epoch). EvictOrgModelRuntime bumps a per-organization
//     monotonic epoch counter. Every request captures the CURRENT epoch as
//     its "ticket" BEFORE calling ResolveOrgModelConfig -- deliberately
//     before resolving, not merely before building (Codex round-3 finding:
//     an earlier revision claimed the ticket inside the build closure,
//     AFTER resolve, which left exactly the window round 3 caught -- a
//     request that resolved a pre-delete configuration, then paused, then
//     claimed a POST-eviction ticket, would build the stale configuration
//     and still pass the equality check, because the ticket claim itself
//     came after the eviction it was supposed to detect). At write time, a
//     build may only write if the epoch is STILL exactly its ticket: any
//     eviction that happened ANYWHERE between this request's resolve and
//     its write invalidates the write. Epoch is bumped ONLY by eviction,
//     never by an ordinary build attempt -- see the generation guard below
//     for why a second, unrelated build attempt does not need to bump it.
//
//  2. Generation-monotonic write guard. Two builds for DIFFERENT
//     generations of the same organization can finish out of order (e.g.
//     an UPDATE lands mid-build, so an old-generation build finishes after
//     a newer-generation build has already cached its result). Rather than
//     involving epoch in this (which would require bumping it on every
//     build attempt, defeating singleflight's coalescing benefit for
//     concurrent SAME-generation callers -- see the group field), a write
//     is additionally refused whenever the entry already cached for this
//     organization has a Generation strictly newer than the one this build
//     was for. This closes the "benign sibling" Codex round 2 also noted:
//     without it, the stale write is harmless from a SERVED-answer
//     perspective (Generation still mismatches on the next read, forcing
//     a fresh rebuild), but it is a wasted rebuild every time it happens;
//     with it, the newer result survives untouched and no extra rebuild is
//     needed.
package modelruntimeresolver

import (
	"context"
	"errors"
	"fmt"
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

	mu sync.Mutex
	// cache holds only entries currently considered valid to SERVE. A
	// build's result reaches here only if it wins BOTH write-time guards
	// (see epoch and runtimeFor's package-doc comment).
	cache map[string]cacheEntry
	// epoch is a per-organization counter bumped ONLY by
	// EvictOrgModelRuntime -- never by an ordinary build attempt (see the
	// package doc comment's generation-guard note for why bumping it per
	// attempt would defeat singleflight coalescing once the ticket is
	// claimed before resolve: concurrent callers for the same
	// organization would each observe a different, still-unbumped-by-
	// eviction epoch value, which is exactly what makes a plain READ here
	// -- not an increment -- the correct ticket for every caller sharing
	// one build). Absent key reads as zero, matching Go's map zero-value
	// convention -- no special-casing needed for an organization that has
	// never been evicted.
	epoch map[string]int64
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
var _ contextfabric.OrgModelRuntimeEvictor = (*Resolver)(nil)

// New constructs a Resolver. build must be non-nil whenever configs is
// non-nil; a nil configs is valid (falls straight through to default on
// every call).
func New(deploymentDefault contextfabric.ModelRuntime, configs contextfabric.OrgModelConfigResolver, build Build) *Resolver {
	return &Resolver{Default: deploymentDefault, Configs: configs, Build: build, cache: make(map[string]cacheEntry), epoch: make(map[string]int64)}
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

// EvictOrgModelRuntime immediately purges orgID's cache entry, if any
// (Codex round-1 finding F4), and fences off any build already in flight
// for this organization -- no matter how far along, including one still
// inside ResolveOrgModelConfig -- from ever writing its result afterward
// (Codex round-2/round-3 finding M2; see the package doc comment). Call
// this after a successful delete of an organization's configuration: the
// Generation-keyed cache comparison alone already prevents a stale runtime
// from ever being SERVED again on a later request (the sequence backing
// Generation never repeats, even across a delete followed by a fresh
// write), but without both the explicit delete AND the epoch fence, (a)
// the stale entry -- and the decrypted credential baked into its
// constructed transport -- would stay resident in process memory
// indefinitely, and (b) a build already running when the delete happened
// could still write its (now-revoked-credential) result into the cache
// after this call returns. Safe to call for an organization with no cache
// entry (no-op) or concurrently with any other Resolver method.
func (r *Resolver) EvictOrgModelRuntime(orgID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.cache, orgID)
	r.epoch[orgID]++
	r.mu.Unlock()
}

// runtimeFor is the resolution decision AC-3775-1..3 describe. It returns
// (nil, nil) only when there is genuinely no runtime to use and no error to
// report -- i.e. no org, no per-org support, and no deployment default;
// InterpretQuestion/SynthesizeAnswer both convert that into
// ErrModelUnavailable, matching the pre-CHAOS-3775 nil-runtime behavior.
func (r *Resolver) runtimeFor(ctx context.Context, orgID string) (contextfabric.ModelRuntime, error) {
	if r.Configs == nil || orgID == "" {
		return r.Default, nil
	}

	// M2 fence, part 1: claim this request's epoch ticket BEFORE resolving
	// the configuration -- see the package doc comment's "Codex round-3
	// finding" note for why claiming it any later (even just after
	// resolve, inside the build closure) leaves a window. Every request
	// pays this one extra uncontended lock/unlock even on a cache hit that
	// never builds; that is the accepted cost of covering the entire
	// resolve -> build -> write window rather than only build -> write.
	r.mu.Lock()
	ticket := r.epoch[orgID]
	r.mu.Unlock()

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
		return r.Default, nil
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
	// actually executes this closure (and so is the only one whose ticket,
	// captured above, matters) -- every other concurrent caller for the
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
		// M2 fence, part 2: the epoch must still match the ticket claimed
		// before resolve -- an eviction ANYWHERE in the resolve-through-
		// build window invalidates this write. The generation guard is
		// independent: refuse to regress an already-cached NEWER
		// generation (see the package doc comment's guard #2).
		existing, hasExisting := r.cache[orgID]
		if r.epoch[orgID] == ticket && (!hasExisting || existing.generation <= resolved.Generation) {
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
