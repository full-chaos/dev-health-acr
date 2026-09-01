package contextfabric

import (
	"context"
	"errors"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var (
	// ErrUnavailable identifies a bounded dependency failure that callers may
	// safely surface as a retryable degraded/unavailable outcome.
	ErrUnavailable = errors.New("context fabric dependency unavailable")
	// ErrInvalidResult identifies an implementation result that violated the
	// Context Fabric domain contract before it reached a consumer.
	ErrInvalidResult = errors.New("context fabric result is invalid")
	// ErrProjectionConflict identifies an out-of-order or incompatible
	// projection batch. The worker must not advance its checkpoint.
	ErrProjectionConflict = errors.New("context fabric projection conflict")
	// ErrProjectionSourceVersionChanged identifies a checkpoint whose
	// stored SourceVersion no longer matches the current batch's
	// SourceVersion (CHAOS-3779 codex round-2 H2 residual): the producer's
	// identity semantics changed since this organization was last
	// projected (e.g. CHAOS-3779's own RelationshipID scheme change). The
	// worker must refuse the incremental advance and must never call
	// ApplyProjectionBatch -- doing so would MERGE a new edge under the
	// new identity beside whatever the old identity already wrote,
	// silently doubling edges. Recovery is the existing rebuild path
	// (projectionrun.Coordinator.Rebuild), which resets the checkpoint to
	// its zero value, clearing the stored SourceVersion along with it.
	ErrProjectionSourceVersionChanged = errors.New("context fabric projection source version changed; rebuild required")
	// ErrRateLimited signals a bounded dependency (graph backend or
	// canonical fact source) rejected a call because a rate or quota limit
	// was exceeded. Adapters must wrap their own vendor-specific
	// rate-limit classification into this at their package boundary so
	// callers (Engine, the route layer) can classify the failure without
	// importing any specific backend's package. This keeps
	// ErrModelRateLimited (a distinct, pre-existing sentinel for the model
	// runtime specifically) and this one both reachable from one
	// vendor-neutral check.
	ErrRateLimited = errors.New("context fabric dependency rate limited")
	// ErrQueryBudgetExceeded identifies a canonical source query that
	// exceeded its own configured read budget (e.g. ClickHouse
	// max_bytes_to_read/max_result_rows). CHAOS-3848: this used to satisfy
	// only ErrUnavailable, which reads to an operator as a transient
	// dependency outage. It is the opposite -- a PERMANENT condition for the
	// current query shape and data volume until the budget or the query
	// itself changes, since retrying the identical query against unchanged
	// data fails the identical way every time. Distinct from ErrUnavailable
	// so the failure names itself; both still hold the checkpoint for
	// replay, because "budget-exceeded but unresolved" is not safe to skip
	// past either.
	ErrQueryBudgetExceeded = errors.New("context fabric canonical source query exceeded its read budget")
	// ErrProjectionWatermarkNotFound is the backend-neutral classification
	// for "ProjectionBackend.ProjectionWatermark has no durable sentinel for
	// this (org, source)" (CHAOS-3882) -- the same
	// ErrInvestigationResultNotFound / ErrOrgModelConfigNotFound pattern:
	// every ProjectionBackend implementation wraps its own vendor-specific
	// not-found sentinel (e.g. falkorgraph.ErrNotFound) in this one so a
	// backend-agnostic caller (projectionrun.Coordinator) can recognize a
	// CONFIRMED absence without importing a specific backend package.
	//
	// This one sentinel deliberately covers two different histories a caller
	// cannot and must not try to tell apart from the error alone: an
	// (org, source) that was never durably projected, and one that WAS
	// projected but whose backend has since lost that state (a purge, or --
	// CHAOS-3882 -- a container restart against non-durable storage).
	// Telling them apart is the CALLER's job, using its own durable state
	// (projectionrun.Coordinator does this by comparing against the Postgres
	// ProjectionCheckpoint's BackendWatermark, a wholly separate store from
	// the graph this sentinel describes): only a caller that already expected
	// a sentinel to exist may treat this error as evidence of drift.
	//
	// Any OTHER error ProjectionWatermark returns (a dependency timeout, a
	// rate limit) must NOT satisfy errors.Is(err, ErrProjectionWatermarkNotFound)
	// -- a transient failure is not a confirmed absence, and a caller that
	// reacts to one as if it were risks a destructive action (e.g. a
	// PurgeOrganization-based rebuild) triggered by a network blip rather
	// than a real divergence.
	ErrProjectionWatermarkNotFound = errors.New("context fabric: projection watermark not found")
	// ErrGraphNotProjected is the backend-neutral classification for
	// "GraphReader.ResolveSubjects/DiscoverContext queried a graph key
	// that has never been created" (CHAOS-4077) -- an org whose projector
	// has never run for it, not a transient dependency problem. Every
	// GraphReader implementation wraps its own vendor-specific
	// missing-graph sentinel (e.g. falkorgraph.ErrNotFound, returned by
	// FalkorDB's GRAPH.RO_QUERY against a key that was never
	// GRAPH.QUERY-created) into this one, so Engine can recognize a
	// never-projected org without importing a specific backend package.
	//
	// Deliberately DISTINCT from ErrProjectionWatermarkNotFound: that
	// sentinel answers "does projectionrun.Coordinator's own checkpoint
	// bookkeeping have a durable watermark for this (org, source)", a
	// question about the PROJECTOR's own state; this one answers "does
	// the graph this investigation is about to READ exist at all", a
	// question about the read path itself. The two can and do diverge --
	// a watermark can exist while a specific epoch's graph key does not
	// (mid-rebuild), and this sentinel says nothing about watermark
	// state either way.
	//
	// Engine's own handling (engine.go, ResolveSubjects call site) treats
	// this exactly like a resolution that legitimately found zero
	// candidates: it skips DiscoverContext entirely (which would query
	// the identical nonexistent key and fail the same way) and degrades
	// straight to the same clean terminalResult path a naturally-empty
	// resolution already uses -- never propagated as a 5xx.
	//
	// Any OTHER error a GraphReader returns (a rate limit, a timeout, a
	// real dependency outage) must NOT satisfy
	// errors.Is(err, ErrGraphNotProjected) -- degrading one of those to a
	// silent "no match" would misreport a real outage as a clean answer.
	ErrGraphNotProjected = errors.New("context fabric: graph not yet projected for this organization")
	// ErrUnsupportedTimeAxis is RETIRED by CHAOS-3781 and deliberately
	// not replaced by an equivalent.
	//
	// It meant "this service cannot answer a historical question at all"
	// (CHAOS-3755 finding H6), which was true and honest while every
	// canonical source below read current state only. It stopped being
	// true when the graph gained validity-window admission and the fact
	// providers gained time bounds: a historical question is now answered
	// on every axis, with the sources that cannot speak for the requested
	// time degrading individually in coverage (AC-3781-5) rather than the
	// whole request being refused.
	//
	// AC-3781-6 required the refusal be removed from the engine, from
	// every provider, and from the route in ONE change, for a specific
	// reason: a partial removal reproduces exactly the false historical
	// answer H6 named. If one layer still refuses, callers see an
	// inconsistent service; if one layer stops refusing while another
	// still cannot bound itself, that layer answers with current data
	// under a historical label.
	//
	// What replaced it is contextfabric.ErrInvalidTimeBound (temporal.go),
	// which is narrower on purpose: not "historical questions are
	// unsupported" but "these particular bounds are not answerable" -- a
	// future instant, or a range wider than this service will read.
)

// Investigator is the consumer-neutral Context Fabric entry point. Ask Dev,
// the standalone Workbench, and MCP must all consume projections of this same
// result rather than implementing separate investigation pipelines.
type Investigator interface {
	Investigate(context.Context, storage.Principal, InvestigationRequest) (InvestigationResult, error)
}

// QuestionInterpreter interprets natural-language engineering questions. It
// may classify reusable analytical capabilities, but it must not require the
// question text to match a finite supported-question registry.
//
// CHAOS-4634 (S4): Interpret ALSO returns the QuestionFamilyOutcome this
// same call resolved -- an IN-PROCESS Go return value only, never on the
// wire (InterpretedQuestion is unchanged; QuestionFamilyOutcome is not a
// contracts/v1 type). Before S4, CHAOS-4632 (S2) resolved and telemetered
// this outcome INSIDE RuntimeQuestionInterpreter.Interpret and then
// discarded it -- shadow only, gating nothing. S4 is the slice where the
// family first affects an answer (chaos4634_family_offer_gate... see
// chaos4579_cohort_structure_gate.go's own CHAOS-4634 header), so the
// outcome must reach Engine rather than die at the interpreter boundary.
// Returned explicitly, not threaded via ctx: this is load-bearing data the
// offer-composition call sites (engine.go) require to gate correctly, and
// a caller who forgets to thread it fails to compile -- the team-lead's
// own standing rule (engine.go's reuseWatermarkSnapshot comment) for any
// data a wrong omission could silently mis-answer with, as opposed to a
// pure cost optimisation (see WithOffersOnlyResolution's contrasting
// ctx-carried precedent, chaos4234_offers_only.go, for a flag whose
// omission is safe by construction).
//
// Zero blast radius beyond this package's own call sites: RuntimeQuestionInterpreter
// is the ONLY production implementation (verified: `git grep QuestionInterpreter`
// finds no second one).
type QuestionInterpreter interface {
	Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error)
}

