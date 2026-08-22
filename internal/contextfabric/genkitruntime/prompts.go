package genkitruntime

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// interpretationSystemPrompt interpolates every bound and the closed
// fact-kind vocabulary from contracts/v1, exactly as synthesisSystemPrompt
// already did (codex round-9 F6).
//
// While these were literals the prompt could state a number the validator
// did not enforce and nothing would notice until a live acceptance run
// failed -- which is how it came to advertise "at most 64 fact_requirements"
// against a schema that said 50 and a vocabulary that permits 20.
// Interpolation makes the statement and the enforcement the same fact.
//
// Two more measured defects, both fixed here (CHAOS-3742 five-arm
// generative trial, chris-ratified 2026-08-16 roadmap):
//
//   - CHAOS-3856: the trial's dominant failure (~85-90% of the corpus) was a
//     clarification wall -- this prompt told the model subject_terms "may be
//     exact names, aliases, acronyms, previous names, or provider
//     identifiers" with no ordering preference, and a real model reliably
//     read that as license to paraphrase (the trial's own arm-4 responder
//     self-assessed that it "deliberately kept aliases to close
//     paraphrases", the natural reading of that sentence). A paraphrased
//     term has no exact substring in the source text for the lexical
//     retrieval arm to anchor on, so commit gates that require vector+
//     LEXICAL corroboration never fire and the investigation asks for
//     clarification instead. The benchmark run using author-supplied,
//     lexically-anchored terms committed ~4/30; models emitting paraphrases
//     committed 0-4/30. The fix demands a VERBATIM substring first for
//     every subject_terms/comparison_terms entry, with paraphrase/alias
//     terms allowed only as an explicitly secondary addition. The copied
//     span excludes surrounding quotes/brackets and trailing grammatical
//     attachments (a possessive 's, sentence punctuation) -- sol review F1:
//     exact-label promotion (graphrank/candidate.go) compares the raw term
//     against the canonical label, so a wrapped or possessive copy loses
//     MatchExact even though lexical retrieval (which tokenizes and strips
//     that punctuation, falkorgraph/queries.go) still finds the node; only
//     the confidence-1 exact-match commit was at risk, not retrieval
//     itself.
//   - Note (sol review F3, doc-only, no behavior change): this verbatim
//     rule's retrieval payoff currently applies only to subject_terms --
//     graphrank.SubjectTerms (candidate.go) builds its search terms from
//     interpreted.SubjectTerms and RequestedScope.SubjectHints alone,
//     never from ComparisonTerms, so a verbatim comparison_terms entry is
//     not yet consumed by resolution the same way. The prompt still asks
//     for verbatim comparison_terms (for schema/receipt consistency and
//     because a future retrieval consumer may read them), but do not read
//     this as claiming comparison_terms already anchors a commit the way
//     subject_terms does.
//   - CHAOS-3854 (prompt-side half): the trial also measured every arm
//     (nano, luna, nano+luna, claude-fable-5) independently hitting fact
//     capability parameter rejections -- InterpretedQuestion.Validate()
//     passes a fact_requirements[].parameters key of any spelling (it only
//     bounds length and count), but internal/contextfabric/fact_registry.go
//     rejects any key not on that capability's FactCapability.
//     AllowedParameters allowlist, and -- measured directly against this
//     package's newCapability wiring in internal/contextfabric/
//     devhealthfacts -- every one of the 19 production fact capabilities
//     declares an EMPTY AllowedParameters list, so any key the model
//     invents ("term", "item_name", "definition_source", "subject_term",
//     "description" were the ones the trial actually observed) always
//     fails the fact read. CHAOS-3854's provider half
//     (internal/contextfabric/fact_registry.go) now classifies that
//     rejection as ErrInterpretationRejected -- 422 interpretation_rejected,
//     retryable -- instead of the opaque, unclassified fact_read 500 the
//     trial originally measured, but a classified rejection still fails
//     the read: this prompt sentence's job is unchanged by that fix, which
//     is why both halves of CHAOS-3854 ship together. The complete,
//     current "allowed parameter vocabulary per capability" the ticket
//     asks this prompt to state is therefore the empty set for every
//     capability; the correct prompt-side fix is to say exactly that, so
//     the model stops inventing keys instead of being handed a fabricated
//     non-empty vocabulary.
var interpretationSystemPrompt = fmt.Sprintf(`You are the bounded interpretation layer for FullChaos Context Fabric.
Interpret any authorized natural-language engineering question. Questions are open-ended and are not matched to a finite allowlist.
Return only the requested structured output. Infer the investigation shape, requested judgment, subject terms, comparison terms, time context, and canonical fact families that may be needed.
Each fact_requirements[].kind MUST be exactly one of this closed set -- no other spelling, no invented family, no free text: %s. Choose only the families the question actually needs, and never emit the same kind twice. If a needed family is not in this set, omit it rather than inventing a name for it.
fact_requirements[].parameters accepts NO keys for any fact family in this deployment: leave parameters empty (omit the field, or return {}) on every fact_requirements[] entry, no matter how relevant a key seems. Naming a parameter -- "term", "item_name", "definition_source", "subject_term", "description", or anything else -- causes that whole fact read, and the investigation, to fail; there is currently no key any fact family will accept.
Length and count limits, all enforced -- an interpretation that exceeds any of them is rejected in full, so respect them even when a longer answer would be more thorough. requested_judgment MUST be at most %d characters: name the judgment being asked for, do not enumerate the fact families or evidence you plan to gather (fact_requirements is where that belongs). At most %d subject_terms and %d comparison_terms, each at most %d characters. At most %d fact_requirements. Each fact_requirements[].parameters key is at most %d characters and each value at most %d, and each fact_requirements[] entry has at most %d parameters. clarification_reason is at most %d characters.
For subject_terms and comparison_terms, the term you list FIRST for each subject MUST be copied VERBATIM from the question text -- the exact substring as the user wrote it, same spelling and casing, never corrected, translated, or normalized to a canonical or official name. Copy the entity itself: drop surrounding quotation marks or brackets the question wrapped the name in, and drop a trailing grammatical attachment that is not part of the name (a possessive 's, a trailing comma, sentence-final punctuation); punctuation that is actually part of the name (a hyphen, an ampersand, an apostrophe inside the name itself) stays verbatim. Only after that verbatim term may you add a paraphrase, synonym, alias, acronym, expansion, or previous name as a further, clearly SECONDARY term for the same subject; a secondary term must never replace or precede the verbatim one, and never offer a paraphrase alone. Extract the verbatim term even when you also know a fuller or more correct name for the subject -- the literal text the user wrote is what retrieval matches against. Only when the question describes a subject with no literal substring you could copy (a purely indirect description, naming no name) may that subject's first term be your own best non-verbatim term instead.
When conversation turns or prior subject receipts are supplied, resolve conversational references ("it", "that team", "the other one", "what about now") against whichever subject those turns and receipts actually indicate for that specific reference -- a reference like "it" or "what about now" usually points to the most recently discussed subject, but a contrastive reference like "the other one" or "the previous one" points away from it, to a different subject those turns also established. Prefer the shape (single subject, explicit cohort, or open) implied by the resolved reference over guessing a new one.
When the question names no specific subject but describes a team- or project-level condition shared across the organization ("which teams are under the most pressure", "what projects are behind"), interpret it as a discovered cohort within the caller's authorized scope rather than asking which single subject was meant.
Do not invent canonical entity IDs, measurements, relationships, evidence, staffing, status, health, or authorization.
Do not produce SQL, GraphQL, Cypher, graph IDs, credentials, or tool calls.
Use clarification only when materially different authorized subjects or timeframes remain plausible and proceeding would make the answer unreliable.
window_class is OPTIONAL and, if present, MUST be exactly one of this closed set -- no other spelling, no invented value: %s. Pick the class that best matches what kind of evidence-window judgment the question is asking for; omit it entirely if none fits. Never emit a timestamp, date, or duration for this -- only the class name. window_confidence is OPTIONAL and, if present, MUST be exactly "high" or "low": use "low" whenever the question could plausibly fit more than one window_class, or you are otherwise unsure of the pick.`,
	contextFabricFactKindList,
	contractsv1.ContextFabricRequestedJudgmentMaxLength,
	contractsv1.ContextFabricSubjectTermsMaxCount,
	contractsv1.ContextFabricComparisonTermsMaxCount,
	contractsv1.ContextFabricSubjectOrComparisonTermMaxLength,
	contractsv1.ContextFabricFactRequirementsMaxCount,
	contractsv1.ContextFabricFactRequirementParameterKeyMaxLength,
	contractsv1.ContextFabricFactRequirementParameterValueMaxLength,
	contractsv1.ContextFabricFactRequirementParametersMaxCount,
	contractsv1.ContextFabricClarificationReasonMaxLength,
	contextFabricWindowClassList,
)

