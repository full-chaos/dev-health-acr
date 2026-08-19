package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type EngineOptions struct {
	ServiceVersion string
	Now            func() time.Time
	NewResultID    func() string
	// ReuseProjectionVersion (CHAOS-3782) is the CURRENT value a fresh
	// investigation's Versions.ProjectionVersion would carry -- composition
	// must wire it from the exact same configuration
	// RuntimeAnswerSynthesizerOptions.ProjectionVersion uses, so it can
	// never drift from what a fresh answer would actually stamp. Engine
	// needs this BEFORE running a fresh investigation -- that is the
	// entire point of reuse -- so it must be known statically at
	// composition time, not read off a result Engine has not produced
	// yet. May be left empty; Dependencies.ReuseGate being nil (or
	// FindReusable never matching an empty ProjectionVersion) is what
	// actually disables reuse.
	ReuseProjectionVersion string
	// ReuseModelIdentities (CHAOS-3782; widened from a single
	// ReuseModelIdentity string to a chain by CHAOS-3786) is the static
	// fallback chain tryReuse uses ONLY when Dependencies.
	// ReuseModelIdentityResolver is nil -- see that field's doc comment.
	// A real per-organization or per-BYO-config deployment should wire
	// the resolver instead; this exists for a deployment with no
	// per-organization model configuration support at all, where the
	// deployment-default's own chain (primary, then fallback if
	// configured) is every organization's effective chain. May be left
	// empty, same "reuse effectively disabled" convention as
	// ReuseProjectionVersion.
	ReuseModelIdentities []string
	// ReuseRetrievalIdentity (CHAOS-3833) is the deployment-CURRENT pair
	// of retrieval discriminators -- embed retrieval identity and
	// retrieval policy version -- computed by composition from the same
	// configuration the graph adapter's own stamping and retrieval use.
	// Engine threads the identical value into every lookup's ReuseKey AND
	// into every Save, so the persisted columns and the compared
	// predicates cannot drift within one process. Either field left empty
	// disables retrieval-keyed reuse participation (rows persist NULL and
	// lookups miss), the same fail-closed convention as the fields above.
	ReuseRetrievalIdentity ReuseRetrievalIdentity
	// ReusePromptVersions (CHAOS-3862) is the deployment-CURRENT pair of
	// interpretation/synthesis prompt versions -- composition must wire it
	// from the SAME genkitruntime defaulting (or Config override, if one
	// is ever added) the actual Interpret/Synthesize calls use, so it can
	// never drift from what a fresh answer would actually have been
	// produced under. Engine needs this BEFORE running a fresh
	// investigation, same as ReuseProjectionVersion above -- reuse runs
	// before Interpret, so the value must be known statically at
	// composition time. Either field left empty disables that dimension
	// of reuse participation (rows persist NULL and lookups miss on it),
	// the same fail-closed convention as every other field here.
	ReusePromptVersions ReusePromptVersions
	// ReuseVersionAuthorities (CHAOS-3862 round 2) is three MORE
	// deployment-current version authorities -- composition must wire it
	// from the SAME constants the corresponding fresh-result composition
	// path uses (devhealthfacts.QueryVersion,
	// contextfabric.CanonicalFactRegistryVersion,
	// genkitruntime.DefaultSchemaVersion), same reasoning and same
	// fail-closed convention as ReusePromptVersions immediately above.
	ReuseVersionAuthorities ReuseVersionAuthorities
}

type EngineDependencies struct {
	Interpreter QuestionInterpreter
	Graph       GraphReader
	Facts       CanonicalFactReader
	Synthesizer AnswerSynthesizer
	Results     InvestigationResultStore
	// Telemetry is optional. When set, Engine reports content-safe
	// operational counters through it -- see EngineTelemetry.
	Telemetry EngineTelemetry
	// ReuseGate is optional (CHAOS-3782). When nil, Engine never attempts
	// answer reuse and behaves exactly as it did before this field
	// existed -- every Investigate call runs a fresh investigation. See
	// AnswerReuseGate's doc comment for the six-condition policy it and
	// Engine jointly enforce.
	ReuseGate AnswerReuseGate
	// ReuseSnapshotter is optional (CHAOS-3782, Codex round-1 F1). When
	// set, Engine captures a source-watermark snapshot itself,
	// immediately before the graph is read for a fresh investigation,
	// and threads it to Save -- see SourceWatermarkSnapshotter's doc
	// comment for why the timing matters. Leaving this nil means a fresh
	// result never carries a snapshot and so never becomes reusable,
	// exactly as if ReuseGate were also nil.
	ReuseSnapshotter SourceWatermarkSnapshotter
	// ReuseEpochSnapshotter is optional (CHAOS-3782, Codex round-2
	// finding #7). When set, Engine captures the organization's current
	// rebuild-invalidation epoch itself, at the same moment as the
	// watermark snapshot (immediately before the graph is read for a
	// fresh investigation), and threads it to Save alongside that
	// snapshot -- see RebuildEpoch's doc comment for why this closes a
	// race the watermark snapshot and timestamp comparison alone could
	// not. Leaving this nil means a fresh result never carries an epoch
	// and so never becomes reusable, exactly as if ReuseGate were also
	// nil.
	ReuseEpochSnapshotter RebuildEpochSnapshotter
	// ReuseModelIdentityResolver is optional (CHAOS-3782, Codex round-2
	// finding #3; CHAOS-3786). When set, tryReuse resolves the CURRENT
	// org-effective model CHAIN through it, per call, instead of using
	// EngineOptions.ReuseModelIdentities' single static chain for every
	// organization -- see ReuseModelIdentityResolver's doc comment for
	// the staleness bug a static chain causes, and for why it is a chain
	// (primary + fallback) rather than one identity. Leaving this nil
	// keeps pre-existing behavior (EngineOptions.ReuseModelIdentities for
	// every organization) -- the correct choice only for a deployment
	// that has no per-organization model configuration at all.
	ReuseModelIdentityResolver ReuseModelIdentityResolver
	// ClarificationSelectionSink is optional (CHAOS-3859, capture-only
	// phase). When set, Engine notifies it every time a caller's
	// PriorSubjectReceipt successfully resolves to a specific candidate
	// from an earlier clarification_required result -- see
	// ClarificationSelectionSink's doc comment for the fail-open contract
	// this dependency must uphold. Leaving this nil means no selection is
	// ever captured, exactly as if the feature did not exist -- capture
	// is strictly additive and never changes Investigate's own behavior.
	ClarificationSelectionSink ClarificationSelectionSink
}