// GraphReader owns subject/cohort discovery and bounded relationship context.
// Implementations may use any selected graph/index backend behind this
// interface; consumers never receive graph-native queries or identifiers.
//
// ResolveInvestigationBinding (CHAOS-3898 §2.1) resolves the organization's
// CURRENT ResolvedGraphBinding -- the full derived graph key and the epoch
// it names. Engine calls this EXACTLY ONCE per investigation, at request
// start, before either of the two calls below and before tryReuse -- see
// ResolvedGraphBinding's own doc comment for why re-resolving independently
// at each call site (the pre-CHAOS-3898 behavior) is a real race once a
// build/flip can move the active epoch mid-request. Both ResolveSubjects and
// DiscoverContext then take that SAME binding explicitly, rather than
// resolving their own -- a caller who forgets to resolve or thread it fails
// to compile.
type GraphReader interface {
	ResolveInvestigationBinding(ctx context.Context, principal storage.Principal) (ResolvedGraphBinding, error)
	// ResolveSubjects' own request parameter already carries ExpectedKinds/
	// SubjectHandles (CHAOS-3972 P3) -- the MCP surface's explicit
	// structure fields -- so implementations read the inferred-tier kind/
	// handle source directly off it, exactly as they already read every
	// other wire field. Only the RECEIPT-confirmed kind gets its own
	// dedicated parameter (confirmedKind below): that value does not exist
	// on the wire request at all, it is canonicalizeStructure's own
	// derived result, and InferredExpectedKinds' own doc comment
	// (ConfirmedExpectedKind, this file) is the reason the two must never
	// share one parameter or one narrowing code path.
	// confirmedAnchor (CHAOS-4042) is ConfirmedExpectedKind's own sibling
	// for the subject_anchor member -- see ConfirmedAnchorSelection's own
	// doc comment.
	// CHAOS-4085: the third return is the CommitBasisSet -- for every
	// subject in the returned Committed list, WHICH CLASS OF PROOF the
	// commit stood on (see CommitBasis). It is in the SIGNATURE rather
	// than a side channel for the reason this file already applies to
	// every other load-bearing value (ResolvedGraphBinding above): the
	// engine's post-synthesis affirmation gate cannot decide correctly
	// without it, so an implementation that omits it must fail to compile,
	// not change behavior silently. An implementation that returns nil is
	// nevertheless SAFE, not broken: every basis then reads back as
	// CommitBasisUnknown, IdentityProven reports false, and every commit
	// takes the strict path. CommitDecisionDigestSet (CHAOS-4087) is the
	// SAME record's wire-safe, persisted companion -- see that type's own
	// doc comment -- with the identical nil-is-safe guarantee: an absent
	// digest reads back as the zero CommitDecisionDigest (CommitGate==""),
	// the fail-closed "nothing recorded" value.
	ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error)
	DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error)
}

// ConfirmedExpectedKind (CHAOS-3900 P1.D, pivot-intent design brief §2.1)
// is a caller-CONFIRMED kindr_ receipt's own resolved value, threaded
// into ResolveSubjects so it can narrow its own candidate pool to this
// kind alone ("the confirmed kind becomes the census scope... A
// RECEIPT-confirmed kind is caller authority and may do this... without
// the extra proof").
//
// DELIBERATELY a dedicated type, not a bare ContextFabricSubjectKind
// (design ruling, P1.D's own tripwire): this is HOW the §2.0
// kind-insensitivity rule stays enforced by construction rather than by
// discipline. §2.0 requires that any kind narrowing NOT sourced from a
// confirmed receipt (an inferred_default proposal, an echo-derived
// value, a future Bridge prior) additionally prove all-kinds
// insensitivity before a decisive outcome may rely on it -- but no such
// inferred-tier kind source exists anywhere in this codebase yet (P1.D
// scoping confirmed this by grep: zero hits). Rather than wire an
// insensitivity-proof GATE with no live branch to guard (untestable
// protection, worse than no protection -- a false sense of coverage),
// this type itself is the guard: graphrank's pool filter
// (filterCandidatesByConfirmedKind) accepts ONLY this type, which only
// canonicalizeStructure's own receipt-confirmation path can construct.
// A future inferred/explicit kind source cannot narrow the pool through
// this same function without EITHER routing through confirmation
// (unlikely to be what such a feature wants) OR a reviewer visibly
// widening this type or adding a second, parallel filter -- and
// kindInsensitivityProof (graphrank/chaos3900_structure_offers.go) is the
// insensitivity-proof primitive P1.D's own scoping named a HARD
// PRECONDITION of introducing any inferred-tier kind source -- WIRED as of
// CHAOS-3972 P3 into RunShadowEvidenceRound's own decisive switch
// (chaos3899_evidence_round.go), consulted whenever the round's PooledKinds
// was narrowed by request.ExpectedKinds (the explicit-tier source this
// ticket introduces) rather than a kindr_ receipt.
type ConfirmedExpectedKind struct {
	Kind contractsv1.ContextFabricSubjectKind
}

// ConfirmedAnchorSelection (CHAOS-4042, sol-max ruling) is a caller-
// CONFIRMED ancr_ receipt's own resolved claimant, threaded into
// ResolveSubjects so the shadow evidence round's FK predicate can use it
// as the anchor discriminator INSTEAD of BindAnchor's own question-derived,
// strict unique-claimant scan -- the ruling's own gap: "confirmed anchor
// receipts today are reverified and echoed but never become the census
// discriminator." A receipt-confirmed selection supplies caller INTENT
// authority (the same authority a confirmed kind already narrows the
// census with, see ConfirmedExpectedKind's own doc comment) -- it does
// NOT set graphrank.Attestation.AnchorUniqueClaimant=true: that field
// asserts BindAnchor's own term-uniqueness proof specifically, which a
// receipt-confirmed selection never runs. See
// graphrank.Attestation.AnchorReceiptConfirmed for the separate,
// non-lying provenance this path sets instead.
//
// DELIBERATELY a dedicated type, not a bare (kind, canonical_id) pair --
// the SAME P1.D tripwire ConfirmedExpectedKind's own doc comment
// describes: only canonicalizeStructure's own RECEIPT-confirmation path
// (structure.go) may construct one, so a future inferred-tier anchor
// source cannot drive the census discriminator through this same channel
// without a reviewer visibly widening this type.
type ConfirmedAnchorSelection struct {
	Kind        contractsv1.ContextFabricSubjectKind
	CanonicalID string
}

// CHAOS-3972 P3 (pivot-intent design brief §2.3/§2.0/DP12(b)) introduces
// the OTHER kind source this type's own doc comment above names as a HARD
// PRECONDITION before it may exist at all: request.ExpectedKinds, the MCP
// surface's own explicit_unattributed kind field (InvestigationRequest,
// this package). It is DELIBERATELY never threaded through
// graphrank.filterCandidatesByConfirmedKind or wrapped in this type -- the
// type-level tripwire ConfirmedExpectedKind's own doc comment describes
// stays exactly as narrow as it was: only canonicalizeStructure's
// RECEIPT-confirmation path may construct a ConfirmedExpectedKind, and
// only that type may narrow the ordinary lexical/vector resolution pool
// (candidatesBySubject) without an insensitivity proof.
//
// request.ExpectedKinds narrows something ELSE, and only there: the
// EVIDENCE ROUND'S OWN pooled-kind hypothesis set (design brief §2.3:
// "they drive census-narrowing... shape the offer sets" -- census-
// narrowing, not candidate-pool narrowing). ResolveSubjects' own request
// parameter already carries it, so no second parameter or wrapper type is
// needed -- see graphrank.narrowPooledKindsByExplicitKinds
// (chaos3900_structure_offers.go) for where the narrowing happens, and
// runShadowEvidenceRoundForResolution's (resolve.go) own
// kindInsensitivityProof call for the decisive-path gate this precondition
// requires: a would_commit/would_no_match outcome the evidence round
// reached under this narrowing is trustworthy only when the SAME round,
// re-run over the PRE-narrowing kind set, agrees -- otherwise the round
// demotes to kind_sensitive_outcome (clarify), never a decisive answer.
// This is the wiring CHAOS-3927/P1.D's own scoping named as the hard
// precondition for introducing any inferred-tier kind source at all.

