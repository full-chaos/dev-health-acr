package genkitruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core"
	"github.com/firebase/genkit/go/genkit"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/invopop/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

const SDKVersion = "v1.11.0"

const (
	// DefaultInterpretationPromptVersion is v3 as of CHAOS-3770's live
	// acceptance: v2 (CHAOS-3754) extended the interpretation system
	// prompt (prompts.go) to cover conversational-reference resolution,
	// alias/acronym/previous-name subject terms, and subjectless
	// team/project cohort framing; v3 closes the canonical fact-kind
	// vocabulary in the prompt itself. Against a real provider, v2
	// invented fact-kind names on ordinary questions
	// (InterpretedQuestion.Validate then rejected every interpretation
	// with "fact requirement violates v1 bounds"), because
	// factRequirementOutput.Kind deliberately carries NO jsonschema enum
	// -- the layering is that Genkit parses permissively and the
	// ACR-owned semantic validator owns the registry. Closing the
	// vocabulary in the prompt is the fix that keeps that layering,
	// mirroring what v3 of the synthesis prompt did for driver
	// categories. A receipt's PromptVersion is part of what a
	// replay/evaluation pipeline uses to interpret
	// ModelExecutionReceipt content (ADR 0008), so a prompt content
	// change must bump this even though the interpretationOutput schema
	// itself is unchanged.
	//
	// v4 is the same defect one layer over: the prompt stated no length or
	// count limit, but InterpretedQuestion.Validate caps
	// requested_judgment at 256 characters and rejects the interpretation
	// in full when it is longer. Measured live, that single omission was
	// the entire difference between a usable and an unusable model:
	// gpt-5-mini failed 5 of 12 investigations on it alone, every one with
	// a requested_judgment of 259-294 characters, because it writes a more
	// thorough judgment than gpt-5-nano and nothing told it not to. The
	// cap itself is a contracts/v1 bound and is deliberately NOT relaxed
	// here; v4 states it, along with the other bounds a model cannot
	// infer, and tells the model where that detail belongs instead
	// (fact_requirements).
	//
	// v5 is the interpretation side of the same mechanical-oracle sweep
	// that produced synthesis v7 below (CHAOS-3770 F3 residual, codex
	// round 2): ContextFabricFactRequirement.Validate enforces a 32-entry
	// cap on one fact_requirements[] item's parameters map, a bound this
	// prompt never stated (a model could write 33 parameters on a single
	// entry and lose the whole interpretation for it). v5 states it.
	//
	// v6 (not otherwise documented above) is the current text prior to v7.
	//
	// v7 (CHAOS-3856 + CHAOS-3854's prompt-side half, CHAOS-3742 five-arm
	// generative trial): two behavioral changes to what the model is told,
	// not just a stated bound, so both are folded into one prompt version
	// rather than left implicit in an unchanged string. See prompts.go's
	// doc comment on interpretationSystemPrompt for the full measured
	// rationale.
	//
	//   - subject_terms/comparison_terms now demand a VERBATIM substring of
	//     the question text FIRST, with paraphrase/alias terms allowed only
	//     as a secondary addition -- replacing v1's "may be exact names,
	//     aliases, acronyms, previous names, or provider identifiers", which
	//     a real model read as license to paraphrase and which starved the
	//     lexical retrieval arm of an exact-substring anchor on ~85-90% of
	//     the trial corpus.
	//   - fact_requirements[].parameters now states plainly that no fact
	//     capability in this deployment accepts any parameter key --
	//     measured true for all 19 production capabilities
	//     (devhealthfacts.newCapability never sets AllowedParameters) --
	//     rather than leaving the model to infer a vocabulary from the
	//     length/count bounds alone and invent keys the registry always
	//     rejects.
	//
	// v8 (CHAOS-3900 W0): adds the two optional window_class/
	// window_confidence sentences (prompts.go) and the matching
	// interpretationOutput schema fields (runtime.go) -- a genuine model-
	// facing prompt AND schema content change (this file's own rule,
	// stated at v3 above: "a prompt content change must bump this even
	// though the interpretationOutput schema itself is unchanged" applies
	// a fortiori here, since BOTH changed), even though nothing in the
	// serving path reads the new fields (W0 is shadow/measurement only).
	// The instruction still perturbs the model's output distribution for
	// EVERY production interpretation call (more prompt content to
	// attend to can shift shape/subject_terms/judgment phrasing), so a
	// replay/evaluation pipeline reading ModelExecutionReceipt.PromptVersion
	// (ADR 0008) must be able to tell a pre-W0 receipt from a post-W0 one,
	// and the reuse key (answer_reuse.go, keyed on this exact constant)
	// must not serve a pre-W0 answer as if it were interchangeable with a
	// post-W0 one.
	//
	// Exported (CHAOS-3862): answer reuse must bind a lookup to the SAME
	// deployment-current value this defaulting uses, before Interpret ever
	// runs -- see contextfabric.ReuseKey.InterpretationPromptVersion's doc
	// comment. Composition reads this constant directly rather than a
	// second, independently-maintained copy, so the two can never drift.
	// v9 (CHAOS-4364): contextFabricFactKindList's interpolated text
	// changed -- the closed set now advertises "flow" and "landscape"
	// alongside the existing kinds, so the prompt bytes changed even
	// though nothing else in interpretationSystemPrompt did. Same
	// discipline as v3's original close-the-vocabulary bump: any change
	// to the interpolated fact-kind list is a prompt content change and
	// must bump this version.
	// v10 (CHAOS-4632): interpretationSystemPrompt gained five paragraphs
	// instructing the model on group_kind, scope_anchor_term,
	// scope_anchor_kind, requested_subject_kind and question_family. The
	// prompt bytes changed, and v9's own doc comment above states the
	// governing rule in as many words: ANY change to the prompt's content
	// is a prompt content change and must bump this version.
	//
	// This bump is REQUIRED even though CHAOS-4632 is a shadow slice, and
	// the reasoning is worth spelling out because it looks at first like a
	// contradiction. Nothing is gated on the family. But this constant is
	// a conjunctive ReuseKey dimension (ports.go, answer_reuse.go:376),
	// and the reuse lookup runs BEFORE Interpret -- so without the bump, a
	// stored answer produced under the OLD prompt keeps being served after
	// deployment, and the questions whose interpretation the new
	// instructions could change would be answered from a cache that
	// predates them. That is a reuse-DECISION change, which is a behavior
	// change however shadow the family itself is. Same class 0022's
	// window_inference_version, 0031's commit_gate_version and 0035's
	// ranking_formula_version each closed for their own decision, and the
	// same rule CHAOS-3862 pinned with
	// TestCHAOS3862_PromptVersionChangeInvalidatesStoredAnswerReuse.
	// v11 (CHAOS-4452 stage 2): interpretationSystemPrompt gained six
	// paragraphs instructing the model on question_frame -- its goals
	// LIST, its subject_expression union and per-variant fields, its
	// temporal axis, and the optional emphasis and dimension sets. The
	// prompt bytes changed, and the governing rule stated at v9 and
	// re-stated at v10 applies unchanged: ANY change to the prompt's
	// content is a prompt content change and must bump this version.
	//
	// REQUIRED for the identical reason v10's own note gives, and the
	// reasoning has not weakened by being repeated. The frame is shadow
	// -- nothing is gated on it. But this constant is a conjunctive
	// ReuseKey dimension and the reuse lookup runs BEFORE Interpret, so
	// without the bump a stored answer produced under the OLD prompt
	// keeps being served after deployment, and the questions whose
	// INTERPRETATION these new instructions could change would be
	// answered from a cache that predates them. A reuse-decision change
	// is a behaviour change however shadow the frame itself is.
	//
	// The frame instructions can change interpretation in a way the
	// family instructions could not, which is why this is not a
	// belt-and-braces bump: the prompt now asks for a goals LIST on every
	// question, and asking a model for more structure changes what it
	// attends to in the question text. That is measured on the rig
	// before this ships, not assumed.
	DefaultInterpretationPromptVersion = "context-fabric-interpretation.v11"
	// DefaultSynthesisPromptVersion is v3 as of CHAOS-3755's adversarial
	// review round: v2 added claimed_facts for value-level closure; v3
	// closes the driver category vocabulary (a fixed 16-value set, no
	// longer free text), requires a claimed fact's subject to be one the
	// citing driver/finding actually names, requires subject labels to
	// match the input verbatim, and marks direct_judgment/current_state/
	// deterministic_answer as advisory-only (ACR recomposes them
	// server-side and never returns the model's own text for them). v4 is
	// CHAOS-3770's live-acceptance fix, the synthesis counterpart of
	// interpretation v3 above: driver standing, derivation method,
	// epistemic status and claimed-fact kind are closed vocabularies with
	// no jsonschema enum on the shared contracts types, and driver_id /
	// finding_id / claim_id carry a minimum length and a
	// claimed_fact_ids-must-resolve rule that a model cannot infer. v3
	// stated none of them, so a real provider failed
	// SynthesisDraft.ValidateAgainst on ordinary inputs; v4 states them.
	//
	// v5 closes the class the first three fixes each patched one instance
	// of: a bound the validator enforces that the prompt never states. It
	// adds the length and count limits (title, summary, qualification,
	// affected_subjects, per-item and per-collection caps, identifier
	// upper bound) that v4 left unstated, and TestPromptsStateEveryModelFacingBound
	// aimed to make a fourth instance of the class unable to ship silently
	// -- but the table behind that test was itself hand-maintained and
	// incomplete (CHAOS-3770 F3 codex review): it never covered
	// warnings, strongest_pressures/drivers/remaining_work/readiness_gaps/
	// conflicts/limitations collection caps, or the top-level
	// evidence_ref_ids cap, so a bound could still be silently dropped
	// from the prompt without the test noticing. The oracle is now
	// mechanical (contracts/v1.ContextFabricModelFacingBounds, the single
	// source both this prompt's authors and the validator's numeric
	// literals must agree with -- see that file's doc comment), and
	// applying it here surfaced exactly the predicted class of gap: this
	// prompt's own text never stated the warnings bound at all, even
	// though ContextFabricInvestigationResult.Validate() has enforced it
	// (250 items, 4000 characters each) unchanged since v2. v6 adds that
	// one missing statement; nothing else in this prompt changes.
	//
	// v7 is the result of a full sweep of every remaining numeric literal
	// in internal/contracts/v1/validate_context_fabric_result.go and
	// validate_context_fabric_helpers.go (CHAOS-3770 F3 residual, codex
	// round 2's structural question: "are there any OTHER inline literals
	// left in the validate path that model output can trip?"). It found
	// one more real gap: validateClaimedFacts's 250-entry cap on the
	// synthesis draft's own top-level claimed_facts list (distinct from
	// the already-stated per-driver/per-finding claimed_fact_ids
	// REFERENCE count) had no registry entry and no prompt statement. v7
	// adds it. At the time, every OTHER literal the sweep found was
	// believed to belong to one of two excluded classes: (a) fields the
	// model only ECHOES verbatim from data ACR already supplied and
	// independently bounded (SubjectRef.CanonicalID/Label, an evidence ref
	// ID's own string length, a claimed fact's Value) -- reasoned to be
	// safe because SynthesisDraft.ValidateAgainst's allowedSubjects/
	// canonicalLabels/allowedEvidence/factValueEqualsScalar checks would
	// reject anything the model didn't copy verbatim before a length bound
	// was ever the operative failure reason; and (b) fields a different
	// subsystem populates, never the interpretation/synthesis model
	// (SubjectCandidate, SubjectResolution, Cohort, RelationshipPath/Edge,
	// SourceObservation, Coverage, VersionSet -- ACR's own graph/canonical-
	// fact layer; ContextFabricEntityProjection and siblings -- CHAOS-3753's
	// projection worker, an unrelated write path). Class (b) held up; class
	// (a) did not -- see v8.
	//
	// v8 (CHAOS-3784 round-3 R3-1) found class (a)'s reasoning was
	// order-contradicted: every one of this codebase's per-item loops
	// (internal/contextfabric/model_runtime.go's SynthesisDraft.ValidateAgainst)
	// calls the model-facing struct's own Validate() method BEFORE its
	// membership/grounding check runs (driver.Validate()/finding.Validate()/
	// claim.Validate() first, THEN the allowedSubjects/allowedEvidence/
	// factValueEqualsScalar check on the same loop iteration), so a length
	// bound enforced INSIDE Validate() always wins the race against a
	// grounding check enforced OUTSIDE it -- the reverse of what class (a)
	// assumed. A model value that is SIMULTANEOUSLY too long and
	// ungrounded is rejected on the length violation, unreported, exactly
	// the CHAOS-3770 F3 defect class one layer over: this file already
	// stated evidence_ref_id's/SubjectRef's/ClaimedFact.Value's other
	// bounds correctly (see prompts.go's driver_id/finding_id/claim_id and
	// path_id/claimed_fact_id statements), it just never stated THESE
	// four. v8 states them: each evidence_ref_id is at most 256
	// characters; every subject reference's canonical ID is at most 256
	// characters and label at most 512; a claimed fact's string value is
	// at most 4000 characters. FactRequirement.Subjects and every MINIMUM-
	// side bound remain correctly excluded for reasons validation order
	// cannot contradict -- see ContextFabricModelFacingBounds's doc
	// comment in internal/contracts/v1/context_fabric_model_bounds.go for
	// the complete, current exclusion list.
	//
	// v9 (CHAOS-3746, resolved during the rebase onto CHAOS-3784's v8):
	// two independent changes both landed on v8 -- CHAOS-3784's four added
	// bound statements above, and this branch's bound CORRECTIONS (a nested
	// evidence_ref_ids count of 200 distinct from the result-level 500, a
	// fact-requirement parameter value of 1000 matching the published
	// schema, and limitations/warnings at 100 entries of 2000 characters
	// each). A prompt version names one exact text, so the merged text --
	// which is neither branch's v8 -- takes the next number rather than
	// leaving two different prompts sharing a version string.
	// v10 (CHAOS-4098) states the one status rule the prompt never had.
	// The model-output schema offers status "clarification_required", the
	// prompt said nothing about when any status is admissible, and a draft
	// that used it on a decisive path produced a result ACR could not
	// validate at all (see applySynthesisStatusOverride's own doc comment
	// for the full defect). The engine now handles that draft rather than
	// failing on it, so this statement is the PREVENTIVE half, not the
	// load-bearing one: nothing depends on the model obeying it, and the
	// override stands whether it does or not. Stated anyway because a
	// model steered away from the status produces the answer the caller
	// actually wanted instead of a relabelled no_match.
	//
	// v11 (CHAOS-4102) fixes a regression v10 itself introduced. v10's
	// fallback clause ("return status no_match ... or degraded/partial if
	// it supports a weaker one") named a status word this prompt had NEVER
	// once used before (token count v9=0, v10=1) as the FIRST-offered
	// option for weak evidence -- a genuine change to the model's output
	// distribution, not the inert steering the v10 comment above claimed.
	// A read-only investigation (lane-4102-inv, 2026-08-22) proved this
	// two ways: the override this clause was meant to be redundant with
	// (applySynthesisStatusOverride) fired ZERO times across the run that
	// exposed it, and the model's own status tally shifted no_match 0/42
	// -> 6/42, complete 5/42 -> 0/42 between v9 and v10 on the same
	// corpus (Fisher one-sided p<=0.03 both directions) -- one case
	// (corpus index 60, all four decisive calls, degraded -> no_match in
	// lockstep) tripped a hard correctness bar (false_no_match) on a
	// TRUSTED case the corpus authors never intended to exercise this
	// path. v11 removes the fallback clause entirely, keeping ONLY the
	// clarification_required steering v10 actually needed to add: the
	// override handles every status the model can return on the decisive
	// path (see applySynthesisStatusOverride's own doc comment), so
	// nothing depends on this prompt naming an alternative, and naming
	// one is exactly what regressed. The model still sees the full closed
	// status vocabulary via the attached output schema, not via this
	// prose -- v11 removes behavioral STEERING toward a status word, not
	// the status word's validity.
	//
	// A version bump cold-caches every reuse-participating row for every
	// org (the reuse key binds on this exact constant -- see
	// contextfabric.ReuseKey.SynthesisPromptVersion). That over-
	// invalidation is the accepted, documented cost of any prompt change,
	// identical in shape to v6-v10 above; it is not a reason to leave two
	// different prompt texts sharing one version string.
	//
	// Exported for the same CHAOS-3862 reason as
	// DefaultInterpretationPromptVersion above.
	//
	// v12 (CHAOS-4364): synthesisSystemPrompt also interpolates
	// contextFabricFactKindList (the "claimed fact's kind MUST be one of"
	// sentence), which now advertises "flow" and "landscape" too -- the
	// same prompt-bytes-changed rationale as
	// DefaultInterpretationPromptVersion's v9 bump, on the sibling prompt.
	//
	// v12 -> v13 (CHAOS-4355 follow-up, codex R1 P2 finding): the SYSTEM
	// prompt text is unchanged, but modelFacingFacts now drops every
	// Rows-shaped canonical fact field from the "canonical_facts" payload
	// synthesisInputFromDomain builds -- a genuine, measured change to
	// what this call sends the model (TestBuildSynthesisPromptExcludesRowsShapedCanonicalFields
	// measures the byte delta), not merely a per-request data variation:
	// two calls for the IDENTICAL question/facts now send DIFFERENT bytes
	// depending on which side of this change ran. Per this constant's own
	// "any prompt change" standing rule above (v6-v12), a row saved under
	// the OLD (Rows-visible) payload must not silently satisfy a reuse
	// lookup as though it were generated under the NEW (Rows-excluded)
	// one -- the version-bump-forces-a-cold-cache mechanism this constant
	// exists for is exactly what CHAOS-3862's
	// TestCHAOS3862_PromptVersionChangeInvalidatesStoredAnswerReuse pins.
	//
	// v13 -> v14 (CHAOS-4690 Commit F, design §4.1): synthesisSystemPrompt
	// gains the coverage_disclosures paragraph -- a genuine change to the
	// bytes this call sends, so a row generated before the model was ever
	// told this field exists must not satisfy a reuse lookup as though it
	// were generated under the new prompt (same standing rule as v6-v13
	// above).
	DefaultSynthesisPromptVersion = "context-fabric-synthesis.v14"
	// DefaultSchemaVersion is the genkit MODEL-OUTPUT JSON SCHEMA version
	// -- ONE value shared by both the interpret and synthesize calls
	// (Config carries a single SchemaVersion field, not a per-operation
	// pair), stamped into every receipt and, via
	// model_runtime.go's Synthesize() composer, into
	// InvestigationResult.Versions.InterpretationVersion.
	//
	// Exported (CHAOS-3862 sol round 2 P2 class-close): answer reuse must
	// bind a lookup to the SAME deployment-current value this defaulting
	// uses, before either model call ever runs -- see
	// contextfabric.ReuseKey.ModelOutputSchemaVersion's doc comment.
	//
	// v1 -> v2 (CHAOS-3742 RUN 3 follow-up, codex adversarial review,
	// 2026-08-21): outputTimeContext.JSONSchema's new per-axis if/then
	// conditional is a MATERIAL tightening of the actual model-output
	// contract (a range axis without start/end, schema-valid under v1, is
	// schema-INVALID under v2) -- leaving this constant at v1 would let
	// answer reuse serve a v1-era decisive result (interpreted before this
	// schema existed) as though it were produced under the SAME contract
	// as a v2 call, which is exactly the version-drift class this field
	// exists to prevent (its own doc comment above). Bumping it is the
	// only change requested by this constant's own contract -- no
	// caller-visible behavior changes beyond reuse eligibility.
	// v3 (CHAOS-4632): interpretationOutput gained five optional fields
	// (question_family, group_kind, scope_anchor_term, scope_anchor_kind,
	// requested_subject_kind), so the schema genkit infers and sends to
	// the provider is a DIFFERENT model-output contract than v2's. Unlike
	// v2's change this is a widening rather than a tightening -- every
	// v2-valid output is still v3-valid -- but the version exists to say
	// which contract a stored result was produced UNDER, not merely
	// whether the old one would still validate: a v2-era result was
	// interpreted by a model that was never offered these fields and
	// could not have emitted them. Leaving this at v2 would let reuse
	// serve such a result as though it came from the same contract as a
	// v3 call, which is exactly the version-drift class this field exists
	// to prevent (its own doc comment above).
	// v4 (CHAOS-4452 stage 2): interpretationOutput gained one optional
	// nested object, question_frame, carrying the goals list, the subject
	// expression with its per-variant fields and its operand list, the
	// temporal axis, and the optional emphasis and dimension sets. The
	// schema genkit infers and sends to the provider is therefore a
	// DIFFERENT model-output contract than v3's.
	//
	// Like v3 and unlike v2 this is a WIDENING -- every v3-valid output is
	// still v4-valid -- and the same reasoning applies unchanged: the
	// version says which contract a stored result was produced UNDER, not
	// merely whether the old one would still validate. A v3-era result was
	// interpreted by a model that was never offered a frame and could not
	// have emitted one, so serving it through reuse as though it came from
	// a v4 call is exactly the version-drift this field exists to prevent.
	DefaultSchemaVersion    = "context-fabric-model-output.v4"
	defaultEvaluatorVersion = "context-fabric-grounding.v1"
	// DefaultPhrasingPromptVersion is v1 (CHAOS-4171 PR2): the SECOND
	// bounded model call's own prompt, versioned independently of
	// InterpretationPromptVersion/SynthesisPromptVersion above because it
	// is a genuinely separate call with its own input shape (an offer
	// set, never the question or any evidence) and its own output shape
	// (a phrasing per option_id). Prompt changes are behavior changes
	// (standing rule): bump this on every text change to phrasingSystemPrompt.
	//
	// Unlike the interpretation/synthesis prompt versions, this one is
	// NOT part of contextfabric.ReuseKey -- offer phrasing only ever runs
	// on a non-decisive (clarification-bearing) terminal, and every path
	// that composes StructureNeeds already bypasses answer reuse before
	// this call happens (see composeStructureNeeds's own callers), so
	// there is no reuse-eligible row this version could ever need to key.
	DefaultPhrasingPromptVersion = "context-fabric-offer-phrasing.v1"
)