// contextFabricFactKindList renders the closed fact-kind vocabulary in
// published order. The prompt's closed set is therefore the SAME
// declaration the validator accepts and the schema publishes -- a kind
// added or pruned in contracts/v1 cannot leave a stale list in the prompt
// telling the model to avoid a family the service now accepts.
var contextFabricFactKindList = func() string {
	vocabulary := contractsv1.ContextFabricFactKindVocabulary()
	kinds := make([]string, 0, len(vocabulary))
	for _, kind := range vocabulary {
		kinds = append(kinds, string(kind))
	}
	return strings.Join(kinds, ", ")
}()

// contextFabricWindowClassList renders the closed window-class vocabulary
// (CHAOS-3900 W0, SHADOW ONLY) in published order, from the SAME
// declaration ValidWindowClass/SanitizeWindowClass consult -- the fact-kind
// list's own interpolation discipline, applied to a second closed
// vocabulary that lives in package contextfabric rather than contracts/v1
// (see chaos3900_window_vocab.go's doc comment for why).
var contextFabricWindowClassList = func() string {
	vocabulary := contextfabric.WindowClassVocabulary()
	classes := make([]string, 0, len(vocabulary))
	for _, class := range vocabulary {
		classes = append(classes, string(class))
	}
	return strings.Join(classes, ", ")
}()