// StructureOfferMaterial is CHAOS-3900 P1.C's own return channel: the
// engine-derived structure-offer candidates ResolveSubjects builds
// alongside its ordinary subject resolution, using the SAME pool/identity/
// question-text material that resolution already computed (pool kind-set,
// identity-universe unique-claimant candidates, handle-grammar bindings --
// see internal/contextfabric/graphrank's own doc comments for exactly
// which existing primitives each candidate list is built from).
//
// DELIBERATELY a widened RETURN VALUE, not a field folded onto
// SubjectResolution or threaded via json:"-" (team-lead ruling, P1.C
// seam decision): SubjectResolution is a WIRE type (it IS the served
// result's own subject_resolution field), and this material feeds a
// DIFFERENT wire field (structure_needs) once composed -- and this
// session's own defect ledger (the reuse-path bypass, the key collapse,
// the schema-parity drift) is entirely the invisible-seam class, so
// load-bearing dataflow belongs in a signature the compiler and every
// reviewer sees, not behind a serialization-discipline convention on a
// persisted type.
//
// The KindOptions/AnchorOptions/HandleOptions below are PARTIALLY built:
// every content field (Kind, Label, CanonicalID, PatternID, Value,
// SourceColumn, OfferSource) is set, but ReceiptID and OptionID
// are left zero-valued -- minting them requires the result's own
// ResultID (mintStructureReceiptID's own "result identity" input, per
// the team-lead's deterministic-minting ruling), which does not exist
// yet at ResolveSubjects' own call time (Engine mints it only once
// composing the final result, after ResolveSubjects/DiscoverContext have
// both already returned). composeStructureNeeds (structure.go) fills
// both ids in once ResultID is known.
type StructureOfferMaterial struct {
	// Missing is the class-conditional §1.3 NEVER-ELICIT-filtered set --
	// ResolveSubjects already has the INTERPRETED question (Shape/cohort_shape)
	// as its own parameter, so it can apply the rule directly rather than
	// deferring it to a caller that would need to re-derive class from the
	// same interpretation a second time.
	Missing       []contractsv1.ContextFabricStructureNeedKind
	KindOptions   []contractsv1.ContextFabricKindOption
	AnchorOptions []contractsv1.ContextFabricAnchorOption
	HandleOptions []contractsv1.ContextFabricHandleOption
	// AnchorOptionsRequireV2 (CHAOS-4042, sol-max ruling) is true when
	// AnchorOptions above were minted under membership-verify semantics
	// (an ambiguous term's claimants, offered without requiring
	// exclusivity) rather than v1's unique-claimant semantics. The result
	// this material attaches to (terminalResult, unresolved.go) must then
	// persist with SchemaVersion == InvestigationResultSchemaV2, not V1 --
	// AnchorOptions itself stays wire-identical either way (the ruling's
	// "JSON fields may remain identical"), so this bool is the ONLY signal
	// that distinguishes the two on this struct.
	AnchorOptionsRequireV2 bool
	// CandidateOptions (CHAOS-4012) fires INDEPENDENTLY of KindOptions above
	// -- see ContextFabricStructureNeedSubjectCandidate's own doc comment.
	CandidateOptions []contractsv1.ContextFabricCandidateOption
}

type GraphDiscoveryRequest struct {
	Request        InvestigationRequest `json:"request"`
	Interpretation InterpretedQuestion  `json:"interpretation"`
	Resolution     SubjectResolution    `json:"resolution"`
	// Binding (CHAOS-3898 §2.1) is the SAME ResolvedGraphBinding this
	// investigation's ResolveSubjects call already used -- never
	// independently re-resolved. See GraphReader's doc comment.
	Binding ResolvedGraphBinding `json:"-"`
	// ScopeAnchorResolved (CHAOS-4622 remainder) is CHAOS-4632's own
	// ScopeAnchorTerm signal (chaos4632_question_family_precedence.go),
	// reduced to whether it resolved -- a RETRIEVAL POINTER, never a value
	// (ScopeAnchorTerm's own doc comment: "nothing anywhere branches on its
	// text"), so only the bool travels this far, never the term itself.
	// True means this turn's winning interpretation sample named a
	// specific subject; false means it did not, regardless of which Shape
	// label the model happened to emit this turn -- Shape alone is the
	// unstable variable CHAOS-4622 traced the garble to. See
	// DiscoverContext's own exact-name census gate for the one place this
	// is read.
	ScopeAnchorResolved bool `json:"-"`
}

// CanonicalFactReader is the typed, read-only boundary back to canonical Dev
// Health services. ACR chooses what facts it needs; this interface remains the
// authority for values and domain rules.
type CanonicalFactReader interface {
	ReadFacts(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error)
}

// AnswerSynthesizer combines graph context and canonical facts into an
// evidence-closed, answer-capable result. It may use deterministic rules and
// optional models, but it cannot introduce facts absent from the input.
type AnswerSynthesizer interface {
	Synthesize(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error)
}

// InvestigationResultStore persists immutable result snapshots for prior-turn
// binding, replay, Workbench inspection, and future consumer projections.
//
// Binding precondition: Get MUST be scoped to principal.OrgID. It must
// never return a result belonging to a different organization, regardless
// of whether resultID happens to collide, is guessed, or is otherwise
// supplied by a caller outside that organization. ContextFabricInvestigationResult
// carries no organization discriminator of its own (by design -- it is a
// per-principal-scoped read, not a self-describing record), so a Get
// implementation that skips organization scoping is a full cross-tenant
// data leak with nothing downstream positioned to catch the wrong record
// by inspecting it.
//
// This is a binding precondition on implementations, not something Engine
// enforces or can enforce by inspecting the returned value. What Engine
// does provide is a second, independent layer specifically for the
// PriorSubjectReceipts consumer of Get (see resolvePriorSubjectHints):
// every subject a receipt resolves to is re-authorized through
// GraphReader's exact-hint path before it can become a candidate, and that
// re-authorization is unconditionally scoped to the *calling* principal's
// own organization graph (GraphReader.ResolveSubjects's node lookups are
// keyed by principal.OrgID, never by anything read back from the prior
// result). So even a Get implementation that violates this precondition
// and returns another organization's result cannot leak that organization's
// subject into the caller's investigation: GraphReader will look for it in
// the caller's own org graph, where it does not exist, and the receipt is
// silently skipped exactly like any other unresolvable one.
type InvestigationResultStore interface {
	// Save's fourth and fifth parameters are the CHAOS-3782 answer-reuse
	// snapshot-time data -- see SourceWatermarkSnapshot's and
	// RebuildEpoch's doc comments. Every implementation and every test
	// fake must accept both explicitly, even one that ignores them
	// entirely (e.g. memoryinvestigation, which doesn't implement answer
	// reuse): their presence in the signature is deliberate, not
	// incidental, so a caller cannot forget to pass either and silently
	// reproduce answer reuse's own fail-open hazard (Codex round-1
	// findings F1/F2, round-2 finding #7) the way a context.Context value
	// could.
	// Save persists one immutable result. The trailing parameters are the
	// reuse bindings Engine captures itself and threads explicitly --
	// never re-derived here, and never smuggled through ctx: a caller who
	// forgets one must fail to compile.
	//
	// The final string is the REUSE TIME-AXIS KEY (CHAOS-3781), computed
	// by Engine from the CLAMPED EFFECTIVE request context -- the same
	// value, byte for byte, that AnswerReuseGate.FindReusable keys its
	// lookup with. Never re-derived here from result.Interpretation.
	//
	// The invariant is SYMMETRY: both sides must derive the key from a
	// value both sides can compute. The lookup runs before Interpret, so
	// only the request context qualifies -- keying Save on the interpreted
	// context would save under a key no identical request could produce,
	// and that whole class of question would silently never reuse.
	//
	// EFFECTIVE, not the raw wire value: a request whose as-of is clamped
	// means a different instant at different arrival times, so the wire
	// value stops describing what the answer means. See
	// contextfabric.TimeAxisKeyFor for the accepted cost.
	//
	// The key's job is REQUEST identity; interpretation identity is
	// covered separately by condition 6's re-resolution against the
	// stored Interpretation.
	//
	// The ReuseRetrievalIdentity is the deployment-CURRENT pair of
	// CHAOS-3833 retrieval discriminators (see that type's doc comment).
	// Save persists them so FindReusable's conjunctive equality
	// predicates have a stored side to compare against -- an in-memory
	// key field alone would leave every stored row with nothing to match.
	// Explicit parameter for the same reason as the snapshot and epoch:
	// a caller who forgets it must fail to compile, and an implementation
	// must never substitute a value it derived some other way.
	//
	// ReusePromptVersions is the CHAOS-3862 twin of the same idea, one
	// dimension over: the deployment-CURRENT interpretation and synthesis
	// prompt versions, persisted so a reuse lookup can bind to the prompt
	// an already-stored answer was actually produced under, not just the
	// model that produced it. Same explicit-parameter discipline, same
	// reason.
	//
	// The final ReuseVersionAuthorities is the CHAOS-3862 round-2
	// class-close: three MORE deployment-current version authorities
	// (query shape, canonical fact registry, model-output schema) --
	// same explicit-parameter discipline, same reason, same migration.
	//
	// The trailing int64 is graphEpoch (CHAOS-3898 §2.1): the SAME
	// ResolvedGraphBinding.Epoch Engine's own graph reads for this result
	// actually used, threaded explicitly so a stored row can never carry an
	// epoch other than the one its graph reads used. Unlike the reuse
	// snapshot/epoch pair above, this is never nil-able at the call site --
	// GraphReader.ResolveInvestigationBinding is a REQUIRED call Engine
	// makes before any graph read can happen at all, so a fresh result
	// always has a real (possibly 0, the legacy graph) epoch to stamp.
	// Implementations persist it as an additive NULLABLE column regardless
	// (pre-migration rows, and rows from a Store with reuse disabled
	// entirely, read back NULL) -- the same NULL-never-matches fail-closed
	// convention as every sibling reuse dimension.
	Save(context.Context, storage.Principal, InvestigationResult, SourceWatermarkSnapshot, RebuildEpoch, string, ReuseRetrievalIdentity, ReusePromptVersions, ReuseVersionAuthorities, int64) error
	// Get returns the CHAOS-3898 §2.4 metadata-bearing
	// StoredInvestigationResult carrier, not a bare InvestigationResult --
	// see that type's own doc comment for why persistence metadata (here,
	// GraphEpoch) must live on the carrier rather than be lost between
	// storage and the ingress taint gate. The org-scoping binding
	// precondition documented above applies identically to the carrier's
	// wrapped Result.
	Get(context.Context, storage.Principal, string) (StoredInvestigationResult, error)
}

