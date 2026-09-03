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
	// RegimeAOffersDisabled (CHAOS-4234) turns OFF the offers-only
	// resolution the class-default window gate runs to compose kind/
	// handle/candidate offers beside the window offer -- restoring
	// CHAOS-4118's window-only disclosure. Zero value = ENABLED (the
	// ruling's default); this exists as the reversibility lever only.
	// See chaos4234_offers_only.go.
	RegimeAOffersDisabled bool
	// MaxItems (CHAOS-4636) is the SERVICE-configured item ceiling the
	// investigation route's own 413 gate enforces (ACR_MAX_ITEMS, default
	// 30). The engine needs it to measure its own assembled answer against
	// the same number the route will, which is stage 3 of the plan-time
	// budget: an over-budget answer is re-synthesized once with a smaller
	// input HERE, before validation and persistence, rather than 413'd
	// after 48 seconds of server work.
	//
	// It is service configuration and never caller input, so it is an
	// engine option rather than a request field -- a caller must not be
	// able to raise the ceiling it is measured against.
	//
	// ZERO MEANS UNBOUNDED, which is what every existing composition and
	// every test that does not set it means. An engine left unconfigured
	// therefore behaves exactly as it did before this slice: it plans, but
	// it never narrows on an item budget it was not told about.
	MaxItems int
	// SynthesisDeadlineReserve (CHAOS-4636, decision D5's non-negotiable
	// half) is the slice of the request deadline held back for a possible
	// re-synthesis.
	//
	// The whole request shares one timeout (internal/api/app.go's
	// timeoutMiddleware) and the pre-read budget only bounds fan-out, so an
	// UNRESERVED retry after a slow first synthesis is a 504 rather than a
	// partial answer -- the second, independent hole D5 records beside the
	// over-determined-contract one. Reserving the slice is what makes the
	// terminal case a designed refusal instead of a timeout.
	//
	// Zero disables the reservation, and with it the retry: an engine that
	// was not told how long it may spend does not gamble the caller's
	// deadline on a second model call.
	SynthesisDeadlineReserve time.Duration
	// MaxSerializedBytes (CHAOS-4636) is the SERVICE-configured byte
	// ceiling (ACR_MAX_SERIALIZED_BYTES, default 1 MiB). The route enforces
	// min(service, caller) on the marshaled body; the engine measures
	// against the same minimum so its own fit decision matches the one the
	// route will make. Zero means unbounded, and then only the caller's own
	// requested ceiling applies -- which is what every existing composition
	// means today.
	MaxSerializedBytes int64
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
	// HandleVerifier is CHAOS-3900 P1.E's redemption-time re-verification
	// dependency for handr_ structure receipts (design brief §2.1). UNLIKE
	// every other optional dependency on this struct, leaving this nil
	// does NOT degrade to "the feature did not exist": canonicalizeStructure's
	// handle reverify hook (structure.go) fails CLOSED when
	// Engine.handleVerifier is nil, vetoing any request that presents a
	// handr_ receipt. This is deliberate -- see HandleVerifier's own doc
	// comment for why an unwired verifier applying a stored value anyway
	// would be a false sense of safety. A deployment that never mints
	// handle offers (P1.C' not yet built) never exercises this path
	// regardless, so leaving it nil is safe ONLY until handle offers exist.
	HandleVerifier HandleVerifier
	// AnchorVerifier is CHAOS-3900 P1.E's redemption-time re-verification
	// dependency for ancr_ structure receipts -- same fail-CLOSED-when-nil
	// contract as HandleVerifier above, see AnchorVerifier's own doc
	// comment (structure.go).
	AnchorVerifier AnchorVerifier
	// AnchorMembershipVerifier is CHAOS-4042's (sol-max ruling) own
	// redemption-time re-verification dependency for a v2 (membership-
	// verify) ancr_ structure receipt -- same fail-CLOSED-when-nil
	// contract as AnchorVerifier above, see AnchorMembershipVerifier's own
	// doc comment (structure.go). A deployment that never mints v2 anchor
	// offers never exercises this path regardless, so leaving it nil is
	// safe ONLY until they exist.
	AnchorMembershipVerifier AnchorMembershipVerifier
	// CandidateVerifier is CHAOS-4012's redemption-time re-verification
	// dependency for candr_ structure receipts -- same fail-CLOSED-when-nil
	// contract as HandleVerifier/AnchorVerifier above, see CandidateVerifier's
	// own doc comment (structure.go). A deployment that never mints
	// candidate offers never exercises this path regardless, so leaving it
	// nil is safe ONLY until they exist.
	CandidateVerifier CandidateVerifier
	// StructureSelectionSink is optional (CHAOS-3927 P4, capture-only
	// phase, mirroring ClarificationSelectionSink's own contract exactly).
	// When set, Engine notifies it every time a caller's kindr_/ancr_/
	// handr_ receipt successfully resolves to a confirmed structure
	// member -- see StructureSelectionSink's own doc comment for the
	// fail-open contract this dependency must uphold. Leaving this nil
	// means no structure selection is ever captured, exactly as if the
	// feature did not exist -- capture is strictly additive and never
	// changes canonicalizeStructure's own resolution behavior.
	StructureSelectionSink StructureSelectionSink
	// PriorConsultant is optional (CHAOS-3977 P5, design brief §3.4). When
	// set, Engine consults it at EXACTLY the two DP4(a)-ruled sites --
	// consultPriorStructureOffers (the StructureNeeds offer builder) and
	// resolveWindowPriorProposal (the inferred-default proposal slot),
	// priors_consult.go -- for prior-sourced, non-decisive offer/default
	// proposals. Leaving this nil means no prior is ever consulted, exactly
	// as if the feature did not exist: every offer stays engine-derived,
	// byte-identical to pre-P5 behavior. See PriorConsultant's own doc
	// comment for the fail-open, org-scoped contract this dependency must
	// uphold.
	PriorConsultant PriorConsultant
	// PriorHandleGrammarChecker is CHAOS-3977 P5's own offer-time grammar
	// check for a prior-proposed subject_handle entry -- the SAME
	// HandleGrammarChecker type graphrank.ResolveDeps already threads for
	// engine-derived explicit-handle offers (structure.go's own doc
	// comment), duplicated onto Engine because a prior-sourced proposal is
	// merged AFTER ResolveSubjects returns, outside graphrank's own call
	// boundary. nil means a prior can never propose a subject_handle offer
	// (the safe degradation -- mergePriorHandleOffers, priors_consult.go --
	// never a redemption-time weakening: HandleVerifier's own fail-closed
	// reverify still gates every handr_ receipt regardless of OfferSource).
	PriorHandleGrammarChecker HandleGrammarChecker
	// OfferPhraser (CHAOS-4171 PR2) is optional. When set, Engine runs a
	// SECOND bounded model call after composeStructureNeeds/
	// composeGatedStructureNeeds compose a request's structural offer set,
	// rewriting each option's presentation-facing Phrasing under a
	// closed-vocabulary guard -- see applyOfferPhrasing's own doc comment
	// (chaos4171_offer_phrasing.go) for the exact hook sites and the
	// fail-open contract. Leaving this nil means no phrasing is ever
	// attempted, exactly as if the feature did not exist: every option's
	// Label stands alone, byte-identical to before this ticket.
	OfferPhraser OfferPhraser
}