// EngineTelemetry receives content-safe operational counters from Engine.
// Implementations must record only counts and fixed classifications --
// never question text, subject labels, canonical IDs, result IDs, or any
// other investigation content -- so a signal is diagnosable without
// becoming a new disclosure surface.
type EngineTelemetry interface {
	// RecordPriorSubjectReceiptsSkipped reports how many of one
	// Investigate call's PriorSubjectReceipts did not end up bound to a
	// resolved subject -- whether because the referenced prior result
	// could not be loaded, no candidate in it matched the receipt, or the
	// resolved subject did not survive current authorization/graph
	// resolution. Investigate never errors or otherwise surfaces this to
	// the caller (a stale, foreign, or now-unauthorized receipt degrades
	// silently), so this count is the only operator-visible signal that it
	// happened.
	RecordPriorSubjectReceiptsSkipped(ctx context.Context, principal storage.Principal, skipped int)
	// RecordAnswerReuse reports the outcome of ONE Investigate call's
	// reuse attempt (CHAOS-3782, AC-3782-8) as a closed AnswerReuseOutcome
	// label -- AnswerReuseHit when a stored result was served with zero
	// model calls, or one of the specific miss reasons when the call ran
	// a fresh investigation instead. The reuse rate and the saved
	// model-call count are both derived from this one stream (rate =
	// hits / total; saved calls = count of hits, each one representing
	// exactly the interpret+synthesize model calls a fresh investigation
	// would otherwise have made); the miss reasons exist so a cratered
	// reuse rate is diagnosable from telemetry (e.g.
	// miss_evidence_containment dominating usually means the recheck's
	// own bounds are the problem, not real staleness) rather than an
	// operator only ever seeing "reuse rarely happens" with no way to
	// tell why.
	RecordAnswerReuse(ctx context.Context, principal storage.Principal, outcome AnswerReuseOutcome)
	// RecordSubjectlessTerminal (CHAOS-3888) reports WHY one Investigate
	// call reached its own subjectless terminal path (terminalResult,
	// unresolved.go) as a closed reason string -- "empty_pool",
	// "authz_filtered_to_empty", or "ambiguous". See
	// subjectlessTerminalReason's own doc comment for the exact
	// classification and why an authorization-narrowing cause specifically
	// must stay telemetry-only, never surfacing in the response contract.
	RecordSubjectlessTerminal(ctx context.Context, principal storage.Principal, reason string)
	// RecordPriorSubjectReceiptSkipReason (CHAOS-3888) reports the SAME
	// aggregate this call's RecordPriorSubjectReceiptsSkipped already
	// reported, split by WHY each receipt in it was skipped -- a closed
	// reason vocabulary: "unloadable" (the receipt's own ResultID/ReceiptID
	// were blank, or the prior InvestigationResult failed to load),
	// "no_match" (the prior result loaded but no candidate in it carried a
	// matching ReceiptID), "failed_reauth" (a hint WAS built from the
	// receipt but its subject did not survive this call's own graph
	// resolution -- e.g. GraphReader's exact-hint authorization check
	// rejected it, or the caller's own SubjectHints already filled the
	// per-request hint budget), or "stale_graph_epoch" (CHAOS-3898 §2.2/
	// §5b cf_receipt_taint_strip: the prior result loaded but its stored
	// StoredInvestigationResult.GraphEpoch differs from -- or is absent
	// relative to -- this investigation's own ResolvedGraphBinding.Epoch,
	// so it is stripped before any of its fields are read). Called once
	// per non-zero reason, so a cratered reuse of prior receipts is
	// diagnosable (was the store down, is authorization narrowing, or did
	// a build/flip invalidate the prior turn's graph epoch?) from
	// telemetry alone, the same motivation RecordAnswerReuse's own
	// miss-reason split already serves for answer reuse.
	//
	// epochDelta (CHAOS-3898 P2 fix-forward, codex retroactive review of
	// #151/#152, chris-verified) is cf_receipt_taint_strip's own required
	// field (design brief §5b: "count + epoch_delta (active − stored,
	// int)") -- meaningful ONLY for reason=="stale_graph_epoch", always 0
	// for every other reason. It is the SUM, across every receipt this
	// call stripped for that reason, of (this investigation's own
	// binding.Epoch − the receipt's own stored GraphEpoch, treating an
	// absent GraphEpoch as 0) -- summed rather than reported per-receipt
	// because this method already reports one aggregate call per reason
	// per Investigate call; an operator recovers the average delta as
	// epochDelta/count. A nonzero, non-tiny magnitude here is what
	// distinguishes "the prior turn predates a recent flip by one epoch"
	// from "this receipt names a pre-migration epoch from a long time
	// ago" -- the count alone cannot.
	RecordPriorSubjectReceiptSkipReason(ctx context.Context, principal storage.Principal, reason string, count int, epochDelta int64)
	// RecordAnswerReuseServedRequestID (CHAOS-3888) reports, for every
	// AnswerReuseHit outcome, the served (originally-stored) request id and
	// whether it differs from the CURRENT call's own request id.
	// AC-3782-2 requires tryReuse to serve the stored result's
	// ResultID/RequestID/GeneratedAt UNCHANGED -- the response body
	// contract, untouched by this ticket -- which means a caller reading
	// InvestigationResult.RequestID off a reuse hit sees the ORIGINAL
	// investigation's request id, not this call's own. That is correct,
	// documented behavior, but easy to misdiagnose as a bug from the
	// outside without a telemetry signal that names it explicitly.
	RecordAnswerReuseServedRequestID(ctx context.Context, principal storage.Principal, servedRequestID string, requestIDMismatch bool)
	// RecordBindingEpochDelta is the CHAOS-3898 §5b flip_during_investigation/
	// cf_binding_epoch_delta pair: at Save, Engine re-resolves the
	// organization's CURRENT active graph epoch (a second,
	// telemetry-only call to GraphReader.ResolveInvestigationBinding --
	// never the binding actually used for this investigation's own graph
	// reads or Save's own stamped epoch, which stay the ORIGINAL binding
	// resolved at request start) and compares it against
	// ResolvedGraphBinding.Epoch, the value this investigation's graph
	// reads actually used. flipped is true when they differ -- a
	// build/flip happened between this investigation's request-start
	// binding resolution and its Save -- and delta is the signed
	// difference (current minus original; 0 when flipped is false).
	// Called unconditionally, once per Save, flipped or not, so
	// zero-vs-nonzero settles "how often does this really happen" by
	// counter rather than by guesswork (the same 3897-pattern motivation
	// §5b's own header names): grace-window and cache-lease (L) tuning on
	// data, not assumption. The re-resolution itself fails OPEN (an error
	// simply skips this one signal) -- it must never affect Save's own
	// success or the epoch actually stamped on the result.
	RecordBindingEpochDelta(ctx context.Context, principal storage.Principal, flipped bool, delta int64)
	// RecordWindowBinderOutcome (CHAOS-3900 W1) reports the proposal-only
	// temporal-expression binder's own closed outcome for one Investigate
	// call's question text -- see WindowBindReason's doc comment for the
	// four-value vocabulary (no_span/temporal_span_unbound/
	// temporal_span_ambiguous/binder_span_routed_inferred). Called
	// unconditionally, once per call to canonicalizeEvidenceWindow, so a
	// zero binder-routed rate is as visible as a nonzero one.
	RecordWindowBinderOutcome(ctx context.Context, principal storage.Principal, reason WindowBindReason)
	// RecordWindowCanonicalization (CHAOS-3900 W1) reports design brief
	// §1.2's own window-canonicalization outcome for one Investigate call
	// -- see WindowCanonicalizationOutcome's doc comment for the closed
	// vocabulary. Called once per call, from Investigate, after
	// canonicalizeEvidenceWindow/composeEffectiveWindow both run.
	RecordWindowCanonicalization(ctx context.Context, principal storage.Principal, outcome WindowCanonicalizationOutcome)
}