type Config struct {
	Genkit   *genkit.Genkit
	Provider string
	Model    string
	// ModelRef is the fully qualified Genkit action name used to select the
	// model at generation time -- "<plugin provider>/<model id>", e.g.
	// "openai/gpt-5-nano". Genkit resolves a model only by that namespaced
	// key (an unqualified name parses to an empty provider and matches no
	// plugin), while Provider and Model stay the plain, replay-facing
	// values recorded in every ModelExecutionReceipt. Defaults to Model
	// when empty, which is what a test double or an already-qualified
	// caller wants.
	ModelRef                    string
	ModelVersion                string
	InterpretationPromptVersion string
	SynthesisPromptVersion      string
	// PhrasingModel (CHAOS-4171 PR2) is the bare model id the offer-
	// phrasing call uses -- independently configurable from Model so a
	// deployment can route phrasing to a smaller/cheaper model (the
	// ratified design's "env-configurable smaller model"). Defaults to
	// Model when empty: the second call runs on the SAME model as
	// interpretation unless an operator opts into a distinct one.
	// PhrasingModelRef is its namespaced Genkit action-name counterpart
	// (see ModelRef's own doc comment), defaulting to ModelRef when
	// PhrasingModel is also unset, or to the plain PhrasingModel value
	// otherwise.
	PhrasingModel         string
	PhrasingModelRef      string
	PhrasingModelVersion  string
	PhrasingPromptVersion string
	SchemaVersion         string
	EvaluatorVersion      string
	Timeout               time.Duration
	MaxAttempts           int
	MaxInputBytes         int
	Fallback              contextfabric.ModelRuntime
	// Logger receives the ACR-owned decision-event log line CHAOS-3889 emits
	// once per model call (see logInterpretDecision/logSynthesizeDecision).
	// Defaults to slog.Default() when nil, matching every other
	// nil-logger-falls-back-to-Default convention in this codebase
	// (contextfabric.NewSlogEngineTelemetry, observability.NewSlogSink,
	// falkorgraph.SlogTelemetry, projectionrun.SlogObserver). Not a new
	// config knob: the event is unconditionally emitted at
	// slog.LevelInfo, gated only by whatever level this logger's handler
	// itself filters at.
	Logger *slog.Logger
	// Telemetry is OPTIONAL (nil-safe) -- CHAOS-4355 follow-up:
	// SynthesizeAnswer reports RecordModelRowsStripped here, once per call
	// where the model still authored a ClaimedFact.Rows despite the
	// prompt no longer showing it any Rows-shaped field (see
	// synthesisInputFromDomain/modelFacingFacts). A nil Telemetry (every
	// test that builds a Runtime without one) is exactly pre-CHAOS-4355
	// behavior.
	Telemetry contextfabric.EngineTelemetry
}

type Runtime struct {
	generator generator
	config    Config
	now       func() time.Time
}

type generator interface {
	Interpret(context.Context, generationRequest) (interpretationOutput, contextfabric.ModelUsage, error)
	Synthesize(context.Context, generationRequest) (synthesisOutput, contextfabric.ModelUsage, error)
	// Phrase (CHAOS-4171 PR2) is the offer-phrasing call's own generation
	// boundary, mirroring Interpret/Synthesize exactly.
	Phrase(context.Context, generationRequest) (phrasingOutput, contextfabric.ModelUsage, error)
}

type generationRequest struct {
	Model  string
	System string
	Prompt string
	// Config (CHAOS-4622, seed scheme superseded by CHAOS-4631) is an
	// OPTIONAL provider decoding config, forwarded verbatim to genkit's
	// ai.WithConfig. Only interpretQuestionWithSample sets this (to
	// chaos4631InterpretDecodingConfig, below) -- Synthesize/Phrase never
	// set it, and sdkGenerator.Synthesize/Phrase never read it, so this
	// field is a no-op for every call site except Interpret.
	Config any
}

// chaos4631InterpretSeedFor derives seed_i = f(stable_question_hash, i)
// (design doc §4.1 mechanism 1; ticket CHAOS-4631, Fable review finding F1)
// -- the INTERPRET call's decoding seed, per sample, never one constant
// shared across every question and every sample.
//
// This SUPERSEDES CHAOS-4622's hotfix (`{"seed": 4622}`, one fixed
// constant for every question and every replicate): that constant reduced
// flip rate (EXECUTED, lane-4622-hotfix) but is unsound as the basis for
// S2's future N-sample consensus. Against a seed-honouring provider, N
// samples run under ONE shared seed return near-identical output, which
// makes the consensus vacuously unanimous -- fakes the very stability S2
// exists to measure, and defeats refuse-to-guess by never producing a tie
// (design §4.1). A derived, per-sample seed keeps the ensemble diverse
// while staying fully reproducible from the question text alone.
//
// Production calls this with sample=0 only (S1 ships N=1; see
// interpretQuestionWithSample). S2 is the first production caller of
// sample>0. The CHAOS-4631 Shape-distribution measurement
// (cmd/acr-interpret-seed-bench) calls InterpretQuestionForSample with
// sample=0..N-1 directly, to reproduce S2's N-distinct-seed scheme before
// S2's resolver exists -- sampling under any other scheme (e.g. one fixed
// seed repeated N times) would measure a distribution the running system
// will never produce (design §4.1, ticket body point 3).
//
// FNV-1a over "<questionHash>:<sample>" is a deterministic, well-mixed
// 64-bit value; masked into the non-negative int64 range because the wire
// type (openai-go's Seed, an int64) documents no sign requirement honoured
// by every provider, and a consistently non-negative seed avoids relying
// on one. questionHash is contextfabric.QuestionHash(request.Question) --
// the SAME reuse-key hash already used to key answer-reuse -- rather than
// a fresh hash construction, so "reproducible from the question hash
// alone" is the same hash a caller can already recompute.
func chaos4631InterpretSeedFor(questionHash string, sample int) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(questionHash))
	_, _ = h.Write([]byte(":"))
	_, _ = h.Write([]byte(strconv.Itoa(sample)))
	return int64(h.Sum64() &^ (1 << 63))
}

// chaos4631InterpretDecodingConfig builds the exact decoding config
// forwarded to the INTERPRET call's generationRequest.Config -- a fixed
// seed, and ONLY a fixed seed.
//
// Temperature is deliberately NOT set here, even though CHAOS-4622 asked
// for "temperature 0 (or the runtime's nearest supported)": the deployed
// model family rejects it outright. EXECUTED repro against the real
// provider (org 70d529e0, kiac rig, this runtime's own configured model)
// with `{"temperature": 0}` in this exact map: every call failed
// `POST https://api.openai.com/v1/chat/completions: 400 Bad Request`
// (body sanitized by ACR's own redaction, but the 400 plus the model
// family is the same "temperature is fixed, only default supported"
// constraint OpenAI's reasoning-model line (o1/o3/gpt-5-class) documents)
// -- classified ErrModelUnavailable and surfaced to callers as
// dependency_unavailable, i.e. temperature:0 would have made EVERY
// interpretation fail, not just made it deterministic. `{"seed": N}`
// alone, same model, same question: 200, interpretation returned. Seed is
// this model family's actual supported determinism lever, so it is the
// "nearest supported" the ticket asks for.
//
// Scope: decision-stability only. This does not change WHICH
// interpretation is correct for an ambiguous/grouped-cohort question --
// that is CHAOS-4452's territory (question-family/investigation-planning
// stage). It only makes the SAME input more likely to reliably produce the
// SAME interpretation, so a still-wrong interpretation for a hard question
// can stabilize rather than flip; that is this ticket's honest limit, not
// a regression -- and the seed provider documents as best-effort, not
// guaranteed (measured: entropy down on 2 of 3 CHAOS-4622 questions, the
// missing-bundle variance surviving on all 3).
//
// Shaped as a map[string]any (JSON field name "seed") rather than the
// openai-go SDK's own openai.ChatCompletionNewParams type, so
// genkitruntime does not import that package directly -- compat_oai's
// ModelGenerator.WithConfig round-trips a map[string]any through
// base.MapToStruct[openai.ChatCompletionNewParams] (JSON marshal/
// unmarshal), landing on the exact same Seed field. compat_oai is the
// only Genkit plugin ACR runs (internal/contextfabric/modelprovider wires
// it exclusively); a future non-OpenAI-compatible plugin that does not
// recognize "seed" at all would silently ignore this whole map (still no
// worse than today), never fail closed.
//
// Deliberately NOT applied to Synthesize/Phrase in this change -- decision
// D3 (design doc §10) scopes narrator seeding to its own follow-up
// measurement ticket, not this one.
func chaos4631InterpretDecodingConfig(seed int64) map[string]any {
	return map[string]any{"seed": seed}
}