// ErrStructureOfferSuperseded is the typed conflict
// InvestigationResultStore.Save returns when the ATOMIC (org,
// prior_result_id, member) supersession claim for one or more of the
// SAVED result's ConfirmedStructure entries (Disposition=applied,
// Source=receipt) lost its race to a concurrent Save (pivot-intent design
// brief §2.1: "supersession is an ALL-OR-NOTHING CLAIM keyed (org,
// PriorResultID, member), written in the SAME transaction as R2's result
// Save -- two concurrent redemptions of the same (org, R1, member) race on
// the claim row, exactly one transaction wins, and the loser's round
// terminates stale_superseded_offer").
//
// A Save returning this error MUST NOT have persisted the result: per
// §2.5's failure-branch table, "A FAILED Save transaction writes no claim,
// so the failed-round rule... is preserved by construction -- the claim
// exists iff the result that redeemed it exists." Symmetrically here: the
// result must exist iff its claims were won, so a losing Save rolls back
// BOTH the claim attempt and the result insert together.
//
// Engine.Investigate is the only intended caller of errors.As against this
// type -- on a match it discards the computed (but unsaved) decisive
// result and terminates the round stale_superseded_offer instead (the
// SAME veto path canonicalizeStructure's own pre-flight consult reaches),
// rather than surfacing an ordinary persistence failure.
type ErrStructureOfferSuperseded struct {
	// Members lists every ConfirmedStructure member whose claim lost the
	// race, in the order Save attempted them. Always non-empty when this
	// error is returned.
	Members []contractsv1.ContextFabricStructureNeedKind
}

func (e *ErrStructureOfferSuperseded) Error() string {
	return "context fabric: structure offer supersession claim conflict for " + joinStructureNeedKinds(e.Members)
}

// StructureSupersessionChecker is an OPTIONAL capability an
// InvestigationResultStore implementation may additionally provide --
// Engine discovers it via a type assertion on whatever satisfies
// InvestigationResultStore (never a new EngineDependencies field), exactly
// the same discovery shape the rest of this package avoids only where a
// dependency is genuinely required. It is deliberately NOT part of the
// InvestigationResultStore interface itself: adding a required method
// there would force every test double and the in-memory store to
// implement transactional claim bookkeeping just to satisfy the
// interface, when the actual atomicity guarantee already lives entirely
// in Save's own typed ErrStructureOfferSuperseded conflict -- this
// checker only ever shortcuts an otherwise-doomed round BEFORE Engine
// pays for the full investigation pipeline.
//
// IsStructureSuperseded reports whether (orgID, priorResultID, member) has
// ALREADY been claimed by a result other than the one this call would
// redeem it into -- a plain, non-atomic read against the SAME claim table
// Save's atomic insert writes to. A true result, or a non-nil error
// (the read itself could not be trusted), both veto the redemption
// canonicalizeStructure is resolving: per the design brief's
// authority-relevant-read fail-closed rule ("any read this section's
// rules depend on that is UNAVAILABLE at decision time tiers the
// affected member DOWN... never proceed-as-confirmed"), an unreadable
// claim table is treated exactly like a confirmed stale claim, never as
// "assume fresh."
//
// This is an optimization only: the narrow race this check cannot close
// (a concurrent Save claiming the SAME tuple between this read and the
// eventual Save) is closed by Save's own atomic insert regardless of
// whether this method is ever called.
type StructureSupersessionChecker interface {
	IsStructureSuperseded(ctx context.Context, orgID, priorResultID string, member contractsv1.ContextFabricStructureNeedKind) (bool, error)
}

func joinStructureNeedKinds(members []contractsv1.ContextFabricStructureNeedKind) string {
	if len(members) == 0 {
		return "(unspecified member)"
	}
	out := string(members[0])
	for _, m := range members[1:] {
		out += ", " + string(m)
	}
	return out
}

// SourceWatermarkSnapshot is the CHAOS-3782 answer-reuse watermark
// snapshot (TRD §19.7.3 condition 3): the CURRENT backend_watermark of
// every source checkpointed for an organization, keyed by source name,
// captured by Engine itself via SourceWatermarkSnapshotter immediately
// before the graph is read for a fresh investigation -- see that
// interface's doc comment for why the timing matters (Codex round-1
// finding F1: a snapshot taken later, at Save, could describe data
// fresher than what the graph read actually used).
//
// nil (the zero value) means no snapshot was captured -- answer reuse is
// disabled for this Engine, or the snapshot read itself failed. An
// InvestigationResultStore.Save implementation that supports answer
// reuse MUST treat nil as "this result never becomes reusable" and MUST
// NOT substitute a live query of its own as a fallback -- that would
// silently reopen the exact race this type's existence, as an explicit
// parameter rather than a context value, is meant to make impossible to
// forget.
type SourceWatermarkSnapshot map[string]string