// Engine coordinates one open-ended investigation. It deliberately composes
// capabilities rather than matching the question against a route/plan table.
type Engine struct {
	interpreter                QuestionInterpreter
	graph                      GraphReader
	facts                      CanonicalFactReader
	synthesizer                AnswerSynthesizer
	results                    InvestigationResultStore
	telemetry                  EngineTelemetry
	reuseGate                  AnswerReuseGate
	reuseSnapshotter           SourceWatermarkSnapshotter
	reuseEpochSnapshotter      RebuildEpochSnapshotter
	reuseModelIdentityResolver ReuseModelIdentityResolver
	reuseProjectionVersion     string
	reuseModelIdentities       []string
	reuseRetrievalIdentity     ReuseRetrievalIdentity
	reusePromptVersions        ReusePromptVersions
	reuseVersionAuthorities    ReuseVersionAuthorities
	clarificationSelectionSink ClarificationSelectionSink
	serviceVersion             string
	now                        func() time.Time
	newResultID                func() string
}

func NewEngine(dependencies EngineDependencies, options EngineOptions) (*Engine, error) {
	if dependencies.Interpreter == nil || dependencies.Graph == nil || dependencies.Facts == nil || dependencies.Synthesizer == nil {
		return nil, errors.New("context fabric engine requires interpreter, graph, facts, and synthesizer")
	}
	if strings.TrimSpace(options.ServiceVersion) == "" {
		return nil, errors.New("context fabric engine service version is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewResultID == nil {
		return nil, errors.New("context fabric engine result ID generator is required")
	}
	return &Engine{
		interpreter: dependencies.Interpreter, graph: dependencies.Graph, facts: dependencies.Facts,
		synthesizer: dependencies.Synthesizer, results: dependencies.Results, telemetry: dependencies.Telemetry,
		reuseGate: dependencies.ReuseGate, reuseSnapshotter: dependencies.ReuseSnapshotter,
		reuseEpochSnapshotter:      dependencies.ReuseEpochSnapshotter,
		reuseModelIdentityResolver: dependencies.ReuseModelIdentityResolver,
		clarificationSelectionSink: dependencies.ClarificationSelectionSink,
		reuseProjectionVersion:     options.ReuseProjectionVersion, reuseModelIdentities: options.ReuseModelIdentities,
		reuseRetrievalIdentity:  options.ReuseRetrievalIdentity,
		reusePromptVersions:     options.ReusePromptVersions,
		reuseVersionAuthorities: options.ReuseVersionAuthorities,
		serviceVersion:          options.ServiceVersion, now: options.Now, newResultID: options.NewResultID,
	}, nil
}

func (e *Engine) Investigate(ctx context.Context, principal storage.Principal, request InvestigationRequest) (InvestigationResult, error) {
	if err := request.Validate(); err != nil {
		return InvestigationResult{}, fmt.Errorf("investigation request: %w", err)
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		return InvestigationResult{}, errors.New("authenticated organization is required")
	}
	// CHAOS-3781: historical questions are ANSWERED now, not refused --
	// the graph admits by validity window and the fact providers bound
	// themselves or decline honestly, so the layers this engine used to
	// protect callers from no longer need protecting from. What survives
	// is a bounds check: a time in the future is a prediction, and a
	// range wider than this service will read is not answerable.
	//
	// This is the FIRST of two checks. It bounds what the caller asked
	// for on the wire; the second (below, after Interpret) bounds what
	// the question was understood to mean. Both are required -- see the
	// second check's comment for why this one alone is not enough.
	// F7: the returned context is CLAMPED -- an instant inside the skew
	// tolerance is pulled back to now, so a future time can never reach a
	// predicate or a label. The clamped value replaces the caller's on
	// the request every layer below sees.
	clampedRequestTime, err := resolveTimeContext(request.TimeContext, e.now())
	if err != nil {
		return InvestigationResult{}, err
	}
	request.TimeContext = clampedRequestTime
	if err := ctx.Err(); err != nil {
		return InvestigationResult{}, err
	}

	// CHAOS-3898 §2.1: resolve the ResolvedGraphBinding EXACTLY ONCE, here,
	// at request start -- BEFORE tryReuse (F1: tryReuse already needs it,
	// for the §2.3 SQL predicate and its own recheck graph calls, so
	// binding this any later would leave tryReuse with nothing to use).
	// The SAME value is threaded, unchanged, to every graph call this
	// investigation makes (fresh or reuse-recheck), to Save, and into the
	// §2.3 lookup -- never re-resolved independently at any of those call
	// sites. Unlike the watermark/epoch snapshots below (which fail OPEN --
	// an optional reuse-only signal), this is REQUIRED infrastructure: no
	// graph call can run without a key to read from, so a resolution
	// failure here fails the whole investigation.
	binding, err := e.graph.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		return InvestigationResult{}, stageError(StageResolution, fmt.Errorf("resolve graph binding: %w", err))
	}

	// CHAOS-3900 W1: canonicalize the REQUEST-side evidence window --
	// receipt resolution and validation -- BEFORE tryReuse, so a resolved
	// window_stated/clarification_confirmed window is part of the reuse key
	// a lookup below actually forms with (see canonicalizeEvidenceWindow's
	// own doc comment for the ordering bug this prevents). A non-empty Veto
	// means the request must short-circuit HERE: no reuse lookup, no
	// interpretation, no inference substituted -- windowVetoResult composes
	// and persists the no_match terminal directly.
	windowCanon := e.canonicalizeEvidenceWindow(ctx, principal, request)
	if windowCanon.Veto != windowVetoNone {
		return e.windowVetoResult(ctx, principal, request, windowCanon.Veto, binding)
	}

	// CHAOS-3782 answer reuse. This MUST run before Interpret -- that
	// ordering is the entire mechanism behind AC-3782-1's zero-model-call
	// guarantee for a reuse hit. tryReuse itself only ever returns
	// ok=false on anything it cannot fully confirm (TRD §19.7.3 fails
	// closed); Investigate always falls through to a fresh investigation
	// in that case, so a reuse-path failure is never visible to the
	// caller as anything other than normal, slightly slower success.
	// Round-3 F1: the reuse key is the CLAMPED EFFECTIVE context, and
	// Save below keys on the same value -- symmetry preserved from
	// round-2 F2, but on the value that describes what the answer
	// actually MEANS rather than what the caller literally typed.
	//
	// Round-1 F6's premise (identical wire requests key identically
	// regardless of arrival) is false precisely when clamping is
	// time-dependent: the same wire instant means a DIFFERENT effective
	// instant at different arrival times, and those answers legitimately
	// differ. Keying on the wire value served a request meaning 12:00:30
	// an answer that had meant 12:00:00.
	if reused, ok := e.tryReuse(ctx, principal, request, clampedRequestTime, windowCanon.KeyComponent, binding); ok {
		return reused, nil
	}

	// Prior-result receipts (PriorSubjectReceipts) name a subject already
	// committed or proposed in an earlier InvestigationResult -- e.g. a
	// conversational follow-up ("what about it") binding back to the
	// subject a prior turn resolved. A receipt is a one-way identifier
	// (ReceiptID), not itself a resolvable subject: only the Engine holds
	// the InvestigationResultStore needed to look one up, so expansion
	// happens here rather than inside GraphReader. The expanded request
	// feeds the exact-hint path GraphReader already has (SubjectHint), so
	// every resolved receipt is independently re-authorized before it can
	// become a candidate -- a stale, foreign, or now-unauthorized receipt
	// is skipped, never trusted outright, and never treated as an error.
	//
	// CHAOS-3898 P1-1 fix-forward (codex retroactive review of #151/#152,
	// chris-verified): this resolution now runs BEFORE Interpret, not
	// after. It used to run after Interpret, which meant Interpret's own
	// input carried request.PriorSubjectReceipts VERBATIM -- every receipt
	// the caller sent, including one this function's own §2.2 taint gate
	// (below) would go on to strip for naming a stale graph epoch, or that
	// never matched any candidate at all. A model interpreting a
	// conversational reference ("it") against that unvalidated set could
	// have its interpretation shaped by a receipt the engine was always
	// going to treat as if it did not exist -- exactly the ingress-taint
	// invariant Class A's design (§2.1: "ingress taint before Interpret")
	// exists to hold everywhere, not just on the graph-reuse path. Moving
	// this block up costs nothing: binding (the taint gate's own
	// dependency) is resolved above, before tryReuse; nothing here reads
	// `interpretation`. Interpret below now receives ONLY the receipts
	// resolvePriorSubjectHints itself validated (priorValidatedReceipts).
	graphRequest := request
	var priorHints []SubjectHint
	var priorValidatedReceipts []BoundSubjectReceipt
	var priorHintsUnloadable, priorHintsNoMatch, priorHintsStaleGraphEpoch int
	var priorHintsStaleGraphEpochDelta int64
	if e.results != nil && len(request.PriorSubjectReceipts) > 0 {
		priorHints, priorValidatedReceipts, priorHintsUnloadable, priorHintsNoMatch, priorHintsStaleGraphEpoch, priorHintsStaleGraphEpochDelta = e.resolvePriorSubjectHints(ctx, principal, request.Consumer, request.PriorSubjectReceipts, binding)
		// The v1 contract bounds RequestedScope.SubjectHints at 50
		// (ContextFabricRequestedScope.Validate). request.Validate()
		// already proved the caller's own hints are within that bound,
		// but Engine's own expansion must not push the combined total
		// back out of it -- drop excess receipt-derived hints (never the
		// caller's own explicit hints), and let the existing skip
		// telemetry in recordPriorSubjectReceiptSkips below count the
		// drop exactly like any other unresolved receipt.
		//
		// CHAOS-3898 P1-1 codex re-review finding (fixed here):
		// priorValidatedReceipts is deliberately NOT truncated by this
		// same cap. maxSubjectHints is a GRAPH-CONTRACT bound
		// (ContextFabricRequestedScope.SubjectHints' own v1 limit) --
		// Interpret's own input carries no such bound, and a validated
		// receipt (one resolvePriorSubjectHints already proved passed the
		// taint/match gate) dropped only because the caller's OWN
		// explicit hints already filled the graph-side budget must still
		// reach the interpreter: the model can still legitimately resolve
		// "it" against it even though GraphReader will never see it as an
		// exact hint. priorHints and priorValidatedReceipts are returned
		// 1:1 index-aligned from resolvePriorSubjectHints, but diverge
		// here on purpose -- each feeds a consumer with its own bound (or
		// none).
		const maxSubjectHints = 50
		if available := maxSubjectHints - len(request.RequestedScope.SubjectHints); len(priorHints) > available {
			if available < 0 {
				available = 0
			}
			priorHints = priorHints[:available]
		}
		if len(priorHints) > 0 {
			graphRequest.RequestedScope.SubjectHints = append(
				append([]SubjectHint(nil), request.RequestedScope.SubjectHints...), priorHints...,
			)
		}
	}

	// CHAOS-3898 P1-1: Interpret sees ONLY the validated receipt subset,
	// never the raw request.PriorSubjectReceipts -- see the block above.
	interpretRequest := request
	interpretRequest.PriorSubjectReceipts = priorValidatedReceipts
	interpretation, err := e.interpreter.Interpret(ctx, principal, interpretRequest)
	if err != nil {
		return InvestigationResult{}, stageError(StageInterpretation, fmt.Errorf("interpret question: %w", err))
	}
	// Bound the INTERPRETED question too, not just the wire request
	// (CHAOS-3755 codex delta review, P2).
	//
	// Interpretation may legitimately change the axis: a caller can send
	// axis=current while the question itself is historical ("what was the
	// status last month"), and a QuestionInterpreter is expected to
	// recognize that and set valid_time. The wire-level check above
	// cannot see this -- it ran before the question was understood.
	//
	// Under CHAOS-3781 this check matters MORE, not less. It is no longer
	// deciding whether to refuse; it is deciding which time every layer
	// below binds itself to. The interpreted axis is what reaches
	// ResolveSubjects, DiscoverContext, the fact providers, and the
	// answer's own temporal label, so an interpreted axis this engine
	// will not answer must be caught before any of them run.
	//
	// The invariant belongs HERE rather than in any QuestionInterpreter
	// implementation: clamping a model's axis inside the runtime adapter
	// would silently rewrite the question into one the caller never
	// asked, and the next interpreter implementation would reopen the
	// hole. The engine owns what it can honestly answer.
	//
	// CHAOS-3898 P1-1 note: prior-receipt expansion no longer sits after
	// this check (it moved above Interpret, see that block's own comment)
	// -- an investigation this check goes on to REJECT still paid for
	// resolvePriorSubjectHints' work (results-store reads, clarification
	// capture) first. This is the required trade-off, not an oversight:
	// Interpret's OWN input must never carry an ungated receipt, and
	// Interpret necessarily runs before its output can be time-bounded.
	// The prior "zero work before axis rejection" guarantee now holds for
	// every capability call below this point, not for receipt resolution.
	clampedInterpretedTime, err := resolveTimeContext(interpretation.TimeContext, e.now())
	if err != nil {
		return InvestigationResult{}, err
	}
	interpretation.TimeContext = clampedInterpretedTime
	// CHAOS-3782 Codex round-1 F1: capture the reuse watermark snapshot
	// HERE, immediately before the graph is read for this fresh
	// investigation -- not later, at Save. A snapshot taken at Save time
	// could describe data fresher than what ResolveSubjects/
	// DiscoverContext below actually used (a projection could advance in
	// between), which would let a later identical question reuse this
	// stale answer under a watermark that merely looks unchanged.
	// reuseWatermarkSnapshot is threaded EXPLICITLY to Save below, as its
	// own parameter -- never through ctx (team-lead veto: load-bearing
	// data belongs in the signature, where a caller who forgets it fails
	// to compile, not in a context value a caller can silently omit).
	//
	// Fails OPEN on the snapshot read itself (never blocks the
	// investigation over an optional dependency); reuseWatermarkSnapshot
	// simply stays nil, and Save (per SourceWatermarkSnapshot's doc
	// comment) must treat nil as "this row never becomes reusable" --
	// the fail-CLOSED outcome for reuse specifically.
	var reuseWatermarkSnapshot SourceWatermarkSnapshot
	if e.reuseSnapshotter != nil {
		if snapshot, snapErr := e.reuseSnapshotter.SnapshotSourceWatermarks(ctx, principal.OrgID); snapErr == nil {
			reuseWatermarkSnapshot = snapshot
		}
	}
	// Codex round-2 finding #7: captured at the SAME point as the
	// watermark snapshot above, for the same reason (see RebuildEpoch's
	// doc comment) -- a value read later, at Save, could describe an
	// invalidation that happened AFTER the graph read this investigation
	// actually used, wrongly clearing this result to reuse under an epoch
	// that no longer describes what it was built from. Fails open on the
	// read itself (reuseEpoch simply stays nil); Save must treat nil as
	// "this row never becomes reusable," the fail-CLOSED outcome for
	// reuse specifically -- same convention as reuseWatermarkSnapshot.
	var reuseEpoch RebuildEpoch
	if e.reuseEpochSnapshotter != nil {
		if epoch, epochErr := e.reuseEpochSnapshotter.SnapshotRebuildEpoch(ctx, principal.OrgID); epochErr == nil {
			reuseEpoch = &epoch
		}
	}
	// CHAOS-3888: resolveCtx carries a fresh counter cell a GraphReader MAY
	// report authorization-dropped candidates through (RecordSubjectCandidatesAuthzDropped) --
	// scoped to exactly this one call, never passed to DiscoverContext or
	// anything below, so this signal can only ever describe THIS
	// resolution. See withSubjectCandidatesAuthzDroppedRecorder's own doc
	// comment for why a context value is the right carrier for this
	// specific, telemetry-only signal.
	resolveCtx, subjectCandidatesAuthzDropped := withSubjectCandidatesAuthzDroppedRecorder(ctx)
	resolution, err := e.graph.ResolveSubjects(resolveCtx, principal, graphRequest, interpretation, binding)
	if err != nil {
		return InvestigationResult{}, stageError(StageResolution, fmt.Errorf("resolve subjects: %w", err))
	}
	if len(request.PriorSubjectReceipts) > 0 {
		e.recordPriorSubjectReceiptSkips(ctx, principal, len(request.PriorSubjectReceipts), priorHints, resolution, priorHintsUnloadable, priorHintsNoMatch, priorHintsStaleGraphEpoch, priorHintsStaleGraphEpochDelta)
	}
	graphContext, err := e.graph.DiscoverContext(ctx, principal, GraphDiscoveryRequest{
		Request: graphRequest, Interpretation: interpretation, Resolution: resolution, Binding: binding,
	})
	if err != nil {
		return InvestigationResult{}, stageError(StageGraph, fmt.Errorf("discover graph context: %w", err))
	}
	graphContext.Resolution = resolution

	// CHAOS-3810: an investigation that resolved NO subject to read facts for
	// terminates here, in its own contract outcome, and never reaches the
	// fact read.
	//
	// This is the blocker's control-flow half. Resolution legitimately fails
	// toward ambiguity under uncertainty (see
	// graphrank.ResolveFromMergedCandidates), but nothing converted that
	// ambiguity into the contract outcome that describes it: the engine
	// carried on with zero committed subjects, validateCanonicalFactRequest
	// rejected the fact request as invalid, and the resulting unclassified
	// error fell through the route's classifier to a 500. An outcome the
	// contract has always had a status for was being reported as an ACR
	// outage.
	//
	// Checked on the SUBJECT LIST, not on Committed alone: a subjectless
	// cohort discovery commits nothing yet has perfectly good subjects to
	// read facts for, and it must keep running.
	subjects := investigationSubjects(resolution, graphContext.Cohort)
	if len(subjects) == 0 {
		return e.terminalResult(ctx, principal, request, interpretation, resolution, graphContext, reuseWatermarkSnapshot, reuseEpoch, *subjectCandidatesAuthzDropped, binding, windowCanon)
	}

	factRequest := CanonicalFactRequest{
		Question:     interpretation,
		Subjects:     subjects,
		Cohort:       graphContext.Cohort,
		Requirements: mergeFactRequirements(interpretation.FactRequirements, graphContext.FactRequirements),
	}
	// The invariant, asserted rather than assumed (CHAOS-3810). The guard
	// above is what makes this unreachable today; this is what keeps it
	// unreachable. A future edit that reintroduces a path to the fact read
	// with no subjects fails here as a NAMED condition the route classifies,
	// instead of rediscovering the unclassified 500.
	if len(factRequest.Subjects) == 0 {
		return InvestigationResult{}, stageError(StageFactRead, fmt.Errorf("%w: read canonical facts", ErrNoInvestigationSubjects))
	}
	facts, err := e.facts.ReadFacts(ctx, principal, factRequest)
	if err != nil {
		return InvestigationResult{}, stageError(StageFactRead, fmt.Errorf("read canonical facts: %w", err))
	}

	result, err := e.synthesizer.Synthesize(ctx, principal, SynthesisInput{
		Request: request, Interpretation: interpretation, Graph: graphContext, Facts: facts,
	})
	if err != nil {
		return InvestigationResult{}, stageError(StageSynthesis, fmt.Errorf("synthesize investigation: %w", err))
	}
	result.SchemaVersion = InvestigationResultSchemaV1
	result.ResultID = e.newResultID()
	result.RequestID = request.RequestID
	result.GeneratedAt = e.now().UTC()
	// Codex round-1 F8: explicit, not merely the zero value -- a
	// Synthesizer implementation that (incorrectly) set Reused=true on
	// its returned draft must not have that survive into a genuinely
	// fresh result. Reused=true is ONLY ever valid on the exact object
	// tryReuse returns.
	result.Reused = false
	result.Question = request.Question
	result.Interpretation = interpretation
	result.SubjectResolution = resolution
	// Codex round-1 F4, per the orchestrator's ruling: a retrieval mechanism
	// that was unavailable for THIS resolution is folded into the answer here,
	// at the engine, rather than by inventing a path from ResolveSubjects into
	// the graph adapter's own Coverage construction. ResolveSubjects reports
	// the request-scoped marker; the engine owns what an answer says about
	// itself.
	//
	// The limitation string is FIXED and non-interpolated. It names no
	// mechanism, no provider, no model, and no error text: a limitation is
	// answer-facing prose, and every cause here (an embed timeout, an
	// unreachable embedder, a server that served the wrong model, a fenced-off
	// stale index) has the same consequence for a reader -- retrieval saw less
	// than it should have. The operator-facing detail belongs in telemetry,
	// which already receives it.
	if resolution.RetrievalDegraded {
		// Deduplicated across BOTH spellings, not by exact equality: a draft
		// that already carries either form must not gain a second, differently
		// worded copy of the same statement. At the contract's cap the last
		// model-authored caveat is DISPLACED rather than the disclosure being
		// dropped or the whole answer refused -- see withRetrievalDegradation.
		composed, displaced := withRetrievalDegradation(result.Limitations)
		result.Limitations = composed
		// Recorded on the RESULT, because the loss is canonical: a model
		// caveat this investigation produced is gone from the stored
		// answer, and the API's canonical view is as much a consumer as
		// the projection is. It cannot be inferred downstream either --
		// a displaced list and a list that simply had room are the same
		// shape and the same length, both ending with the disclosure.
		result.LimitationsDisplaced += displaced
		result.Coverage.Partial = true
	}
	if result.Cohort == nil {
		result.Cohort = graphContext.Cohort
	}
	if strings.TrimSpace(result.Versions.ServiceVersion) == "" {
		result.Versions.ServiceVersion = e.serviceVersion
	}
	if strings.TrimSpace(result.Versions.ContractVersion) == "" {
		result.Versions.ContractVersion = InvestigationResultSchemaV1
	}
	if strings.TrimSpace(result.Versions.CanonicalServiceVersion) == "" {
		result.Versions.CanonicalServiceVersion = facts.Version
	}
	if strings.TrimSpace(result.Versions.ModelIdentity) == "" {
		result.Versions.ModelIdentity = "unwired"
	}
	// CHAOS-3781 AC-3781-2: a historical answer states the time it speaks
	// for in a structured field. Composed HERE, from the interpretation
	// and the coverage the sources actually returned, rather than inside
	// any AnswerSynthesizer: a synthesizer may use a model, and what time
	// an answer covers is a fact about which reads ran, never something a
	// model may assert. The result contract refuses a non-current axis
	// carrying no label, so a composition bug fails loudly here rather
	// than shipping an unlabeled historical answer.
	result.Temporal = composeTemporalLabel(interpretation, result.Coverage, facts.TemporalGrain)
	temporallyLimited, temporalDisplaced := appendTemporalLimitations(result.Limitations, interpretation)
	result.Limitations = temporallyLimited
	result.LimitationsDisplaced += temporalDisplaced
	// CHAOS-3900 W1: composeEffectiveWindow decides precedence steps 2-3
	// (design brief §1.2) now that Interpret has run -- windowCanon.Effective
	// (step 1, a confirmed/stated window) always wins unchanged when set;
	// otherwise this is the ONLY point an INFERRED default can be decided,
	// since it needs the interpreted Shape and the model's own window
	// class/confidence pick.
	result.EffectiveEvidenceWindow = composeEffectiveWindow(interpretation, windowCanon.Effective, windowCanon.BinderProposal, e.now())
	if e.telemetry != nil {
		e.telemetry.RecordWindowCanonicalization(ctx, principal, windowCanonicalizationOutcome(windowCanon, result.EffectiveEvidenceWindow))
	}
	if err := result.Validate(); err != nil {
		return InvestigationResult{}, stageError(StageValidation, fmt.Errorf("%w: %w", ErrInvalidResult, err))
	}
	if e.results != nil {
		// Keyed from the CLAMPED REQUEST context -- byte-for-byte the
		// value tryReuse keyed its lookup with (round-3 F1). Save and
		// FindReusable must agree or the saved row is unreachable, which
		// is what round-2 F2 fixed; round 3 moved BOTH to the effective
		// value rather than moving them apart.
		//
		// Deliberately the clamped REQUEST context, not the clamped
		// interpreted one: the lookup runs before Interpret and can only
		// know the former, so keying Save on the latter would reopen the
		// same asymmetry from the other side.
		epochDeltaSample := e.sampleBindingEpochDelta(ctx, principal, binding)
		if err := e.results.Save(ctx, principal, result, reuseWatermarkSnapshot, reuseEpoch, composeTimeAxisKey(TimeAxisKeyFor(clampedRequestTime), windowCanon.KeyComponent), e.reuseRetrievalIdentity, e.reusePromptVersions, e.reuseVersionAuthorities, binding.Epoch); err != nil {
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
		e.emitBindingEpochDelta(ctx, principal, epochDeltaSample)
	}
	return result, nil
}

// resolvePriorSubjectHints expands PriorSubjectReceipts into SubjectHints by
// loading each referenced prior InvestigationResult (deduplicated per
// ResultID) and matching ReceiptID against that result's
// SubjectResolution.Candidates. A receipt that fails to load (not found,
// unauthorized, unavailable) or has no matching candidate is silently
// skipped: an unresolvable prior-turn reference must degrade to "not bound"
// rather than fail the whole investigation or fall back to an unauthorized
// guess.
//
// CHAOS-3859: a successful match here -- a receipt naming a real candidate
// in a real prior result -- IS the observable "the caller resolved a
// clarification" event, independent of whether the resulting SubjectHint
// later survives re-authorization/graph resolution. captureClarification
// Selection is called at exactly this point, never later, so capture
// reflects what the caller asked for, not what Engine ultimately did with
// it (condition 6's re-authorization is a separate, already-covered
// concern -- see AnswerReuseGate's doc comment for the identical
// distinction drawn between "the caller's request" and "what the backend
// independently proves").
// Returns (hints, validated, unloadable, noMatch, staleGraphEpoch,
// staleGraphEpochDelta) -- unloadable/noMatch/staleGraphEpoch (CHAOS-3888,
// extended by CHAOS-3898 §2.2) split what used to be one implicit
// "receiptCount - len(hints)" gap into WHY each non-hint receipt fell out:
// unloadable counts a receipt with a blank ResultID/ReceiptID or whose
// prior InvestigationResult failed to load; staleGraphEpoch (§2.2) counts a
// receipt whose prior result loaded but whose StoredInvestigationResult
// carrier failed the ingress taint gate -- a DISTINCT reason from
// unloadable, because the row itself loaded fine, it simply describes a
// different graph epoch than this investigation is reading from; noMatch
// counts a receipt whose prior result loaded, passed the taint gate, and
// still named no matching candidate.
// recordPriorSubjectReceiptSkips (below) computes the fourth reason,
// failed_reauth, from what happens to the returned hints afterward.
//
// staleGraphEpochDelta (CHAOS-3898 P2 fix-forward, codex retroactive
// review of #151/#152) is cf_receipt_taint_strip's required epoch_delta
// field's own accumulator: the SUM, over every receipt counted in
// staleGraphEpoch, of (binding.Epoch − the receipt's own stored
// GraphEpoch, treating an absent GraphEpoch as 0) -- see
// EngineTelemetry.RecordPriorSubjectReceiptSkipReason's own doc comment
// for why this is a sum rather than one value per receipt.
//
// validated is the SUBSET of receipts (same order as hints, 1:1) that
// actually matched a candidate -- CHAOS-3898 P1-1 fix-forward (codex
// retroactive review of #151/#152): this investigation's own INTERPRETER
// call must never see a receipt that has not yet passed this function's own
// taint/match gate, so a caller resolving receipts pre-Interpret (the
// required ordering below) needs the validated subset, not just derived
// hints, to build Interpret's own input.
//
// binding is THIS investigation's own ResolvedGraphBinding (CHAOS-3898
// §2.1/§2.2 ingress taint): before any field of a loaded prior result is
// read -- no id, no label, no candidate -- its carrier's GraphEpoch is
// compared against binding.Epoch. A mismatch (including a nil GraphEpoch,
// a pre-migration or reuse-disabled row) strips the receipt ENTIRELY,
// exactly like an unloadable one: a receipt naming a subject discovered
// under a graph epoch this investigation is not reading from must never
// contribute a label or a hint, however innocuous re-using its identifier
// alone might look.
func (e *Engine) resolvePriorSubjectHints(ctx context.Context, principal storage.Principal, consumer ConsumerInfo, receipts []BoundSubjectReceipt, binding ResolvedGraphBinding) ([]SubjectHint, []BoundSubjectReceipt, int, int, int, int64) {
	hints := make([]SubjectHint, 0, len(receipts))
	validated := make([]BoundSubjectReceipt, 0, len(receipts))
	loaded := make(map[string]InvestigationResult, len(receipts))
	unloadable := 0
	noMatch := 0
	staleGraphEpoch := 0
	var staleGraphEpochDelta int64
	for _, receipt := range receipts {
		if ctx.Err() != nil {
			return hints, validated, unloadable, noMatch, staleGraphEpoch, staleGraphEpochDelta
		}
		resultID := strings.TrimSpace(receipt.ResultID)
		receiptID := strings.TrimSpace(receipt.ReceiptID)
		if resultID == "" || receiptID == "" {
			unloadable++
			continue
		}
		prior, ok := loaded[resultID]
		if !ok {
			fetched, err := e.results.Get(ctx, principal, resultID)
			if err != nil {
				unloadable++
				continue
			}
			// CHAOS-3898 §2.2: the taint gate runs BEFORE any field of
			// fetched.Result is read -- a carrier whose GraphEpoch is
			// absent, or differs from this investigation's own binding,
			// is stripped entirely, never partially trusted. Counted as
			// its OWN reason (cf_receipt_taint_strip, §5b), distinct from
			// unloadable: the row loaded fine, it simply names a
			// different graph epoch.
			if fetched.GraphEpoch == nil || *fetched.GraphEpoch != binding.Epoch {
				staleGraphEpoch++
				// storedEpoch defaults to 0 for an absent GraphEpoch (a
				// pre-migration or reuse-disabled row) -- the same "no
				// epoch stamped" convention this package already uses
				// elsewhere (a nil EpochResolver degrades every rewired
				// site to epoch 0's key).
				var storedEpoch int64
				if fetched.GraphEpoch != nil {
					storedEpoch = *fetched.GraphEpoch
				}
				staleGraphEpochDelta += binding.Epoch - storedEpoch
				continue
			}
			prior = fetched.Result
			loaded[resultID] = prior
		}
		matched := false
		for _, candidate := range prior.SubjectResolution.Candidates {
			if candidate.ReceiptID != receiptID {
				continue
			}
			matched = true
			e.captureClarificationSelection(ctx, principal, consumer, resultID, prior, candidate)
			hints = append(hints, SubjectHint{
				Kind: candidate.Subject.Kind, ID: candidate.Subject.CanonicalID,
				Label: candidate.Subject.Label, Source: "prior_subject_receipt",
			})
			validated = append(validated, receipt)
			break
		}
		if !matched {
			noMatch++
		}
	}
	return hints, validated, unloadable, noMatch, staleGraphEpoch, staleGraphEpochDelta
}

// captureClarificationSelection builds and hands off a
// ClarificationSelectionEvent (CHAOS-3859 capture-only phase) for one
// receipt that resolvePriorSubjectHints just matched against a real
// candidate in a real prior result. A nil clarificationSelectionSink is the
// ordinary "capture is off" case and this is a no-op. The sink call is
// synchronous but MUST return promptly by its own documented contract --
// see ClarificationSelectionSink's doc comment -- so this never adds
// meaningful latency to Investigate, and it MUST NOT be able to fail this
// call: there is no error path back from RecordSelection by design.
func (e *Engine) captureClarificationSelection(ctx context.Context, principal storage.Principal, consumer ConsumerInfo, priorResultID string, prior InvestigationResult, selected SubjectCandidate) {
	if e.clarificationSelectionSink == nil {
		return
	}
	// sol review F3: gate on the EXACT same condition
	// answerprojection.projectClarification uses (project.go:401,
	// `result.Status != contractsv1.ContextFabricInvestigationClarificationRequired`)
	// -- a caller can attach a PriorSubjectReceipts entry naming ANY prior
	// result's ReceiptID regardless of that result's own Status
	// (resolvePriorSubjectHints' own matching loop, correctly, does not
	// care -- re-authorizing an already-committed subject on a
	// conversational follow-up is a real, intended use of the SAME
	// mechanism). But a result that was never presented as a clarification
	// choice in the first place -- complete, partial, degraded, or
	// no_match -- was never something a caller "selected" FROM; capturing
	// its candidates as a labeled clarification choice would poison the
	// training signal with pairs that never happened. Only a genuine
	// clarification_required prior result is a real selection event.
	if prior.Status != InvestigationClarificationRequired {
		return
	}
	offered := make([]ClarificationOfferedCandidate, len(prior.SubjectResolution.Candidates))
	var selectedOffered ClarificationOfferedCandidate
	for i, candidate := range prior.SubjectResolution.Candidates {
		offered[i] = ClarificationOfferedCandidate{
			ReceiptID: candidate.ReceiptID, SubjectKind: string(candidate.Subject.Kind),
			SubjectCanonicalID: candidate.Subject.CanonicalID,
			State:              string(candidate.State), Confidence: candidate.Confidence, Rank: i,
		}
		if candidate.ReceiptID == selected.ReceiptID {
			selectedOffered = offered[i]
		}
	}
	e.clarificationSelectionSink.RecordSelection(ctx, ClarificationSelectionEvent{
		OrgID: principal.OrgID, CapturedAt: e.now().UTC(),
		QuestionHash: QuestionHash(prior.Question), PriorResultID: priorResultID,
		OfferedCandidates: offered, Selected: selectedOffered,
		SelectionProvenance: clarificationSelectionProvenance(principal, consumer),
		ProjectionVersion:   e.reuseProjectionVersion, ModelIdentities: e.reuseModelIdentities,
		RetrievalIdentity: e.reuseRetrievalIdentity, PromptVersions: e.reusePromptVersions,
		VersionAuthorities: e.reuseVersionAuthorities,
	})
}

// recordPriorSubjectReceiptSkips counts every PriorSubjectReceipt that did
// not end up bound to a resolved subject on this Investigate call and
// reports only that count (never receipt, result, or subject content)
// through the optional EngineTelemetry hook. A receipt counts as skipped
// both when resolvePriorSubjectHints already dropped it (unloadable prior
// result, no matching candidate) and when it became a hint but the subject
// did not survive graph resolution -- e.g. GraphReader's exact-hint
// authorization check silently rejected it, the same path an ordinary
// client-supplied SubjectHint goes through. Either way Investigate itself
// never errors or otherwise surfaces the skip, so this is the only
// operator-visible signal.
// unloadable, noMatch, and staleGraphEpoch (CHAOS-3888, extended by
// CHAOS-3898 §2.2/§5b) are resolvePriorSubjectHints' own three skip-reason
// counts (see its doc comment); this function computes the fourth,
// failed_reauth, from priorHints/resolution, and reports all four through
// RecordPriorSubjectReceiptSkipReason IN ADDITION TO the existing,
// unchanged RecordPriorSubjectReceiptsSkipped aggregate below -- the
// aggregate's own formula and call site are byte-identical to before this
// ticket. staleGraphEpoch is cf_receipt_taint_strip (§5b): a receipt whose
// carrier named a different graph epoch than this investigation's own
// binding, reported under its own reason so an operator can tell "the
// prior turn's answer predates the current build/flip" apart from every
// other skip cause.
func (e *Engine) recordPriorSubjectReceiptSkips(ctx context.Context, principal storage.Principal, receiptCount int, priorHints []SubjectHint, resolution SubjectResolution, unloadable, noMatch, staleGraphEpoch int, staleGraphEpochDelta int64) {
	if e.telemetry == nil {
		return
	}
	resolved := make(map[string]struct{}, len(resolution.Candidates)+len(resolution.Committed))
	for _, candidate := range resolution.Candidates {
		resolved[subjectKeyForModel(candidate.Subject)] = struct{}{}
	}
	for _, subject := range resolution.Committed {
		resolved[subjectKeyForModel(subject)] = struct{}{}
	}
	survived := 0
	for _, hint := range priorHints {
		if _, ok := resolved[string(hint.Kind)+"\x00"+hint.ID]; ok {
			survived++
		}
	}
	skipped := receiptCount - survived
	if skipped > 0 {
		e.telemetry.RecordPriorSubjectReceiptsSkipped(ctx, principal, skipped)
	}
	// CHAOS-3888: failed_reauth is everything skipped that resolvePriorSubjectHints
	// itself did NOT already attribute to unloadable/no_match/stale_graph_epoch
	// -- a hint that was built (or would have been, had the per-request
	// SubjectHints budget not truncated it away, engine.go's own
	// maxSubjectHints comment) but did not survive THIS call's graph
	// resolution. Never negative: skipped is computed over priorHints
	// (which resolvePriorSubjectHints' unloadable/noMatch/staleGraphEpoch
	// counts already exclude), so skipped can only ever be >= the sum of
	// the other three.
	if failedReauth := skipped - unloadable - noMatch - staleGraphEpoch; failedReauth > 0 {
		e.telemetry.RecordPriorSubjectReceiptSkipReason(ctx, principal, "failed_reauth", failedReauth, 0)
	}
	if unloadable > 0 {
		e.telemetry.RecordPriorSubjectReceiptSkipReason(ctx, principal, "unloadable", unloadable, 0)
	}
	if noMatch > 0 {
		e.telemetry.RecordPriorSubjectReceiptSkipReason(ctx, principal, "no_match", noMatch, 0)
	}
	// CHAOS-3898 §5b cf_receipt_taint_strip: epochDelta is the ONLY reason
	// this method's epochDelta parameter is ever non-zero (see
	// EngineTelemetry.RecordPriorSubjectReceiptSkipReason's own doc
	// comment).
	if staleGraphEpoch > 0 {
		e.telemetry.RecordPriorSubjectReceiptSkipReason(ctx, principal, "stale_graph_epoch", staleGraphEpoch, staleGraphEpochDelta)
	}
}

// bindingEpochDeltaSample is sampleBindingEpochDelta's own result: ok is
// false whenever telemetry is disabled or the re-resolution itself failed
// (fails open, same convention as every other optional signal in this
// file), in which case emitBindingEpochDelta must be a no-op.
type bindingEpochDeltaSample struct {
	ok      bool
	flipped bool
	delta   int64
}

// sampleBindingEpochDelta is the CHAOS-3898 §5b flip_during_investigation/
// cf_binding_epoch_delta signal's SAMPLE half (see
// EngineTelemetry.RecordBindingEpochDelta's own doc comment for the full
// contract). original is the ResolvedGraphBinding this investigation's own
// graph reads and Save actually used -- captured once, at request start,
// and NEVER re-resolved for correctness anywhere else in this package.
// This function's own re-resolution exists SOLELY to produce the telemetry
// comparison; its result is read nowhere but by emitBindingEpochDelta.
//
// CHAOS-3898 P2 fix-forward (codex retroactive review of #151/#152,
// chris-verified): this call MUST happen immediately BEFORE Save, not
// after it -- Save's own I/O duration used to sit inside the window this
// re-resolution measures, so a flip landing strictly AFTER Save had
// already persisted the result (work this investigation was no longer
// doing) could still be attributed to "during" it. Sampling right before
// Save closes that gap; emitBindingEpochDelta (below) still only reports
// the sample once Save has actually succeeded, so a failed Save still
// emits nothing, exactly as before.
func (e *Engine) sampleBindingEpochDelta(ctx context.Context, principal storage.Principal, original ResolvedGraphBinding) bindingEpochDeltaSample {
	if e.telemetry == nil {
		return bindingEpochDeltaSample{}
	}
	current, err := e.graph.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		return bindingEpochDeltaSample{}
	}
	delta := current.Epoch - original.Epoch
	return bindingEpochDeltaSample{ok: true, flipped: delta != 0, delta: delta}
}