type sdkGenerator struct {
	genkit *genkit.Genkit
}

func (g sdkGenerator) Interpret(ctx context.Context, request generationRequest) (interpretationOutput, contextfabric.ModelUsage, error) {
	output, response, err := genkit.GenerateData[interpretationOutput](ctx, g.genkit,
		ai.WithModelName(request.Model),
		ai.WithSystem(request.System),
		ai.WithPrompt("%s", request.Prompt),
		ai.WithCustomConstrainedOutput(),
		ai.WithConfig(request.Config),
	)
	if err != nil {
		return interpretationOutput{}, contextfabric.ModelUsage{}, err
	}
	if output == nil {
		return interpretationOutput{}, contextfabric.ModelUsage{}, errors.New("genkit returned no interpretation output")
	}
	return *output, modelUsage(response), nil
}

func (g sdkGenerator) Synthesize(ctx context.Context, request generationRequest) (synthesisOutput, contextfabric.ModelUsage, error) {
	output, response, err := genkit.GenerateData[synthesisOutput](ctx, g.genkit,
		ai.WithModelName(request.Model),
		ai.WithSystem(request.System),
		ai.WithPrompt("%s", request.Prompt),
		ai.WithCustomConstrainedOutput(),
	)
	if err != nil {
		return synthesisOutput{}, contextfabric.ModelUsage{}, err
	}
	if output == nil {
		return synthesisOutput{}, contextfabric.ModelUsage{}, errors.New("genkit returned no synthesis output")
	}
	return *output, modelUsage(response), nil
}

func (g sdkGenerator) Phrase(ctx context.Context, request generationRequest) (phrasingOutput, contextfabric.ModelUsage, error) {
	output, response, err := genkit.GenerateData[phrasingOutput](ctx, g.genkit,
		ai.WithModelName(request.Model),
		ai.WithSystem(request.System),
		ai.WithPrompt("%s", request.Prompt),
		ai.WithCustomConstrainedOutput(),
	)
	if err != nil {
		return phrasingOutput{}, contextfabric.ModelUsage{}, err
	}
	if output == nil {
		return phrasingOutput{}, contextfabric.ModelUsage{}, errors.New("genkit returned no offer phrasing output")
	}
	return *output, modelUsage(response), nil
}

func modelUsage(response *ai.ModelResponse) contextfabric.ModelUsage {
	if response == nil || response.Usage == nil {
		return contextfabric.ModelUsage{}
	}
	return contextfabric.ModelUsage{
		InputTokens:  response.Usage.InputTokens,
		OutputTokens: response.Usage.OutputTokens,
		TotalTokens:  response.Usage.TotalTokens,
	}
}

func New(config Config) (*Runtime, error) {
	if config.Genkit == nil {
		return nil, errors.New("genkit instance is required")
	}
	return newWithGenerator(config, sdkGenerator{genkit: config.Genkit})
}

func newWithGenerator(config Config, gen generator) (*Runtime, error) {
	if gen == nil {
		return nil, errors.New("structured generator is required")
	}
	for name, value := range map[string]string{
		"provider": config.Provider,
		"model":    config.Model,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 256 {
			return nil, fmt.Errorf("%s is required and must be bounded", name)
		}
	}
	if strings.TrimSpace(config.ModelRef) == "" {
		config.ModelRef = config.Model
	}
	if strings.TrimSpace(config.ModelVersion) == "" {
		config.ModelVersion = config.Model
	}
	if strings.TrimSpace(config.InterpretationPromptVersion) == "" {
		config.InterpretationPromptVersion = DefaultInterpretationPromptVersion
	}
	if strings.TrimSpace(config.SynthesisPromptVersion) == "" {
		config.SynthesisPromptVersion = DefaultSynthesisPromptVersion
	}
	// CHAOS-4171 PR2: PhrasingModel defaults to the primary Model -- the
	// SAME "unset means share the interpretation model" default the
	// ratified design calls for. PhrasingModelRef/PhrasingModelVersion
	// then default off the (possibly just-defaulted) PhrasingModel,
	// mirroring ModelRef/ModelVersion's own defaulting immediately above.
	if strings.TrimSpace(config.PhrasingModel) == "" {
		config.PhrasingModel = config.Model
	}
	if strings.TrimSpace(config.PhrasingModelRef) == "" {
		if config.PhrasingModel == config.Model {
			config.PhrasingModelRef = config.ModelRef
		} else {
			config.PhrasingModelRef = config.PhrasingModel
		}
	}
	if strings.TrimSpace(config.PhrasingModelVersion) == "" {
		if config.PhrasingModel == config.Model {
			config.PhrasingModelVersion = config.ModelVersion
		} else {
			config.PhrasingModelVersion = config.PhrasingModel
		}
	}
	if strings.TrimSpace(config.PhrasingPromptVersion) == "" {
		config.PhrasingPromptVersion = DefaultPhrasingPromptVersion
	}
	if strings.TrimSpace(config.SchemaVersion) == "" {
		config.SchemaVersion = DefaultSchemaVersion
	}
	if strings.TrimSpace(config.EvaluatorVersion) == "" {
		config.EvaluatorVersion = defaultEvaluatorVersion
	}
	if config.Timeout <= 0 {
		config.Timeout = 45 * time.Second
	}
	if config.Timeout < time.Second || config.Timeout > 2*time.Minute {
		return nil, errors.New("model timeout must be between one second and two minutes")
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 2
	}
	if config.MaxAttempts < 1 || config.MaxAttempts > 3 {
		return nil, errors.New("model attempts must be between one and three")
	}
	if config.MaxInputBytes == 0 {
		config.MaxInputBytes = 512 << 10
	}
	if config.MaxInputBytes < 8<<10 || config.MaxInputBytes > 1<<20 {
		return nil, errors.New("model input bound must be between 8 KiB and 1 MiB")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Runtime{generator: gen, config: config, now: time.Now}, nil
}

// InterpretQuestion is the production entry point: it always samples the
// interpretation exactly once (S1 ships N=1 -- see chaos4631InterpretSeedFor's
// doc comment), under the sample-0 derived seed.
func (r *Runtime) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return r.interpretQuestionWithSample(ctx, principal, request, 0)
}

// InterpretQuestionForSample is CHAOS-4631's measurement-only entry point.
// It is NOT part of the contextfabric.ModelRuntime interface and no
// production request path calls it (every real investigation calls
// InterpretQuestion, sample=0, above) -- it exists so the CHAOS-4631
// Shape-distribution measurement (cmd/acr-interpret-seed-bench) can
// reproduce S2's future N-distinct-derived-seed consensus scheme against
// the real configured model, without any wire-visible seed override and
// without widening contextfabric.ModelRuntime (which every existing
// ModelRuntime caller -- RuntimeQuestionInterpreter, the fallback chain --
// keeps depending on unchanged). A method on the concrete *Runtime type,
// not the interface, is exactly how CHAOS-4631's "no contract change"
// claim stays true while still shipping the sample parameter the design
// says must ship now for S2 to consume later.
//
// CAVEAT (codex round 1, P2): the sample index only governs the PRIMARY
// model's seed. If r.config.Fallback is configured and the primary call
// fails or returns invalid output, the fallback leg runs through
// r.config.Fallback.InterpretQuestion -- the plain contextfabric.ModelRuntime
// interface method, which has no sample parameter at all, so a fallback
// response is always generated under whatever decoding the fallback
// ModelRuntime uses on its own sample 0, never seed_i. The returned receipt
// still reports this truthfully (Outcome=="fallback", FallbackUsed==true),
// so a caller doing an N-sample measurement MUST check FallbackUsed and
// exclude/flag any such sample -- treating it as a genuine seed_i data
// point would silently corrupt a distribution measurement with a repeated
// fallback-sample-0 result. interpretseedbench.Run surfaces FallbackUsed on
// every Sample for exactly this reason.
func (r *Runtime) InterpretQuestionForSample(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, sample int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return r.interpretQuestionWithSample(ctx, principal, request, sample)
}

func (r *Runtime) interpretQuestionWithSample(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest, sample int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	if err := request.Validate(); err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("interpretation request: %w", err)
	}
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, errors.New("authenticated organization is required")
	}
	payload := interpretationInput{
		Question:             request.Question,
		Conversation:         request.Conversation,
		SubjectHints:         request.RequestedScope.SubjectHints,
		RequestedScope:       request.RequestedScope,
		TimeContext:          request.TimeContext,
		PriorSubjectReceipts: request.PriorSubjectReceipts,
	}
	encoded, err := boundedJSON(payload, r.config.MaxInputBytes)
	if err != nil {
		return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, err
	}

	// CHAOS-3889 (H6/H7/H8): emit one decision-event log line for this
	// call, covering every return path below via defer instead of a
	// duplicated call at each return statement. receipt is mutated in
	// place exactly as it already was; axisSource and
	// primaryFailureClassification are set at the point each becomes
	// knowable. The deferred read runs after the function's return values
	// are computed, so it always sees each variable's final state. This
	// is pure observability -- it changes nothing about what
	// InterpretQuestion returns or how an outcome is decided.
	var (
		receipt                      contextfabric.ModelExecutionReceipt
		axisSource                   string
		primaryFailureClassification string
		// rejectionReason names WHICH validator rule rejected this
		// interpretation, mutated in place at the rejecting return exactly
		// as receipt/axisSource/primaryFailureClassification are, and read
		// by the deferred telemetry call below. Empty on every path that
		// is not a rejection -- success, generation failure, and fallback
		// success all leave it empty, and logInterpretDecision appends the
		// field only when it is non-empty. This mirrors
		// SynthesizeAnswer's own rejectionReason variable one function
		// down; see it for the CHAOS-4522 precedent this completes.
		rejectionReason string
	)
	// CHAOS-4631: the derived seed is a pure function of the question text
	// and the sample index, known before the call is even attempted -- so
	// it is computed once, up front, and threaded into both the actual
	// request and the deferred telemetry read below, rather than mutated
	// in place the way receipt/axisSource/primaryFailureClassification are.
	questionHash := contextfabric.QuestionHash(request.Question)
	decodingSeed := chaos4631InterpretSeedFor(questionHash, sample)
	defer func() {
		r.logInterpretDecision(ctx, principal.OrgID, request.RequestID, receipt, primaryFailureClassification, axisSource, decodingSeed, sample, rejectionReason)
	}()

	started := r.now().UTC()
	var output interpretationOutput
	var usage contextfabric.ModelUsage
	attempts, generationErr := r.withRetry(ctx, func(callCtx context.Context) error {
		var err error
		output, usage, err = r.generator.Interpret(callCtx, generationRequest{
			Model: r.config.ModelRef, System: interpretationSystemPrompt, Prompt: string(encoded),
			Config: chaos4631InterpretDecodingConfig(decodingSeed),
		})
		return err
	})
	completed := r.now().UTC()
	var classifiedErr error
	if generationErr != nil {
		classifiedErr = classifyModelError(generationErr)
	}
	receipt = r.receipt(contextfabric.ModelOperationInterpret, r.config.InterpretationPromptVersion, started, completed, attempts, encoded, nil, usage, classifiedErr)
	// RequestID correlates the durable receipt row back to this
	// investigation (audit MED item, CHAOS-3889 secondary). Stamped once,
	// up front, so it survives every return path below -- including
	// mergeFallbackReceipt, which never touches this field.
	receipt.RequestID = request.RequestID
	if generationErr != nil {
		primaryFailureClassification = receipt.Outcome
		if r.config.Fallback != nil {
			interpreted, fallbackReceipt, fallbackErr := r.config.Fallback.InterpretQuestion(ctx, principal, request)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				// CHAOS-4631 codex round 3, P2: assign the MERGED receipt back
				// onto the outer `receipt` variable before returning it, not
				// just as the return value. logInterpretDecision's defer reads
				// `receipt` (mutated in place, per this function's own
				// preamble comment) -- returning mergeFallbackReceipt's result
				// without also writing it back left the deferred telemetry
				// read seeing the PRIMARY's stale Model/ModelVersion even
				// though Outcome/FallbackUsed correctly showed "fallback":
				// the log line reported model_id=<primary> for an answer the
				// fallback model actually produced. EXECUTED by codex against
				// a real fallback-configured pair.
				receipt = mergeFallbackReceipt(receipt, fallbackReceipt)
				return interpreted, receipt, nil
			}
			// Both the primary and the fallback failed (CHAOS-3770 F4): the
			// receipt must not claim FallbackUsed/Outcome="fallback", which
			// means the fallback produced usable output -- it did not -- and
			// the caller must see the FINAL (fallback) leg's own
			// classification, not the primary's. fallbackReceipt.Outcome is
			// already the fallback's own receiptOutcomeForError result (the
			// fallback is itself a ModelRuntime that builds and returns a
			// receipt on every call, success or failure), so reusing it here
			// keeps the outcome vocabulary consistent with a direct call to
			// that same fallback.
			receipt.Outcome = fallbackReceipt.Outcome
			// The SECOND fallback-failure branch, and it needs the same
			// derivation as the one further down for the same reason. The
			// two differ only in why the PRIMARY failed -- here the primary
			// call itself failed to generate, there the primary produced
			// output that was semantically invalid -- but in both the
			// FALLBACK's error is what the caller receives, so the
			// fallback's rule is what the receipt and decision line must
			// name. Fixing only the lower branch left this one silent: a
			// review round constructed exactly this path (primary
			// generation failure + a fallback rejected for shape_invalid)
			// and observed receipt="" with no decision-line reason.
			if reason := contextfabric.InterpretationRejectionReasonOf(fallbackErr); reason != contextfabric.InterpretationRejectionUnclassified {
				rejectionReason = string(reason)
				receipt.InterpretationRejectionReason = reason
			}
			return contextfabric.InterpretedQuestion{}, receipt, fallbackErr
		}
		return contextfabric.InterpretedQuestion{}, receipt, classifiedErr
	}
	interpreted, err := output.toDomain(request.TimeContext)
	// H6: the caller's default TimeContext is substituted BEFORE Validate
	// whenever the model returns an empty Axis (toDomain, below this
	// function) -- record which happened so a defaulted axis is never
	// byte-identical, telemetry-wise, to a model-chosen one. Read directly
	// off the raw model output (not the post-toDomain InterpretedQuestion),
	// which is the only place that still distinguishes them.
	if strings.TrimSpace(output.TimeContext.Axis) == "" {
		axisSource = "default"
	} else {
		axisSource = "model"
	}
	if err != nil {
		receipt.Outcome = "invalid_output"
		primaryFailureClassification = receipt.Outcome
		if r.config.Fallback != nil {
			fallback, fallbackReceipt, fallbackErr := r.config.Fallback.InterpretQuestion(ctx, principal, request)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				// Same fix as the generation-error branch above -- write the
				// merged receipt back onto `receipt` so the deferred
				// telemetry read reports the fallback's own model identity.
				receipt = mergeFallbackReceipt(receipt, fallbackReceipt)
				return fallback, receipt, nil
			}
			// Both legs failed -- CHAOS-3770 F4 residual: this branch (the
			// primary's output was parseable but semantically invalid) had
			// the same bug as the generation-error branch above. See its
			// comment: report the fallback's own outcome/classification,
			// not the primary's stale invalid_output/ErrModelOutput.
			receipt.Outcome = fallbackReceipt.Outcome
			// The FALLBACK's error is the one the caller receives here, so
			// its reason is the one the receipt and the decision line must
			// report. A fallback that fails semantically (its own
			// interpretation violated a rule) carries a reason exactly like
			// a primary rejection does; without this the outer artifacts
			// were silent while the returned error was still correctly
			// classifiable -- the artifact/error disagreement this ticket
			// exists to remove. Empty when the fallback failed for a
			// non-semantic reason, which is correct: there is no rule to
			// name for a transport or rate-limit failure.
			if reason := contextfabric.InterpretationRejectionReasonOf(fallbackErr); reason != contextfabric.InterpretationRejectionUnclassified {
				rejectionReason = string(reason)
				receipt.InterpretationRejectionReason = reason
			}
			return contextfabric.InterpretedQuestion{}, receipt, fallbackErr
		}
		// CHAOS-3784 F1: this is the production ModelRuntime's OWN
		// Validate() rejection (interpreted.Validate() inside toDomain
		// above), not merely RuntimeQuestionInterpreter.Interpret's
		// defensive re-check -- it must carry the same
		// ErrInterpretationRejected/ModelBoundViolation classification, or
		// every real bound violation reaches the route as an
		// indistinguishable bare ErrModelOutput.
		rejection := contextfabric.ClassifyInterpretationRejection(interpreted, err)
		// Read the reason back OFF the classified error rather than
		// deriving it a second time here. One derivation, one source of
		// truth: a second call to the diagnosis mirror could drift from
		// what the error itself carries, and the value that reaches the
		// route (and any future consumer that inspects the error) would
		// then disagree with the value in the telemetry that is supposed
		// to explain it.
		reason := contextfabric.InterpretationRejectionReasonOf(rejection)
		rejectionReason = string(reason)
		receipt.InterpretationRejectionReason = reason
		return contextfabric.InterpretedQuestion{}, receipt, rejection
	}
	outputBytes, _ := json.Marshal(output)
	receipt.OutputDigest = contextfabric.DigestModelValue(outputBytes)
	receipt.Outcome = "success"
	// CHAOS-3900 W0 (SHADOW ONLY): sanitize-before-validate (F5 control
	// flow) -- the raw model pick is checked against the closed vocabulary
	// HERE, strictly after toDomain/interpreted.Validate() already
	// succeeded above, so an out-of-vocab window_class can never be the
	// reason a whole interpretation is rejected. Captured on the receipt
	// only (never on `interpreted`); nothing downstream of this call reads
	// it, so this line changes no serving-path behavior. Shared with
	// ParseInterpretationOutputWindow (exchange_support.go) so an
	// alternate transport's receipt carries an IDENTICAL classification,
	// never a transport-specific reimplementation that could silently
	// drift.
	receipt.WindowClass, receipt.WindowConfidence, receipt.WindowClassUnrecognized = sanitizeWindowOutput(output)
	// CHAOS-4632 (SHADOW ONLY): the same sanitize-after-validate step for
	// the family signals. Captured on the receipt only, never on
	// `interpreted`; nothing downstream of this call reads them to decide
	// anything, so these lines change no serving-path behavior. Shared
	// with ParseInterpretationOutputFamily (exchange_support.go) so an
	// alternate transport's receipt carries an IDENTICAL capture.
	applyFamilyCapture(&receipt, sanitizeFamilyOutput(output))
	applyFrameCapture(&receipt, sanitizeFrameOutput(output))
	return interpreted, receipt, nil
}