// ReuseKey is the CHAOS-3782 answer-reuse lookup key (TRD §19.7.2): the
// canonicalized question hash plus the three version dimensions AC-3782-7
// binds reuse to. The organization is deliberately NOT part of this type --
// every AnswerReuseGate method takes the caller's storage.Principal and
// must scope by principal.OrgID, mirroring InvestigationResultStore's own
// convention (see its doc comment on why Get's org scoping is a binding
// precondition, not optional).
//
// devhealthsource's own producer SourceVersion (bumped v2->v3 by
// CHAOS-3785) is deliberately NOT a fourth dimension here either -- codex
// round-5 raised this as a P1 (API deploys on v3 while an org's checkpoint
// still holds v2, pre-rebuild) and it was investigated, not assumed. During
// that window every already-projected organization's checkpoint carries a
// stale SourceVersion, so ProjectionWorker.RunOnce (projector.go) refuses
// EVERY tick with ErrProjectionSourceVersionChanged BEFORE ever calling
// ProjectionBackend.ApplyProjectionBatch -- the graph backend is
// structurally untouched for that entire window, so a FRESH investigation
// during it reads the exact same pre-rebuild graph a reused answer would;
// reuse adds zero staleness beyond what the un-rebuilt graph already has.
// And the moment the graph actually changes -- the operator-prescribed
// recovery, `acr-projector rebuild --org` (docs/operations.md) ->
// Coordinator.performRebuild -- resetAllCheckpoints
// (projectionrun/coordinator.go) already blanks every source's
// backend_watermark to "" before InvalidateOrganizationReuse runs, so
// condition 3 (watermarksStillMatch, pginvestigation/store.go) fails for
// any pre-rebuild candidate before the epoch fence (condition 4,
// RebuildEpoch's own doc comment) even needs to. Binding SourceVersion
// into this key would be redundant with a fence that already closes the
// window from both ends. See TestAC_3782_4_RebuildBetweenSnapshotAndSaveIsCaughtByEpochNotTimestamp
// / TestAC_3782_4_CompletedRebuildInvalidatesReuseForTheOrganization for
// the binding proof.
//
// ModelIdentities (CHAOS-3786; was the single-valued ModelIdentity) is the
// org's CURRENT effective model CHAIN, primary first and then the fallback
// if one is configured -- never a single static identity. A stored
// candidate matches on this dimension if its OWN identity (the single
// model that actually produced it -- see VersionSet.ModelIdentity) is a
// MEMBER of this chain, not only if it equals the primary. This closes two
// defects a primary-only key had: (a) hit-rate -- a candidate the fallback
// produced never matched a primary-only key, so it could never be reused;
// (b) correctness -- the key was blind to the fallback dimension changing
// at all, so a candidate produced by an OLD fallback stayed reusable
// forever even after the org reconfigured its fallback model, as long as
// the primary was untouched. AnswerReuseGate.FindReusable implements the
// membership test (e.g. `model_identity = ANY(...)`); this type only
// carries the chain to test membership against.
//
// TimeAxisKey is the CHAOS-3781 fifth dimension. Before it, QuestionHash
// -- which hashes the question TEXT only -- was a sound key because every
// stored result was implicitly a current-axis answer: non-current axes
// were refused outright, so no historical answer could ever be stored.
// The moment historical answers became storable, the identical question
// text asked "as of March" and "as of June" collapsed onto ONE key, and a
// June answer would be served for a March question -- a silent wrong
// answer, strictly worse than the refusal CHAOS-3781 removed.
//
// It is deliberately NOT folded into QuestionHash. That hash's contract
// is "the SHA-256 of the canonicalized question text" (see
// CanonicalizeQuestion), and conflating two independent things into one
// opaque digest would destroy the per-condition diagnosability the
// six-condition policy depends on -- a reuse miss must stay attributable
// to a specific condition.
// EmbedRetrievalIdentity and RetrievalPolicyVersion are the CHAOS-3833
// sixth and seventh dimensions (embed-text spec v2 §4, review P1-2
// closure). Both are CONJUNCTIVE equality dimensions, persisted on every
// reuse-participating row (migration 0014), and both are computed from
// the RUNNING binary's configuration at save and lookup time -- which is
// what makes them immune to the deploy->rebuild race an epoch bump
// cannot close: the discriminator changes atomically with deployed
// config.
//
// EmbedRetrievalIdentity is the canonical
// "<provider>/<model>#<composition tag>" of the configured embedder --
// or the literal "none" when no embedder is configured, never the empty
// string. It moves when the embedder, the embed-text composition, or any
// semantic embed config (rune cap, body gate, prefix selector) changes.
// It is deliberately NOT appended into ModelIdentities: that chain's
// members are ALTERNATIVES (`model_identity = ANY(...)`), so an appended
// entry is never actually compared for a row stored under a bare LLM
// identity -- a disjunctive dimension cannot carry a conjunctive
// constraint. It is also NOT composed into ProjectionVersion, whose
// contract is "which source versions built the graph"; folding
// retrieval-config semantics into it would destroy the per-dimension
// diagnosability this type's other doc comments repeatedly name as
// load-bearing.
//
// RetrievalPolicyVersion covers retrieval-policy changes that reinterpret
// EXISTING vectors without re-embedding (tau, K/over-fetch, HNSW
// parameters): no node stamp moves, so stored answers derived under the
// old policy need their own dimension to stop matching. Its own column
// rather than a suffix on the embed identity, so a reuse miss stays
// attributable to policy-vs-embed specifically.
//
// InterpretationPromptVersion and SynthesisPromptVersion are the
// CHAOS-3862 eighth and ninth dimensions. Answer reuse runs BEFORE
// Interpret (Engine.tryReuse, AC-3782-1's zero-model-call guarantee) and a
// hit is served without ever calling Synthesize either -- so a reused
// answer skips BOTH model steps, not just interpretation. Before this
// fix, a prompt bump (e.g. interpretation v6->v7) left every already-
// stored answer for the identical question fully reusable for up to the
// configured staleness window: nothing in the key changed, because the
// prompt text a stored answer was PRODUCED under was never part of it,
// even though ModelExecutionReceipt.PromptVersion already recorded it on
// every fresh execution. Two dedicated columns, not one and not folded
// into ModelIdentities, for the same per-dimension-diagnosability reason
// EmbedRetrievalIdentity/RetrievalPolicyVersion are two columns rather
// than one: a prompt-version miss must stay attributable to
// interpretation vs. synthesis specifically, and ModelIdentities'
// membership test is disjunctive (ALTERNATIVES a stored row's identity
// must be ONE of), which cannot carry a second, unrelated conjunctive
// constraint -- see EmbedRetrievalIdentity's doc comment immediately
// above for the identical reasoning applied to a different dimension.
//
// Both are computed from the RUNNING binary's configuration (genkitruntime's
// own prompt-version defaulting -- see
// genkitruntime.DefaultInterpretationPromptVersion/
// DefaultSynthesisPromptVersion) at save and lookup time, exactly like
// EmbedRetrievalIdentity/RetrievalPolicyVersion: the discriminator changes
// atomically with a deploy, no rebuild required, so it closes the same
// deploy->cutover gap for a prompt bump that 0014 closed for an embed-text
// change.
//
// QueryVersion, CanonicalServiceVersion, and ModelOutputSchemaVersion are
// the CHAOS-3862 round-2 tenth through twelfth dimensions -- sol review's
// class-close on "version authority missing from ReuseKey," a defect
// class that had by then hit this codebase three times independently
// (CHAOS-3833/3834's embed retrieval identity, this ticket's own round-1
// prompt versions, and this round). See reuse_key_completeness_test.go
// for the durable, reflection-based close: it enumerates every
// version-shaped field VersionSet and ModelExecutionReceipt carry and
// requires each to be either a ReuseKey member or an explicit,
// reasoned exclusion, so a FUTURE version authority cannot be added to
// either struct without a red test naming it.
//
// QueryVersion is devhealthfacts.QueryVersion -- which ClickHouse SQL/
// column shape produced the canonical facts a stored answer was built
// from (already stamped into every fresh result's
// Versions.QueryVersion, hosted/open.go's contextFabricSynthesizerOptions
// -- it was simply never a reuse discriminator).
//
// CanonicalServiceVersion is contextfabric.CanonicalFactRegistryVersion --
// which canonical-fact-registry contract version composed the facts.
//
// ModelOutputSchemaVersion is genkitruntime.DefaultSchemaVersion -- the
// genkit model-output JSON Schema version. ONE dimension, not two: the
// SAME value governs both the interpretation and synthesis output
// shapes (genkitruntime.Config carries a single SchemaVersion field, not
// a per-operation pair) -- see VersionSet.InterpretationVersion's
// classification in reuse_key_completeness_test.go for why that
// confusingly-named field is what this dimension actually binds.
//
// All three follow the identical NULL/fail-closed persistence and
// conjunctive-equality-predicate rules the prompt-version pair above
// does (migration 0015, extended in this round rather than a new
// migration -- same mechanism, same rows).
type ReuseKey struct {
	QuestionHash                string
	ContractVersion             string
	ProjectionVersion           string
	ModelIdentities             []string
	TimeAxisKey                 string
	EmbedRetrievalIdentity      string
	RetrievalPolicyVersion      string
	InterpretationPromptVersion string
	SynthesisPromptVersion      string
	QueryVersion                string
	CanonicalServiceVersion     string
	ModelOutputSchemaVersion    string
	// IdentityNormalizationVersion (CHAOS-3884) is graphrank's
	// IdentityNormalizationVersion constant ("identity_norm_v1" for slice
	// 1: TrimSpace+ToLower, no NFC -- see NormalizeAliasTerm's own doc
	// comment for why deferring NFC is judged safe only BECAUSE this
	// dimension exists). A conjunctive predicate exactly like the eleven
	// above: a future tightening (e.g. adding NFC) changes what two
	// differently-cased/differently-composed aliases normalize to, which
	// changes the identity fast path's own commit decision -- a stored
	// answer produced under the OLD normalization must not be silently
	// replayed as if produced under the new one. Same NULL-never-matches
	// fail-closed discipline as every other dimension here.
	IdentityNormalizationVersion string
	// WindowInferenceVersion (CHAOS-3900 W1) is a FOURTEENTH conjunctive
	// dimension, same NULL-never-matches fail-closed shape as
	// IdentityNormalizationVersion immediately above -- the precedent this
	// one follows exactly. It binds reuse to the window class vocabulary,
	// the class-to-default table (windowDefaultPolicy, window.go), and the
	// binder's own post-pass rules: a future tightening of any of those
	// changes what INFERRED window an otherwise-identical question would
	// receive, which changes what the answer actually covers, so a stored
	// answer produced under the OLD rules must not be silently replayed
	// under the new ones.
	//
	// This dimension is APPLIED UNCONDITIONALLY to every reuse-participating
	// row, exactly like every sibling in this bundle -- it is NOT
	// conditionally omitted for a stated/confirmed-window row (codex
	// review, W1 round 1: an earlier draft of this comment claimed a
	// scoping this implementation does not actually enforce). For a
	// stated/confirmed row the dimension is REDUNDANT with TimeAxisKey's
	// own rel:/abs: fragment (windowKeyComponent, window.go) -- see
	// TimeAxisKeyFor's doc comment -- which already fully disambiguates
	// it, so a deploy that only changes inference rules will also
	// needlessly cold-cache stated/confirmed rows. That is an accepted,
	// SAFE cost (over-invalidation, never a wrong answer or a false hit),
	// identical in shape to every other conjunctive dimension's own
	// fail-closed convention -- true per-row scoping (populating this
	// field only when the window that actually rode the answer was
	// inferred) would require a dynamic, per-Save value rather than one
	// deployment-current constant, and is not implemented in W1.
	WindowInferenceVersion string
	// CommitGateVersion (CHAOS-4085) is a FIFTEENTH conjunctive dimension,
	// same NULL-never-matches fail-closed shape as every sibling above. It
	// binds reuse to contextfabric.CommitGateVersion -- the rules deciding
	// WHICH SUBJECTS AN ANSWER MAY COMMIT TO: the resolution-time refusal
	// of the vector-margin rescue for a tied top under a truncated search,
	// and the post-synthesis affirmation gate that retracts a
	// statistically-committed subject the answer does not support.
	//
	// This is the ONE dimension in this bundle whose absence is a SAFETY
	// hole rather than a staleness one. tryReuse runs BEFORE Interpret and
	// BEFORE synthesis (engine.go), so a hit serves the stored result
	// verbatim -- Committed list included -- without the new gate ever
	// executing. Without this dimension, the exact wrong commit CHAOS-4085
	// refuses would continue to be served from cache for every repeat of
	// the same question inside the staleness window, and no amount of
	// engine-side gating would stop it. Bump the constant whenever the
	// commit gate's own decision rules change.
	//
	// Over-invalidation is the accepted, safe cost, identical in shape to
	// WindowInferenceVersion's own note above: a bump cold-caches every
	// reuse-participating row for the org, including rows whose commits the
	// new rules would have reached identically.
	CommitGateVersion string
	// RankingFormulaVersion (CHAOS-4398 PR3, R4 ruling) is a SIXTEENTH
	// conjunctive dimension, same NULL-never-matches fail-closed shape as
	// every sibling above. It binds reuse to
	// contextfabric.RankingFormulaVersion -- the cohort ranking formula
	// (cohort_ranking.go): the signal weights, the Score-nil threshold,
	// and everything else RankCohort computes a member's Score/
	// RankingBasis/Outcome/MissingSignals from.
	//
	// Why reuse MUST be fenced on it, rather than left to age out.
	// RankCohort runs AFTER FindReusable in the request path (engine.go):
	// a hit serves the stored InvestigationResult verbatim, ranking table
	// and all, without RankCohort ever re-running. v2 of the formula
	// (CHAOS-4398 PR3, design doc §8) changed what Score/Outcome even
	// MEANS for the same signal weights -- a member the OLD formula
	// scored now maps to insufficient_evidence/not_applicable with no
	// Score at all, or vice versa. Without this dimension, a cohort
	// question answered before a formula bump would keep being served
	// from cache, under the OLD formula's semantics, for every repeat
	// inside the staleness window -- exactly the class of bug
	// WindowInferenceVersion and CommitGateVersion each closed for their
	// own decision. Bump the constant whenever RankCohort's weights,
	// thresholds, or signal set change.
	//
	// Applied UNCONDITIONALLY to every reuse-participating row, exactly
	// like WindowInferenceVersion's own precedent -- NOT scoped to only
	// cohort-shaped questions. A non-cohort row never exercises
	// RankCohort at all, so this dimension is redundant for it; over-
	// invalidating a non-cohort row on an unrelated formula bump is the
	// same accepted, safe cost (never a wrong answer or a false hit) the
	// precedent already establishes, and true per-row scoping would need
	// a dynamic, per-Save value rather than one deployment-current
	// constant.
	RankingFormulaVersion string
	// QuestionFamilyVersion (CHAOS-4634 S4, per the CHAOS-4632/S2 note
	// deferred here) is a NINTH conjunctive dimension, same NULL-never-
	// matches fail-closed shape as every sibling above. It binds reuse to
	// contextfabric.QuestionFamilyTableVersion -- the family definition
	// table (chaos4632_question_family_registry.go): ApplicableAxes,
	// AskOrder, RequireDrivers, RequireRanking, RenderKinds, Budget, and
	// the precedence table that resolves a family in the first place.
	//
	// Why reuse MUST be fenced on it, rather than left to age out. S4 is
	// the FIRST slice where the family affects an answer at all --
	// GateOffersByFamily (chaos4579_cohort_structure_gate.go) reads
	// ApplicableAxes to decide which structure_needs axes a turn-1
	// disclosure carries. tryReuse runs BEFORE Interpret (engine.go), so a
	// hit serves the stored StructureNeeds verbatim, without the family
	// gate ever re-running. A table edit that widens or narrows
	// ApplicableAxes for a family (S2's own doc comment states the bump
	// rule) must not keep serving a pre-edit disclosure from cache for the
	// identical question -- exactly the class WindowInferenceVersion and
	// CommitGateVersion each closed for their own decision.
	//
	// Deliberately NOT scoped to only questions that resolved a
	// non-unclassified family: unclassified's own ApplicableAxes
	// (allStructureNeedKinds()) is itself a row in the SAME table, so a
	// table edit could in principle change what "unclassified" discloses
	// too. Applied unconditionally, same precedent as RankingFormulaVersion
	// immediately above.
	QuestionFamilyVersion string
	// GraphEpoch (CHAOS-3898 §2.3) is this investigation's own
	// ResolvedGraphBinding.Epoch -- a THIRTEENTH conjunctive dimension,
	// structurally distinct from every version-string dimension above: a
	// stored candidate matches only if it was generated under the SAME
	// active graph epoch this lookup is running under. Unlike
	// RebuildEpoch/invalidation_epoch (a coarse "has this org been rebuilt
	// since" fence bumped only by an explicit invalidation), GraphEpoch
	// moves on every ordinary build/flip -- see ResolvedGraphBinding's own
	// doc comment for why the two must never be conflated. A row saved
	// under a different (or NULL, pre-migration) epoch never matches; see
	// FindReusable's own doc comment for the METADATA-ONLY classifier that
	// distinguishes that specific miss (ReuseMissStaleGraphEpoch) from an
	// ordinary no-candidate miss.
	GraphEpoch int64
}

