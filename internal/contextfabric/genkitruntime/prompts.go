package genkitruntime

import (
	"fmt"

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