// interpretationFamilyCapture is the sanitized CHAOS-4632 capture from one
// raw interpretationOutput.
type interpretationFamilyCapture struct {
	Family                contextfabric.QuestionFamily
	FamilyUnrecognized    bool
	GroupKind             contextfabric.SubjectKind
	GroupKindUnrecognized bool
	ScopeAnchorTerm       string
	ScopeAnchorTruncated  bool
	// ScopeAnchorKind and RequestedKind are the two halves of the
	// precedence table's row-2 asymmetry test. Both reach the receipt --
	// see ModelExecutionReceipt's own field comment for why the labelled
	// measurement requires them to be durably captured rather than only
	// passed through in memory.
	//
	// Their `unrecognized` flags are kept for a reason an earlier revision
	// of this code got wrong: it DISCARDED both, on the theory that an
	// unrecognized qualifier is "not a signal in its own right". That is
	// false for the one number this slice exists to produce. The gating
	// measurement counts FALSE EMISSION, and discarding the flag makes a
	// model that emitted `requested_subject_kind="still_not_a_kind"`
	// indistinguishable from one that correctly emitted NOTHING -- so a
	// model inventing kinds would score as a model behaving perfectly,
	// and the gate would report a correctness number that is too high by
	// exactly the amount that matters.
	ScopeAnchorKind             contextfabric.SubjectKind
	ScopeAnchorKindUnrecognized bool
	RequestedKind               contextfabric.SubjectKind
	RequestedKindUnrecognized   bool
}

// sanitizeFamilyOutput applies the CHAOS-4632 sanitize step to a raw
// interpretationOutput's family fields. The SOLE place this happens --
// Runtime.InterpretQuestion and ParseInterpretationOutputFamily
// (exchange_support.go) both call it, so a genkit call and a non-genkit
// responder capture byte-identical values, never a transport-specific
// reimplementation that could silently drift. Exactly the arrangement
// sanitizeWindowOutput's own doc comment defends.
// subjectOperandOutput and subjectExpressionOutput are the model-facing
// shape of the CHAOS-4452 stage-2 subject expression.
//
// FLAT-ISH BY DESIGN, and the server ASSEMBLES the union. The domain type
// is a discriminated union with six variant pointers; asking a model to
// emit that shape reliably is a bet against the one thing this codebase
// has actually measured about interpreters -- CHAOS-4674 found the model
// dropping `subject_terms`, a field that has been on the contract for
// months, in 3 of 14 replicates. So the model emits a KIND plus the flat
// fields that kind uses, and sanitizeFrameOutput builds the union from the
// pick. That is the authorship rule working exactly as §13.2.1 states it:
// the model picks from closed vocabularies, the server validates the pick
// and owns the structure.
//
// NO jsonschema enum tags anywhere below, for the reason the subject-kind
// fields already carry: a schema enum makes the provider reject the WHOLE
// response for an out-of-set value, which turns a shadow capture into a
// new way to fail a real investigation. The vocabularies are stated in the
// prompt and enforced by the sanitizer.
type subjectOperandOutput struct {
	Kind        string   `json:"kind,omitempty"`
	Terms       []string `json:"terms,omitempty"`
	AnchorTerms []string `json:"anchor_terms,omitempty"`
	MemberKind  string   `json:"member_kind,omitempty"`
}

type subjectExpressionOutput struct {
	Kind        string                 `json:"kind,omitempty"`
	Terms       []string               `json:"terms,omitempty"`
	AnchorTerms []string               `json:"anchor_terms,omitempty"`
	MemberKind  string                 `json:"member_kind,omitempty"`
	GroupKind   string                 `json:"group_kind,omitempty"`
	Operands    []subjectOperandOutput `json:"operands,omitempty"`
}

type questionFrameOutput struct {
	Goals             []string                 `json:"goals,omitempty"`
	SubjectExpression *subjectExpressionOutput `json:"subject_expression,omitempty"`
	Temporal          string                   `json:"temporal,omitempty"`
	Emphasis          []string                 `json:"emphasis,omitempty"`
	Dimensions        []string                 `json:"dimensions,omitempty"`
}

// interpretationFrameCapture is what one interpret call produced for the
// frame, before validation. The counters are sanitize OUTCOMES and exist
// for the same reason the family capture's unrecognized flags do: a model
// inventing vocabulary members must be COUNTABLE, or the gating
// measurement cannot tell a model emitting nothing from a model emitting
// nonsense.
type interpretationFrameCapture struct {
	Frame            contextfabric.QuestionFrame
	Present          bool
	GoalsDropped     int
	TermsTruncated   int
	KindUnrecognized bool
}

// sanitizeFrameOutput is THE ONE PLACE the stage-2 frame is built from raw
// model output.
//
// SOLE SITE, exactly as sanitizeWindowOutput and sanitizeFamilyOutput are
// sole sites, and for a reason that has already bitten once: both
// Runtime.InterpretQuestion and the file-exchange transport
// (exchange_support.go) call this, so a genkit call and a non-genkit
// responder produce a BYTE-IDENTICAL capture from identical raw output. A
// transport-specific reimplementation would diverge silently, and the
// divergence would land in the very measurement this slice exists to make
// -- labelled semantic correctness of the whole expression -- where it
// would be indistinguishable from the model behaving differently.
//
// It never fails. Every unknown member is dropped and counted, every term
// is bounded and truncation counted, and an unrecognized expression kind
// leaves the union's discriminator empty for invariant I1 to reject by
// name. This runs strictly AFTER interpreted.Validate() has succeeded, and
// a shadow capture must never become a new way for a sound interpretation
// to fail.
func sanitizeFrameOutput(output interpretationOutput) interpretationFrameCapture {
	if output.QuestionFrame == nil {
		return interpretationFrameCapture{}
	}
	raw := *output.QuestionFrame
	capture := interpretationFrameCapture{Present: true}

	capture.Frame.Goals, capture.GoalsDropped = contextfabric.SanitizeInvestigationGoals(raw.Goals)
	capture.Frame.Temporal, _ = contextfabric.SanitizeTemporalIntent(raw.Temporal)
	capture.Frame.Emphasis, _ = contextfabric.SanitizeAnswerEmphasis(raw.Emphasis)
	capture.Frame.Dimensions, _ = contextfabric.SanitizeHealthDimensions(raw.Dimensions)

	if raw.SubjectExpression == nil {
		return capture
	}
	expression := *raw.SubjectExpression
	kind, unrecognized := contextfabric.SanitizeSubjectExpressionKind(expression.Kind)
	capture.KindUnrecognized = unrecognized
	capture.Frame.SubjectExpression.Kind = kind

	terms, truncated := contextfabric.SanitizeSubjectTerms(expression.Terms)
	capture.TermsTruncated += truncated
	anchors, anchorTruncated := contextfabric.SanitizeSubjectTerms(expression.AnchorTerms)
	capture.TermsTruncated += anchorTruncated
	memberKind, _ := contextfabric.SanitizeSubjectKind(expression.MemberKind)
	groupKind, _ := contextfabric.SanitizeSubjectKind(expression.GroupKind)

	// ONLY the variant the sanitized Kind names is populated. Building the
	// variant the model's own fields suggest instead would make the
	// pointer set disagree with the discriminator, which is exactly the
	// state invariant I1 exists to reject -- and a validator that never
	// sees the state it guards is a validator that cannot fail.
	switch kind {
	case contextfabric.SubjectExpressionNamed:
		capture.Frame.SubjectExpression.Named = &contextfabric.NamedSubjectExpression{Terms: terms}
	case contextfabric.SubjectExpressionDiscoveredKind:
		capture.Frame.SubjectExpression.Discovered = &contextfabric.DiscoveredSetExpression{MemberKind: memberKind}
	case contextfabric.SubjectExpressionChildrenOfScope:
		capture.Frame.SubjectExpression.Scoped = &contextfabric.ScopedSetExpression{AnchorTerms: anchors, MemberKind: memberKind}
	case contextfabric.SubjectExpressionGroupedMembers:
		capture.Frame.SubjectExpression.Grouped = &contextfabric.GroupedSetExpression{GroupKind: groupKind, MemberKind: memberKind}
	case contextfabric.SubjectExpressionOrganizationScope:
		org := &contextfabric.OrganizationScopeExpression{}
		if memberKind != "" {
			org.MemberKind = &memberKind
		}
		capture.Frame.SubjectExpression.Org = org
	case contextfabric.SubjectExpressionExplicitSet:
		operands := make([]contextfabric.SubjectOperand, 0, len(expression.Operands))
		for _, rawOperand := range expression.Operands {
			operand, operandTruncated := sanitizeOperandOutput(rawOperand)
			capture.TermsTruncated += operandTruncated
			operands = append(operands, operand)
		}
		capture.Frame.SubjectExpression.Explicit = &contextfabric.ExplicitSetExpression{Operands: operands}
	}
	return capture
}

func sanitizeOperandOutput(raw subjectOperandOutput) (contextfabric.SubjectOperand, int) {
	operand := contextfabric.SubjectOperand{}
	trimmed := strings.TrimSpace(raw.Kind)
	switch contextfabric.SubjectOperandKind(trimmed) {
	case contextfabric.SubjectOperandNamed:
		operand.Kind = contextfabric.SubjectOperandNamed
	case contextfabric.SubjectOperandScoped:
		operand.Kind = contextfabric.SubjectOperandScoped
	default:
		// An unrecognized operand kind leaves the discriminator empty and
		// both pointers nil, which invariant I19 rejects BY NAME. Guessing
		// a variant from whichever fields happen to be populated would
		// repair a malformed operand into a well-formed different one.
		return operand, 0
	}
	terms, truncated := contextfabric.SanitizeSubjectTerms(raw.Terms)
	anchors, anchorTruncated := contextfabric.SanitizeSubjectTerms(raw.AnchorTerms)
	memberKind, _ := contextfabric.SanitizeSubjectKind(raw.MemberKind)
	switch operand.Kind {
	case contextfabric.SubjectOperandNamed:
		operand.Named = &contextfabric.NamedSubjectExpression{Terms: terms}
	case contextfabric.SubjectOperandScoped:
		operand.Scoped = &contextfabric.ScopedSetExpression{AnchorTerms: anchors, MemberKind: memberKind}
	}
	return operand, truncated + anchorTruncated
}