// ReuseRetrievalIdentity carries the deployment-CURRENT values of the two
// CHAOS-3833 retrieval discriminators, computed once at composition time
// from the running binary's configuration. Engine uses the SAME value on
// both sides -- inside the ReuseKey a lookup builds AND as Save's
// explicit parameter -- so the persisted column and the compared
// predicate can never drift apart within one process. Either field left
// empty means "this deployment does not participate in retrieval-keyed
// reuse": Save persists SQL NULL (the row never becomes reusable under
// the conjunctive predicates) and FindReusable reports an ordinary miss.
type ReuseRetrievalIdentity struct {
	// EmbedRetrievalIdentity is "<provider>/<model>#<composition tag>",
	// or the literal "none" when no embedder is configured.
	EmbedRetrievalIdentity string
	// RetrievalPolicyVersion is the manually bumped retrieval-policy
	// constant (e.g. "rp1") -- see falkorgraph.RetrievalPolicyVersion.
	RetrievalPolicyVersion string
}

// ReusePromptVersions carries the deployment-CURRENT interpretation and
// synthesis prompt versions (CHAOS-3862), computed once at composition
// time from the SAME genkitruntime defaulting InterpretQuestion and
// SynthesizeAnswer actually use (genkitruntime.DefaultInterpretationPromptVersion/
// DefaultSynthesisPromptVersion, or a Config override if one is ever
// wired). Engine uses the SAME value on both sides -- inside the ReuseKey
// a lookup builds AND as Save's explicit parameter -- so the persisted
// columns and the compared predicates can never drift apart within one
// process, mirroring ReuseRetrievalIdentity's contract exactly. Either
// field left empty means "this deployment does not participate in that
// prompt-version-keyed dimension of reuse": Save persists SQL NULL and
// FindReusable reports an ordinary miss on it -- fail-closed, never a
// lookup that silently ignores the dimension.
type ReusePromptVersions struct {
	InterpretationPromptVersion string
	SynthesisPromptVersion      string
}

// ReuseVersionAuthorities carries the deployment-CURRENT values of three
// MORE version authorities (CHAOS-3862 round 2, sol review's class-close --
// see ReuseKey's doc comment for what each one binds and why). Computed
// once at composition time from the SAME constants the corresponding
// production code paths already use (devhealthfacts.QueryVersion,
// contextfabric.CanonicalFactRegistryVersion,
// genkitruntime.DefaultSchemaVersion), so they cannot drift from what a
// fresh result would actually stamp. Engine uses the SAME value on both
// sides -- inside the ReuseKey a lookup builds AND as Save's explicit
// parameter -- mirroring ReuseRetrievalIdentity/ReusePromptVersions
// exactly. Any field left empty means "this deployment does not
// participate in that dimension of reuse": Save persists SQL NULL and
// FindReusable reports an ordinary miss on it -- fail-closed, never a
// lookup that silently ignores the dimension.
type ReuseVersionAuthorities struct {
	QueryVersion             string
	CanonicalServiceVersion  string
	ModelOutputSchemaVersion string
	// IdentityNormalizationVersion (CHAOS-3884) is graphrank.
	// IdentityNormalizationVersion -- see ReuseKey's own field doc comment
	// for the full reasoning. Composed here (the deployment-current
	// authorities bundle) rather than a fifth standalone struct, since it
	// is the identical "one more version constant, same fail-closed
	// discipline" shape as the three siblings above.
	IdentityNormalizationVersion string
	// WindowInferenceVersion (CHAOS-3900 W1) is ONE MORE version constant,
	// same shape again -- see ReuseKey.WindowInferenceVersion's own field
	// doc comment for what it binds and, importantly, for why it is
	// applied UNCONDITIONALLY to every reuse-participating row (codex
	// review, W1 round 2: an earlier version of this comment claimed a
	// per-row inferred-only scoping this bundle -- a single deployment-
	// current constant threaded identically to every sibling field here --
	// cannot express).
	WindowInferenceVersion string
	// CommitGateVersion (CHAOS-4085) is ONE MORE version constant, same
	// shape again -- see ReuseKey.CommitGateVersion's own field doc comment
	// for what it binds and why its absence is a safety hole rather than a
	// staleness one.
	CommitGateVersion string
	// RankingFormulaVersion (CHAOS-4398 PR3, R4 ruling) is ONE MORE
	// version constant, same shape again -- see
	// ReuseKey.RankingFormulaVersion's own field doc comment for what it
	// binds and why it is applied unconditionally to every reuse-
	// participating row, not only cohort-shaped ones.
	RankingFormulaVersion string
	// QuestionFamilyVersion (CHAOS-4634 S4) is ONE MORE version constant,
	// same shape again -- see ReuseKey.QuestionFamilyVersion's own field
	// doc comment for what it binds (contextfabric.QuestionFamilyTableVersion)
	// and why it is applied unconditionally to every reuse-participating
	// row.
	QuestionFamilyVersion string
}