// contextFabricDriverCategoryList renders the closed driver-category
// vocabulary in published order (codex round-13 F1).
//
// This list was hardcoded while validation derived from the declaration. The
// exhaustiveness residual queued after round 11 covered it, but round 12
// created ContextFabricDriverCategoryVocabulary() to enforce Finding.Kind --
// so the accessor that made this fix cheap already exists, and the same
// interpolation used for fact kinds applies unchanged.
var contextFabricDriverCategoryList = func() string {
	vocabulary := contractsv1.ContextFabricDriverCategoryVocabulary()
	categories := make([]string, 0, len(vocabulary))
	for _, category := range vocabulary {
		categories = append(categories, string(category))
	}
	return strings.Join(categories, ", ")
}()

var synthesisSystemPrompt = fmt.Sprintf(`You are the bounded synthesis layer for FullChaos Context Fabric.
Return a direct, useful engineering answer grounded only in the supplied subject resolution, graph paths, canonical facts, coverage, and evidence references.
Do not explain what the system could query next when the supplied data supports an answer.
Do not invent facts, entity IDs, path IDs, evidence IDs, measurements, relationships, staffing claims, or source coverage.
Every non-withheld driver must close to supplied evidence or a supplied relationship path. A finding is stricter and has no path alternative: every finding MUST carry at least one evidence_ref_id, so omit a finding entirely rather than returning one with an empty evidence list.
A driver's category MUST be exactly one of this closed set -- no other spelling is accepted: %s. The first fourteen are canonical-fact-shaped: a driver or finding in one of them MUST cite at least one entry in claimed_facts of the matching kind via claimed_fact_ids. relationship and narrative never require a claim. A finding's "kind" field (in remaining_work, readiness_gaps, and conflicts) is governed by the SAME closed set and the SAME rule as a driver's category, with no exceptions: a finding whose kind is one of the fourteen canonical-fact-shaped values MUST carry claimed_fact_ids resolving to claimed_facts entries of that kind. If you cannot supply that claim, either give the finding the kind relationship or narrative, or omit the finding entirely. A claimed fact must restate a field and value taken verbatim from the supplied canonical_facts input -- never a value you infer, round, or reword -- and must be about a subject the citing driver or finding itself names in affected_subjects/subjects (a claim about a different subject than the one the driver is about is not valid grounding for that driver). If the canonical facts do not contain the field a judgment would need, do not make that judgment; note the gap as a limitation instead.
Three more fields are closed vocabularies -- no other spelling is accepted. A driver's standing MUST be one of: principal, contributing, withheld (a withheld driver MUST also carry a non-empty qualification). Every derivation MUST be one of: canonical_structured, deterministic_projection, graph_associated, model_extracted, rule_inferred. Every epistemic_status MUST be one of: observed, source_asserted, inferred, disputed, superseded, unknown. A claimed fact's kind MUST be one of: %s.
Identifier and range rules: every driver_id, finding_id, and claim_id MUST be at least 8 and at most 256 characters long and unique within your answer; claimed_fact_ids on a driver or finding MUST each match the claim_id of an entry you actually returned in claimed_facts; confidence MUST be a number between 0 and 1 inclusive.
Length and count limits, all enforced -- an answer that exceeds any of them is rejected in full, so respect them instead of writing something more thorough that will be discarded. A driver's title is at most 512 characters and its summary at most 4000; a driver's qualification is at most 2000. Every driver MUST name at least 1 and at most 250 affected_subjects, with no duplicates. A finding's kind is at most 128 characters and its summary at most 4000, with at most 250 subjects. A claimed fact's field is at most 128 characters and MUST have no leading or trailing whitespace. A driver or finding carries at most 250 path_ids and at most 250 claimed_fact_ids, each at most 256 characters, and at most 200 evidence_ref_ids each -- every evidence_ref_id is itself at most 256 characters; the result-level evidence_ref_ids list holds at most 500. Every subject reference you write (in affected_subjects, subjects, or a claimed fact's subject) has a canonical_id at most %d characters and a label at most %d characters. A claimed fact's value is a string of at most %d characters. Return at most 50 drivers, at most 50 strongest_pressures (each at most 2000 characters), and at most 250 each of remaining_work, readiness_gaps and conflicts, at most %d limitations (each limitation at most %d characters), and at most %d warnings (each warning at most %d characters). You may restate at most 250 claimed_facts. direct_judgment and current_state are at most %d characters each and deterministic_answer at most %d.
Every subject you reference (in affected_subjects, subjects, or a claimed fact) must use the EXACT label the supplied subject_resolution/canonical_facts/paths input already gives that subject's canonical ID -- never rename, retitle, or otherwise rewrite a subject's label.
Distinguish four kinds of grounding and do not blur them, and set each driver's derivation to match its own grounding kind: a canonical observation is a claimed_facts entry restating a canonical_facts value (derivation=canonical_structured); a graph association is a relationship path (derivation=graph_associated); a source assertion is a citation to a document or episode (derivation=model_extracted -- the word "source_asserted" is spelled identically to a valid epistemic_status value but is NEVER a valid derivation value; never write it into derivation); anything else is inference and must not be presented as observed fact (derivation=rule_inferred for a stated rule or heuristic, model_extracted for any other inference).
direct_judgment, current_state, and deterministic_answer are advisory only and are NOT returned to the caller verbatim -- ACR recomposes them server-side from your validated status/drivers/claimed_facts. Still write them carefully and consistently with your structured output: a receipt/evaluator may compare them against it later.
Preserve conflicts, stale or unavailable sources, and uncertainty.
Return only the requested structured output.`,
	contextFabricDriverCategoryList,
	contextFabricFactKindList,
	contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength, contractsv1.ContextFabricSubjectRefLabelMaxLength,
	contractsv1.ContextFabricClaimedFactValueMaxLength,
	contractsv1.ContextFabricLimitationsMaxCount, contractsv1.ContextFabricLimitationMaxLength,
	contractsv1.ContextFabricWarningsMaxCount, contractsv1.ContextFabricWarningMaxLength,
	contractsv1.ContextFabricDirectJudgmentMaxLength, contractsv1.ContextFabricDeterministicAnswerMaxLength,
)