// EngineTelemetry receives content-safe operational counters from Engine.
// Implementations must record only counts and fixed classifications --
// never question text, subject labels, canonical IDs, result IDs, or any
// other investigation content -- so a signal is diagnosable without
// becoming a new disclosure surface.
type EngineTelemetry interface {
	// QuestionFamilyTelemetry (CHAOS-4632 §4.3) is EMBEDDED, not offered
	// as a separate optional interface a caller might or might not
	// implement.
	//
	// That is deliberate and it is the CHAOS-4085 lesson applied
	// structurally: CommitAffirmationTelemetry was optional, nothing in
	// production implemented it, every retraction failed its type
	// assertion, and the whole event class vanished with tests green
	// throughout. Embedding makes a telemetry implementation that cannot
	// report family resolutions a COMPILE ERROR rather than a silently
	// empty log stream -- see chaos4085_telemetry_sink_test.go's header
	// for the full account of what that miss cost.
	QuestionFamilyTelemetry
	// PlanTelemetry (CHAOS-4636) is embedded for the same reason
	// QuestionFamilyTelemetry above is: a required interface, never an
	// optional one discovered by type assertion. See PlanTelemetry's own
	// doc comment for the CHAOS-4085 incident that rule comes from.
	PlanTelemetry
	// FrameValidationTelemetry (CHAOS-4452 stage 2, §13.6) is embedded on
	// the same rule as the two above: a required interface, never an
	// optional one discovered by type assertion.
	//
	// Embedding matters more here than usual, because the frame layer's
	// ONLY output in phase 1 is its measurement. Nothing is gated on the
	// frame, so a telemetry implementation that silently failed to report
	// validations would not break a single behaviour -- it would simply
	// make the slice produce nothing, and the shadow gate would read as
	// "no invalid frames" rather than "no frames observed". Embedding
	// makes that state a compile error.
	FrameValidationTelemetry

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
	// RecordAnswerReuseContainment reports the condition-6 containment
	// MEASUREMENT for one reuse attempt whose evidence leg actually ran
	// (CHAOS-4831; the differentiation half of the sibling telemetry
	// ticket): how many distinct references the stored payload would
	// serve, how many a fresh discovery proved this caller can still see,
	// how many could not be proven, whether any of those was a top-level
	// citation, and -- when the attempt degraded rather than refused --
	// exactly what was removed from the served copy.
	//
	// Why a measurement and not another label: an outcome label answers
	// "did reuse happen", never "how close was it". A reuse rate that is
	// low because the recheck demands references a fresh discovery
	// structurally cannot return looks IDENTICAL, in a label-only stream,
	// to one that is low because authorization genuinely narrowed. That
	// ambiguity hid a total (0/8) failure on real data behind a value
	// whose own name suggested staleness. These counts are what make the
	// difference readable from the run's own artifacts.
	//
	// Emitted only when the containment leg ran -- never as zeros for an
	// attempt that refused earlier, which would be indistinguishable from
	// a genuine "demanded nothing".
	RecordAnswerReuseContainment(ctx context.Context, principal storage.Principal, event AnswerReuseContainmentEvent)
	// RecordSubjectlessTerminal (CHAOS-3888) reports WHY one Investigate
	// call reached its own subjectless terminal path (terminalResult,
	// unresolved.go) as a closed reason string -- "empty_pool",
	// "authz_filtered_to_empty", or "ambiguous". See
	// subjectlessTerminalReason's own doc comment for the exact
	// classification and why an authorization-narrowing cause specifically
	// must stay telemetry-only, never surfacing in the response contract.
	RecordSubjectlessTerminal(ctx context.Context, principal storage.Principal, reason string)
	// RecordSynthesisStatusOverride (CHAOS-4098) reports that the engine
	// served a DIFFERENT investigation status than the synthesis step
	// returned -- today only clarification_required -> no_match, when the
	// synthesized draft asked for clarification on a path that has none to
	// offer (applySynthesisStatusOverride).
	//
	// Declared on THIS interface rather than as an optional side interface
	// so every implementation must carry it or fail to compile. CHAOS-4085
	// shipped its own retraction telemetry behind an optional interface,
	// nothing in production implemented it, and every retraction vanished
	// silently until #207 caught it; a decision branch whose telemetry can
	// go missing by omission is the CHAOS-4089 failure mode itself.
	//
	// Without this stream the override is invisible: the caller receives a
	// perfectly ordinary no_match answer, and nothing distinguishes "the
	// evidence genuinely supported no match" from "the model declined to
	// conclude and the engine relabelled it". The defect this exists for
	// (case 60 of the v9 rerun) was diagnosable ONLY by reading the raw
	// model-exchange files off a scratch directory after the run -- exactly
	// the archaeology CANONICAL ARCHITECTURE's diagnosis-in-artifacts rule
	// forbids relying on.
	RecordSynthesisStatusOverride(ctx context.Context, principal storage.Principal, outcome SynthesisStatusOverrideOutcome)
	// RecordFactScopeExpansion (CHAOS-4099) reports ONE fact-read scope
	// decision: for a given requirement kind, origin subject kind and named
	// policy, whether the fact family could be reached from the subjects
	// this investigation resolved, and if not, why not.
	//
	// Declared on THIS interface rather than as an optional side interface,
	// for the reason RecordSynthesisStatusOverride's own comment above
	// spells out: CHAOS-4085 shipped telemetry behind an optional interface
	// that nothing implemented, and every event vanished silently. An
	// expansion decision that can go missing by omission is the CHAOS-4089
	// failure mode itself, and this branch is the one the whole ticket
	// exists to make diagnosable.
	//
	// WITHOUT THIS STREAM THE DECISION IS INVISIBLE. The caller receives an
	// ordinary answer carrying a fixed, deliberately non-specific
	// disclosure -- it names no fact family, no policy and no subject kind,
	// because a reader cannot act on any of those. So nothing in the
	// response distinguishes "the metrics policy is still disabled" from
	// "the traversal ran and this project genuinely touches no repository"
	// from "authorization removed every candidate". Those demand three
	// different operator responses, and this event is the only place they
	// are told apart.
	//
	// Emitted once per (requirement, origin kind, policy) triple that
	// needed a decision, and NOT AT ALL for a requirement answerable
	// directly from its own subjects -- a not_needed event on every
	// ordinary requirement would bury the signal under the base rate.
	//
	// Content-safe by construction like every method beside it: closed
	// enums and counts only. AuthorizationDroppedCount in particular is
	// telemetry-ONLY and must never reach the answer or public provenance
	// (ruling invariant 9) -- "there were subjects you may not see" is an
	// existence side-channel, and this stream is where it is safely said.
	RecordFactScopeExpansion(ctx context.Context, principal storage.Principal, event FactScopeExpansionEvent)
	// RecordCategoryFactComposition (CHAOS-4347) reports ONE status-category
	// composition decision: a bare FactStatus requirement was expanded into
	// the closed fact-kind set for one resolved subject kind
	// (statusCategoryFactKindComposition, chaos4347_status_category_composition.go).
	// Declared on THIS interface, not an optional side interface, for the
	// SAME reason RecordFactScopeExpansion/RecordSynthesisStatusOverride are:
	// a decision branch whose telemetry sink can be omitted by a compiling
	// implementation is the CHAOS-4085/CHAOS-4089 failure mode this repo
	// keeps re-learning. Content-safe: three closed enums/enum-slices only.
	RecordCategoryFactComposition(ctx context.Context, principal storage.Principal, event CategoryFactCompositionEvent)
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
	// RecordWindowCarry (CHAOS-4360) reports the outcome of ONE
	// same-conversation window-carry attempt -- see WindowCarryOutcome's
	// own doc comment for the closed vocabulary (chaos4360_carry.go).
	// Called at most once per Investigate call, ONLY when this turn's own
	// window would otherwise be inferred_default (the same "once per
	// non-zero signal" convention RecordWindowCanonicalization's sibling
	// counters already use) -- so the denominator across every call this
	// fires for IS the carry-eligible population the N-turn harness'
	// "carry hit rate" measures. chainDepth is meaningful only when
	// outcome==hit (0 for every miss).
	RecordWindowCarry(ctx context.Context, principal storage.Principal, outcome WindowCarryOutcome, chainDepth int)
	// RecordKindCarry reports the outcome of ONE same-conversation
	// expected_kind carry attempt -- see KindCarryOutcome's own doc comment
	// for the closed vocabulary (structure_axis_carry.go). Called at most
	// once per Investigate call, ONLY when this turn confirmed no kind of
	// its own, so the denominator across every call this fires for IS the
	// carry-eligible population, exactly as RecordWindowCarry's is. A
	// SEPARATE counter rather than one call with an axis label: a reader
	// diagnosing an ask/answer oscillation must be able to tell WHICH axis
	// missed, and the two axes have independent hit rates.
	RecordKindCarry(ctx context.Context, principal storage.Principal, outcome KindCarryOutcome, chainDepth int)
	// RecordStructureNeedsDisclosed (CHAOS-3900 P1.F, design brief §2.1's
	// cf_structure_needs_disclosed{member}) reports one member appearing
	// in a composed StructureNeeds.Missing -- called once per member,
	// only on the subjectless-terminal path StructureNeeds is ever
	// composed on (structure.go's own scope note: never on the main
	// synthesized-answer path).
	RecordStructureNeedsDisclosed(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind)
	// RecordGatedOfferResolution (CHAOS-4234) reports, once per
	// class-default gated request, whether the offers-only resolution
	// composed offers beside the window offer -- closed vocabulary
	// GatedOfferResolutionOutcome (chaos4234_offers_only.go).
	RecordGatedOfferResolution(ctx context.Context, principal storage.Principal, outcome GatedOfferResolutionOutcome)
	// RecordCohortStructureGate (CHAOS-4579/CHAOS-4531) reports which side
	// of §1.3's class-conditional gate one StructureOfferMaterial landed
	// on: the question had no subject axis and the subject_anchor/
	// subject_handle rows were removed ("applied"), had no subject axis but
	// carried nothing to remove ("no_op"), or has a subject axis and passed
	// through under the standing zero-candidates ruling
	// ("subject_bearing"). Both outcomes AND the denominator are reported,
	// so "cohort vs subject clarification" is a countable split in the
	// run's own artifacts rather than an inference from a missing log line
	// -- see GateSubjectAxisOffers (chaos4579_cohort_structure_gate.go).
	// Both arguments are closed enums; neither carries question text, a
	// subject identifier, or an offer label.
	//
	// DENOMINATOR, stated exactly (codex round 1, findings 2 and 3 -- an
	// earlier version of this comment claimed "once per composed
	// StructureNeeds disclosure", which was wrong in BOTH directions):
	// this fires once per GateSubjectAxisOffers call, i.e. once per request
	// that reached a candidate-pool offer decision. That is deliberately
	// NOT the same set as "requests whose result carries StructureNeeds",
	// and the two differ in both directions:
	//
	//   - A gate-1 (explicit-unconfirmed) window terminal composes a
	//     window-only StructureNeeds and emits NO event. That gate fires
	//     before Interpret runs, so there is no model-set shape to report
	//     -- only windowConfirmationRequiredResult's own synthesized
	//     ShapeOpen placeholder, and reporting that as if it were the
	//     question's class would be a fabricated reading. It also builds no
	//     anchor or handle material, so there is no decision to record.
	//   - A request whose material is empty (a never-projected org, say)
	//     emits an event and then composes no StructureNeeds at all,
	//     because composeStructureNeeds returns nil for empty material.
	//     Suppressing the event there would ALSO suppress the "applied"
	//     event for a cohort request whose anchor/handle rows were its only
	//     material -- exactly the case this ticket exists to make visible.
	//
	// So: to count clarification disclosures, read
	// cf_structure_needs_disclosed. To count class-gate decisions, read
	// this. Neither is the other's denominator.
	RecordCohortStructureGate(ctx context.Context, principal storage.Principal, outcome CohortStructureGateOutcome, shape InvestigationShape)
	// RecordWindowGateOfferDisclosure (CHAOS-4314) reports, once per
	// window-gated terminal (both windowConfirmationRequiredResult call
	// sites -- explicit-unconfirmed gate 1 and class-default gate 2), whether
	// the composed StructureNeeds carried a window_expand recommendation.
	// offered=true is the "window_gated_offered" report-schema split;
	// offered=false is "window_gated_silent" -- gate 1 and every gate-2
	// origin other than GatedOfferResolutionComposed report false by
	// construction (gatedMaterial is the zero value there).
	RecordWindowGateOfferDisclosure(ctx context.Context, principal storage.Principal, offered bool)
	// RecordWindowExpandOfferRedeemed (CHAOS-4314) reports one successful
	// winr_ receipt redemption (resolveWindowReceipts) whose receipt was
	// ALSO offered as this same result's own window_expand recommendation --
	// the "accepted" half of the offer_kind=window_expand accepted/declined
	// split (declined is report-layer derived: offered on turn 1, never
	// redeemed by turn 2). Called only on full redemption success, never on
	// a veto/conflict/stale-superseded branch.
	RecordWindowExpandOfferRedeemed(ctx context.Context, principal storage.Principal)
	// RecordStructureOfferCount (CHAOS-3900 P1.F, design brief §2.1's
	// cf_structure_offer_count{member,source}) reports how many offers one
	// member's StructureNeeds carried, split by OfferSource (engine|prior
	// -- v1 mints only engine; the source axis exists so a future prior
	// contribution is visible without a schema change to this event).
	// Called once per (member, source) pair with a NONZERO count -- a
	// member with zero offers in one source contributes no call, mirroring
	// RecordPriorSubjectReceiptSkipReason's own "once per non-zero reason"
	// convention.
	RecordStructureOfferCount(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, source contractsv1.ContextFabricStructureOfferSource, count int)
	// RecordStructureReceipt (CHAOS-3900 P1.F, design brief §2.1's
	// cf_structure_receipt{member,outcome}) reports the OUTCOME of one
	// structure-receipt-bearing member for one Investigate call --
	// StructureReceiptOutcome's own doc comment (structure.go) for the
	// four-value vocabulary (CHAOS-3927 P4 added "stale") and why atomicity
	// makes every receipt-bearing member share the SAME outcome on a veto.
	// Called once per member that carried at least one receipt (kindr_/
	// ancr_/handr_), but NOT always immediately after canonicalizeStructure
	// returns any more (CHAOS-3927 P4, codex round-1/round-2 adversarial
	// review): a PRE-FLIGHT veto (unresolved/conflict/stale) is still final
	// the instant canonicalizeStructure returns it and is recorded right
	// there; the "applied"/"stale" outcome for a request that CONFIRMED
	// something is deferred until the confirming Save actually succeeds or
	// loses the Save-time supersession race -- see
	// Engine.recordStructureConfirmationOutcome/
	// Engine.structureSupersessionVetoResult (structure.go) for the two
	// call sites this now comes from, both of which can carry a non-empty
	// structureCanon.Confirmed.
	RecordStructureReceipt(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureReceiptOutcome)
	// RecordStructureExplicit (CHAOS-3972 P3, design brief §2.1/§2.5's
	// cf_structure_explicit{member,outcome}) reports the outcome of one
	// EXPLICIT (non-receipt) structure field -- request.ExpectedKinds/
	// SubjectHandles -- for one Investigate call. See
	// StructureExplicitOutcome's own doc comment (structure.go). Called
	// unconditionally, once per member that carried at least one explicit
	// value, immediately after canonicalizeStructure returns -- mirrors
	// RecordStructureReceipt's own call-site discipline exactly.
	RecordStructureExplicit(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome StructureExplicitOutcome)
	// RecordPriorConsulted (CHAOS-3977 P5, design brief §3.4's
	// cf_prior_consulted{member,outcome}) reports the outcome of consulting
	// the org's active prior version for ONE structure/window member, for
	// one Investigate call -- see PriorConsultedOutcome's own doc comment
	// (priors.go) for the closed vocabulary. Called at most once per member
	// per call, and only when a candidate prior entry for that member
	// existed at all (priors_consult.go's own "a member with nothing to say
	// contributes no call" discipline, mirroring RecordStructureOfferCount).
	RecordPriorConsulted(ctx context.Context, principal storage.Principal, member contractsv1.ContextFabricStructureNeedKind, outcome PriorConsultedOutcome)
	// RecordPriorDegradation (CHAOS-3977 P5, design brief §3.4's
	// cf_prior_degradation{state}) reports a consult-level failure to read
	// the prior store at all -- see PriorDegradationState's own doc comment
	// for the closed vocabulary. Every state degrades consultation to
	// engine-derived offers only and never fails or delays the round;
	// called at most once per Investigate call's prior consult (the read is
	// shared between both DP4(a) sites -- see Investigate's own call site).
	RecordPriorDegradation(ctx context.Context, principal storage.Principal, state PriorDegradationState)
	// RecordOfferPhrasing (CHAOS-4171 PR2) reports ONE applyOfferPhrasing
	// attempt's classified outcome -- generated / rejected_by_guard /
	// fell_back_structural / call_failed, the ratified telemetry names
	// (2026-08-24 22:05 PDT ruling comment). Declared on THIS interface
	// rather than an optional side interface, for the same reason
	// RecordSynthesisStatusOverride's own comment above states: an
	// outcome-affecting branch whose telemetry can go missing by omission
	// is the CHAOS-4089 failure mode itself. Called ONLY when phrasing was
	// actually attempted (e.offerPhraser non-nil and the composed
	// StructureNeeds carried at least one phraseable option) -- never for
	// a request with nothing to phrase or no phraser configured, the same
	// "nothing to do is not an outcome" convention this file's other
	// gated telemetry (e.g. RecordGatedOfferResolution's own callers)
	// already follows.
	RecordOfferPhrasing(ctx context.Context, principal storage.Principal, outcome OfferPhrasingOutcome)
	// RecordProjectedRowsCount (CHAOS-4355) reports, once per Synthesize
	// call that reaches claim assembly (draft.ValidateAgainst already
	// passed -- see RuntimeAnswerSynthesizer.Telemetry's own doc comment
	// for exactly which calls that excludes), how many ClaimedFact.Rows
	// entries attachCanonicalRows attached across the whole result -- the
	// "projected_rows_count" dimension the ticket asks for -- and whether
	// any single claim's table lost content relative to what its canonical
	// fact actually carried, whether an unambiguous table was capped to
	// fit ContextFabricClaimedFactMaxRows, or no table was attached at all
	// because the fact carried more than one Rows-shaped field and
	// canonicalFieldRows fails closed rather than guess which one a claim
	// means -- the fact-plan-adjacent "dropped by cap/pruning" signal.
	// Declared on THIS interface rather
	// than an optional side interface, for the same reason
	// RecordSynthesisStatusOverride's own comment above states: a branch
	// that can go missing by omission is the CHAOS-4089 failure mode
	// itself. count=0 included on every call this fires for, so "no
	// producer emitted a renderable table this call" stays distinguishable
	// from "nobody is counting".
	RecordProjectedRowsCount(ctx context.Context, principal storage.Principal, count int, truncated bool)
	// RecordProjectedRowsByFactKind (CHAOS-4418) reports the SAME total
	// RecordProjectedRowsCount reports, broken down per FactKind the
	// model claimed something about this call -- diagnosing WHICH fact
	// kind's producer did or did not carry a renderable table without
	// re-reading source (this file's own CANONICAL ARCHITECTURE doctrine:
	// a defect must be diagnosable from the run's own artifacts alone).
	// byKind carries an entry for every kind claimed this call, INCLUDING
	// a kind that claimed but attached zero rows -- attachCanonicalRows'
	// own doc comment explains why that must not collapse into "kind
	// absent" the same way RecordFactScopeExpansion's zero-valued counts
	// must not collapse into "nobody counted".
	RecordProjectedRowsByFactKind(ctx context.Context, principal storage.Principal, byKind map[FactKind]int)
	// RecordDualTableFacts (CHAOS-4682, §5.1 P2, standing order from the
	// same ruling) reports, once per Synthesize call that reaches claim
	// assembly -- same gating as RecordProjectedRowsCount, which this
	// always accompanies -- how many claims this call attached a SECOND,
	// additive time_series table to (attachCanonicalRowsWithDualTableTelemetry's
	// dualTableClaims), and the total serialized byte size of every
	// TimeSeriesRows attached this call (secondaryRowsBytes). P2's own
	// design doc measured this cost as low single-digit KB per affected
	// claim against a 256KB response ceiling on one real fixture; this is
	// the ongoing measurement at scale the ruling asked for rather than
	// trusting that one-off number indefinitely. Both zero-included on
	// every call, same "quiet run is as visible as a busy one" convention
	// as RecordProjectedRowsCount.
	RecordDualTableFacts(ctx context.Context, principal storage.Principal, dualTableClaims, secondaryRowsBytes int)
	// RecordRenderShapeSelection (CHAOS-4415) reports which conditional
	// render shapes the deterministic selection rules chose for THIS
	// answer, and -- equally important -- which eligible rule produced
	// nothing and why. Fires once per investigation that reaches shape
	// selection, INCLUDING when nothing was selected: "this answer
	// warranted no chart" and "nobody evaluated the rules" are different
	// states, and the first is the common, correct one. Declared on THIS
	// interface rather than an optional side interface for the same
	// reason RecordProjectedRowsCount is: a chart is an outcome-affecting
	// decision, and a branch whose telemetry can go missing by omission
	// is the CHAOS-4089 failure mode itself. Content-safe by
	// construction: RenderShapeSelectionEvent carries only closed
	// vocabulary values and counts, never a label, subject or number.
	RecordRenderShapeSelection(ctx context.Context, principal storage.Principal, event RenderShapeSelectionEvent)
	// RecordServerStatusShadow (CHAOS-4452 stage 2, behaviour change B8)
	// reports the SERVER-derived terminal status beside the model-authored
	// one, with the fact that drove it. Fires once per investigation that
	// reaches assembly with a plan -- which is the stated denominator: a
	// terminal exit has no plan to hold an answer against, and an
	// observation there would carry no information.
	//
	// REPORTS, NEVER ROUTES. The served status is unchanged. This exists
	// so the authorship move T6 proposes is decided on measured
	// disagreement rather than on the design's own argument, `status`
	// being a required wire field consumers branch on.
	//
	// Content-safe by construction: ServerStatusShadow carries two status
	// enums, one closed basis token, two booleans and a version constant.
	RecordServerStatusShadow(ctx context.Context, principal storage.Principal, event ServerStatusShadow)
	// RecordPlanCarry (CHAOS-4736, seam 7) reports the ONE point where a
	// prior turn's family replaces this turn's, which is the only place a
	// carried route is observable.
	//
	// WHY IT HAS TO EXIST. The family-resolution event is built and sent
	// inside the interpreter; applyCarriedPlan runs later, in the engine. So
	// `family_source=carried` could never appear on that line -- it was a
	// permanent zero bucket, and a comment elsewhere told stream readers to
	// "join on the plan-carry event" when no such producer existed. This is
	// that producer. It is a SECOND line, never a second family-resolution
	// line: re-emitting that one would double-count the flip counters the
	// whole slice exists to make readable.
	RecordPlanCarry(ctx context.Context, principal storage.Principal, event PlanCarryEvent)
	// RecordModelRowsStripped (CHAOS-4355 follow-up, cf_model_rows_stripped)
	// reports the count of ClaimedFacts entries whose model-authored Rows
	// was cleared before draft.ValidateAgainst ran, so an operator can tell
	// how often the model still attempts to author Rows despite
	// CHAOS-4364's model-facing facts excluding Rows-shaped fields from
	// the prompt (RuntimeAnswerSynthesizer.Synthesize's own doc comment
	// names the two call sites that can fire this). Called ONLY when
	// claims>0 -- the "nothing to do is not an outcome" convention this
	// file's other gated telemetry already follows, since a call that
	// strips nothing is byte-identical to every pre-CHAOS-4355 Synthesize
	// call and reporting a zero here on every single call would drown the
	// signal in noise.
	RecordModelRowsStripped(ctx context.Context, principal storage.Principal, claims int)
	// RecordCohortRanked (CHAOS-4398) reports the outcome of ONE RankCohort
	// pass: how many members were scored, the deterministic formula
	// version (prompt-changes-are-behavior-changes discipline applied to
	// this deterministic function too -- a later formula revision is a
	// counted, diagnosable event, not a silent drift), how many members
	// landed DataCompleteness=degraded, and a per-signal-family count of
	// how many members that family actually contributed to (the
	// "signals_available histogram" the ticket asks for) -- so a signal
	// family that stops contributing across an entire org (a producer
	// outage, not a real data gap) is visible in telemetry before anyone
	// notices the ranking went flat. Declared on THIS interface, not an
	// optional side interface, for the same reason every sibling method
	// above is: a branch that can go missing by omission is the
	// CHAOS-4089 failure mode this repo keeps re-learning. Called once
	// per RankCohort call that actually ran (cohort != nil, members > 0);
	// never called for an offers-only cohort that never reaches ranking.
	RecordCohortRanked(ctx context.Context, principal storage.Principal, event CohortRankedEvent)
	// RecordCohortDriverNarration (CHAOS-4398 PR3b) reports the outcome of
	// ONE narrateCohortDriverJudgments call: the closed
	// CohortDriverNarrationOutcome (emitted/budget_exhausted/no_drivers)
	// plus counts -- team-lead's standing order that this new judgment-
	// emission branch carry the same decision-basis-in-the-same-change
	// telemetry every other outcome-affecting branch in this codebase
	// does (root AGENTS.md). Called once per Investigate call that reaches
	// this composer (graphContext.Cohort != nil), independent of whether
	// anything was actually emitted -- budget_exhausted and no_drivers are
	// themselves the diagnosable event, not a silent no-op.
	RecordCohortDriverNarration(ctx context.Context, principal storage.Principal, event CohortDriverNarrationEvent)
	// RecordEvidenceLabelFallback (CHAOS-4690 item 4, design §3.2) reports
	// how many entries in ONE result's EvidenceRefLabels map fell back to
	// the generic "Evidence"/"Evidence: <id>" label because their
	// acr:v1:<entity-type>:<id> ref named an entity-type segment outside
	// the contracts display-label registry's closed set
	// (contextFabricEvidenceEntityLabels).
	//
	// CHAOS-4698 closed the segment vocabulary at the producer signature
	// (evidenceRefID / contractsv1.EvidenceRefID take the closed
	// ContextFabricEvidenceEntityType enum, registry-asserted total), so
	// this fallback is now STRUCTURALLY UNREACHABLE for a ref an acr
	// producer mints today -- a new segment cannot compile without joining
	// the registry. It stays reachable, and this counter stays the only
	// way an operator sees it move, for a ref that predates the enum or
	// was minted by another system and lands in a legacy stored row.
	//
	// Content-safe by construction, same discipline every sibling method
	// on this interface follows and this ticket's own design insists on
	// (r2 F5): a COUNT only, never the unlabeled segment or ref id itself
	// -- an arbitrary provider-minted segment is content-bearing and
	// high-cardinality, exactly what engine.go's telemetry contract
	// forbids. The concrete unlabeled ref is diagnosable from the run's
	// own result artifact (it sits in evidence_ref_labels beside its raw
	// ref id), never from telemetry.
	//
	// Declared on THIS interface, not an optional side interface, for the
	// same CHAOS-4085/CHAOS-4089 reason every sibling method above is: a
	// branch whose telemetry sink can be omitted by a compiling
	// implementation is the exact failure mode this repo keeps
	// re-learning. Called only when count > 0 -- the "nothing to do is
	// not an outcome" convention this file's other gated telemetry
	// already follows (RecordModelRowsStripped's own doc comment).
	RecordEvidenceLabelFallback(ctx context.Context, principal storage.Principal, count int)
	// RecordCoverageDisclosurePhrasing (CHAOS-4690 Commit F, design §4.2)
	// reports the outcome of ONE Synthesize call's coverage-disclosure
	// guard (RuntimeAnswerSynthesizer.Synthesize, model_runtime.go) --
	// the closed CoverageDisclosureOutcome vocabulary (phrased/
	// partial_absent/rejected_by_guard/discarded_undecodable/absent) plus
	// how many of the result's coverage details ended up carrying a
	// Phrasing (phrased) out of how many exist in total (total).
	//
	// violation (CHAOS-4734) is the closed CoverageDisclosureViolation
	// naming WHY a rejected_by_guard outcome fired -- empty on every other
	// outcome. Its two "unknown" members are what make the guard's fix
	// countable from artifacts: UnknownToModelFacingSet is a canonical
	// detail_id the model was never shown (a hallucinated-but-real
	// reference -- the case this ticket's guard-universe fix exists to
	// catch), UnknownDetailID is a detail_id that names nothing canonical
	// at all. Before a narrower model-facing projection ships, the two
	// converge to the same practical meaning; they diverge the moment one
	// does, and an operator needs the split before that day, not after.
	//
	// Declared on THIS interface, not an optional side interface, for the
	// same CHAOS-4085/CHAOS-4089 reason every sibling method above is: a
	// branch whose telemetry sink can be omitted by a compiling
	// implementation is the exact failure mode this repo keeps
	// re-learning.
	//
	// Called UNCONDITIONALLY, once per Synthesize call that reaches
	// result composition (ValidateAgainst already passed) -- unlike the
	// "nothing to do is not an outcome" gated methods beside it
	// (RecordModelRowsStripped, RecordEvidenceLabelFallback), absent is
	// itself one of this method's own closed outcome values and is the
	// expected common case (most answers disclose nothing), so it is the
	// denominator every other outcome's rate is read against, not a
	// signal worth suppressing.
	//
	// Content-safe by construction: two closed enums and two counts only,
	// never a detail_id, a phrasing's text, or a Label.
	RecordCoverageDisclosurePhrasing(ctx context.Context, principal storage.Principal, outcome CoverageDisclosureOutcome, violation CoverageDisclosureViolation, phrased, total int)
}