func sanitizeFamilyOutput(output interpretationOutput) interpretationFamilyCapture {
	capture := interpretationFamilyCapture{}
	capture.Family, capture.FamilyUnrecognized = contextfabric.SanitizeQuestionFamily(output.QuestionFamily)
	capture.GroupKind, capture.GroupKindUnrecognized = contextfabric.SanitizeGroupKind(output.GroupKind)
	capture.ScopeAnchorTerm, capture.ScopeAnchorTruncated = contextfabric.SanitizeScopeAnchorTerm(output.ScopeAnchorTerm)
	// Both unrecognized flags are KEPT, not discarded -- see the struct's
	// own field comment for why discarding them would inflate the gating
	// measurement precisely where it must not be inflated.
	capture.ScopeAnchorKind, capture.ScopeAnchorKindUnrecognized = contextfabric.SanitizeGroupKind(output.ScopeAnchorKind)
	capture.RequestedKind, capture.RequestedKindUnrecognized = contextfabric.SanitizeGroupKind(output.RequestedSubjectKind)
	return capture
}

func applyFamilyCapture(receipt *contextfabric.ModelExecutionReceipt, capture interpretationFamilyCapture) {
	receipt.QuestionFamily = capture.Family
	receipt.QuestionFamilyUnrecognized = capture.FamilyUnrecognized
	receipt.GroupKind = capture.GroupKind
	receipt.GroupKindUnrecognized = capture.GroupKindUnrecognized
	receipt.ScopeAnchorTerm = capture.ScopeAnchorTerm
	receipt.ScopeAnchorTermTruncated = capture.ScopeAnchorTruncated
	receipt.ScopeAnchorKind = capture.ScopeAnchorKind
	receipt.ScopeAnchorKindUnrecognized = capture.ScopeAnchorKindUnrecognized
	receipt.RequestedSubjectKind = capture.RequestedKind
	receipt.RequestedSubjectKindUnrecognized = capture.RequestedKindUnrecognized
}

// applyFrameCapture stamps the raw, sanitized, NOT-YET-VALIDATED frame and
// its sanitize counters onto the receipt.
//
// The frame here is the model's PROPOSAL. Validation, normalization and
// the one bounded repair happen a layer up, where the telemetry port lives
// and where a refusal can be recorded -- see RuntimeQuestionInterpreter.
// Keeping the two apart is what lets the transport seam produce an
// identical proposal while only the engine decides what to do with it.
func applyFrameCapture(receipt *contextfabric.ModelExecutionReceipt, capture interpretationFrameCapture) {
	if !capture.Present {
		return
	}
	frame := capture.Frame
	receipt.QuestionFrame = &frame
	receipt.FrameGoalsDropped = capture.GoalsDropped
	receipt.FrameTermsTruncated = capture.TermsTruncated
	receipt.FrameKindUnrecognized = capture.KindUnrecognized
}

// sanitizeWindowOutput applies the CHAOS-3900 W0 sanitize-before-validate
// step to a raw model interpretationOutput's window fields. The SOLE place
// this sanitization happens -- both Runtime.InterpretQuestion (above) and
// ParseInterpretationOutputWindow (exchange_support.go, the file-exchange/
// alternate-transport seam) call this, so a genkit call and a non-genkit
// responder capture byte-identical classifications from the identical raw
// output shape.
func sanitizeWindowOutput(output interpretationOutput) (contextfabric.WindowClass, contextfabric.WindowConfidence, bool) {
	class, unrecognized := contextfabric.SanitizeWindowClass(output.WindowClass)
	confidence := contextfabric.SanitizeWindowConfidence(output.WindowConfidence)
	return class, confidence, unrecognized
}

func (r *Runtime) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, errors.New("authenticated organization is required")
	}
	payload := synthesisInputFromDomain(principal.OrgID, input)
	encoded, err := boundedJSON(payload, r.config.MaxInputBytes)
	if err != nil {
		return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, err
	}

	// CHAOS-3889 (H6/H7/H8): see the matching comment in InterpretQuestion.
	// grounding is set from whichever SynthesisDraft (primary or fallback)
	// actually reaches a successful return; it stays zero-valued on every
	// failure path, since no draft was ever produced to count.
	var (
		receipt                      contextfabric.ModelExecutionReceipt
		primaryFailureClassification string
		grounding                    synthesisGroundingCounts
		// CHAOS-4522: rejectionReason names WHICH rule rejected a draft,
		// from the closed contextfabric.SynthesisRejectionReason
		// vocabulary. Empty on every non-rejection path (success,
		// fallback, transport failure), so the decision line carries the
		// field only when there is a rejection to name.
		rejectionReason string
		// factGroupSize is how many canonical facts shared the REJECTING
		// claim's (Kind, Subject) -- the ambiguity groundClaim closed over
		// for that one claim. Reported beside the reason because
		// "claim_field_unobserved at fact_group_size=17" and the same
		// reason at 1 are different defects: the first is a multi-fact
		// grounding problem, the second is a model claiming a field that
		// genuinely does not exist. Logged as fact_group_size, not _max:
		// codex R1 finding 1 -- a maximum over the draft would describe a
		// claim ValidateAgainst never reached.
		factGroupSize int
		// groundedBeyondFirst counts the admitted claims the widened
		// CHAOS-4522 closure actually RESCUED -- each would have been
		// rejected by the old first-match-wins lookup (codex R3 finding 2).
		// factGroupSize alone cannot say that: a large group whose first
		// member already matched needed no closure at all.
		groundedBeyondFirst int
	)
	// setDiagnostics populates the two rejection fields from the error whose
	// outcome the receipt reports -- and ONLY when that error really is a
	// synthesis rejection (codex R2 finding 1). A transport or rate-limit
	// failure is not a rejection: labelling one `rejection_reason=
	// unclassified` would assert that a rule rejected the draft when no
	// draft was ever judged. Leaving the fields absent says the true thing.
	// setGroundingSignal records the grounding decision basis for a draft
	// that was ACCEPTED -- on the primary path and on either fallback-success
	// path (codex R3 finding 1: a fallback draft has claims and groups of its
	// own, and clearing the signal there left `outcome=fallback` lines with
	// grounding counts but no grounding basis, undiagnosable for a custom or
	// non-logging fallback).
	setGroundingSignal := func(draft contextfabric.SynthesisDraft) {
		factGroupSize = contextfabric.MaxClaimFactGroupSize(input.Facts.Facts, draft.ClaimedFacts)
		groundedBeyondFirst = contextfabric.ClaimsGroundedBeyondFirstGroupMember(input.Facts.Facts, draft.ClaimedFacts)
	}
	setDiagnostics := func(cause error) {
		var rejection *contextfabric.SynthesisRejection
		if !errors.As(cause, &rejection) {
			rejectionReason, factGroupSize = "", 0
			return
		}
		rejectionReason = string(contextfabric.SynthesisRejectionReasonOf(cause))
		factGroupSize = contextfabric.SynthesisFactGroupSizeOf(cause)
	}
	defer func() {
		r.logSynthesizeDecision(ctx, principal.OrgID, input.Request.RequestID, receipt, primaryFailureClassification, grounding, rejectionReason, factGroupSize, groundedBeyondFirst)
	}()

	started := r.now().UTC()
	var output synthesisOutput
	var usage contextfabric.ModelUsage
	attempts, generationErr := r.withRetry(ctx, func(callCtx context.Context) error {
		var err error
		output, usage, err = r.generator.Synthesize(callCtx, generationRequest{
			Model: r.config.ModelRef, System: synthesisSystemPrompt, Prompt: string(encoded),
		})
		return err
	})
	completed := r.now().UTC()
	var classifiedErr error
	if generationErr != nil {
		classifiedErr = classifyModelError(generationErr)
	}
	receipt = r.receipt(contextfabric.ModelOperationSynthesize, r.config.SynthesisPromptVersion, started, completed, attempts, encoded, nil, usage, classifiedErr)
	// RequestID correlates the durable receipt row back to this
	// investigation -- see the matching comment in InterpretQuestion.
	receipt.RequestID = input.Request.RequestID
	if generationErr != nil {
		primaryFailureClassification = receipt.Outcome
		if r.config.Fallback != nil {
			draft, fallbackReceipt, fallbackErr := r.config.Fallback.SynthesizeAnswer(ctx, principal, input)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				grounding = groundingCountsFrom(draft)
				setGroundingSignal(draft)
				return draft, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
			// See the matching comment in InterpretQuestion: both legs
			// failed, so the receipt must reflect the fallback's own
			// (final) outcome and the caller must see its classification,
			// not the primary's.
			//
			// codex R2 finding 1: the primary failed in TRANSPORT here, so
			// no draft of its own was ever judged -- but the fallback's
			// failure may well be a synthesis rejection, and this branch
			// previously populated no diagnostics at all, silently dropping
			// the only rule name in play.
			receipt.Outcome = fallbackReceipt.Outcome
			setDiagnostics(fallbackErr)
			return contextfabric.SynthesisDraft{}, receipt, fallbackErr
		}
		return contextfabric.SynthesisDraft{}, receipt, classifiedErr
	}
	draft, err := output.toDomain()
	if err == nil {
		// CHAOS-4355 follow-up (tolerance): this is the production
		// ModelRuntime's OWN ValidateAgainst call -- the actual live 422
		// source (see the matching comment on the classification below) --
		// so the strip belongs HERE, before that call, not only in
		// RuntimeAnswerSynthesizer.Synthesize's defensive re-check, which
		// never even runs when this call already rejects. Rows are
		// attached server-side from the SAME canonical fact a claim
		// cites (contextfabric.attachCanonicalRows), never from the
		// model, so a model-authored Rows array here is pure noise: never
		// a reason to reject an otherwise-valid answer, and never
		// content this receipt/log line should reflect either way.
		var stripped int
		draft.ClaimedFacts, stripped = contextfabric.StripModelAuthoredClaimedFactTableContent(draft.ClaimedFacts)
		if stripped > 0 && r.config.Telemetry != nil {
			r.config.Telemetry.RecordModelRowsStripped(ctx, principal, stripped)
		}
		err = draft.ValidateAgainst(input)
	}
	if err != nil {
		receipt.Outcome = "invalid_output"
		primaryFailureClassification = receipt.Outcome
		// CHAOS-4522: the reason and the rejecting claim's fact-group size
		// are carried BY the error, attached at the statement that
		// rejected -- never re-derived here from the shape of the
		// resulting draft, which is a consequence of the rejecting branch
		// and not an observation of it (AGENTS.md verification rule 1:
		// assert the mechanism, not the outcome). toDomain's own
		// required-field rejection carries its reason the same way, so
		// both legs above are covered by this one read.
		//
		// codex R1 finding 1: the group size must be the REJECTING claim's,
		// not a maximum over the draft. ValidateAgainst short-circuits, so
		// a scan of every claim can report a LATER claim's group -- one
		// that was never evaluated -- which would make the documented
		// "1 versus >1" reading wrong in exactly the case it diagnoses.
		setDiagnostics(err)
		if r.config.Fallback != nil {
			fallback, fallbackReceipt, fallbackErr := r.config.Fallback.SynthesizeAnswer(ctx, principal, input)
			if fallbackErr == nil {
				receipt.FallbackUsed = true
				receipt.Outcome = "fallback"
				// codex R1 finding 2: the decision line's outcome and its
				// rejection diagnostics must describe the SAME leg. The
				// fallback succeeded, so this call did NOT end in a
				// rejection and must not carry one -- the primary's
				// failure is already reported by
				// primaryFailureClassification, which exists for exactly
				// that.
				//
				// codex R3 finding 1: the REJECTION reason is cleared, but
				// the GROUNDING signal is then recomputed from the
				// fallback's own draft rather than left at zero -- that
				// draft has claims and fact groups of its own, and the
				// line already reports its grounding counts.
				rejectionReason = ""
				setGroundingSignal(fallback)
				grounding = groundingCountsFrom(fallback)
				return fallback, mergeFallbackReceipt(receipt, fallbackReceipt), nil
			}
			// Both legs failed -- see the matching comment in
			// InterpretQuestion's semantic-invalid-output branch (CHAOS-3770
			// F4 residual). codex R1 finding 2: the receipt reports the
			// FALLBACK's outcome and the caller receives the FALLBACK's
			// error, so the diagnostics must describe that leg too, not the
			// primary's stale ones.
			receipt.Outcome = fallbackReceipt.Outcome
			setDiagnostics(fallbackErr)
			return contextfabric.SynthesisDraft{}, receipt, fallbackErr
		}
		// CHAOS-3784 F1: this is the production ModelRuntime's OWN
		// draft.ValidateAgainst(input) call above (it has the
		// SynthesisInput the check needs, unlike
		// RuntimeAnswerSynthesizer.Synthesize's defensive re-check), so it
		// must carry the same ErrSynthesisRejected/ModelBoundViolation
		// classification -- see the matching comment in InterpretQuestion.
		return contextfabric.SynthesisDraft{}, receipt, contextfabric.ClassifySynthesisRejection(draft, input, err)
	}
	outputBytes, _ := json.Marshal(output)
	receipt.OutputDigest = contextfabric.DigestModelValue(outputBytes)
	receipt.Outcome = "success"
	grounding = groundingCountsFrom(draft)
	// codex R2 finding 4: grounding a claim against a LATER fact of its
	// (Kind, Subject) group is an outcome-changing branch -- it turns what
	// used to be a rejection into an answer -- so a SUCCESSFUL run must
	// record that it fired, not only a failing one. Without this the
	// closure was diagnosable only when it did not save the answer.
	setGroundingSignal(draft)
	return draft, receipt, nil
}

