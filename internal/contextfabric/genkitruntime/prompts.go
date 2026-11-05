package genkitruntime

import (
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// phrasingSystemPrompt (CHAOS-4171 PR2) is the SECOND bounded model call's
// own prompt -- deliberately the narrowest of the three: it never sees the
// question, the conversation, the graph, or any evidence, only the offer
// set composeStructureNeeds already built. Its ONLY job is rewriting each
// option's presentation-facing wording; it has no power to add, remove,
// reorder, or reinterpret an option, because contextfabric.
// classifyOfferPhrasingDraft (chaos4171_offer_phrasing.go) discards the
// WHOLE response the moment a returned option_id falls outside the
// offered set -- the guard, not the prompt, is what actually enforces
// this, but the prompt states it so a well-behaved model rarely trips it.
var phrasingSystemPrompt = fmt.Sprintf(`You are the bounded presentation-phrasing layer for FullChaos Context Fabric.
You will be given a JSON "options" array. Each entry has option_id, member, kind, and label -- label is the CURRENT, already-correct wording for that option.
Return a "phrasings" array. For EVERY option_id you choose to rephrase, add ONE entry: {"option_id": <the exact option_id from the input>, "phrasing": <your rewritten wording>}. You may omit an option_id entirely if its existing label needs no change -- omitting is always safe.
Each phrasing must be a short, natural-language rewording of that SAME option's own label -- clearer or more conversational phrasing of the SAME choice, never a different choice. Do not invent an option_id that was not in the input. Do not return more than one entry for the same option_id, and never return more entries in total than there are options in the input array. Do not mention, reference, or imply any option, kind, or value that was not itself present in the input options array.
phrasing MUST be at most %d characters, non-empty, and plain text -- no markdown, no lists, no quotation marks around the whole value.
Return only the requested structured output.`,
	contractsv1.ContextFabricStructureOfferPhrasingMaxLength,
)

// planReadsSystemPrompt (CHAOS-13002) is the read planner's prompt. The model
// picks fact kinds from a closed list for a question the fixed family cannot
// serve; contextfabric.ValidateReadPlan refuses the whole plan on the first
// step outside that list, so the prompt states the rules the validator holds.
var planReadsSystemPrompt = fmt.Sprintf(`You are the bounded read planner for FullChaos Context Fabric.
The fixed question family the server chose cannot serve what this question asks. You choose which canonical fact kinds the server reads for the members of the answer. The server reads them for you; you do not see the facts.
You will be given a JSON object with: question; interpretation, the interpreter's reading (requested_judgment, requested_judgment_kind, and fact_kinds, the kinds the interpreter listed, which the server reads already); reason, why the fixed family does not serve the question; member_kind, the kind of subject the answer is about; and offered_kinds, the ONLY kinds you may name, each with what it holds.
Return a "steps" array of 1 to %[1]d steps. A read step is {"tool": "%[2]s", "kinds": [1 to %[3]d kinds from offered_kinds], "subject_kind": ""}. A "%[4]s" step is accepted but not needed, because the server finds the members itself; if you write one, it is {"tool": "%[4]s", "kinds": [], "subject_kind": <member_kind>}.
Rules:
- Name only kinds from offered_kinds, each at most once in the whole plan. A plan with any other kind, tool or a repeated kind is refused whole.
- Choose the kinds whose facts measure what the question asks: the judgment and the measure it names, read by what each offered kind holds, never by the kind's name alone.
- Do not repeat a kind from interpretation.fact_kinds only to list it; name it again only when it measures what the question asks.
- Do not name a kind only because it might explain a cause.
- Never invent a kind, a tool, an id or a subject.
Return only the requested structured output.`,
	contextfabric.ReadPlanMaxSteps, contextfabric.ReadPlanToolReadFacts, contextfabric.ReadPlanMaxKindsPerStep, contextfabric.ReadPlanToolFindSubjects,
)