// CohortRankedEvent is RecordCohortRanked's content-safe payload: counts and
// a closed-vocabulary formula version only, never a subject name, a score
// value, or any text a team could be identified by -- the "no
// person-to-person rankings" guardrail's team-to-team analogue does not
// license leaking WHICH team ranked where into an operator log line.
type CohortRankedEvent struct {
	// CohortKind is the subject kind of the cohort that was ranked and
	// served -- a closed-vocabulary value, content-safe by the same
	// reasoning as every other field here.
	//
	// It rides on the served-answer line because "which cohort kinds does
	// this system actually SERVE" had no answer in the record: the served
	// side carried counts, and the graph side carried a refusal basis with
	// no kind. An operator watching a newly admitted kind reach production
	// had nothing to watch.
	CohortKind          SubjectKind
	MemberCount         int
	FormulaVersion      string
	DegradedMemberCount int
	// SignalsAvailable maps a top-level signal-family name (the same
	// RankingSignal* constants cohort_ranking.go's RankingBasis values
	// draw from) to the count of members whose Score actually drew from
	// it this call.
	SignalsAvailable map[string]int
	// OutcomeCounts (CHAOS-4398 PR3, design doc §8) maps a
	// ContextFabricCohortMemberOutcome value to the count of members that
	// landed there this call -- operational visibility into how often a
	// cohort answer actually clears the qualification threshold, distinct
	// from DegradedMemberCount's data-availability-only measure.
	OutcomeCounts map[string]int
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
	structureSelectionSink     StructureSelectionSink
	handleVerifier             HandleVerifier
	anchorVerifier             AnchorVerifier
	anchorMembershipVerifier   AnchorMembershipVerifier
	candidateVerifier          CandidateVerifier
	priorConsultant            PriorConsultant
	priorHandleGrammarChecker  HandleGrammarChecker
	offerPhraser               OfferPhraser
	regimeAOffersDisabled      bool
	maxItems                   int
	maxSerializedBytes         int64
	synthesisDeadlineReserve   time.Duration
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
		structureSelectionSink:     dependencies.StructureSelectionSink,
		handleVerifier:             dependencies.HandleVerifier,
		anchorVerifier:             dependencies.AnchorVerifier,
		anchorMembershipVerifier:   dependencies.AnchorMembershipVerifier,
		candidateVerifier:          dependencies.CandidateVerifier,
		priorConsultant:            dependencies.PriorConsultant,
		priorHandleGrammarChecker:  dependencies.PriorHandleGrammarChecker,
		offerPhraser:               dependencies.OfferPhraser,
		reuseProjectionVersion:     options.ReuseProjectionVersion, reuseModelIdentities: options.ReuseModelIdentities,
		reuseRetrievalIdentity:   options.ReuseRetrievalIdentity,
		reusePromptVersions:      options.ReusePromptVersions,
		reuseVersionAuthorities:  options.ReuseVersionAuthorities,
		regimeAOffersDisabled:    options.RegimeAOffersDisabled,
		maxItems:                 options.MaxItems,
		maxSerializedBytes:       options.MaxSerializedBytes,
		synthesisDeadlineReserve: options.SynthesisDeadlineReserve,
		serviceVersion:           options.ServiceVersion, now: options.Now, newResultID: options.NewResultID,
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
		// CHAOS-4088: StageGraphBinding, not StageResolution -- a binding
		// outage never got as far as a subject/commit-gate query, and
		// conflating the two populations is exactly what this split fixes.
		return InvestigationResult{}, stageError(StageGraphBinding, fmt.Errorf("resolve graph binding: %w", err))
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
		// CHAOS-3478: nil -- resolvePriorSubjectHints has not run yet at
		// this call site (see engine.go's ordering comment at its own call
		// site below), the same "nothing attempted yet" convention every
		// other pre-receipt-resolution veto uses.
		//
		// CHAOS-4335: preInterpretExplicitStructure threads a bare explicit
		// ExpectedKinds/SubjectHandles hint through this short-circuit --
		// cheap and store-free, so it costs this class of request nothing
		// extra (structureCanon itself, which DOES need the store for a
		// receipt-carrying request, still has not run and is not attempted
		// here).
		return e.windowVetoResult(ctx, principal, request, windowCanon.Veto, nil, windowCanon.StaleEntry, binding, nil, e.preInterpretExplicitStructure(request), nil)
	}
	// CHAOS-4040 (sol-max ruling 2026-08-21, "GATE ALL INFERRED WINDOWS
	// out of decisive terminals"): an MCP bare explicit evidence_window
	// field resolved here, at precedence step 1, with NO decisive
	// authority of its own (windowCanon.ExplicitUnconfirmed) -- gated
	// BEFORE tryReuse and BEFORE Interpret, exactly like a genuine veto
	// above, so this class of request pays for zero interpreter/graph/
	// fact/synthesis work (CHAOS-4040's own run-3 acceptance bar). The
	// OTHER inferred-window origin (no request-side window at all, the
	// class-table/binder default) cannot be known yet at this point --
	// see the second gate, after Interpret, below.
	if windowCanon.ExplicitUnconfirmed {
		// CHAOS-3478: nil, not an empty slice -- resolvePriorSubjectHints has
		// not run yet at this gate (it sits below, after Interpret), so
		// there is genuinely nothing to echo yet, the same "nothing
		// attempted" convention structureCanon's own nil argument here
		// already carries for structure receipts.
		//
		// windowExpandUnavailable=false (CHAOS-4336): this gate makes no
		// claim about the current window's pool content at all -- it
		// fires BEFORE Interpret/ResolveSubjects ever run, by design (this
		// function's own doc comment above) -- so there is nothing to be
		// "unavailable"; the tier-ordering fact composeWindowExpandOption
		// needs (pickWindowExpandTarget) is available from windowCanon.Effective
		// alone, unlike gate 2's own offers-only read.
		return e.windowConfirmationRequiredResult(ctx, principal, request, nil, *windowCanon.Effective, nil, WindowCanonicalizationGatedExplicitUnconfirmed, binding, StructureOfferMaterial{}, false, nil, nil, nil)
	}

	// CHAOS-3900 P1 (pivot-intent design brief §2.1): canonicalize
	// structure receipts (kindr_/ancr_/handr_) BEFORE tryReuse too, same
	// ordering discipline and the same reason as canonicalizeEvidenceWindow
	// above -- resolving them here, before any reuse lookup, means a
	// follow-up confirming structure via receipt can never be served a
	// cached answer generated under unconfirmed inference instead.
	structureCanon := e.canonicalizeStructure(ctx, principal, request, binding)
	if structureCanon.Veto != structureVetoNone {
		// CHAOS-3900 P1.F: a PRE-FLIGHT veto is FINAL the instant
		// canonicalizeStructure returns it -- nothing downstream can still
		// change this outcome, so telemetry records it immediately here,
		// exactly as before CHAOS-3927 P4. (P4 codex review: this is
		// deliberately NOT true of the success/no-veto path any more --
		// see the deferred call below, right before/after the decisive
		// Save, for why that one moved.)
		recordStructureReceiptTelemetry(ctx, e.telemetry, principal, request, structureCanon)
		// CHAOS-3972 P3: cf_structure_explicit{member,outcome} -- mirrors
		// recordStructureReceiptTelemetry's own placement immediately
		// above, moved here alongside it by the CHAOS-3927 P4 rebase (a
		// pre-flight veto is final the instant canonicalizeStructure
		// returns it, for explicit fields exactly as much as for
		// receipts); the success path is recorded once Save has actually
		// won, via recordStructureConfirmationOutcome (structure.go).
		recordStructureExplicitTelemetry(ctx, e.telemetry, principal, request, structureCanon)
		// StaleMembers (structureVetoStaleSupersededOffer) and VetoedEntries
		// (CHAOS-3963, every other veto reason) are mutually exclusive by
		// construction -- exactly one is ever non-empty for a given veto.
		echoEntries := structureCanon.StaleMembers
		if len(echoEntries) == 0 {
			echoEntries = structureCanon.VetoedEntries
		}
		// CHAOS-4003 (codex xhigh review finding): windowCanon already ran
		// and may have cleanly confirmed a window BEFORE structureCanon's
		// own veto fired here -- the same "one entry per carried member,
		// including vetoed ones" wire rule composeConfirmedStructure's own
		// doc comment cites means a successfully confirmed window must not
		// silently vanish from THIS terminal's echo just because a
		// DIFFERENT member (kind/anchor/handle) is why the whole request
		// was rejected. composeConfirmedStructure builds the one
		// applied-disposition entry the SAME way the decisive path does.
		if windowCanon.ConfirmedMember != nil {
			echoEntries = append(echoEntries, composeConfirmedStructure([]confirmedStructureMember{*windowCanon.ConfirmedMember}, nil)...)
		}
		// CHAOS-3478: nil -- canonicalizeStructure fires before
		// resolvePriorSubjectHints (this call site's own ordering), the
		// same "nothing attempted yet" convention every other
		// pre-receipt-resolution veto in this file uses.
		return e.structureVetoResult(ctx, principal, request, structureCanon.Veto, echoEntries, binding, nil, nil)
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
	//
	// CHAOS-3900 P1 (design brief §2.1/DP11): a non-empty confirmed-structure
	// set BYPASSES the reuse lookup entirely -- v1 picks bypass over folding
	// structure into ReuseKey (deferred until confirmation-turn volume
	// justifies the optimization). This is also what makes the extended
	// source-ineligibility rule (no structure-bearing result is ever a
	// reuse SOURCE) sound from the consuming side too: a request that just
	// confirmed structure never even attempts to read the cache a
	// structure-bearing row might otherwise sit in.
	//
	// CHAOS-3478 (codex round-1 finding, High): PriorSubjectReceipts joins
	// the SAME bypass condition, for the SAME reason. ReuseKey carries no
	// PriorSubjectReceipts dimension, so a cached row cannot distinguish
	// "generated with this exact receipt honored" from "generated some
	// other way" -- without this, a request naming a receipt could be
	// served a stored answer produced before the receipt was ever
	// resolved, silently answering about whatever subject that OTHER
	// investigation happened to commit instead of the one this receipt
	// names. Prior-subject receipts do not (yet) get their own
	// ReuseKey-folding optimization for the identical reason structure
	// receipts don't (DP11, above) -- bypass is the v1 answer for both.
	if len(structureCanon.Confirmed) == 0 && len(request.PriorSubjectReceipts) == 0 {
		if reused, ok := e.tryReuse(ctx, principal, request, clampedRequestTime, windowCanon.KeyComponent, windowCanon.KeyEncoding, binding); ok {
			// CHAOS-4413 (codex xhigh round-1 P1, confirmed): a reuse hit
			// can serve a row persisted before Completeness existed --
			// ValidateStored's legacy exemption lets it stay in storage,
			// but this is the SERVING path, not storage, and every other
			// exit stamps a fresh, correct value here. ComputeAnswerCompleteness
			// is a pure function of fields the row already carries, so
			// recomputing is a backfill, never an invention: unaffected
			// (and no-op) for a row that already has it, and it is what
			// makes an old row's projection/re-serve pass the SAME
			// required-field validation a brand-new answer must pass,
			// instead of 500ing the moment a bounded consumer projects it.
			reused.Completeness = ComputeAnswerCompleteness(reused)
			// chris's promise of record, verbatim: "reuse and stored reads
			// are re-validated against the current budget and refuse if they
			// no longer fit." A stored row keyed WITHOUT the response budget
			// can be served under a budget it no longer fits -- the route
			// then refuses it with no engine-side measurement at all. This
			// re-validates against the CURRENT effective budget.
			//
			// Nothing is stamped (the stored document already carries whatever
			// plan it was saved with) and NOTHING IS PERSISTED -- the stored
			// row is left exactly as it was; only the decision to serve it is
			// made here. The REMEDY when it no longer fits (budget-keyed reuse
			// vs re-investigation) is floor paper C2 and is ticketed
			// separately; refusing is the interim answer, not the final one.
			reused, reuseBudgetErr := e.finalizeServed(ctx, principal, BudgetAssertReuse, reused, nil, e.effectiveResponseBudget(request))
			if reuseBudgetErr != nil {
				return InvestigationResult{}, reuseBudgetErr
			}
			return reused, nil
		}
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
	// carryCtx carries ONE per-request memo of prior-result loads, shared by
	// prior-subject-hint resolution and by every carry axis below
	// (withCarryResultCache, structure_axis_carry.go). Without it, a turn
	// that resolves hints AND attempts both carries loads the same prior
	// result three times -- and that turn is precisely the one a struggling
	// clarification chain keeps landing on.
	carryCtx := withCarryResultCache(ctx)
	var priorHints []SubjectHint
	var priorValidatedReceipts []BoundSubjectReceipt
	var priorOutcomes []priorSubjectReceiptOutcome
	// priorLoadedResults (CHAOS-4636) are the prior results already fetched and
	// taint-gated above, reused by the plan carry so a follow-up turn costs no
	// extra store round-trip.
	var priorLoadedResults map[string]InvestigationResult
	var priorHintsStaleGraphEpochDelta int64
	if e.results != nil && len(request.PriorSubjectReceipts) > 0 {
		priorHints, priorValidatedReceipts, priorHintsStaleGraphEpochDelta, priorOutcomes, priorLoadedResults = e.resolvePriorSubjectHints(carryCtx, principal, request.Consumer, request.PriorSubjectReceipts, binding)
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
			// codex CHAOS-3813 round-1 finding: a truncated hint never
			// reaches GraphReader, so composePriorSubjectReceiptDispositions
			// must not report it "applied" on the strength of some OTHER
			// hint resolving the same subject -- mark the dropped tail
			// before slicing so the wire disposition (and the telemetry
			// derived from it) reflect this receipt's own fate, matching
			// the comment above's stated intent.
			markTrailingHintOutcomesDroppedByBudget(priorOutcomes, len(priorHints)-available)
			priorHints = priorHints[:available]
		}
		if len(priorHints) > 0 {
			graphRequest.RequestedScope.SubjectHints = append(
				append([]SubjectHint(nil), request.RequestedScope.SubjectHints...), priorHints...,
			)
		}
	} else if len(request.PriorSubjectReceipts) > 0 {
		// e.results == nil (CHAOS-3478 codex round-1 finding): no
		// InvestigationResultStore is configured, so no receipt can
		// possibly be loaded -- classify every one the same way an
		// unloadable prior result would, rather than silently producing
		// neither a disposition entry nor a telemetry count for a receipt
		// the caller actually sent.
		priorOutcomes = make([]priorSubjectReceiptOutcome, 0, len(request.PriorSubjectReceipts))
		for _, receipt := range request.PriorSubjectReceipts {
			priorOutcomes = append(priorOutcomes, priorSubjectReceiptOutcome{receipt: receipt, preGraphSkipReason: priorSubjectReceiptSkipUnloadable})
		}
	}

	// CHAOS-3898 P1-1: Interpret sees ONLY the validated receipt subset,
	// never the raw request.PriorSubjectReceipts -- see the block above.
	interpretRequest := request
	interpretRequest.PriorSubjectReceipts = priorValidatedReceipts
	interpretation, familyOutcome, err := e.interpreter.Interpret(ctx, principal, interpretRequest)
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
	// CHAOS-4636 -- the PLANNING STAGE (design §6.1). Deterministic, no
	// model call, no I/O, placed between interpretation and discovery
	// because that is the first point where the family is known and the
	// last point before anything expensive is decided.
	//
	// The plan is a value the rest of Investigate reads, not a mutation of
	// anything it can already see. It is stamped on the result at the end,
	// so an answer that did not fit names the number that was wrong instead
	// of leaving a 413 to be re-derived by re-running with instrumentation
	// added afterward.
	// CHAOS-4636 carry (extends CHAOS-4387): a follow-up turn that resolved
	// no family of its own continues the previous turn's reading. One hop,
	// taint-gated, conflict-fails-closed -- and never the member list, which
	// would carry an authorization decision (North Star check 18).
	planCarry := e.resolveCarriedPlan(ctx, principal, request, priorValidatedReceipts, binding, priorLoadedResults)
	familyOutcome = e.applyAndRecordCarry(ctx, principal, familyOutcome, planCarry)
	plan := PlanAnswer(PlanAnswerInput{
		Family:           familyOutcome,
		Interpretation:   interpretation,
		Budget:           e.effectiveResponseBudget(request),
		MaxCohortMembers: request.Options.MaxCohortMembers,
	})
	// CHAOS-3900 W1 (codex review finding, round 1): a question_stated/
	// clarification_confirmed window was canonicalized above against the
	// REQUEST's own current axis (canonicalizeEvidenceWindow only ever
	// resolves windowCanon.Effective when request.TimeContext.Axis is
	// current) -- but Interpret may still move the axis away from current
	// ("what was the status last month" on an axis=current request). A
	// window commitment survives that flip only by accident: without this
	// check it is silently dropped (composeEffectiveWindow's own
	// interpreted-axis gate) while the reuse/save key upstream still
	// carries it, with no disclosed reason either way. Name the
	// disagreement instead: no answer is synthesized under a window
	// commitment interpretation no longer honors.
	if windowCanon.Effective != nil && clampedInterpretedTime.Axis != TemporalCurrent {
		// CHAOS-3478/CHAOS-3813 (codex round-1 finding): this veto returns
		// before ResolveSubjects ever runs, so it is a never-resolved
		// terminal exactly like the ErrGraphNotProjected/CHAOS-4234 gated
		// branches -- disclose and record telemetry here too, rather than
		// silently dropping receipts on a path the old code never reached
		// recordPriorSubjectReceiptSkips from at all.
		axisConflictDispositions := composePriorSubjectReceiptDispositions(priorOutcomes, SubjectResolution{})
		if len(axisConflictDispositions) > 0 {
			e.recordPriorSubjectReceiptSkips(ctx, principal, axisConflictDispositions, priorHintsStaleGraphEpochDelta)
		}
		// CHAOS-4335: structureCanon has ALREADY run by this point
		// (unconditional, right after gate 1, well before Interpret) -- its
		// Explicit field is the REAL, conflict-checked-against-receipts
		// result, more accurate than re-deriving one fresh (a member already
		// receipt-confirmed correctly produces no Explicit entry for
		// itself -- resolveExplicitStructure's own confirmedMemberValue
		// match-and-say-nothing branch). Confirmed is deliberately NOT
		// passed here -- see windowVetoResult's own explicitStructure
		// parameter doc comment for why a receipt-derived entry cannot
		// safely reach this veto path.
		// CHAOS-4636 / codex round 3 finding 1: this veto returns AFTER the
		// planning stage has run, so it must stamp the plan like every other
		// post-plan exit. It is reachable -- a current-axis request carrying
		// a confirmed window receipt whose interpretation moves the axis to
		// historical lands here -- and the result is SAVED, so a plan
		// omitted here is missing from a persisted answer permanently.
		veto, vetoErr := e.windowVetoResult(ctx, principal, request, windowVetoAxisConflict, &interpretation, nil, binding, axisConflictDispositions, structureCanon.Explicit, &plan)
		return veto, vetoErr
	}
	// CHAOS-3977 P5 (design brief §3.4): ONE prior consult per Investigate
	// call, shared by BOTH DP4(a) sites (the offer-builder merge below,
	// and the window slot right here -- and terminalResult's own
	// subjectless-terminal twin, unresolved.go) -- see fetchPriorEntries'
	// own doc comment (priors_consult.go) for why this is the sole I/O
	// call site. Moved up from its PRE-CHAOS-4040 position (immediately
	// after ResolveSubjects) to right here, post-Interpret, so the window
	// gate below can run before ResolveSubjects too -- fetchPriorEntries
	// itself has no dependency on resolution (QuestionHash(request.Question)
	// alone), so this reordering changes nothing about what it reads, only
	// when. No-op (returns nil) when e.priorConsultant is nil.
	priorEntries := e.fetchPriorEntries(ctx, principal, QuestionHash(request.Question))
	// CHAOS-4040 (sol-max ruling 2026-08-21): precedence step 2 --
	// windowCanon.Effective is nil here by construction (a non-nil,
	// ExplicitUnconfirmed Effective already returned at gate 1 above; a
	// non-nil, confirmed/stated Effective would have kept KeyComponent
	// non-empty and reached this point unaffected, see the Provenance
	// switch inside composeEffectiveWindow) -- so ANY inferred_default
	// this call produces is the class-table/binder default, the SECOND
	// origin the ruling requires gated, computed EARLY (before
	// ResolveSubjects/DiscoverContext/ReadFacts/Synthesize) instead of at
	// its pre-CHAOS-4040 position near the end of this function, so a
	// gated request pays for interpretation only -- CHAOS-4040's own
	// run-3 acceptance bar ("class-default cases interpretation-only").
	priorWindow := e.resolveWindowPriorProposal(ctx, principal, priorEntries, windowCanon)
	effectiveWindow := composeEffectiveWindow(interpretation, windowCanon.Effective, windowCanon.BinderProposal, priorWindow, e.now())
	// CHAOS-4360: same-conversation window carry. Attempted ONLY when this
	// turn's own canonicalization would otherwise be inferred_default --
	// windowCanon.Effective is nil by construction at this point (see the
	// comment two lines above), so a request-side confirmed/stated window
	// already returned decisively above and is never second-guessed here.
	// A hit REPLACES effectiveWindow with the carried (non-inferred)
	// window, which is what keeps the CHAOS-4234 gate below from firing at
	// all -- every downstream use of effectiveWindow (the decisive path,
	// terminalResult, Save's own key) then sees the carried value exactly
	// as if it had been confirmed on this turn. See chaos4360_carry.go for
	// the mechanism and the defect this closes.
	var windowCarry windowCarryResult
	if effectiveWindow != nil && effectiveWindow.Provenance == WindowInferredDefault {
		// codex R1 P1 (fixed): priorValidatedReceipts, never the raw
		// request.PriorSubjectReceipts -- see carryReferencedResultIDs' own
		// doc comment (chaos4360_carry.go) for why an unmatched
		// PriorSubjectReceipts entry must not be able to seed the walk.
		windowCarry = e.resolveCarriedWindow(carryCtx, principal, request, priorValidatedReceipts, binding)
		e.recordWindowCarry(ctx, principal, windowCarry)
		if windowCarry.Outcome == WindowCarryHit {
			effectiveWindow = windowCarry.Window
		}
	}
	// Same-conversation expected_kind carry (structure_axis_carry.go), the
	// structure-axis twin of the window carry above. Attempted ONLY when
	// this turn states no kind of its own -- by receipt OR explicitly. A kind
	// stated on THIS request is the caller speaking now, and an inherited
	// value must never override it; see statedExpectedKindThisTurn for why
	// the explicit case is a validity requirement, not just precedence.
	//
	// Placed here, before the CHAOS-4234 gate below, so BOTH the gated
	// offers-only resolution and the decisive resolution read the same
	// effective kind. Sequenced after the window carry deliberately: the two
	// axes are independent (each fails closed on its own), and a window
	// carry miss must not suppress a kind carry hit.
	var kindCarry kindCarryResult
	if !statedExpectedKindThisTurn(request, structureCanon) {
		kindCarry = e.resolveCarriedKind(carryCtx, principal, request, priorValidatedReceipts, binding)
		e.recordKindCarry(ctx, principal, kindCarry)
	}
	carriedStructureEntries := []*contractsv1.ContextFabricConfirmedStructureEntry{
		composeCarriedWindowEntry(windowCarry),
		composeCarriedKindEntry(kindCarry),
	}
	if effectiveWindow != nil && effectiveWindow.Provenance == WindowInferredDefault {
		// CHAOS-4234: the gate still fires HERE, before anything decisive,
		// but it now composes kind/handle/candidate offers from an
		// offers-only resolution whose commit-bearing outputs are discarded
		// -- see chaos4234_offers_only.go for the ruling and the two
		// safety layers.
		gatedMaterial, gatedMaterialWindowExpandUnavailable := e.gatedOfferMaterial(ctx, principal, request, graphRequest, interpretation, familyOutcome, binding, structureCanon, kindCarry, priorEntries)
		// CHAOS-3478/CHAOS-4234: priorOutcomes was already computed above
		// (resolvePriorSubjectHints runs before Interpret, this gate fires
		// after) but this gate's own resolution is offers-only and
		// discarded by the CHAOS-4234 ruling -- never a real
		// SubjectResolution to re-verify a matched receipt's hint against.
		// A zero-value SubjectResolution{} classifies every matched hint as
		// skipped_failed_reauth ("not re-verified this call"), the same
		// honest convention the ErrGraphNotProjected branch above uses,
		// never silently omitting these receipts from the response the way
		// this path did before this ticket (a real, previously-unclosed
		// gap: this early return skipped recordPriorSubjectReceiptSkips
		// entirely).
		gatedDispositions := composePriorSubjectReceiptDispositions(priorOutcomes, SubjectResolution{})
		if len(gatedDispositions) > 0 {
			e.recordPriorSubjectReceiptSkips(ctx, principal, gatedDispositions, priorHintsStaleGraphEpochDelta)
		}
		gated, gatedErr := e.windowConfirmationRequiredResult(ctx, principal, request, &interpretation, *effectiveWindow, &structureCanon, WindowCanonicalizationGatedClassDefault, binding, gatedMaterial, gatedMaterialWindowExpandUnavailable, carriedStructureEntries, gatedDispositions, &plan)
		return gated, gatedErr
	}
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
	// CHAOS-3900 P1.D: threads structureCanon's own resolved expected_kind
	// confirmation (nil for every request that carried none -- the common
	// case) so ResolveSubjects can narrow its pool to it. See
	// ConfirmedExpectedKind's own doc comment for why this is a dedicated
	// type and why that matters.
	// CHAOS-4085: commitBases records, per committed subject, WHICH CLASS OF
	// PROOF the commit stood on -- caller-supplied canonical id, an
	// authoritative keyed identity, or a score comparison. It is consumed
	// once, after synthesis, by applyCommitAffirmation below; nothing
	// between here and there reads or alters it. A GraphReader that returns
	// nil leaves every commit reading CommitBasisUnknown, which is the
	// STRICT treatment (see CommitBasis).
	// CARRIED, NOT RE-DERIVED (CHAOS-4736 bar 5): resolution gets this
	// turn's validated frame so the kind-hinted pool search reads declared
	// kinds instead of guessing at the question's words.
	resolution, structureMaterial, commitBases, commitDigests, err := e.graph.ResolveSubjects(resolveCtx, principal, graphRequest, interpretation, binding, effectiveConfirmedKind(structureCanon.Confirmed, kindCarry), confirmedAnchorSelection(structureCanon.Confirmed), familyOutcome.Frame)
	if err != nil {
		// CHAOS-4077: a never-projected org (ResolveSubjects queried a
		// graph key that has never been created) degrades to the SAME
		// clean terminal a legitimately-empty resolution already
		// produces, below -- never a 5xx. DiscoverContext is skipped
		// entirely here, not called and then also handled: it would
		// query the identical nonexistent graph key and fail the same
		// way, one call later, undoing this branch. See
		// ErrGraphNotProjected's own doc comment (ports.go) for why this
		// is safe to degrade (a confirmed, unambiguous "no such graph
		// key" classification, never a generic dependency failure).
		if errors.Is(err, ErrGraphNotProjected) {
			// Candidates/Committed must be non-nil empty slices, never a
			// bare nil: ContextFabricSubjectResolution.Validate rejects a
			// nil array as violating v1 bounds (it cannot tell "resolved
			// to genuinely zero candidates" from "this field was never
			// populated at all").
			emptyResolution := SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}, GraphNotProjected: true}
			// codex xhigh review round 2 (confirmed real, MEDIUM): the
			// ordinary zero-subjects terminal a few lines below reaches
			// BOTH of these before terminalResult -- this branch must
			// too, not silently drop them because it returns earlier.
			// consultPriorStructureOffers is a safe no-op here in
			// practice (structureMaterial.Missing is empty on this error
			// path, nothing on ResolveSubjects computed one), kept for
			// the same reason engine.go's own invariant assertions stay
			// in place after their guard makes them unreachable: assert,
			// don't assume. recordPriorSubjectReceiptSkips is NOT a
			// no-op: with emptyResolution's own zero candidates/committed,
			// every prior receipt the caller submitted correctly reports
			// as skipped (none could possibly have survived a
			// non-existent graph) -- exactly the honest diagnostic signal
			// an operator needs, previously silently dropped.
			structureMaterial = e.consultPriorStructureOffers(ctx, principal, priorEntries, structureMaterial)
			if len(request.PriorSubjectReceipts) > 0 {
				// CHAOS-3478/CHAOS-3813: the wire echo and the telemetry
				// counts are both built from the SAME dispositions slice
				// here, so they can never disagree.
				emptyResolution.PriorSubjectReceiptDispositions = composePriorSubjectReceiptDispositions(priorOutcomes, emptyResolution)
				e.recordPriorSubjectReceiptSkips(ctx, principal, emptyResolution.PriorSubjectReceiptDispositions, priorHintsStaleGraphEpochDelta)
			}
			terminal, terminalErr := e.terminalResult(ctx, principal, request, interpretation, familyOutcome, emptyResolution, GraphContext{}, reuseWatermarkSnapshot, reuseEpoch, 0, binding, windowCanon, structureCanon, structureMaterial, effectiveWindow, windowCarry.Outcome == WindowCarryHit, carriedStructureEntries, &plan)
			return terminal, terminalErr
		}
		// CHAOS-4088: StageSubjectResolution, not StageResolution -- the
		// binding above already succeeded, so this is the distinct
		// commit-gate/subject-matching failure population StageGraphBinding
		// deliberately does not cover.
		return InvestigationResult{}, stageError(StageSubjectResolution, fmt.Errorf("resolve subjects: %w", err))
	}
	// priorEntries: fetched ABOVE, before this call (CHAOS-4040 reordering
	// -- see that call site's own comment for why it moved), reused here
	// unchanged -- still the SAME single I/O call site fetchPriorEntries'
	// own doc comment promises, just earlier in the function.
	// CHAOS-3977 P5 (design brief §2.4/§3.4, DP4(a) site one): merge
	// prior-sourced offers into the engine-derived material BEFORE
	// composeStructureNeeds mints any receipt/option id (unresolved.go/
	// structure.go), so a prior-sourced offer's id is minted through the
	// exact same path an engine-derived one's is.
	structureMaterial = e.consultPriorStructureOffers(ctx, principal, priorEntries, structureMaterial)
	if len(request.PriorSubjectReceipts) > 0 {
		// CHAOS-3478/CHAOS-3813: attached to `resolution` itself (not a
		// copy) so it survives unchanged through every later assignment
		// that copies this value onto the final result (graphContext.Resolution
		// below, result.SubjectResolution = resolution further down) --
		// the same "one entry per carried receipt, including skipped ones"
		// disclosure structure receipts already carry via ConfirmedStructure.
		resolution.PriorSubjectReceiptDispositions = composePriorSubjectReceiptDispositions(priorOutcomes, resolution)
		e.recordPriorSubjectReceiptSkips(ctx, principal, resolution.PriorSubjectReceiptDispositions, priorHintsStaleGraphEpochDelta)
	}
	// CHAOS-4636 STAGE 1 -- cardinality, PRE-READ (design §6.3).
	//
	// Only cardinality is knowable here, and the clamp rides the cap
	// DiscoveredCohort already honours (discovery.Request.Options.
	// MaxCohortMembers) rather than a second, parallel limit.
	//
	// The declared basis is truthful rather than aspirational.
	// DiscoveredCohort fills members in the order its candidate nodes
	// arrive, and since CHAOS-4630 that order is a total sort on
	// SubjectKey -- kind plus canonical id -- so within a single-kind
	// cohort it IS canonical-id-lexical. Before CHAOS-4630 it was Go map
	// iteration order, i.e. not an order at all, which is exactly why an
	// earlier revision of this design proposed narrowing on something that
	// did not exist. Attention rank is NOT available here and this stage
	// does not pretend otherwise: RankCohort runs after the fact read.
	//
	// Before/After are the CAPS, not a known population: how many members
	// exist is precisely what has not been read yet.
	// A carried narrowing basis keeps two turns of one conversation
	// narrowing the same way. A follow-up that silently changed basis would
	// make the two answers incomparable while looking like a refinement.
	if planCarry.Outcome == PlanCarryHit && planCarry.NarrowingBasis != "" {
		plan.Budget.NarrowingBasis = planCarry.NarrowingBasis
	}
	if clamped := plan.Budget.MaxMembers; clamped > 0 && (graphRequest.Options.MaxCohortMembers <= 0 || clamped < graphRequest.Options.MaxCohortMembers) {
		e.recordPlanNarrowingStep(&plan, PlanNarrowing{
			Stage:  contractsv1.ContextFabricPlanNarrowingCardinality,
			Basis:  contractsv1.ContextFabricNarrowingBasisCanonicalIDLexical,
			Before: graphRequest.Options.MaxCohortMembers,
			After:  clamped,
		})
		e.recordPlanNarrowing(ctx, principal, PlanNarrowingEventFrom(plan, contractsv1.ContextFabricPlanNarrowingCardinality, graphRequest.Options.MaxCohortMembers, clamped, false, false, "", ""))
		graphRequest.Options.MaxCohortMembers = clamped
	}
	graphContext, err := e.graph.DiscoverContext(ctx, principal, GraphDiscoveryRequest{
		Request: graphRequest, Interpretation: interpretation, Resolution: resolution, Binding: binding,
		ScopeAnchorResolved: scopeAnchorResolved(familyOutcome),
		// CARRIED, NOT RE-DERIVED (CHAOS-4736 bar 5): the frame comes off
		// the family outcome this turn's interpretation already produced.
		// Nothing here reconstructs a frame from the family, from Shape or
		// from the interpretation's flat term fields.
		Frame: familyOutcome.Frame,
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
		terminal, terminalErr := e.terminalResult(ctx, principal, request, interpretation, familyOutcome, resolution, graphContext, reuseWatermarkSnapshot, reuseEpoch, *subjectCandidatesAuthzDropped, binding, windowCanon, structureCanon, structureMaterial, effectiveWindow, windowCarry.Outcome == WindowCarryHit, carriedStructureEntries, &plan)
		return terminal, terminalErr
	}

	// CHAOS-4347: expand a bare "status" category requirement (the model's
	// own 1:1 pick from the closed FactKind vocabulary -- there is no way
	// for Interpret() to know FactStatus is work_item-only) into the
	// composed set for repository/team subjects BEFORE merging in the
	// graph-derived requirements below, so mergeFactRequirements' own
	// first-kind-wins dedup sees the composed kinds like any other
	// requirement. See composeStatusCategoryRequirements' own doc comment.
	statusComposedRequirements := e.composeStatusCategoryRequirements(ctx, principal, interpretation.FactRequirements, subjects)
	// CHAOS-4398 (subject-model-and-cohort-answers.md §3a, "must be resolved
	// in PR1"): investigationScopeSubjects only fans the SUBJECT set out to
	// cohort members -- it does not decide which fact KINDS get read. If
	// the interpreter's own FactRequirements for "which teams are
	// struggling" named only a subset of the ranking formula's five
	// families, the other providers would never run and RankCohort could
	// not compute the documented formula -- a silent, non-obvious failure
	// mode, not a validation error. So for any cohort answer, the five
	// ranking-formula kinds are injected here, LAST in merge order (so a
	// more specific existing requirement for the same kind -- e.g. one
	// carrying its own Subjects/Parameters -- always wins; this only fills
	// a kind that is otherwise absent). RankCohort's own per-signal
	// "missing" handling still applies if a given provider returns no rows
	// for a given member even after being read.
	//
	// CHAOS-4636: that injection is now a PLAN LOOKUP rather than a pointer
	// test. The plan declares which fact kinds the family needs (a cohort
	// family declares the ranking-formula kinds), so the requirement list
	// is PLANNED rather than sampled. The set is unchanged for every family
	// that reached a cohort before, and the merge position is unchanged
	// too -- LAST, so a more specific existing requirement for the same
	// kind always wins and this only fills a kind that is otherwise absent.
	//
	// The pointer test is kept as a conjunct deliberately: a plan may
	// declare a cohort family for a question that then resolves no cohort
	// at all, and reading five cohort-ranking fact kinds for a subject that
	// is not a cohort would be a widening this slice has no evidence for.
	//
	// CHAOS-4636: the plan is what DECLARES those kinds now, so a cohort
	// family's fact needs are plannable BEFORE the read rather than
	// discovered after it. But the plan may only ever WIDEN this list, and
	// the unconditional injection stays: RankCohort runs whenever a cohort
	// exists, whatever family was resolved, so a cohort that materialized
	// under a family the plan did not expect to produce one -- an
	// `unclassified` question, the fallback that exists precisely so
	// today's behaviour is unchanged -- must still get the formula's inputs
	// read. A plan that could REMOVE a kind would be a plan that can make
	// an answer worse, which is the one thing the widening-only rule
	// forbids.
	var cohortRankingRequirements []FactRequirement
	if graphContext.Cohort != nil {
		seenRankingKind := make(map[FactKind]struct{}, len(plan.FactKinds)+len(cohortRankingFormulaKinds))
		appendRankingKind := func(kind FactKind) {
			if kind == "" {
				return
			}
			if _, exists := seenRankingKind[kind]; exists {
				return
			}
			seenRankingKind[kind] = struct{}{}
			cohortRankingRequirements = append(cohortRankingRequirements, FactRequirement{Kind: kind})
		}
		for _, kind := range cohortRankingFormulaKinds {
			appendRankingKind(kind)
		}
		for _, kind := range plan.FactKinds {
			appendRankingKind(kind)
		}
	}
	// CHAOS-4636: the plan's own member kind is stamped from the cohort the
	// graph actually returned, never guessed from the question's wording.
	// The kind a cohort turned out to have is a fact about the answer, and
	// a plan asserting one the answer does not have would be the planner
	// defect this field exists to make visible.
	if graphContext.Cohort != nil {
		plan.MemberKind = graphContext.Cohort.Kind
		if plan.GroupKind == plan.MemberKind {
			// A group axis that collapsed onto the member kind partitions a
			// set by itself, which no grouping can mean. Drop the axis
			// rather than emit a plan the contract refuses -- the answer is
			// then the flat one it would have been before this slice.
			plan.GroupKind = ""
		}
	}
	factRequest := CanonicalFactRequest{
		Question:     factReadQuestion(interpretation, effectiveWindow),
		Subjects:     subjects,
		Cohort:       graphContext.Cohort,
		Requirements: mergeFactRequirements(statusComposedRequirements, graphContext.FactRequirements, cohortRankingRequirements),
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
	// CHAOS-4099 / CHAOS-4089 standing order: every scope-expansion decision
	// this read made is reported here, immediately, whether it expanded,
	// declined, or failed.
	//
	// BEFORE the error check, not after (codex review finding). A fact read
	// that resolved its scope and THEN failed -- an unbuildable query, a
	// provider result the merge rejected -- is precisely the run an operator
	// most needs the expansion decisions for, and emitting after the early
	// return would drop them exactly then. ReadFacts returns the in-progress
	// bundle alongside its error so Scope survives; a nil Scope (an error
	// raised before resolution ran) simply emits nothing.
	e.recordFactScopeExpansion(ctx, principal, facts.Scope)
	if err != nil {
		return InvestigationResult{}, stageError(StageFactRead, fmt.Errorf("read canonical facts: %w", err))
	}

	// CHAOS-4398: RankCohort runs HERE -- after the fact read, before
	// Synthesize -- the same ordering slot attachCanonicalRows documents
	// for itself (model_runtime.go:551): the server computes a number from
	// facts it already has, the model only ever narrates a number it was
	// GIVEN. graphContext.Cohort is nil for every non-cohort investigation
	// (RankCohort no-ops on nil), so this line changes nothing for the
	// single-subject path.
	// cohortSignalCitations (CHAOS-4398 PR3b) are RankCohort's own computed-
	// but-not-yet-minted citations -- see cohortMemberSignalCitations' own
	// doc comment for the team-lead ruling this implements ("minting
	// follows citation, not ranking"): RankCohort hands these forward;
	// narrateCohortDriverJudgments (post-synthesis, below) is what actually
	// mints a ClaimedFact, and only for a driver it decides to narrate.
	//
	// CHAOS-4636 STAGE 2 -- bound what SYNTHESIS IS GIVEN, post-read,
	// pre-synthesis (design §6.3).
	//
	// It cannot bound the answer, because the answer does not exist:
	// synthesis is what CREATES Drivers, RemainingWork, ReadinessGaps,
	// Conflicts and ClaimedFacts, and those are precisely the terms the
	// item budget charges. So this stage does the only thing it honestly
	// can -- it bounds the INPUT, leaving the plan's declared headroom for
	// what synthesis will add.
	//
	// The group axis is built HERE, and it has to be: a member's owning
	// group is read off that member's own facts, which is the first moment
	// they exist. See chaos4636_grouped_cohort.go for why the design's
	// group-first phrasing is not reachable and why this direction is.
	//
	// ORDER IS LOAD-BEARING: group, then narrow, then rank. RankCohort
	// min-max normalizes workload WITHIN the cohort, so every member's
	// score depends on which members are present -- ranking before
	// narrowing would leave the surviving members carrying scores computed
	// against members that are no longer in the answer.
	if graphContext.Cohort != nil && plan.GroupKind != "" {
		// CHAOS-4733: captured BEFORE BuildCohortGroups/
		// ApplyGroupedCohortCompleteness run, so the telemetry below reports
		// the pre-grouping, discovery-level state -- the exact signal that
		// used to have no surviving representation once grouped.
		preGroupingComplete, preGroupingTruncated := graphContext.Cohort.Complete, graphContext.Cohort.Truncated
		groups, ungrouped := BuildCohortGroups(plan, graphContext.Cohort, facts.Facts)
		if len(groups) > 0 {
			cohort := *graphContext.Cohort
			cohort.Groups = groups
			ApplyGroupedCohortCompleteness(&cohort)
			graphContext.Cohort = &cohort
			groupsMarkedIncomplete := 0
			for _, group := range groups {
				if !group.Complete {
					groupsMarkedIncomplete++
				}
			}
			e.recordGroupedCohortCompleteness(ctx, principal, GroupedCohortCompletenessEvent{
				Family:                 plan.Family,
				PreGroupingComplete:    preGroupingComplete,
				PreGroupingTruncated:   preGroupingTruncated,
				GroupCount:             len(groups),
				GroupsMarkedIncomplete: groupsMarkedIncomplete,
				Complete:               cohort.Complete,
				Truncated:              cohort.Truncated,
			})
		} else if ungrouped > 0 {
			// The group axis was planned and NOTHING could be placed. The
			// answer degrades to the flat cohort it would have been, and
			// the plan stops claiming a group axis it did not deliver --
			// a plan asserting groups the answer does not carry is exactly
			// the planner defect the persisted plan exists to expose.
			plan.GroupKind = ""
		}
	}
	// stage2GroupedBasis is which grouped order (if any) THIS stage actually
	// ran, hoisted above the block below so it survives to assemblyParams:
	// stage 3's "fits" event (codex round 3, EXECUTED) measures the cohort
	// AFTER this stage shaped it, and reported a stale default basis when it
	// had no way to know what this stage had already done.
	var stage2GroupedBasis contractsv1.ContextFabricNarrowingBasis
	if graphContext.Cohort != nil && plan.Budget.MaxMembers > 0 && len(graphContext.Cohort.Members) > plan.Budget.MaxMembers {
		before := len(graphContext.Cohort.Members)
		cohort := *graphContext.Cohort
		var kept []CohortMember
		var narrowed bool
		var basis contractsv1.ContextFabricNarrowingBasis
		if len(cohort.Groups) > 0 {
			var narrowedGroups []contractsv1.ContextFabricCohortGroup
			kept, narrowedGroups, narrowed, basis = NarrowGroupedCohort(&cohort, plan.Budget.MaxMembers)
			if narrowed {
				cohort.Groups = narrowedGroups
			}
		} else {
			kept, narrowed = NarrowFlatCohort(&cohort, plan.Budget.MaxMembers)
		}
		if narrowed {
			removed := RemovedCohortMembers(cohort.Members, kept)
			cohort.Members = kept
			// Facts for a removed member must not reach synthesis: a claim
			// minted about a subject the answer no longer contains is an
			// UNGROUNDED claim, and the evidence-closure validator would
			// reject the whole result -- turning a narrowed answer into a
			// failed one.
			facts.Facts = RetainFactsForCohort(facts.Facts, &cohort, removed)
			if len(cohort.Groups) > 0 {
				ApplyGroupedCohortCompleteness(&cohort)
			} else {
				cohort.Complete = false
				cohort.Truncated = true
			}
			graphContext.Cohort = &cohort
			// groupAxis is what the selection ACTUALLY ran over -- this
			// branch calls NarrowGroupedCohort exactly when the cohort has
			// groups. groupsNarrowed stays FALSE: decision D2 is member-first
			// and every group survives, so the D2 counter must not tick.
			narrowedGroupAxis := len(cohort.Groups) > 0
			e.recordPlanNarrowingStep(&plan, PlanNarrowing{
				Stage:  contractsv1.ContextFabricPlanNarrowingSynthesisInput,
				Basis:  planStageBasis(contractsv1.ContextFabricPlanNarrowingSynthesisInput, narrowedGroupAxis, basis),
				Before: before,
				After:  len(kept),
			})
			e.recordPlanNarrowing(ctx, principal, PlanNarrowingEventFrom(plan, contractsv1.ContextFabricPlanNarrowingSynthesisInput, before, len(kept), narrowedGroupAxis, false, "", basis))
			if narrowedGroupAxis {
				stage2GroupedBasis = basis
			}
		}
	}
	var cohortSignalCitations cohortMemberSignalCitations
	// rankedForServedResult is seeded here and REPLACED by stage 3 if the
	// retry re-ranks; e.emit publishes whichever describes the served answer.
	var rankedForServedResult *CohortRankedEvent
	if graphContext.Cohort != nil {
		var rankEvent CohortRankedEvent
		graphContext.Cohort, rankEvent, cohortSignalCitations = RankCohort(graphContext.Cohort, facts.Facts, facts.Coverage)
		// DEFERRED, not emitted here: stage 3 may re-rank a narrowed cohort
		// for the retry, and the event that reaches an operator must describe
		// the cohort actually SERVED. Emitting at this point published a
		// ranking computed over members the caller never received whenever a
		// retry fired.
		rankedForServedResult = &rankEvent
	}

	assemblyParams := synthesisAssemblyParams{
		Request: request, Interpretation: interpretation, Frame: familyOutcome.Frame,
		Graph: graphContext, Facts: facts,
		Resolution: resolution, CohortSignalCitations: cohortSignalCitations,
		EffectiveWindow: effectiveWindow, WindowCanon: windowCanon, WindowCarry: windowCarry,
		StructureCanon: structureCanon, CarriedStructureEntries: carriedStructureEntries,
		CommitBases: commitBases, CommitDigests: commitDigests,
		GroupedNarrowingBasis: stage2GroupedBasis,
	}
	// The retry's base is snapshotted BEFORE the first pass runs. Taking it
	// afterwards copied state pass one had already dirtied in place -- see
	// synthesisAssemblyParams.snapshot for the two fields and why ordering,
	// not the existence of a copy, was the defect.
	retryBase := assemblyParams.snapshot()
	result, pendingTelemetry, err := e.synthesizeAndAssemble(ctx, principal, assemblyParams)
	if err != nil {
		// CHAOS-4726: attach the narrowing state as of THIS call site --
		// stage 1 and (if it ran) stage 2 are the only stages that can have
		// acted before synthesis was invoked, and this is the last point in
		// the pipeline where plan.Narrowing is both complete for that window
		// and still in scope. Every path out of synthesizeAndAssemble on
		// error, rejection included, passes through here.
		return InvestigationResult{}, withSynthesisNarrowingSnapshot(err, plan)
	}
	pendingTelemetry.CohortRanked = rankedForServedResult
	// CHAOS-4636 STAGE 3 -- measure the ASSEMBLED result and, if it does not
	// fit, RE-SYNTHESIZE ONCE with a smaller input (design §6.3).
	//
	// Here, inside the engine, AFTER synthesis and BEFORE validation and
	// persistence. Not in the route: by then validation and persistence have
	// already run, so a change there can make the stored and served answers
	// diverge. Not with the route's encoder either: internal/api imports
	// internal/contextfabric and not the reverse, which is the import cycle
	// revision 3 of this design died on. The measurement lives in
	// internal/contracts/v1, which both planes import, so the number checked
	// here is the number the route will check.
	// CODEX ROUND 1, FINDING 1 (P1): stage 3 used to run HERE, before the
	// plan, the render shapes and the completeness block were stamped -- but
	// the route marshals the FINAL document. The engine therefore measured a
	// smaller thing than the route would, and could accept a result the route
	// then 413'd on bytes. Gate agreement is the entire reason the
	// measurement was moved to internal/contracts/v1, so measuring a
	// different document defeated it.
	//
	// So the answer is FINALIZED first -- plan, shapes, completeness -- and
	// stage 3 measures that. The retry re-runs assembly AND finalization, so
	// the shape measured on the second pass is the shape that would be
	// served on the second pass.
	result = e.finalizeResult(result, plan, familyOutcome.Frame)
	result, pendingTelemetry, err = e.fitAssembledResult(ctx, principal, &plan, result, pendingTelemetry, retryBase)
	if err != nil {
		return InvestigationResult{}, err
	}
	// The plan is re-stamped after the fit, because stage 3 may have appended
	// narrowing steps to it: the persisted plan must describe the answer that
	// was actually produced, not the one first attempted.
	stampedPlan := plan
	result.AnswerPlan = &stampedPlan
	// EVERY per-investigation decision event the assembly produced fires
	// ONCE here, for the result actually served. The assembly runs twice on a
	// retry and its first answer is discarded, so emitting from inside it
	// double-counted every one of them -- see assemblyTelemetry for why this
	// is a class rule rather than a fix to one emitter.
	e.emit(ctx, principal, pendingTelemetry)
	// Render-shape telemetry fires ONCE, for the result actually served --
	// selection itself is pure and was already run by finalizeResult, but a
	// retry would otherwise double-count a decision an operator counts.
	if e.telemetry != nil {
		_, renderShapeEvent := SelectRenderShapes(result, familyOutcome.Frame)
		e.telemetry.RecordRenderShapeSelection(ctx, principal, renderShapeEvent)
		// B8's shadow gate, emitted from the SAME once-per-served-result
		// point and for the same reason: the derivation is pure and could
		// run inside finalizeResult, but finalizeResult runs again on a
		// retry and a disagreement counted twice is a disagreement RATE
		// that is wrong. It reads the result AFTER the plan re-stamp, so
		// the demands it holds the answer against are the ones the served
		// plan actually states.
		e.telemetry.RecordServerStatusShadow(ctx, principal, DeriveServerStatus(result, familyOutcome.FrameObligations))
	}
	// CHAOS-4690: the SINGLE stamp point for the decisive path -- AFTER
	// finalizeResult/fitAssembledResult (fitAssembledResult can re-run
	// assembly, so composing labels any earlier could stamp a Coverage/
	// EvidenceRefLabels shape the retry then replaces), immediately before
	// Validate. Sweep-enumerated (mirrors CHAOS-4636 sweep 2's own
	// discipline): every other fresh-result exit from Investigate calls
	// the SAME composer at its own equivalent point (unresolved.go's
	// terminalResult, window.go's windowVetoResult/
	// windowConfirmationRequiredResult, structure.go's structureVetoResult)
	// -- never on tryReuse's reuse path, which serves an immutable stored
	// result (design §7.3's named legacy exception).
	if fallbacks := applyCoverageDisplayLabels(&result); fallbacks > 0 && e.telemetry != nil {
		e.telemetry.RecordEvidenceLabelFallback(ctx, principal, fallbacks)
	}
	// Y3: the FINAL budget assertion. fitAssembledResult above measured the
	// result BEFORE the plan re-stamp and before applyCoverageDisplayLabels,
	// and both of those add bytes to the document the route will serialize --
	// so the thing measured there was not the thing served. This is the point
	// at which the document is final, and it is the point four successive
	// revisions of the minimal-answer-floor specification failed to reach.
	//
	// plan.Budget already carries the EFFECTIVE budget (effectiveResponseBudget
	// mirrors the route's own min(config, request) exactly), so it is passed
	// explicitly rather than re-derived -- projected into ResponseBudget the
	// same way fitAssembledResult projects it, so both measurements are taken
	// against the same two numbers -- and deliberately NOT folded into
	// Validate(), which takes no budget parameter and would have to grow a
	// contract-wide signature change for one caller. See budget_assertion.go.
	// plan is nil here, and that is not an omission: this path re-stamps the
	// plan itself a few lines above (the narrowing steps stage 3 appended must
	// reach the served document), so there is nothing left for finalizeServed
	// to stamp. It still measures, which is the half that matters.
	result, budgetErr := e.finalizeServed(ctx, principal, BudgetAssertDecisive, result, nil, ResponseBudget{MaxItems: plan.Budget.MaxItems, MaxSerializedBytes: plan.Budget.MaxSerializedBytes})
	if budgetErr != nil {
		return InvestigationResult{}, budgetErr
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
			// CHAOS-3927 P4 (design brief §2.1): a decisive result carrying
			// confirmed structure can still lose the atomic (org,
			// prior_result_id, member) supersession claim to a concurrent
			// Save that got there first -- the narrow race
			// canonicalizeStructure's own pre-flight consult cannot fully
			// close (StructureSupersessionChecker's own doc comment). This
			// computed result must NEVER reach the caller: the round
			// terminates stale_superseded_offer instead, the SAME veto
			// terminal a pre-flight detection would have produced, echoing
			// whichever confirmed member actually lost the race (never
			// silently discarding the conflict information Save reported).
			var superseded *ErrStructureOfferSuperseded
			if errors.As(err, &superseded) {
				recordWindowSupersessionRaceTelemetry(ctx, e.telemetry, principal, superseded)
				// CHAOS-3478 (codex round-2 finding): result.SubjectResolution
				// already carries the dispositions this Save attempt was
				// about to persist (set on the SAME resolution value
				// earlier in this function) -- the race terminal must not
				// silently drop them.
				// CHAOS-4636 sweep 2: a THIRD post-plan exit that serves and
				// persists a result. Found by enumerating every return in
				// Investigate rather than by fixing the two that were
				// reported -- which is the whole point of doing this as a
				// sweep: the class was "post-plan exits", never "this exit".
				superseding, supersededErr := e.structureSupersessionVetoResult(ctx, principal, request, mergeConfirmedMembers(structureCanon.Confirmed, windowCanon.ConfirmedMember), superseded, binding, result.SubjectResolution.PriorSubjectReceiptDispositions, carriedStructureEntries, &plan)
				return superseding, supersededErr
			}
			return InvestigationResult{}, stageError(StagePersistence, fmt.Errorf("save investigation result: %w", err))
		}
		e.emitBindingEpochDelta(ctx, principal, epochDeltaSample)
		// CHAOS-3927 P4 (codex adversarial review fix): THIS is the point
		// the caller can finally prove a structure confirmation is durable
		// -- Save just succeeded past the atomic supersession-claim check
		// above, so every member in structureCanon.Confirmed genuinely won
		// its claim (or there were none to claim, the common case, in
		// which case this is a no-op). See
		// recordStructureConfirmationOutcome's own doc comment for why this
		// call is shared with terminalResult's own Save call site, not
		// hand-copied.
		e.recordStructureConfirmationOutcome(ctx, principal, request, structureCanon)
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
// Returns (hints, validated, staleGraphEpochDelta, outcomes).
// CHAOS-3478/CHAOS-3813: WHY each non-hint receipt fell out used to be
// three separate int counts (unloadable/noMatch/staleGraphEpoch, CHAOS-3888,
// extended by CHAOS-3898 §2.2); it is now carried per-receipt on outcomes
// (priorSubjectReceiptOutcome, one entry per input receipt, in order) so the
// SAME classification can drive both the wire disposition echo
// (composePriorSubjectReceiptDispositions) and the telemetry counts
// (recordPriorSubjectReceiptSkips) without computing it twice. unloadable
// covers a receipt with a blank ResultID/ReceiptID or whose prior
// InvestigationResult failed to load; stale_graph_epoch (§2.2) covers a
// receipt whose prior result loaded but whose StoredInvestigationResult
// carrier failed the ingress taint gate -- a DISTINCT reason from
// unloadable, because the row itself loaded fine, it simply describes a
// different graph epoch than this investigation is reading from; no_match
// covers a receipt whose prior result loaded, passed the taint gate, and
// still named no matching candidate. The fourth reason, failed_reauth, is
// NOT knowable here -- it depends on what happens to a matched hint after
// this call returns -- so an outcome with hasHint=true carries no
// disposition yet; composePriorSubjectReceiptDispositions resolves it
// against the caller's own final SubjectResolution.
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
// priorSubjectReceiptPreGraphSkipReason is the closed set of reasons
// resolvePriorSubjectHints itself can strip a receipt BEFORE any graph
// resolution runs -- the same three telemetry reasons
// EngineTelemetry.RecordPriorSubjectReceiptSkipReason already names,
// pulled out as typed constants so composePriorSubjectReceiptDispositions
// (below) and the telemetry it replaced can never name a fourth string
// pair that silently drifts apart.
type priorSubjectReceiptPreGraphSkipReason string

const (
	priorSubjectReceiptSkipUnloadable      priorSubjectReceiptPreGraphSkipReason = "unloadable"
	priorSubjectReceiptSkipNoMatch         priorSubjectReceiptPreGraphSkipReason = "no_match"
	priorSubjectReceiptSkipStaleGraphEpoch priorSubjectReceiptPreGraphSkipReason = "stale_graph_epoch"
)

// priorSubjectReceiptOutcome pairs ONE PriorSubjectReceipts entry with what
// resolvePriorSubjectHints did with it, in the caller's own request order
// (CHAOS-3478/CHAOS-3813). This is the single source both the wire
// disposition echo (composePriorSubjectReceiptDispositions) and the
// telemetry skip counts (recordPriorSubjectReceiptSkips) are built from, so
// the two can never disagree about a receipt's fate. hasHint is false for
// every pre-graph strip (preGraphSkipReason names why); true means the
// receipt matched a real candidate in its named prior result and produced
// hint -- whether that hint went on to survive THIS call's own graph
// re-authorization is a question only the caller's own SubjectResolution
// can answer, which is why this type carries no "applied" verdict itself.
type priorSubjectReceiptOutcome struct {
	receipt             BoundSubjectReceipt
	hint                SubjectHint
	hasHint             bool
	preGraphSkipReason  priorSubjectReceiptPreGraphSkipReason
	droppedByHintBudget bool
}

// markTrailingHintOutcomesDroppedByBudget marks the last `dropped` outcomes
// with hasHint==true (CHAOS-3813 codex round-1 finding). priorHints and the
// hasHint==true subset of priorOutcomes are appended together, in the same
// relative order, inside resolvePriorSubjectHints -- so slicing
// priorHints[:available] in the caller and marking this same subset's own
// trailing N hasHint==true entries here removes the SAME logical hints from
// both, without needing the two slices to share indices.
func markTrailingHintOutcomesDroppedByBudget(outcomes []priorSubjectReceiptOutcome, dropped int) {
	for i := len(outcomes) - 1; i >= 0 && dropped > 0; i-- {
		if outcomes[i].hasHint {
			outcomes[i].droppedByHintBudget = true
			dropped--
		}
	}
}

// The fifth return (CHAOS-4636) is every prior result this call ALREADY
// loaded and taint-gated, so a later carry can read a plan out of one without
// paying for a second store round-trip. Every entry has passed the CHAOS-3898
// §2.2 epoch gate below before being put there, so a reader of this map
// inherits that check rather than needing to repeat it.
func (e *Engine) resolvePriorSubjectHints(ctx context.Context, principal storage.Principal, consumer ConsumerInfo, receipts []BoundSubjectReceipt, binding ResolvedGraphBinding) ([]SubjectHint, []BoundSubjectReceipt, int64, []priorSubjectReceiptOutcome, map[string]InvestigationResult) {
	hints := make([]SubjectHint, 0, len(receipts))
	validated := make([]BoundSubjectReceipt, 0, len(receipts))
	outcomes := make([]priorSubjectReceiptOutcome, 0, len(receipts))
	loaded := make(map[string]InvestigationResult, len(receipts))
	var staleGraphEpochDelta int64
	for _, receipt := range receipts {
		if ctx.Err() != nil {
			return hints, validated, staleGraphEpochDelta, outcomes, loaded
		}
		resultID := strings.TrimSpace(receipt.ResultID)
		receiptID := strings.TrimSpace(receipt.ReceiptID)
		if resultID == "" || receiptID == "" {
			outcomes = append(outcomes, priorSubjectReceiptOutcome{receipt: receipt, preGraphSkipReason: priorSubjectReceiptSkipUnloadable})
			continue
		}
		prior, ok := loaded[resultID]
		if !ok {
			fetched, err := carryLoadResult(ctx, e.results, principal, resultID)
			if err != nil {
				outcomes = append(outcomes, priorSubjectReceiptOutcome{receipt: receipt, preGraphSkipReason: priorSubjectReceiptSkipUnloadable})
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
				outcomes = append(outcomes, priorSubjectReceiptOutcome{receipt: receipt, preGraphSkipReason: priorSubjectReceiptSkipStaleGraphEpoch})
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
			hint := SubjectHint{
				Kind: candidate.Subject.Kind, ID: candidate.Subject.CanonicalID,
				Label: candidate.Subject.Label, Source: "prior_subject_receipt",
			}
			hints = append(hints, hint)
			validated = append(validated, receipt)
			outcomes = append(outcomes, priorSubjectReceiptOutcome{receipt: receipt, hint: hint, hasHint: true})
			break
		}
		if !matched {
			outcomes = append(outcomes, priorSubjectReceiptOutcome{receipt: receipt, preGraphSkipReason: priorSubjectReceiptSkipNoMatch})
		}
	}
	return hints, validated, staleGraphEpochDelta, outcomes, loaded
}

// composePriorSubjectReceiptDispositions builds CHAOS-3478/CHAOS-3813's
// wire-visible disposition echo for every PriorSubjectReceipts entry the
// caller sent, in the caller's own order -- the same "one entry per
// carried item, including skipped ones" rule composeConfirmedStructure
// (structure.go) already applies to structure receipts, extended here to
// the plural prior-subject-receipt list. nil when outcomes is empty (the
// ordinary case: no receipts sent, or an earlier veto returned before
// resolvePriorSubjectHints ever ran), mirroring every other empty-echo
// convention in this package.
//
// resolution is the FINAL SubjectResolution this Investigate call actually
// produced (or a zero-value SubjectResolution -- empty Candidates/Committed
// -- for a call that never ran a real graph resolution: CHAOS-4077's
// ErrGraphNotProjected branch, and CHAOS-4234's gated-class-default branch,
// which discards its own offers-only resolution by ruling). A receipt that
// matched a real prior candidate but cannot be confirmed against a real
// resolution here reads as skipped_failed_reauth, honestly reporting "not
// re-verified this call" rather than misreporting "applied" -- the exact
// convention recordPriorSubjectReceiptSkips already used for the
// ErrGraphNotProjected branch before this ticket, now shared instead of
// duplicated.
func composePriorSubjectReceiptDispositions(outcomes []priorSubjectReceiptOutcome, resolution SubjectResolution) []contractsv1.ContextFabricPriorSubjectReceiptEntry {
	if len(outcomes) == 0 {
		return nil
	}
	resolved := make(map[string]struct{}, len(resolution.Candidates)+len(resolution.Committed))
	for _, candidate := range resolution.Candidates {
		resolved[subjectKeyForModel(candidate.Subject)] = struct{}{}
	}
	for _, subject := range resolution.Committed {
		resolved[subjectKeyForModel(subject)] = struct{}{}
	}
	entries := make([]contractsv1.ContextFabricPriorSubjectReceiptEntry, 0, len(outcomes))
	for _, outcome := range outcomes {
		disposition := contractsv1.ContextFabricPriorSubjectReceiptSkippedUnloadable
		if outcome.droppedByHintBudget {
			// This receipt's own hint never reached GraphReader -- report
			// its actual fate even if some OTHER hint happened to resolve
			// the same subject this call (CHAOS-3813 codex round-1
			// finding); checking `resolved` here would misreport this
			// receipt as "applied" on the strength of a hint that was not
			// its own.
			disposition = contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth
		} else if !outcome.hasHint {
			switch outcome.preGraphSkipReason {
			case priorSubjectReceiptSkipNoMatch:
				disposition = contractsv1.ContextFabricPriorSubjectReceiptSkippedNoMatch
			case priorSubjectReceiptSkipStaleGraphEpoch:
				disposition = contractsv1.ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch
			default:
				disposition = contractsv1.ContextFabricPriorSubjectReceiptSkippedUnloadable
			}
		} else if _, ok := resolved[string(outcome.hint.Kind)+"\x00"+outcome.hint.ID]; ok {
			disposition = contractsv1.ContextFabricPriorSubjectReceiptApplied
		} else {
			disposition = contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth
		}
		entries = append(entries, contractsv1.ContextFabricPriorSubjectReceiptEntry{
			PriorResultID: outcome.receipt.ResultID,
			ReceiptID:     outcome.receipt.ReceiptID,
			Disposition:   disposition,
		})
	}
	return entries
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

// recordPriorSubjectReceiptSkips reports the skip counts EngineTelemetry
// already tracked before CHAOS-3478/CHAOS-3813, now DERIVED from
// dispositions (composePriorSubjectReceiptDispositions' own output) instead
// of recomputing them a second time from priorHints/resolution -- the two
// call sites (the wire echo and this telemetry) shared the exact same
// "did this receipt end up bound to a resolved subject" question, and
// computing the answer twice is exactly the class of divergence risk
// AGENTS.md's differential-oracle guidance warns about (two
// implementations of the same logic that can silently disagree). dispositions
// is index-for-index the SAME classification the caller already put on the
// wire (nil/empty is a no-op, matching every early-return convention here).
// staleGraphEpochDelta (CHAOS-3898 §5b cf_receipt_taint_strip) is the one
// figure dispositions cannot carry (a magnitude, not a disposition) and
// stays a separate parameter, exactly as before.
func (e *Engine) recordPriorSubjectReceiptSkips(ctx context.Context, principal storage.Principal, dispositions []contractsv1.ContextFabricPriorSubjectReceiptEntry, staleGraphEpochDelta int64) {
	if e.telemetry == nil || len(dispositions) == 0 {
		return
	}
	var unloadable, noMatch, staleGraphEpoch, failedReauth, skipped int
	for _, entry := range dispositions {
		switch entry.Disposition {
		case contractsv1.ContextFabricPriorSubjectReceiptSkippedUnloadable:
			unloadable++
			skipped++
		case contractsv1.ContextFabricPriorSubjectReceiptSkippedNoMatch:
			noMatch++
			skipped++
		case contractsv1.ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch:
			staleGraphEpoch++
			skipped++
		case contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth:
			failedReauth++
			skipped++
		}
	}
	if skipped > 0 {
		e.telemetry.RecordPriorSubjectReceiptsSkipped(ctx, principal, skipped)
	}
	if failedReauth > 0 {
		e.telemetry.RecordPriorSubjectReceiptSkipReason(ctx, principal, "failed_reauth", failedReauth, 0)
	}
	if unloadable > 0 {
		e.telemetry.RecordPriorSubjectReceiptSkipReason(ctx, principal, "unloadable", unloadable, 0)
	}
	if noMatch > 0 {
		e.telemetry.RecordPriorSubjectReceiptSkipReason(ctx, principal, "no_match", noMatch, 0)
	}
	// CHAOS-3898 §5b cf_receipt_taint_strip: epochDelta is the ONLY reason
	// this method's staleGraphEpochDelta parameter is ever non-zero (see
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

// scopeAnchorResolved (CHAOS-4622 remainder) reduces the winning
// question-family sample's ScopeAnchorTerm to whether it resolved --
// GraphDiscoveryRequest.ScopeAnchorResolved's own doc comment explains why
// only the bool travels past this point. WinningSampleIndex is -1 when
// ResolveQuestionFamily found no consensus winner (no majority, or zero
// samples) -- outcome.WinningSample is then its zero value, whose empty
// ScopeAnchorTerm would already read as false on its own -- the explicit
// index check is belt-and-braces, making "no winner" and "winner named
// nothing" two distinct paths to the same false rather than one relying on
// zero-value behavior a future WinningSample field addition could change.
func scopeAnchorResolved(outcome QuestionFamilyOutcome) bool {
	return outcome.WinningSampleIndex >= 0 && outcome.WinningSample.ScopeAnchorTerm != ""
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