// emitBindingEpochDelta reports a sample sampleBindingEpochDelta already
// took -- called only after Save has succeeded, so this can never affect
// whether a result is persisted or what epoch it is stamped with. A
// not-ok sample (telemetry disabled, or the sample's own re-resolution
// failed) is silently skipped, matching every other fail-open signal here.
func (e *Engine) emitBindingEpochDelta(ctx context.Context, principal storage.Principal, sample bindingEpochDeltaSample) {
	if !sample.ok {
		return
	}
	e.telemetry.RecordBindingEpochDelta(ctx, principal, sample.flipped, sample.delta)
}

func investigationSubjects(resolution SubjectResolution, cohort *Cohort) []SubjectRef {
	seen := make(map[string]struct{})
	result := make([]SubjectRef, 0, len(resolution.Committed))
	appendSubject := func(subject SubjectRef) {
		key := string(subject.Kind) + "\x00" + subject.CanonicalID
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, subject)
	}
	for _, subject := range resolution.Committed {
		appendSubject(subject)
	}
	if cohort != nil {
		for _, member := range cohort.Members {
			appendSubject(member.Subject)
		}
	}
	return result
}

func mergeFactRequirements(groups ...[]FactRequirement) []FactRequirement {
	result := make([]FactRequirement, 0)
	seen := make(map[FactKind]struct{})
	for _, group := range groups {
		for _, requirement := range group {
			if _, exists := seen[requirement.Kind]; exists {
				continue
			}
			seen[requirement.Kind] = struct{}{}
			result = append(result, requirement)
		}
	}
	return result
}