// PhraseStructureOffers (CHAOS-4171 PR2) implements
// contextfabric.OfferPhrasingModelRuntime -- the raw, mechanical model-call
// boundary for the SECOND bounded model call, mirroring InterpretQuestion's
// own shape but with none of its fallback-chain or domain-validation
// machinery: offer phrasing has no fallback model (ratified design: a
// failure degrades to structural, never to a second model), and the
// closed-vocabulary guard that decides whether this raw output is usable
// lives one layer up, in contextfabric.RuntimeOfferPhraser -- this method
// hands back exactly what the model said, unguarded, plus a receipt that
// always reflects the ATTEMPT (success/failure), never the guard's later
// verdict on the content.
func (r *Runtime) PhraseStructureOffers(ctx context.Context, principal storage.Principal, input contextfabric.StructureOfferPhrasingInput) (contextfabric.StructureOfferPhrasingDraft, contextfabric.ModelExecutionReceipt, error) {
	if strings.TrimSpace(principal.OrgID) == "" {
		return contextfabric.StructureOfferPhrasingDraft{}, contextfabric.ModelExecutionReceipt{}, errors.New("authenticated organization is required")
	}
	if len(input.Options) == 0 {
		return contextfabric.StructureOfferPhrasingDraft{}, contextfabric.ModelExecutionReceipt{}, errors.New("offer phrasing requires at least one offered option")
	}
	payload := phrasingInput{Options: make([]phrasingOption, 0, len(input.Options))}
	for _, opt := range input.Options {
		payload.Options = append(payload.Options, phrasingOption{OptionID: opt.OptionID, Member: string(opt.Member), Kind: string(opt.Kind), Label: opt.Label})
	}
	encoded, err := boundedJSON(payload, r.config.MaxInputBytes)
	if err != nil {
		return contextfabric.StructureOfferPhrasingDraft{}, contextfabric.ModelExecutionReceipt{}, err
	}
	started := r.now().UTC()
	var output phrasingOutput
	var usage contextfabric.ModelUsage
	attempts, generationErr := r.withRetry(ctx, func(callCtx context.Context) error {
		var err error
		output, usage, err = r.generator.Phrase(callCtx, generationRequest{
			Model: r.config.PhrasingModelRef, System: phrasingSystemPrompt, Prompt: string(encoded),
		})
		return err
	})
	completed := r.now().UTC()
	var classifiedErr error
	if generationErr != nil {
		classifiedErr = classifyModelError(generationErr)
	}
	receipt := r.receipt(contextfabric.ModelOperationPhraseOffers, r.config.PhrasingPromptVersion, started, completed, attempts, encoded, nil, usage, classifiedErr)
	receipt.Model = r.config.PhrasingModel
	receipt.ModelVersion = r.config.PhrasingModelVersion
	// RequestID correlates this receipt back to the investigation that
	// triggered it, the same audit purpose InterpretQuestion's own
	// receipt.RequestID stamping serves -- stamped unconditionally, on
	// every return path below, mirroring InterpretQuestion's own
	// "survives every return path" comment.
	receipt.RequestID = input.RequestID
	if generationErr != nil {
		return contextfabric.StructureOfferPhrasingDraft{}, receipt, classifiedErr
	}
	outputBytes, _ := json.Marshal(output)
	receipt.OutputDigest = contextfabric.DigestModelValue(outputBytes)
	receipt.Outcome = "success"
	draft := contextfabric.StructureOfferPhrasingDraft{Phrasings: make([]contextfabric.StructureOfferPhrasingEntry, 0, len(output.Phrasings))}
	for _, entry := range output.Phrasings {
		draft.Phrasings = append(draft.Phrasings, contextfabric.StructureOfferPhrasingEntry{OptionID: entry.OptionID, Phrasing: entry.Phrasing})
	}
	return draft, receipt, nil
}

func (r *Runtime) withRetry(ctx context.Context, fn func(context.Context) error) (int, error) {
	var last error
	for attempt := 1; attempt <= r.config.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return attempt, err
		}
		callCtx, cancel := context.WithTimeout(ctx, r.config.Timeout)
		err := fn(callCtx)
		cancel()
		if err == nil {
			return attempt, nil
		}
		last = err
		if !retryable(err) || attempt == r.config.MaxAttempts {
			return attempt, err
		}
	}
	return r.config.MaxAttempts, last
}

// receipt builds the content-safe execution receipt for one generation
// attempt. err, when non-nil, must already be the classifyModelError output
// (or a context cancellation/deadline error) so the receipt Outcome reflects
// the same rate-limit/invalid-output/unavailable classification callers see
// on the returned Go error.
func (r *Runtime) receipt(operation contextfabric.ModelOperation, promptVersion string, started, completed time.Time, attempts int, input, output []byte, usage contextfabric.ModelUsage, err error) contextfabric.ModelExecutionReceipt {
	outcome := receiptOutcomeForError(err)
	receipt := contextfabric.ModelExecutionReceipt{
		Operation: operation, Provider: r.config.Provider, Model: r.config.Model,
		ModelVersion: r.config.ModelVersion, PromptVersion: promptVersion,
		SchemaVersion: r.config.SchemaVersion, EvaluatorVersion: r.config.EvaluatorVersion,
		StartedAt: started, CompletedAt: completed, Attempts: attempts,
		InputDigest: contextfabric.DigestModelValue(input), Usage: usage, Outcome: outcome,
	}
	if len(output) > 0 {
		receipt.OutputDigest = contextfabric.DigestModelValue(output)
	}
	return receipt
}

// receiptOutcomeForError maps a nil or already-classified generation error
// to the receipt Outcome vocabulary from ADR 0008 (success, fallback,
// invalid_output, unavailable), plus rate_limited for the finer-grained
// classification callers get from classifyModelError. err is expected to be
// nil or the direct result of classifyModelError; a context cancellation or
// deadline error (passed through classifyModelError unchanged) counts as
// unavailable from a receipts standpoint, since the runtime's own bounded
// deadline is what stopped the call.
func receiptOutcomeForError(err error) string {
	switch {
	case err == nil:
		return "pending_validation"
	case errors.Is(err, contextfabric.ErrModelRateLimited):
		return "rate_limited"
	case errors.Is(err, contextfabric.ErrModelOutput):
		return "invalid_output"
	default:
		return "unavailable"
	}
}

// decisionEventMessage is the fixed message every CHAOS-3889 decision-event
// line uses -- one literal string, never interpolated with any request- or
// model-derived text, so grepping/alerting on it is stable regardless of
// field values.
const decisionEventMessage = "context fabric model decision"

// decisionOrgIDHash returns a bounded, non-reversible hash of orgID for the
// decision-event log line: 6 bytes of SHA-256, hex-encoded. This mirrors
// devhealthsource.redactOrg's own org-id-for-logs convention
// (internal/contextfabric/devhealthsource/clickhouse.go) byte-for-byte, so a
// hash computed by either package means the same thing in log aggregation.
// It is duplicated here rather than imported: redactOrg is unexported, and
// devhealthsource is a source-ingest package genkitruntime has no other
// reason to depend on.
func decisionOrgIDHash(orgID string) string {
	sum := sha256.Sum256([]byte(orgID))
	return hex.EncodeToString(sum[:6])
}

// logInterpretDecision emits the CHAOS-3889 decision-event line for one
// InterpretQuestion call (H6/H7). Every field is a count, enum, id, or bool:
// request_id/org_id_hash are opaque correlation identifiers (the latter
// irreversibly hashed), operation/outcome/axis_source/
// primary_failure_classification are closed vocabularies already enforced
// elsewhere (ModelOperation, receiptOutcomeForError, the model|default axis
// source, and receiptOutcomeForError's own rate_limited/invalid_output/
// unavailable set), and attempts/fallback_used are the receipt's own
// bounded numeric/boolean fields. NONE of the model's prompt, question
// text, extracted subject/term text, or output text ever reaches this call
// -- see TestDecisionEventNeverCarriesCorpusText for the standing
// assertion. Unconditionally logged at slog.LevelInfo: no new config knob
// gates it, only the configured Logger's own handler level (Config.Logger's
// doc comment).
// safeLogRequestID is DEFENCE IN DEPTH at the log sink. It is not the
// guard that makes this value safe, and an earlier version of this comment
// wrongly claimed it was.
//
// The correction, because the false version shipped: the request id reaching
// here is NOT client-controlled. internal/api/app.go's request-id middleware
// strips CR/LF, rejects every other control character
// (isSafeRequestIDHeaderValue), bounds the length, and falls back to a
// server-minted id when any of that fails; observability.parseRequestID
// additionally enforces a strict `req_` + 32-hex format. The Context Fabric
// route then OVERWRITES the decoded body's RequestID with that sanitized
// context value (context_fabric_routes.go: `request.RequestID =
// RequestID(r.Context())`) BEFORE Validate() runs -- which is why the v1
// contract validating RequestID by length alone is not the hole it looks
// like in isolation. That middleware comment records a live repro of
// "evil\nFAKE_LOG_LINE=injected" already being replaced.
//
// So CodeQL's go/log-injection alert on the decision line is a FALSE
// POSITIVE: the sanitizer exists and is thorough, it simply is not one
// CodeQL models as a barrier. This function does not clear the alert either
// (verified: still `state=open` on the tip that added it), so it is kept
// only because a sink-side guard costs nothing and protects any future
// caller that reaches this logger without passing through that middleware --
// never as evidence that the value was dangerous.
//
// Replacement, not rejection: a request id is a correlation handle, and
// dropping the field would destroy the correlation this telemetry exists
// for.
func safeLogRequestID(requestID string) string {
	if len(requestID) > 256 {
		requestID = requestID[:256]
	}
	sanitized := []rune(requestID)
	for i, r := range sanitized {
		if r < 0x20 || r > 0x7e {
			sanitized[i] = '?'
		}
	}
	return string(sanitized)
}

func (r *Runtime) logInterpretDecision(ctx context.Context, orgID, requestID string, receipt contextfabric.ModelExecutionReceipt, primaryFailureClassification, axisSource string, decodingSeed int64, sample int, rejectionReason string) {
	fields := []any{
		"request_id", safeLogRequestID(requestID),
		"org_id_hash", decisionOrgIDHash(orgID),
		"operation", string(receipt.Operation),
		"outcome", receipt.Outcome,
		"attempts", receipt.Attempts,
		"fallback_used", receipt.FallbackUsed,
		"primary_failure_classification", primaryFailureClassification,
		"axis_source", axisSource,
		// CHAOS-4631: the exact decoding config chaos4631InterpretDecodingConfig
		// applied to this call (seed + which sample index derived it), logged
		// as concrete values (never the request/response payload) so replay
		// can prove determinism was actually requested, and which sample it
		// was, without re-inspecting the receipt. This function only ever
		// logs the interpret operation, so this is unconditional, unlike
		// logSynthesizeDecision's appended-only fields. No temperature field:
		// this model family rejects a non-default temperature outright (see
		// chaos4631InterpretDecodingConfig's doc comment for the executed
		// repro), so seed is the only decoding parameter this change applies.
		"decoding_seed", decodingSeed,
		"sample", sample,
		// CHAOS-4631 ticket point 4: model id and prompt version recorded in
		// telemetry, not on the result -- receipt.Model/ModelVersion/
		// PromptVersion are already the durable ModelExecutionReceipt sink's
		// own fields (model_runtime.go), so this is the SAME values on the
		// log-line surface too, closing the "diagnosable from the run's own
		// completed artifacts alone" bar for this decision event specifically
		// rather than only the separate receipt sink.
		"model_id", receipt.Model,
		"model_version", receipt.ModelVersion,
		"prompt_version", receipt.PromptVersion,
	}
	// Appended only when a rejection actually happened, exactly as
	// logSynthesizeDecision does with its own rejection_reason: an
	// unconditional field would put rejection_reason="" on every
	// successful interpretation, which reads as "rejected for no reason"
	// in aggregation and makes a count of rejections by rule impossible
	// to write without also filtering the empty case.
	//
	// The value is a closed-vocabulary constant returned by the
	// contracts/v1 canonical table, never model- or question-derived text
	// -- see that table's doc comment for why the table returns its own
	// constant rather than the caller's input, and
	// TestDecisionEventNeverCarriesCorpusText for the standing assertion
	// that this whole line stays corpus-free.
	if rejectionReason != "" {
		fields = append(fields, "rejection_reason", rejectionReason)
	}
	r.config.Logger.InfoContext(ctx, decisionEventMessage, fields...)
}

// synthesisGroundingCounts is H8's fix: how many of the synthesis draft's
// grounding collections actually carried content, so a draft that grounded
// 1-of-10 available facts is no longer telemetry-identical to one that used
// all 10. Findings aggregates RemainingWork+ReadinessGaps+Conflicts (all
// contextfabric.Finding-typed) into the single "findings" count the H8 field
// list names, rather than three separate counts.
type synthesisGroundingCounts struct {
	Drivers      int
	Findings     int
	Claims       int
	EvidenceRefs int
}

// groundingCountsFrom derives synthesisGroundingCounts from the ACTUAL
// SynthesisDraft a caller is about to receive -- the primary's on a direct
// success, or the fallback's own draft on a fallback-success return -- never
// from the raw model output, so the counts always describe what really
// reached the caller.
func groundingCountsFrom(draft contextfabric.SynthesisDraft) synthesisGroundingCounts {
	return synthesisGroundingCounts{
		Drivers:      len(draft.Drivers),
		Findings:     len(draft.RemainingWork) + len(draft.ReadinessGaps) + len(draft.Conflicts),
		Claims:       len(draft.ClaimedFacts),
		EvidenceRefs: len(draft.EvidenceRefIDs),
	}
}

// logSynthesizeDecision is logInterpretDecision's SynthesizeAnswer
// counterpart (H7/H8). See logInterpretDecision's doc comment for the
// corpus-safety and log-level-gating rationale, which applies identically
// here.
func (r *Runtime) logSynthesizeDecision(ctx context.Context, orgID, requestID string, receipt contextfabric.ModelExecutionReceipt, primaryFailureClassification string, grounding synthesisGroundingCounts, rejectionReason string, factGroupSize, groundedBeyondFirst int) {
	fields := []any{
		"request_id", safeLogRequestID(requestID),
		"org_id_hash", decisionOrgIDHash(orgID),
		"operation", string(receipt.Operation),
		"outcome", receipt.Outcome,
		"attempts", receipt.Attempts,
		"fallback_used", receipt.FallbackUsed,
		"primary_failure_classification", primaryFailureClassification,
		"drivers", grounding.Drivers,
		"findings", grounding.Findings,
		"claims", grounding.Claims,
		"evidence_refs", grounding.EvidenceRefs,
	}
	// CHAOS-4522: appended, never unconditional, so a successful or
	// transport-failed call's line stays byte-identical to its pre-4522
	// shape and only a rejection carries the two new fields. Both values
	// are closed/bounded -- a vocabulary member and a count -- so the
	// corpus-safety guarantee in this function's doc comment is unchanged.
	if rejectionReason != "" {
		fields = append(fields, "rejection_reason", rejectionReason)
	}
	// Emitted independently of the reason (codex R2 finding 4): a SUCCESS
	// carries a group size and no reason, a rejection normally carries both,
	// and a non-claim rejection carries a reason and no group. Pairing them
	// would have made the success case unreportable.
	if factGroupSize > 0 {
		fields = append(fields, "fact_group_size", factGroupSize)
		// Emitted alongside the size on every accepted draft, INCLUDING
		// zero (codex R3 finding 2): 0 means the widened closure changed
		// nothing about this answer however large its groups were, and
		// that is the distinguishing fact, not an absence worth hiding.
		fields = append(fields, "grounded_beyond_first", groundedBeyondFirst)
	}
	r.config.Logger.InfoContext(ctx, decisionEventMessage, fields...)
}

func boundedJSON(value any, maximum int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode bounded model input: %w", err)
	}
	if len(encoded) > maximum {
		return nil, fmt.Errorf("model input exceeds %d bytes", maximum)
	}
	return encoded, nil
}

