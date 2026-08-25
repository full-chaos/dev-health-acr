package contextfabric

import (
	"context"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// StructureOfferPhrasingOption is one offered option's PRESENTATION-FACING
// input to the phrasing model call -- option_id, member kind and the
// structural label already minted by composeStructureNeeds. This is
// EVERYTHING the model receives about an offer: no evidence, no subject
// resolution, no canonical IDs beyond what Label already discloses --
// "input is the offer set only" per the ratified design (CHAOS-4171,
// 2026-08-24 15:30 PDT ruling).
type StructureOfferPhrasingOption struct {
	OptionID string
	Member   contractsv1.ContextFabricStructureNeedKind
	Kind     contractsv1.ContextFabricSubjectKind
	Label    string
}

// StructureOfferPhrasingInput is the SECOND bounded model call's own
// request shape -- composed strictly AFTER composeStructureNeeds /
// composeGatedStructureNeeds, from their output, never from anything
// Interpret saw.
type StructureOfferPhrasingInput struct {
	Options []StructureOfferPhrasingOption
}

// StructureOfferPhrasingEntry is one raw, UNGUARDED phrasing the model
// proposed for one option_id. classifyOfferPhrasingDraft is the only
// consumer that checks OptionID against the input's own offer set -- never
// treat a value read off this type as membership-verified before that
// check runs.
type StructureOfferPhrasingEntry struct {
	OptionID string `json:"option_id"`
	Phrasing string `json:"phrasing"`
}

// StructureOfferPhrasingDraft is the phrasing model's raw output --
// UNGUARDED: nothing here has been checked against the offered option set
// yet. See classifyOfferPhrasingDraft for the guard.
type StructureOfferPhrasingDraft struct {
	Phrasings []StructureOfferPhrasingEntry
}

// OfferPhrasingModelRuntime is the raw, mechanical model-call boundary for
// offer phrasing -- genkitruntime.Runtime's own additional method,
// mirroring ModelRuntime.InterpretQuestion/SynthesizeAnswer's shape
// exactly but declared as ITS OWN interface rather than a third
// ModelRuntime method: extending ModelRuntime would force every
// ModelRuntime implementation and test double in this repository
// (including modelruntimeresolver.Resolver) to grow a method most have no
// use for. A caller obtains one by type-asserting the deployment's
// contextfabric.ModelRuntime against this interface at composition time
// (internal/runtime/hosted) -- an implementation that does not satisfy it
// simply leaves offer phrasing nil, the same "optional dependency absent"
// contract every other EngineDependencies field uses.
type OfferPhrasingModelRuntime interface {
	PhraseStructureOffers(ctx context.Context, principal storage.Principal, input StructureOfferPhrasingInput) (StructureOfferPhrasingDraft, ModelExecutionReceipt, error)
}

// OfferPhrasingOutcome is the closed vocabulary EngineTelemetry.
// RecordOfferPhrasing reports (CHAOS-4171 PR2 ratified telemetry names,
// 2026-08-24 22:05 PDT ruling comment: "phrasing generated / rejected-by-
// guard / fell back to structural rendering", plus call_failed for a
// transport/provider failure the other three cannot describe).
type OfferPhrasingOutcome string

const (
	// OfferPhrasingGenerated: the call succeeded, every returned entry
	// passed the closed-vocabulary guard, and at least one option's Label
	// was rewritten.
	OfferPhrasingGenerated OfferPhrasingOutcome = "phrasing_generated"
	// OfferPhrasingRejectedByGuard: the call succeeded and produced a
	// well-formed draft, but the closed-vocabulary guard rejected it --
	// an entry named an option_id outside the offered set, or a duplicate
	// option_id. The WHOLE response is discarded, never applied
	// partially; structural labels ship unchanged.
	OfferPhrasingRejectedByGuard OfferPhrasingOutcome = "rejected_by_guard"
	// OfferPhrasingFellBackStructural: the call succeeded but the draft
	// was unusable independent of the guard -- empty, or an entry
	// violated the wire-shape bound (StructureOfferPhrasingEntry's own
	// Phrasing length, matching the four option types' v1 Phrasing
	// bound). Structural labels ship unchanged.
	OfferPhrasingFellBackStructural OfferPhrasingOutcome = "fell_back_structural"
	// OfferPhrasingCallFailed: the model call itself errored or timed out
	// (transport, provider, or context deadline). Structural labels ship
	// unchanged.
	OfferPhrasingCallFailed OfferPhrasingOutcome = "call_failed"
)

// StructureOfferPhrasingResult is OfferPhraser.Phrase's own return: the
// classified outcome plus, ONLY when Outcome==OfferPhrasingGenerated, the
// guard-verified option_id -> phrasing text map ready to apply.
type StructureOfferPhrasingResult struct {
	Outcome   OfferPhrasingOutcome
	Phrasings map[string]string
}

// OfferPhraser is the Engine-facing port for offer phrasing (CHAOS-4171
// PR2): a SECOND bounded model call that runs strictly after
// composeStructureNeeds/composeGatedStructureNeeds compose the structural
// offer set, guarded and fail-open to structure. Optional dependency on
// EngineDependencies; nil disables phrasing exactly like every other
// unset optional Engine dependency -- Investigate stays byte-identical to
// before this ticket, every option's Label stands alone.
//
// Phrase never returns an error: a failure at any stage (transport, shape,
// guard) classifies to a StructureOfferPhrasingResult outcome with an
// empty Phrasings map -- "fail-open to structure, never to the model"
// (ratified design) is enforced by this method's own signature, not by a
// caller remembering to catch and swallow an error.
type OfferPhraser interface {
	Phrase(ctx context.Context, principal storage.Principal, input StructureOfferPhrasingInput) StructureOfferPhrasingResult
}

// phrasingMaxLabelLength is contractsv1.ContextFabricStructureOfferPhrasingMaxLength
// by another name -- kept as a package-local alias so every guard check in
// this file reads as a Phrasing-bound check without an inline package
// qualifier, while still sharing the ONE declaration the wire Validate()
// methods and the phrasing prompt (genkitruntime/prompts.go) also read.
const phrasingMaxLabelLength = contractsv1.ContextFabricStructureOfferPhrasingMaxLength

// RuntimeOfferPhraser wraps an OfferPhrasingModelRuntime with the
// closed-vocabulary guard and receipt recording -- the offer-phrasing
// counterpart of RuntimeQuestionInterpreter/RuntimeAnswerSynthesizer
// (model_runtime.go).
type RuntimeOfferPhraser struct {
	Runtime OfferPhrasingModelRuntime
	Sink    ModelReceiptSink
}

func (r RuntimeOfferPhraser) Phrase(ctx context.Context, principal storage.Principal, input StructureOfferPhrasingInput) StructureOfferPhrasingResult {
	if r.Runtime == nil {
		return StructureOfferPhrasingResult{Outcome: OfferPhrasingFellBackStructural}
	}
	draft, receipt, err := r.Runtime.PhraseStructureOffers(ctx, principal, input)
	outcome, phrasings := classifyOfferPhrasingDraft(input, draft, err)
	// recordModelReceipt no-ops when receipt.Operation is empty (its own
	// convention, model_runtime.go) -- a well-formed
	// OfferPhrasingModelRuntime always stamps ModelOperationPhraseOffers,
	// same as InterpretQuestion/SynthesizeAnswer always stamp their own
	// operation, so a receipt reaches the sink on every attempted call,
	// success or failure.
	if sinkErr := recordModelReceipt(ctx, principal, r.Sink, receipt); sinkErr != nil {
		// A sink failure is audit-trail loss, not a phrasing-quality
		// signal -- it never upgrades a successful phrasing to a
		// fabricated failure and never downgrades a real one. Falling
		// back to structural is the safe default whenever the attempt
		// could not be durably recorded.
		return StructureOfferPhrasingResult{Outcome: OfferPhrasingFellBackStructural}
	}
	return StructureOfferPhrasingResult{Outcome: outcome, Phrasings: phrasings}
}

// classifyOfferPhrasingDraft is the closed-vocabulary guard itself
// (ratified design: "the model may rephrase, never add/remove/reorder
// options or change option_id/kind"). It is a WHOLE-response guard: any
// single entry that fails membership, uniqueness, or the shape bound
// discards the entire draft rather than applying the entries that did
// pass -- "fail-open to structure, never to the model" means a partially-
// trusted model response is treated the same as an untrusted one.
func classifyOfferPhrasingDraft(input StructureOfferPhrasingInput, draft StructureOfferPhrasingDraft, callErr error) (OfferPhrasingOutcome, map[string]string) {
	if callErr != nil {
		return OfferPhrasingCallFailed, nil
	}
	if len(draft.Phrasings) == 0 {
		return OfferPhrasingFellBackStructural, nil
	}
	offered := make(map[string]struct{}, len(input.Options))
	for _, opt := range input.Options {
		offered[opt.OptionID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(draft.Phrasings))
	phrasings := make(map[string]string, len(draft.Phrasings))
	for _, entry := range draft.Phrasings {
		text := strings.TrimSpace(entry.Phrasing)
		if text == "" || len(text) > phrasingMaxLabelLength {
			return OfferPhrasingFellBackStructural, nil
		}
		if _, ok := offered[entry.OptionID]; !ok {
			// An option_id the model invented, or one belonging to a
			// DIFFERENT investigation's offer set entirely -- exactly the
			// "never add ... options or change option_id" violation the
			// guard exists to catch.
			return OfferPhrasingRejectedByGuard, nil
		}
		if _, dup := seen[entry.OptionID]; dup {
			// A repeated option_id is "reorder/duplicate" territory --
			// the model is not entitled to say more than one thing about
			// the same offer.
			return OfferPhrasingRejectedByGuard, nil
		}
		seen[entry.OptionID] = struct{}{}
		phrasings[entry.OptionID] = text
	}
	return OfferPhrasingGenerated, phrasings
}

// applyOfferPhrasing (CHAOS-4171 PR2) is the SINGLE hook both
// StructureNeeds-composing call sites route their composed *needs through,
// immediately after composing it and before it reaches the
// InvestigationResult -- unresolved.go's terminalResult (composeStructureNeeds,
// regime B / the ordinary post-resolution path) and window.go's
// windowConfirmationRequiredResult (composeGatedStructureNeeds, which
// itself calls composeStructureNeeds -- CHAOS-4234's regime-A gated
// offers). composeStructureNeeds has exactly two callers; this covers both
// regimes from those same two call sites, per the ratified design.
//
// needs may be nil (nothing to disclose) or carry no phraseable option
// (a window-only disclosure, e.g. CHAOS-4118's turn-1 window gate before
// CHAOS-4234 with no kind/handle/candidate offers) -- both return needs
// unchanged, no call attempted, no telemetry: mirrors every other optional
// Engine dependency's convention that "nothing to do" is not an outcome.
// e.offerPhraser == nil (no phrasing model configured) degrades
// identically -- Investigate stays byte-identical to before this ticket.
func (e *Engine) applyOfferPhrasing(ctx context.Context, principal storage.Principal, needs *contractsv1.ContextFabricStructureNeeds) *contractsv1.ContextFabricStructureNeeds {
	if needs == nil || e.offerPhraser == nil {
		return needs
	}
	options := phrasableOptions(needs)
	if len(options) == 0 {
		return needs
	}
	result := e.offerPhraser.Phrase(ctx, principal, StructureOfferPhrasingInput{Options: options})
	if e.telemetry != nil {
		e.telemetry.RecordOfferPhrasing(ctx, principal, result.Outcome)
	}
	if result.Outcome != OfferPhrasingGenerated {
		return needs
	}
	applyPhrasings(needs, result.Phrasings)
	return needs
}

// phrasableOptions extracts the phrasing model's own input from an
// already-composed StructureNeeds -- kind/anchor/handle/candidate options
// only. WindowOptions and AcceptedGrammars carry no Phrasing field and are
// never sent: window offers are CHAOS-4118's own closed relative-window
// registry text, not a free-form structural label, and accepted grammars
// are a pattern-id disclosure, not a Label a caller reads.
func phrasableOptions(needs *contractsv1.ContextFabricStructureNeeds) []StructureOfferPhrasingOption {
	options := make([]StructureOfferPhrasingOption, 0, len(needs.KindOptions)+len(needs.AnchorOptions)+len(needs.HandleOptions)+len(needs.CandidateOptions))
	for _, opt := range needs.KindOptions {
		options = append(options, StructureOfferPhrasingOption{OptionID: opt.OptionID, Member: contractsv1.ContextFabricStructureNeedExpectedKind, Kind: opt.Kind, Label: opt.Label})
	}
	for _, opt := range needs.AnchorOptions {
		options = append(options, StructureOfferPhrasingOption{OptionID: opt.OptionID, Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, Kind: opt.Kind, Label: opt.Label})
	}
	for _, opt := range needs.HandleOptions {
		options = append(options, StructureOfferPhrasingOption{OptionID: opt.OptionID, Member: contractsv1.ContextFabricStructureNeedSubjectHandle, Kind: opt.Kind, Label: opt.Label})
	}
	for _, opt := range needs.CandidateOptions {
		options = append(options, StructureOfferPhrasingOption{OptionID: opt.OptionID, Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, Kind: opt.Kind, Label: opt.Label})
	}
	return options
}

// applyPhrasings writes each guard-verified phrasing into its matching
// option's Phrasing field, in place. Only ever called with
// OfferPhrasingGenerated's own map, whose keys are already proven (by
// classifyOfferPhrasingDraft) to be a SUBSET of this exact needs' own
// option_ids -- a phrasings entry with no matching option here is
// unreachable, not silently dropped.
func applyPhrasings(needs *contractsv1.ContextFabricStructureNeeds, phrasings map[string]string) {
	for i := range needs.KindOptions {
		if text, ok := phrasings[needs.KindOptions[i].OptionID]; ok {
			needs.KindOptions[i].Phrasing = text
		}
	}
	for i := range needs.AnchorOptions {
		if text, ok := phrasings[needs.AnchorOptions[i].OptionID]; ok {
			needs.AnchorOptions[i].Phrasing = text
		}
	}
	for i := range needs.HandleOptions {
		if text, ok := phrasings[needs.HandleOptions[i].OptionID]; ok {
			needs.HandleOptions[i].Phrasing = text
		}
	}
	for i := range needs.CandidateOptions {
		if text, ok := phrasings[needs.CandidateOptions[i].OptionID]; ok {
			needs.CandidateOptions[i].Phrasing = text
		}
	}
}