// retrievalDegradedLimitation, retrievalDegradedLimitationLegacy and
// isRetrievalDegradedLimitation now live in contracts/v1 and are aliased
// here (CHAOS-3746).
//
// The move is what the answer projection needed: it must recognise this
// limitation on a stored row, and it may not import this package --
// answerprojection is import-pure so both the hosted API and the MCP
// sidecar can call it. See context_fabric_limitations.go for what each
// string means and why both spellings are permanent.
//
// REBASE-TIME OBLIGATION (CHAOS-3778, carried deliberately): a REUSED
// answer must carry its stored limitation forward VERBATIM -- including
// the legacy spelling -- and must not have one synthesized for it. That
// behavior lives on CHAOS-3786's reuse path. The ordering it relies on is
// already traced: Engine.tryReuse returns before ResolveSubjects runs, so
// a reuse hit computes no marker of its own.
const (
	retrievalDegradedLimitation       = contractsv1.ContextFabricRetrievalDegradedLimitation
	retrievalDegradedLimitationLegacy = contractsv1.ContextFabricRetrievalDegradedLimitationLegacy
)

var isRetrievalDegradedLimitation = contractsv1.IsContextFabricRetrievalDegradedLimitation

// hasRetrievalDegradedLimitation reports whether any limitation in the
// slice is one of the two spellings. Aliased to the contract's own scanner
// (CHAOS-3746 round-16): contracts/v1 needs it to enforce
// LimitationsDisplaced's coherence rule, and a second copy here would be a
// second thing that can drift from the vocabulary it scans for.
var hasRetrievalDegradedLimitation = contractsv1.HasContextFabricRetrievalDegradedLimitation