// retryable decides whether a generation failure may be retried with the
// EXACT SAME encoded payload. It classifies ONLY on structured status/code
// (a Genkit gRPC-style status, or context.DeadlineExceeded) -- never by
// sniffing an error's message text (CHAOS-3770 F2).
//
// That restriction is deliberate, not merely cautious: the OpenAI SDK's own
// error type (openai-go/internal/apierror.Error, aliased as openai.Error)
// formats Error() as "%s %q: %d %s %s" with the raw provider response body
// as the last verbatim component, and compat_oai/generate.go wraps that
// error with a plain fmt.Errorf rather than a *core.GenkitError. So by the
// time an error from the real production transport reaches this function,
// it is routinely UNSTRUCTURED, and its message can legitimately be a
// non-transient validation failure whose body happens to contain a word
// like "timeout" or the digits "502" -- retrying that with the identical
// payload would resubmit a request that can never succeed, violating the
// no-retry-same-input rule (operations.md) for no benefit. An unstructured
// error is therefore never retried by this function, full stop; genuinely
// transient transport conditions (connection resets, 429/5xx, provider
// timeouts) remain covered by the OpenAI SDK's own transport-level retry
// loop (ACR_CONTEXT_FABRIC_MODEL_MAX_TRANSPORT_RETRIES), which runs BEFORE
// its terminal error ever reaches genkitruntime.
func retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Genkit (and well-behaved plugins) surface transport and provider
	// failures as *core.GenkitError with a gRPC-style status. Prefer that
	// structured signal over string sniffing when it is present -- and once
	// present, trust it completely: an unmatched status (e.g. INTERNAL, or
	// anything else Genkit's own validation paths raise) is not retryable
	// either, never falling through to string matching on its own Message.
	var genkitErr *core.GenkitError
	if errors.As(err, &genkitErr) {
		switch genkitErr.Status {
		case core.RESOURCE_EXHAUSTED, core.UNAVAILABLE, core.DEADLINE_EXCEEDED, core.ABORTED:
			return true
		default:
			// Includes INVALID_ARGUMENT (malformed request or
			// schema-incompatible output; the model is not going to
			// succeed against the same input, so this fails closed
			// instead of retrying -- ADR 0008: invalid output fails
			// closed) and every other structured status.
			return false
		}
	}
	// No structured signal: not retryable. See the function doc comment.
	return false
}

// contextFabricSanitizedStatusPattern matches ONLY the fixed, ACR-controlled
// token sanitizeProviderErrorBody embeds in every sanitized non-2xx provider
// response ("provider response redacted by ACR (status <code> <text>)"). No
// real provider supplies this text, and no incidental error-string component
// (a request URL, an ephemeral test port, a BYO endpoint's hostname or path)
// can produce it, so a match here is a reliable anchor for the true HTTP
// status -- unlike a bare digit scan over the whole error text, which can
// collide with unrelated numbers embedded elsewhere in the string (see
// classifyModelError).
var contextFabricSanitizedStatusPattern = regexp.MustCompile(`provider response redacted by ACR \(status (\d{3})\b`)

// classifyModelError maps a raw generation error into one of the ACR-owned
// model runtime sentinels (ErrModelRateLimited, ErrModelOutput,
// ErrModelUnavailable) so callers can apply distinct handling and alerting
// per CHAOS-3756. Only the error class and provider status name are
// preserved in the wrapped message; the original error (which may carry
// provider response fragments) is intentionally dropped rather than
// wrapped, so raw prompt or response content never reaches logs, receipts,
// or telemetry built from this error.
func classifyModelError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var genkitErr *core.GenkitError
	if errors.As(err, &genkitErr) {
		switch genkitErr.Status {
		case core.RESOURCE_EXHAUSTED:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelRateLimited, genkitErr.Status)
		case core.INVALID_ARGUMENT:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelOutput, genkitErr.Status)
		case core.INTERNAL:
			// Genkit reports its own structured-output/schema mismatch as
			// INTERNAL (see ai.jsonHandler.ParseMessage), so INTERNAL is
			// ambiguous between a genuine provider fault and invalid model
			// output. Distinguish by the fixed prefix Genkit always uses for
			// the schema-mismatch case; never inspect or forward the rest of
			// the message, which may quote the malformed output.
			if strings.HasPrefix(genkitErr.Message, "model failed to generate output matching expected schema") {
				return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelOutput, genkitErr.Status)
			}
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelUnavailable, genkitErr.Status)
		case core.UNAVAILABLE, core.DEADLINE_EXCEEDED, core.ABORTED:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelUnavailable, genkitErr.Status)
		default:
			return fmt.Errorf("%w: provider status %s", contextfabric.ErrModelUnavailable, genkitErr.Status)
		}
	}
	text := err.Error()
	// Prefer the ACR-controlled sanitized-status token when present: it is
	// the ONLY reliable signal in an unstructured error string, because
	// nothing else can produce it. Never fall back to scanning the raw text
	// for a bare "429" -- the OpenAI SDK's apierror.Error.Error() embeds the
	// full request URL verbatim, so an ephemeral test port or a BYO
	// endpoint's own hostname/port/path containing those digits would
	// misclassify an unrelated status as rate-limited.
	//
	// Take the LAST match, not the first: the request URL precedes the
	// response body in the SDK's error format, so a BYO endpoint whose URL
	// happens to embed the literal token text (an adversarial or coincidental
	// path segment) must not shadow the real sanitized token that always
	// follows it.
	if all := contextFabricSanitizedStatusPattern.FindAllStringSubmatch(text, -1); len(all) > 0 {
		m := all[len(all)-1]
		if code, convErr := strconv.Atoi(m[1]); convErr == nil && code == 429 {
			return fmt.Errorf("%w: model generation failed", contextfabric.ErrModelRateLimited)
		}
		return fmt.Errorf("%w: model generation failed", contextfabric.ErrModelUnavailable)
	}
	// No sanitized status token means there was no HTTP response object at
	// all (connection refused, DNS failure, etc.). Fall back to
	// word-anchored phrase checks -- deliberately excluding a bare "429"
	// digit check, which is not safe against incidental numbers elsewhere in
	// the text.
	lower := strings.ToLower(text)
	for _, phrase := range []string{"rate limit", "too many requests", "quota exceeded"} {
		if strings.Contains(lower, phrase) {
			return fmt.Errorf("%w: model generation failed", contextfabric.ErrModelRateLimited)
		}
	}
	return fmt.Errorf("%w: model generation failed", contextfabric.ErrModelUnavailable)
}

// mergeFallbackReceipt builds the receipt returned for a SUCCESSFUL
// fallback call: started from primary (the failed/rejected leg's own
// receipt, for its timing/attempts/input digest), but with Provider/Model/
// ModelVersion overwritten from fallback -- the leg that actually produced
// the returned output (CHAOS-3786 Bug A). Before this fix, those three
// fields stayed the PRIMARY's, so every fallback-answered
// InterpretQuestion/SynthesizeAnswer call reported (and
// RuntimeAnswerSynthesizer.Synthesize then PERSISTED, as
// Versions.ModelIdentity) an identity that never produced the content --
// silently mislabeling every fallback answer as if the primary had
// produced it. Provider is expected to already be equal between the two
// legs (modelprovider.Config/ResolvedOrgModelConfig share one Provider
// field across Model and FallbackModel), but it is copied here too rather
// than assumed, so this stays correct even if that constraint ever
// loosens.
func mergeFallbackReceipt(primary, fallback contextfabric.ModelExecutionReceipt) contextfabric.ModelExecutionReceipt {
	primary.FallbackUsed = true
	primary.Provider = fallback.Provider
	primary.Model = fallback.Model
	primary.ModelVersion = fallback.ModelVersion
	if fallback.OutputDigest != "" {
		primary.OutputDigest = fallback.OutputDigest
	}
	primary.Usage.InputTokens += fallback.Usage.InputTokens
	primary.Usage.OutputTokens += fallback.Usage.OutputTokens
	primary.Usage.TotalTokens += fallback.Usage.TotalTokens
	return primary
}

type interpretationInput struct {
	Question             string                              `json:"question"`
	Conversation         []contextfabric.ConversationTurn    `json:"conversation,omitempty"`
	SubjectHints         []contextfabric.SubjectHint         `json:"subject_hints,omitempty"`
	RequestedScope       contextfabric.RequestedScope        `json:"requested_scope,omitempty"`
	TimeContext          contextfabric.TimeContext           `json:"time_context"`
	PriorSubjectReceipts []contextfabric.BoundSubjectReceipt `json:"prior_subject_receipts,omitempty"`
}

type interpretationOutput struct {
	Shape               string                  `json:"shape" jsonschema:"enum=single_subject,enum=explicit_cohort,enum=discovered_cohort,enum=open"`
	RequestedJudgment   string                  `json:"requested_judgment"`
	SubjectTerms        []string                `json:"subject_terms,omitempty"`
	ComparisonTerms     []string                `json:"comparison_terms,omitempty"`
	TimeContext         outputTimeContext       `json:"time_context"`
	FactRequirements    []factRequirementOutput `json:"fact_requirements"`
	ClarificationNeeded bool                    `json:"clarification_needed"`
	ClarificationReason string                  `json:"clarification_reason,omitempty"`
	// WindowClass/WindowConfidence (CHAOS-3900 W0, SHADOW ONLY): the
	// model's one-enum-pick classification of the question's evidence-
	// window shape (design brief §2.1). Deliberately NOT part of
	// contextfabric.InterpretedQuestion/toDomain -- sanitized directly in
	// InterpretQuestion (below toDomain's own call site) onto
	// ModelExecutionReceipt, so an out-of-vocab pick can never fail
	// interpreted.Validate() (the F5 control-flow fix: interpreted.Validate()
	// runs inside toDomain, before any caller-side fallback could run, so
	// closed-enum enforcement must sit strictly before it, never inside
	// it). The model never emits a timestamp for this -- only the class
	// pick; the engine-side post-pass (graphrank.ClassifyWindow) owns bounds.
	WindowClass      string `json:"window_class,omitempty" jsonschema:"enum=trend_assessment,enum=recent_activity_lookup,enum=state_snapshot,enum=explicit_window"`
	WindowConfidence string `json:"window_confidence,omitempty" jsonschema:"enum=high,enum=low"`
	// QuestionFamily/GroupKind/ScopeAnchorTerm/ScopeAnchorKind
	// (CHAOS-4632, SHADOW ONLY) are the family pick and the two new
	// structure signals the §4.2 precedence table keys on. Same discipline
	// as WindowClass immediately above and for the same reason:
	// deliberately NOT part of contextfabric.InterpretedQuestion/toDomain,
	// sanitized directly in InterpretQuestion onto ModelExecutionReceipt,
	// so an out-of-vocabulary pick can never be the reason an otherwise
	// sound interpretation is rejected (the F5 control-flow rule --
	// interpreted.Validate() runs inside toDomain, before any caller-side
	// fallback could run, so closed-enum enforcement must sit strictly
	// AFTER it, never inside it).
	//
	// NO jsonschema enum tag on group_kind/scope_anchor_kind, unlike
	// window_class: the subject-kind vocabulary has 15 members and is
	// rendered into the PROMPT (contextFabricSubjectKindList) rather than
	// pinned in the response schema. That is deliberate -- a schema enum
	// makes the provider reject the whole response for an out-of-set
	// value, which converts a shadow capture into a way to fail a real
	// investigation. Sanitization handles the out-of-set case instead,
	// and the unrecognized flag makes it countable.
	QuestionFamily  string `json:"question_family,omitempty"`
	GroupKind       string `json:"group_kind,omitempty"`
	ScopeAnchorTerm string `json:"scope_anchor_term,omitempty"`
	ScopeAnchorKind string `json:"scope_anchor_kind,omitempty"`
	// RequestedSubjectKind is the kind of thing the ANSWER is about, as
	// distinct from the kind of any scope anchor. It exists because §4.2's
	// row 2 is an ASYMMETRY test -- "ScopeAnchorTerm set AND the question
	// asks about a different kind than the anchor's" -- and both halves
	// are required for the row to fire.
	//
	// The alternative was to read the caller's own ContextFabricRequestedScope.
	// ExpectedKinds, and that is wrong here: ExpectedKinds is the CALLER's
	// explicit structure (see its own doc comment in contracts/v1), set by
	// a panel or an agent that already knows the shape. Q-B's caller sets
	// nothing, so row 2 would never fire and the scoped family would be
	// unreachable -- which would make this slice's whole gating
	// measurement unmeasurable for the one acceptance question it exists
	// to fix. Asking the model instead puts this field in the SAME
	// labelled measurement as group_kind and the anchor, where its
	// correctness is checked rather than assumed.
	RequestedSubjectKind string `json:"requested_subject_kind,omitempty"`
	// QuestionFrame (CHAOS-4452 stage 2, SHADOW ONLY) is the
	// compositional frame -- what the user is asking the system to
	// ESTABLISH. Same discipline as the two blocks above and for the
	// same reason: NOT part of contextfabric.InterpretedQuestion/toDomain,
	// sanitized directly onto ModelExecutionReceipt, so an
	// out-of-vocabulary pick can never be the reason an otherwise sound
	// interpretation is rejected.
	QuestionFrame *questionFrameOutput `json:"question_frame,omitempty"`
}

type outputTimeContext struct {
	Axis  string     `json:"axis" jsonschema:"enum=current,enum=valid_time,enum=observed_time,enum=range"`
	AsOf  *time.Time `json:"as_of,omitempty"`
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}

// JSONSchema hand-authors outputTimeContext's schema (invopop/jsonschema's
// Marshaler convention, respected by genkit's own core.InferSchemaMap --
// see InterpretationOutputSchema's doc comment) instead of the
// struct-tag-derived default, to encode the axis-shape rules no jsonschema
// struct tag can express: each of the four axes allows a DIFFERENT,
// mutually exclusive set of the as_of/start/end fields, matching
// contractsv1.ContextFabricTimeContext.Validate() exactly (the SAME
// function contextfabric.InterpretedQuestion.Validate wraps as
// "time_context: %w", the actual production check this schema exists to
// front-run):
//
//   - current:                    none of as_of/start/end.
//   - valid_time/observed_time:   as_of required, start/end absent.
//   - range:                      start+end required together, as_of absent.
//
// A struct-tag reflection leaves Start/End/AsOf (Go *time.Time,
// `omitempty`) as merely optional properties regardless of axis, so any
// responder -- including CHAOS-3742's own file-exchange codex responder,
// which found the range-without-bounds gap on live run 3 -- can satisfy
// the schema with {"axis":"range"} alone (or {"axis":"current","as_of":
// ...}, or {"axis":"valid_time"} with no as_of): every one of these is an
// output Validate() correctly rejects downstream, but that this schema
// should have refused to accept from the model in the first place.
// codex adversarial review (2026-08-21) confirmed the range-only version
// of this schema left the other three shapes open; this closes all four.
//
// Ordering (range's end >= start) is deliberately NOT encoded here: JSON
// Schema has no native date-time comparison, and Validate() already
// catches it -- this schema's job is presence/absence of fields per axis,
// not a full reimplementation of domain validation.
func (outputTimeContext) JSONSchema() *jsonschema.Schema {
	properties := orderedmap.New[string, *jsonschema.Schema]()
	properties.Set("axis", &jsonschema.Schema{
		Type: "string",
		Enum: []any{"current", "valid_time", "observed_time", "range"},
	})
	properties.Set("as_of", &jsonschema.Schema{Type: "string", Format: "date-time"})
	properties.Set("start", &jsonschema.Schema{Type: "string", Format: "date-time"})
	properties.Set("end", &jsonschema.Schema{Type: "string", Format: "date-time"})

	axisIs := func(value string) *jsonschema.Schema {
		axis := orderedmap.New[string, *jsonschema.Schema]()
		axis.Set("axis", &jsonschema.Schema{Const: value})
		return &jsonschema.Schema{Properties: axis}
	}
	axisIn := func(values ...any) *jsonschema.Schema {
		axis := orderedmap.New[string, *jsonschema.Schema]()
		axis.Set("axis", &jsonschema.Schema{Enum: values})
		return &jsonschema.Schema{Properties: axis}
	}
	// noneOf builds the "not(any of these fields present)" clause for a
	// Then schema's own Not field -- a bare "required" lists fields that
	// MUST be present, so forbidding presence needs its negation.
	noneOf := func(fields ...string) *jsonschema.Schema {
		anyOf := make([]*jsonschema.Schema, len(fields))
		for i, field := range fields {
			anyOf[i] = &jsonschema.Schema{Required: []string{field}}
		}
		return &jsonschema.Schema{AnyOf: anyOf}
	}

	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           properties,
		Required:             []string{"axis"},
		AdditionalProperties: jsonschema.FalseSchema,
		AllOf: []*jsonschema.Schema{
			{If: axisIs("current"), Then: &jsonschema.Schema{Not: noneOf("as_of", "start", "end")}},
			{If: axisIn("valid_time", "observed_time"), Then: &jsonschema.Schema{
				Required: []string{"as_of"}, Not: noneOf("start", "end"),
			}},
			{If: axisIs("range"), Then: &jsonschema.Schema{
				Required: []string{"start", "end"}, Not: noneOf("as_of"),
			}},
		},
	}
}