// AnswerReuseGate finds a stored InvestigationResult eligible for reuse
// under the TRD §19.7.3 watermark-bound staleness policy -- but only FIVE
// of its six conditions: the lookup itself proves 1 (question hash), 2
// (organization), 5, and 7 (contract/projection/model identity all
// match); the implementation independently proves 3 (every configured
// projection source's backend_watermark is unchanged since the candidate
// was generated) and 4 (the candidate is inside the staleness window AND
// was generated after the organization's most recent rebuild
// invalidation, if any).
//
// It deliberately does NOT check condition 6 (current authorization for
// every subject and evidence reference in the stored result) -- that
// recheck needs GraphReader, which lives in Engine's composition, not
// here, so Engine performs it itself immediately before serving whatever
// this returns. ok=false is an ordinary cache miss (no matching row, or a
// matching row failed 3/4), never an error: a caller must always be able
// to fall back to running a fresh investigation.
//
// The returned ReuseMissReason (CHAOS-3898 v4.1 F5) is meaningful only when
// ok=false: ReuseMissNoCandidate is the ordinary miss; ReuseMissStaleGraphEpoch
// means a row matched every OTHER dimension but was generated under a
// different graph_epoch, proof a build/flip -- not staleness -- is why it
// does not apply. Implementations MUST classify this without ever
// selecting or returning payload bytes (or any stored label/id field) on
// the classification path -- metadata columns only (design brief §5's
// SQL-predicate pin).
type AnswerReuseGate interface {
	FindReusable(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, ReuseMissReason, error)
}

// ReuseInvalidator is notified when an organization's projected graph
// state has just been rebuilt from scratch (CHAOS-3782, TRD §19.7.3's
// "known hazard, drift item D15": a rebuild's backend_watermark is not
// guaranteed to differ from what it purged, so watermark equality alone
// cannot prove a stored result survived a rebuild). Implementations must
// record the invalidation as a separate, mutable fact -- never by
// rewriting a row in the immutable investigation-results table -- so that
// InvalidateOrganizationReuse(orgID) followed immediately by
// AnswerReuseGate.FindReusable(orgID, ...) never returns a result
// generated before the call to InvalidateOrganizationReuse.
type ReuseInvalidator interface {
	InvalidateOrganizationReuse(ctx context.Context, orgID string) error
}

// SourceWatermarkSnapshotter reads the CURRENT backend_watermark of every
// source checkpointed for an organization, keyed by source name
// (CHAOS-3782, Codex round-1 finding F1). Engine calls this itself, once,
// at (or immediately before) the graph read for a FRESH investigation --
// never at Save time, which is too late: a projection could advance
// between the graph read and Save, and a snapshot taken then would
// describe data possibly fresher than what the graph read actually used,
// silently letting a later identical question reuse a stale answer under
// a watermark that looks unchanged. The captured snapshot is passed
// explicitly to Save as its SourceWatermarkSnapshot parameter -- Save
// implementations must NOT take their own later snapshot as a substitute
// when Engine passes nil.
type SourceWatermarkSnapshotter interface {
	SnapshotSourceWatermarks(ctx context.Context, orgID string) (SourceWatermarkSnapshot, error)
}

// RebuildEpoch is the CHAOS-3782 rebuild-invalidation epoch (Codex
// round-2 finding #7): the organization-scoped monotonic counter
// RebuildEpochSnapshotter reads and ReuseInvalidator.
// InvalidateOrganizationReuse alone bumps, captured by Engine at the SAME
// moment as SourceWatermarkSnapshot -- immediately before the graph is
// read for a fresh investigation -- and passed to Save as its own
// explicit parameter, alongside the watermark snapshot.
//
// nil (the alias's zero value) means "no epoch was captured" -- reuse
// disabled for this Engine, or the snapshot read failed -- exactly
// mirroring SourceWatermarkSnapshot's own nil convention, and for the
// same reason: a Save implementation must treat nil as "this result
// never becomes reusable" and must never substitute a later, live query
// of its own as a fallback for either field independently. This is a
// type ALIAS for *int64 (not a defined type), so it composes with plain
// pointer values and literals at every call site without conversion --
// the name exists purely for signature readability.
//
// Why this exists (the race a timestamp-only comparison cannot close):
// condition 4b -- "generated after the organization's most recent
// rebuild invalidation, if any" -- was originally checked by comparing a
// candidate row's OWN created_at (DB clock_timestamp() at INSERT) against
// invalidated_at. That comparison is blind to WHEN the investigation's
// underlying graph read actually happened relative to the invalidation:
// an investigation whose graph read began before (or raced) a rebuild's
// InvalidateOrganizationReuse call, but whose Save happened to land AFTER
// it purely due to processing time, would still show created_at >
// invalidated_at and pass the old check -- serving an answer built from
// stale/mid-rebuild data as if it reflected the post-rebuild state.
// Binding to a captured-at-snapshot-time epoch instead closes this: a
// candidate is reuse-eligible only if the epoch it captured still equals
// the organization's CURRENT epoch at lookup time, which is true if and
// only if NO invalidation occurred anywhere in [snapshot-capture,
// lookup] -- a strictly larger, and therefore safe, superset of
// [snapshot-capture, this row's own created_at].
type RebuildEpoch = *int64

// RebuildEpochSnapshotter reads the CURRENT rebuild-invalidation epoch
// for an organization (CHAOS-3782, Codex round-2 finding #7) -- see
// RebuildEpoch's doc comment for what the value means and why Engine
// must capture it at the same moment as the watermark snapshot, never
// later. Implementations must read the SAME counter
// ReuseInvalidator.InvalidateOrganizationReuse bumps for this
// organization; an organization never invalidated reads as epoch 0 (the
// baseline every fresh organization starts at, matching "no
// invalidations table row exists yet").
type RebuildEpochSnapshotter interface {
	SnapshotRebuildEpoch(ctx context.Context, orgID string) (int64, error)
}

// ReuseModelIdentityResolver resolves the CURRENT org-effective model
// CHAIN a reuse lookup's ReuseKey.ModelIdentities must use (CHAOS-3782,
// Codex round-2 finding #3; widened from a single identity to a chain by
// CHAOS-3786). Engine calls this itself, explicitly, from tryReuse --
// BEFORE building the ReuseKey and calling AnswerReuseGate.FindReusable --
// never a value captured once at engine-construction time and reused for
// every organization thereafter.
//
// Why a static identity is wrong (the bug this closes): a LOOKUP built
// from a single static identity fixed at startup never changes when an
// organization's own configuration does -- so after an organization
// reconfigures its model (or is configured for the first time, diverging
// from the deployment default), tryReuse keeps querying for the OLD
// identity, which still matches the row saved before the change, and
// keeps serving it. Condition 7 (model identity match) exists specifically
// to invalidate reuse across a model change; a static lookup key defeats
// it silently for every organization whose effective identity differs
// from the deployment default, without ever producing an error or a miss
// that would surface the problem.
//
// Why a CHAIN, not one identity (CHAOS-3786): a fresh investigation's
// SAVED Versions.ModelIdentity names whichever ONE model actually answered
// it (RuntimeAnswerSynthesizer derives it from the execution receipt --
// see genkitruntime.mergeFallbackReceipt, fixed by CHAOS-3786 to carry the
// FALLBACK leg's own Provider/Model/ModelVersion when the fallback is what
// produced the result, not the primary's). Which of primary or fallback
// will answer a given fresh call is unknowable ahead of the call -- §19.3.4
// records the fallback answering often, not as a rare edge case -- so a
// lookup that only ever tries the primary's identity can never match a
// fallback-produced candidate. Implementations must therefore return the
// FULL current chain (primary, then fallback if one is configured) and let
// AnswerReuseGate.FindReusable test chain MEMBERSHIP (the stored
// candidate's own single identity must be an element of this slice), not
// equality against one value.
//
// NOTE on rows saved before this fix: a fallback-produced investigation
// saved before CHAOS-3786 shipped is persisted under the PRIMARY's
// identity (the genkitruntime bug this fix also closes), not the
// fallback's -- and, being an immutable row, can never be retroactively
// relabeled. Left alone, such a row would keep matching whenever the
// primary is in the current chain, indistinguishable from a row the
// primary genuinely produced. It is NOT left alone: migration 0012 is a
// one-time cutover that bumps every existing organization's
// reuse-invalidation epoch exactly once, at deploy, which quarantines
// every row saved before that migration ran via the same epoch mechanism
// a projection rebuild uses (see RebuildEpoch's doc comment) -- no
// payload touched, no manual per-organization action required. A FRESH
// investigation saved after the cutover captures the CURRENT (bumped)
// epoch as its own invalidation_epoch, so it is unaffected going forward.
// An operator who additionally wants to invalidate a SPECIFIC
// organization at any later point (e.g. after a configuration change --
// see internal/api/context_fabric_model_config_routes.go's PUT/DELETE
// handlers, which already do this automatically on every write) can call
// ReuseInvalidator.InvalidateOrganizationReuse directly.
//
// Implementations MUST resolve the same organization-effective chain that
// would actually be tried for a fresh investigation for orgID right now
// (mirroring modelruntimeresolver.Resolver's own config-then-default
// fallthrough, including its FallbackModel) so the lookup key can never
// diverge from what Save would have written for an equivalent fresh call.
// Returning an error means the chain could not be determined for this
// organization right now (e.g. its stored BYO configuration exists but
// fails to decrypt) -- tryReuse treats that exactly like "no candidate
// found" and falls through to a fresh investigation; it must never fall
// back to a different, possibly wrong chain as a substitute.
type ReuseModelIdentityResolver interface {
	ResolveReuseModelIdentity(ctx context.Context, orgID string) ([]string, error)
}