type factRequirementOutput struct {
	Kind       string            `json:"kind"`
	Parameters map[string]string `json:"parameters,omitempty"`
}

func (o interpretationOutput) toDomain(defaultTime contextfabric.TimeContext) (contextfabric.InterpretedQuestion, error) {
	timeContext := contextfabric.TimeContext{Axis: contextfabric.TemporalAxis(o.TimeContext.Axis), AsOf: o.TimeContext.AsOf, Start: o.TimeContext.Start, End: o.TimeContext.End}
	if strings.TrimSpace(o.TimeContext.Axis) == "" {
		timeContext = defaultTime
	}
	requirements := make([]contextfabric.FactRequirement, 0, len(o.FactRequirements))
	seen := make(map[contextfabric.FactKind]struct{})
	for _, requirement := range o.FactRequirements {
		kind := contextfabric.FactKind(strings.TrimSpace(requirement.Kind))
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		requirements = append(requirements, contextfabric.FactRequirement{Kind: kind, Parameters: cloneStringMap(requirement.Parameters)})
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.InvestigationShape(strings.TrimSpace(o.Shape)), RequestedJudgment: strings.TrimSpace(o.RequestedJudgment),
		SubjectTerms: trimmedUnique(o.SubjectTerms), ComparisonTerms: trimmedUnique(o.ComparisonTerms),
		TimeContext: timeContext, FactRequirements: requirements,
		ClarificationNeeded: o.ClarificationNeeded, ClarificationReason: strings.TrimSpace(o.ClarificationReason),
	}
	if err := interpreted.Validate(); err != nil {
		// Return interpreted (not a zero value) alongside the error: the
		// caller (Runtime.InterpretQuestion) needs the actual rejected
		// value to diagnose which bound it violated (CHAOS-3784 F1) --
		// this is the production ModelRuntime's OWN Validate() call site,
		// the only place that still has it before it would otherwise be
		// discarded.
		return interpreted, err
	}
	return interpreted, nil
}

type synthesisInput struct {
	Question         string                            `json:"question"`
	Interpretation   contextfabric.InterpretedQuestion `json:"interpretation"`
	Resolution       contextfabric.SubjectResolution   `json:"subject_resolution"`
	Cohort           *contextfabric.Cohort             `json:"cohort,omitempty"`
	Paths            []contextfabric.RelationshipPath  `json:"paths"`
	DriverCandidates []contextfabric.DriverJudgment    `json:"driver_candidates"`
	Facts            []contextfabric.CanonicalFact     `json:"canonical_facts"`
	Coverage         contextfabric.Coverage            `json:"coverage"`
}

// synthesisInputFromDomain composes the exact bounded-JSON payload
// SynthesizeAnswer sends the model. orgID (CHAOS-4690) feeds
// contextfabric.MergeCoverage's own fail-open reconcile WARN log only --
// never merge semantics; BuildSynthesisPrompt's prompt-preview path has no
// authenticated principal in scope and passes "".
func synthesisInputFromDomain(orgID string, input contextfabric.SynthesisInput) synthesisInput {
	return synthesisInput{
		Question: input.Request.Question, Interpretation: input.Interpretation,
		Resolution: input.Graph.Resolution, Cohort: input.Graph.Cohort,
		Paths: input.Graph.Paths, DriverCandidates: input.Graph.DriverCandidates,
		Facts: modelFacingFacts(input.Facts.Facts), Coverage: contextfabric.MergeCoverage(orgID, input.Graph.Coverage, input.Facts.Coverage),
	}
}

// modelFacingFacts returns a copy of facts with every Rows-shaped field
// (CHAOS-4347) dropped before the fact set is serialized into the
// synthesis prompt (CHAOS-4355 follow-up). A model shown a Rows-shaped
// field in canonical_facts has nothing to do with it but echo the shape
// back into its own ClaimedFacts.Rows, which
// SynthesisDraft.ValidateAgainst unconditionally rejects (rows are
// attached server-side, from the SAME canonical fact, only AFTER
// validation -- engine.go's attachCanonicalRows). Every scalar field the
// model can legitimately ground a claim in is untouched -- only the
// table-shaped entries are excluded, never a fact's identity
// (Kind/Subject) or its other fields. Used by both the real Synthesize
// call and BuildSynthesisPrompt (exchange_support.go), so a non-genkit
// responder sees byte-identical input to what genkit actually sends.
func modelFacingFacts(facts []contextfabric.CanonicalFact) []contextfabric.CanonicalFact {
	out := make([]contextfabric.CanonicalFact, len(facts))
	for i, fact := range facts {
		clone := fact
		if len(fact.Fields) > 0 {
			fields := make(map[string]contextfabric.FactValue, len(fact.Fields))
			for key, value := range fact.Fields {
				if len(value.Rows) > 0 {
					continue
				}
				fields[key] = value
			}
			clone.Fields = fields
		}
		out[i] = clone
	}
	return out
}

type synthesisOutput struct {
	Status             string                         `json:"status" jsonschema:"enum=complete,enum=partial,enum=degraded,enum=clarification_required,enum=no_match"`
	DirectJudgment     string                         `json:"direct_judgment"`
	CurrentState       string                         `json:"current_state"`
	StrongestPressures []string                       `json:"strongest_pressures"`
	Drivers            []contextfabric.DriverJudgment `json:"drivers"`
	RemainingWork      []contextfabric.Finding        `json:"remaining_work"`
	ReadinessGaps      []contextfabric.Finding        `json:"readiness_gaps"`
	Conflicts          []contextfabric.Finding        `json:"conflicts"`
	Limitations        []string                       `json:"limitations"`
	EvidenceRefIDs     []string                       `json:"evidence_ref_ids"`
	// ClaimedFacts is the model's restatement of the canonical fact field
	// values (from the "canonical_facts" input) that its fact-shaped
	// drivers/findings rely on. See ContextFabricClaimedFact's doc comment
	// -- SynthesisDraft.ValidateAgainst checks every entry against the
	// canonical fact bundle by exact value equality before this can ever
	// reach a persisted result.
	ClaimedFacts        []contextfabric.ClaimedFact `json:"claimed_facts"`
	DeterministicAnswer string                      `json:"deterministic_answer"`
	Warnings            []string                    `json:"warnings"`
	// CoverageDisclosures (CHAOS-4690 Commit F, design §4.1) is
	// DELIBERATELY typed json.RawMessage, not a typed []struct -- genkit's
	// invopop/jsonschema reflector special-cases json.RawMessage as an
	// UNCONSTRAINED schema (reflect.go:231 rawMessageType,
	// reflectSliceOrArray's own `if t == rawMessageType { return }`
	// early-out, verified against the vendored invopop/jsonschema@v0.13.0
	// this module pins). A typed field here would instead produce a
	// normal array-of-object schema, and genkit v1.11 validates the WHOLE
	// parsed JSON against the output schema BEFORE unmarshalling
	// (ai/format_json.go:76, ai/generate.go:1157) -- so one malformed
	// entry (e.g. `"text": 17`) would reject the ENTIRE generation
	// upstream of any local guard, discarding a perfectly valid
	// status/drivers/claimed_facts payload over one bad disclosure. With
	// an unconstrained schema, structured-output validation can never
	// fail on this field by construction; the model's guidance comes
	// entirely from the jsonschema description below and the synthesis
	// prompt (prompts.go), and ALL content policing is local: toDomain
	// leniently decodes this raw value (parseCoverageDisclosures), and
	// contextfabric.applyCoverageDisclosures guards the decoded result
	// against the same merged coverage details the answer actually
	// carries. A malformed value here is reported as "undecodable", never
	// as a decode error toDomain's caller would propagate -- see
	// parseCoverageDisclosures' own doc comment.
	CoverageDisclosures json.RawMessage `json:"coverage_disclosures,omitempty" jsonschema:"description=Optional. An array of at most one entry per coverage.details entry: {detail_id, text}. detail_id must exactly match a coverage.details[].detail_id from the input. text is one short plain-language sentence, faithful only to that entry's own fields (no invented cause/system/name), and must contain NO digits -- the quantity is already stated beside it."`
}

// phrasingOption is one offered option's own model-facing input row
// (CHAOS-4171 PR2) -- deliberately narrow: option_id, member, kind, and the
// structural Label composeStructureNeeds already minted. No evidence, no
// subject resolution, no canonical IDs beyond what Label already discloses
// -- "input is the offer set only" (ratified design).
type phrasingOption struct {
	OptionID string `json:"option_id"`
	Member   string `json:"member"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
}

type phrasingInput struct {
	Options []phrasingOption `json:"options"`
}

// phrasingEntryOutput is the model's own phrasing for ONE option_id.
// Unguarded at this layer -- see contextfabric.classifyOfferPhrasingDraft
// for the closed-vocabulary membership/uniqueness/bound check that runs on
// this raw output one layer up.
type phrasingEntryOutput struct {
	OptionID string `json:"option_id"`
	Phrasing string `json:"phrasing"`
}

type phrasingOutput struct {
	Phrasings []phrasingEntryOutput `json:"phrasings"`
}

func (o synthesisOutput) toDomain() (contextfabric.SynthesisDraft, error) {
	draft := contextfabric.SynthesisDraft{
		Status:         contextfabric.InvestigationStatus(strings.TrimSpace(o.Status)),
		DirectJudgment: strings.TrimSpace(o.DirectJudgment), CurrentState: strings.TrimSpace(o.CurrentState),
		StrongestPressures: trimmedUnique(o.StrongestPressures), Drivers: append([]contextfabric.DriverJudgment(nil), o.Drivers...),
		RemainingWork: append([]contextfabric.Finding(nil), o.RemainingWork...), ReadinessGaps: append([]contextfabric.Finding(nil), o.ReadinessGaps...),
		Conflicts: append([]contextfabric.Finding(nil), o.Conflicts...), Limitations: trimmedUnique(o.Limitations),
		EvidenceRefIDs: trimmedUnique(o.EvidenceRefIDs), ClaimedFacts: append([]contextfabric.ClaimedFact(nil), o.ClaimedFacts...),
		DeterministicAnswer: strings.TrimSpace(o.DeterministicAnswer), Warnings: trimmedUnique(o.Warnings),
	}
	draft.CoverageDisclosures, draft.CoverageDisclosuresUndecodable = parseCoverageDisclosures(o.CoverageDisclosures)
	if strings.TrimSpace(draft.DeterministicAnswer) == "" {
		// Return draft (not a zero value) alongside the error: the caller
		// (Runtime.SynthesizeAnswer) needs it to diagnose whether any
		// OTHER field also violates a bound (CHAOS-3784 F1), the same
		// reason interpretationOutput.toDomain does this above.
		//
		// CHAOS-4522: this is a VALIDATION failure, not a decode failure --
		// the model's JSON decoded into synthesisOutput perfectly well, it
		// just omitted a required field, and it is the very same rule
		// SynthesisDraft.ValidateAgainst enforces one statement later. So
		// it carries that rule's own reason rather than anything about
		// decoding. Nothing in this path can actually fail to DECODE
		// (genkit has already populated synthesisOutput by the time
		// toDomain runs), which is why no "undecodable" reason exists in
		// the vocabulary: a reason that no branch can ever emit is a claim
		// the telemetry cannot back.
		return draft, contextfabric.NewSynthesisRejection(
			contextfabric.RejectionReasonDeterministicAnswerMissing,
			errors.New("deterministic answer is required"),
		)
	}
	return draft, nil
}

// parseCoverageDisclosures lenient-decodes the model's raw
// coverage_disclosures subdocument (CHAOS-4690 Commit F, design §4.1).
// Because synthesisOutput.CoverageDisclosures is deliberately UNCONSTRAINED
// in the output schema (see that field's own doc comment), genkit's
// structured-output validation can never reject a malformed value here --
// this is the ONLY place that malformation is ever detected, and it is
// detected leniently: any unmarshal failure (a non-array top-level value,
// a non-string "text" such as `"text": 17`, a non-string "detail_id", ...)
// reports undecodable=true and a nil slice, NEVER an error -- the whole
// synthesis call must still succeed, serving every coverage detail
// Label-only (design §4.1's own r2 F1 scenario: "valid answer +
// `"text": 17` => answer served"). An absent field, a JSON null, or
// whitespace-only raw bytes is legitimately "nothing offered" -- reported
// as (nil, false), NOT undecodable, so
// contextfabric.RuntimeAnswerSynthesizer.Synthesize's telemetry can tell
// "the model tried and we could not read it" (discarded_undecodable) apart
// from "the model offered nothing this call" (absent).
//
// ParseSynthesisOutput (exchange_support.go) calls synthesisOutput.toDomain
// directly, so it routes through this exact same lenient parser -- there is
// no second decode path for the file-exchange transport to drift from.
func parseCoverageDisclosures(raw json.RawMessage) ([]contextfabric.CoverageDisclosure, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, false
	}
	var disclosures []contextfabric.CoverageDisclosure
	if err := json.Unmarshal(trimmed, &disclosures); err != nil {
		return nil, true
	}
	return disclosures, false
}

func trimmedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = values[key]
	}
	return result
}

// mergeCoverage is deliberately GONE (CHAOS-4690): this package used to
// carry its own duplicate coverage-merge implementation (no state
// priority, no structured details) that could disagree with
// contextfabric's own merge at model_runtime.go:1336 -- the exact dual-write
// drift risk design §3.4 exists to close. Both call sites
// (synthesisInputFromDomain below) now route through the ONE shared pure
// normalizer, contextfabric.MergeCoverage.

var _ contextfabric.ModelRuntime = (*Runtime)(nil)
var _ contextfabric.OfferPhrasingModelRuntime = (*Runtime)(nil)