// ProjectionBackend is the write-side graph/index boundary. Applying a batch
// must be idempotent for the same batch ID and source version.
//
// Precondition: callers must serialize ApplyProjectionBatch calls per
// organization. Not every implementation of this interface can be assumed
// to have a real attribute-level compare-and-swap against concurrent
// writers touching the same subject: an implementation without one that
// reads a node's canonical attributes, merges in the new projection's
// attributes, and writes the merge back has no protection against a
// second, concurrent projection interleaving between that read and write
// for the same org, and two sources projecting the same subject at the
// same time can lose one side's canonical metadata (aliases, provider
// IDs) to a stale merge that overwrites it. A CHAOS-3753 projection worker
// MUST serialize batches per organization (e.g. one in-flight
// ApplyProjectionBatch call per org at a time); it must not run multiple
// sources' batches for the same org concurrently. This is a caller
// obligation ProjectionBackend itself cannot enforce -- and is required
// regardless of whether the current implementation's own merge happens to
// be atomic (ADR 0009): other batch-level ordering guarantees still
// depend on the per-organization serialization, not just attribute-merge
// atomicity.
type ProjectionBackend interface {
	ApplyProjectionBatch(context.Context, ProjectionBatch) (ProjectionReceipt, error)
	// ProjectionWatermark reads the durable per-(org, source) sentinel this
	// backend wrote at projection time. Implementations MUST report a
	// confirmed absence -- no such sentinel exists, whether never projected
	// or since lost -- by returning an error satisfying
	// errors.Is(err, ErrProjectionWatermarkNotFound) (CHAOS-3882); any other
	// error must NOT satisfy that check, so a caller like
	// projectionrun.Coordinator can distinguish "definitely absent" from
	// "could not tell" without importing this backend's own package.
	ProjectionWatermark(context.Context, string, string) (ProjectionWatermark, error)
	PurgeOrganization(context.Context, string) error
}

// EmbedderIdentity names WHICH embedder produced a vector, and at what
// dimension (CHAOS-3778 / AC-3778-7). It is recorded alongside every stored
// vector and compared before any vector index is queried, so that changing the
// embedder or its dimension is DETECTABLE rather than silently producing
// nonsense similarities against vectors from a different model.
//
// Provider and Model are recorded verbatim from configuration and never
// interpreted -- no code path checks them for a specific vendor.
type EmbedderIdentity struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Dimension int    `json:"dimension"`
}

// String renders the identity in the stable form written onto graph nodes.
// Deliberately excludes Dimension, which is stored as its own typed property
// so a mismatch can be detected numerically rather than by string comparison.
func (i EmbedderIdentity) String() string {
	return i.Provider + "/" + i.Model
}

// Embedder is the ACR-owned embedding port (TRD §19.4.7: "the embedder is an
// ACR-owned port; the vendor client stays inside the adapter"). Note this is a
// NEW port, not a change to GraphReader -- §19.4's "no port change is
// expected" refers to GraphReader, whose signature CHAOS-3778 leaves alone.
//
// Embed returns one vector per input text, in the SAME ORDER as the input.
// Implementations must reject a response that does not preserve order or
// count, rather than silently mis-pairing a vector with the wrong text -- a
// mis-paired vector is not a degraded result, it is a wrong one, and nothing
// downstream could detect it.
//
// Embed is called with a BATCH at projection time (one call per projection
// batch, not one per node) and with a single text on the read path (the
// question). Implementations must be safe for concurrent use.
//
// Identity is separate from Embed because it is configuration-derived and is
// needed at index-bootstrap and projection time WITHOUT making a model call.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Identity() EmbedderIdentity
}

// ProjectionSource reads canonical projection batches from a bounded adapter,
// such as a Dev Health outbox or typed canonical read service.
type ProjectionSource interface {
	NextProjectionBatch(context.Context, ProjectionCheckpoint) (ProjectionBatch, bool, error)
}

// ProjectionSourceEnablement is an OPTIONAL capability (CHAOS-3898 S2a-2,
// design brief §3.3/item 5's BuildCompletionDisabledAtFreeze) a
// ProjectionSource may implement to report whether it is currently
// operator-disabled by configuration (e.g. TeamsProjectsSource's own
// `enabled` field, ACR_CONTEXT_FABRIC_PROJECT_TEAMS_PROJECTS_ENABLED) --
// distinct from a source that is enabled but genuinely has zero rows for an
// organization. Without this, a build-completion classifier cannot tell
// "disabled at freeze" apart from "empty" for a required source that always
// reports available=false either way; both are terminal completion modes
// (the flip gate does not care which), but only this capability lets the
// §5b cf_build_source_progress signal name the difference precisely for an
// operator. A source that does not implement this capability is never
// treated as disabled -- only as possibly empty.
type ProjectionSourceEnablement interface {
	Enabled() bool
}

// ProjectionSourceVersion is an OPTIONAL capability (CHAOS-3887) a
// ProjectionSource may implement to report its current producer
// SourceVersion even when NextProjectionBatch found nothing to build a
// batch from (available=false). Without this, "current" SourceVersion is
// only ever visible on a built ProjectionBatch -- which a dormant
// organization (no new rows since its last checkpoint) never produces, so
// the freshness guard in ProjectionWorker.RunOnce (and, downstream, the
// per-tick freshness telemetry projectionrun.Coordinator emits) has no
// baseline to compare a stale checkpoint against for exactly the
// organizations most likely to be silently stale.
//
// Telemetry-only: nothing in RunOnce's accept/refuse decision depends on
// whether a source implements this.
type ProjectionSourceVersion interface {
	CurrentProjectionSourceVersion() string
}

// ProjectionProgress is an OPTIONAL capability a ProjectionSource may
// implement (CHAOS-3802). It exists for one narrow, real situation: a source
// can consume rows that are provably unpublishable -- today, ownership rows
// omitted because their project_key resolves to more than one project -- and
// those rows still occupy cursor space.
//
// Without this, such rows are a permanent stall rather than a slow patch.
// A payload-free ContextFabricProjectionBatch cannot carry their cursor
// (Validate rejects an empty batch outright), so a source that finds only
// unpublishable rows must answer available=false; the worker then reads
// "caught up", the DURABLE checkpoint never moves, and every later tick
// replays the same prefix forever, leaving publishable rows beyond the block
// unreachable. Skipping them inside one NextProjectionBatch call only defers
// the wall: whatever bound that skipping uses, an organization can exceed it.
//
// Implementations report a cursor covering ONLY rows they proved carry
// nothing publishable. ok=false means "no progress to record" and must be the
// answer whenever a source is genuinely caught up, so an idle tick never
// writes.
//
// Safety (the CHAOS-3778 rule that a watermark must never advance past
// unreconciled publishable state): the worker persists this progress with the
// BackendWatermark and SourceVersion UNCHANGED. Only the source-side cursor
// moves, and its meaning is "rows consumed", never "rows applied". Nothing
// publishable is skipped, because a page qualifies only when it produced no
// publishable candidate at all -- so there is no unreconciled state in the
// range being passed over.
type ProjectionProgress interface {
	ConsumedWithoutPublishing(context.Context, ProjectionCheckpoint) (ConsumedProgress, bool, error)
}

// ConsumedProgress is what a source reports about rows it consumed without
// publishing. SourceVersion is not optional decoration: progress MUST be bound
// to the producer identity that derived it (CHAOS-3802 codex round-4 F1).
//
// Without that binding, progress persisted after a source-version bump
// advances the durable cursor under the PRIOR version -- and rows the NEW
// version would publish (a relaxed join, a corrected id space; exactly what
// past version bumps in this repository did) are then unreachable behind the
// advanced cursor, silently, with no rebuild triggered. Reporting the version
// lets the worker apply the same rule the batch path already applies: a
// mismatch against a non-empty stored version is
// ErrProjectionSourceVersionChanged and forces a rebuild, never a quiet
// advance.
type ConsumedProgress struct {
	NextCursor    string
	SourceVersion string
}

// ProjectionCheckpointStore advances only after the backend has durably
// accepted a batch. CompareAndSwapProjectionCheckpoint must return
// ErrProjectionConflict when expected no longer matches the durable cursor, so
// concurrent projectors cannot silently skip or reorder batches.
type ProjectionCheckpointStore interface {
	LoadProjectionCheckpoint(context.Context, string, string) (ProjectionCheckpoint, error)
	CompareAndSwapProjectionCheckpoint(context.Context, ProjectionCheckpoint, ProjectionCheckpoint) error
}
