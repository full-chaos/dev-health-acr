package genkitruntime

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestPromptsStateEveryModelFacingBound closes a defect class rather than a
// defect. Three separate CHAOS-3770 live-acceptance failures had the same
// shape: contracts/v1 enforces a bound on model output, the system prompt
// never states it, and the model -- having no way to infer it -- produces
// output that is rejected in full. Interpretation v3 was the fact-kind
// vocabulary, synthesis v4 was the standing/derivation/epistemic/claim-kind
// vocabularies, interpretation v4 was the 256-character requested_judgment
// cap that cost gpt-5-mini 5 of 12 investigations.
//
// Each case pins one bound with three independent assertions:
//
//	atLimit  -- a value exactly AT the limit validates, so the limit in this
//	            table is not merely <= the real one.
//	over     -- a value one past the limit is rejected, so the limit in this
//	            table is not merely >= the real one.
//	mentions -- the prompt text names the field and the limit.
//
// Together, atLimit and over pin the table's limit to the validator's exactly.
// So if someone widens or tightens a bound in contracts/v1 without updating
// the prompt, this test fails: the table no longer matches the validator, and
// correcting the table forces the prompt statement to be corrected with it.
//
// That alone was not enough (CHAOS-3770 F3 codex review): this table used to
// be maintained purely by hand, so a bound the validator enforced but nobody
// ever added a case for shipped unstated with the test still green -- exactly
// what happened to the top-level collection caps (strongest_pressures,
// drivers, remaining_work, readiness_gaps, conflicts, limitations, warnings)
// and the top-level evidence_ref_ids cap. TestModelFacingBoundRegistryIsFullyCovered
// closes that: modelFacingBounds() below must cover every single entry in
// contracts/v1.ContextFabricModelFacingBounds -- the validator-side registry
// those Validate() methods themselves read their numeric literals from (see
// that file's doc comment) -- so a bound can no longer be silently absent
// from this table at all, let alone from the prompt.

// boundPhrases gives each bound a phrase that is UNIQUE to it and carries
// its number, where {N} is substituted with the validator-enforced limit.
//
// Proximity was not enough (codex round-6 F5): the interpretation prompt
// states 128, 1000 and 32 in a single clause about parameters, so a stale
// parameter count could be satisfied by the key-length number beside it.
// An exact phrase ties each assertion to the words the model actually
// reads for that bound, and TestChangingOnePromptNumberFailsExactlyOneBound
// proves the ties are one-to-one.
var boundPhrases = map[string]string{
	"interpretation.requested_judgment.max_length":               "requested_judgment MUST be at most {N} characters",
	"interpretation.subject_terms.max_count":                     "At most {N} subject_terms",
	"interpretation.comparison_terms.max_count":                  "{N} comparison_terms",
	"interpretation.subject_term.max_length":                     "comparison_terms, each at most {N} characters",
	"interpretation.fact_requirements.max_count":                 "At most {N} fact_requirements.",
	"interpretation.fact_requirement.parameter_key.max_length":   "parameters key is at most {N} characters",
	"interpretation.fact_requirement.parameter_value.max_length": "each value at most {N}",
	"interpretation.fact_requirement.parameters.max_count":       "entry has at most {N} parameters",
	"interpretation.clarification_reason.max_length":             "clarification_reason is at most {N} characters",

	"synthesis.driver.title.max_length":             "A driver's title is at most {N} characters",
	"synthesis.driver.summary.max_length":           "its summary at most {N};",
	"synthesis.driver.qualification.max_length":     "a driver's qualification is at most {N}",
	"synthesis.driver.affected_subjects.max_count":  "at most {N} affected_subjects",
	"synthesis.finding.kind.max_length":             "A finding's kind is at most {N} characters",
	"synthesis.finding.summary.max_length":          "its summary at most {N}, with at most",
	"synthesis.finding.subjects.max_count":          "with at most {N} subjects",
	"synthesis.claimed_fact.field.max_length":       "A claimed fact's field is at most {N} characters",
	"synthesis.driver.path_ids.max_count":           "carries at most {N} path_ids",
	"synthesis.driver.claimed_fact_ids.max_count":   "at most {N} claimed_fact_ids",
	"synthesis.finding.claimed_fact_ids.max_count":  "at most {N} claimed_fact_ids",
	"synthesis.claimed_fact.claim_id.max_length":    identifierLengthPhrase,
	"synthesis.driver.evidence_ref_ids.max_count":   "at most {N} evidence_ref_ids each",
	"synthesis.finding.evidence_ref_ids.max_count":  "at most {N} evidence_ref_ids each",
	"synthesis.evidence_ref_ids.max_count":          "result-level evidence_ref_ids list holds at most {N}",
	"synthesis.drivers.max_count":                   "Return at most {N} drivers",
	"synthesis.strongest_pressures.max_count":       "at most {N} strongest_pressures",
	"synthesis.strongest_pressures.item_max_length": "strongest_pressures (each at most {N} characters)",
	"synthesis.remaining_work.max_count":            "at most {N} each of remaining_work",
	"synthesis.readiness_gaps.max_count":            "at most {N} each of remaining_work",
	"synthesis.conflicts.max_count":                 "at most {N} each of remaining_work",
	"synthesis.limitations.max_count":               "at most {N} limitations",
	"synthesis.limitations.item_max_length":         "each limitation at most {N} characters",
	"synthesis.warnings.max_count":                  "at most {N} warnings",
	"synthesis.warnings.item_max_length":            "each warning at most {N} characters",
	"synthesis.claimed_facts.max_count":             "restate at most {N} claimed_facts",
	"synthesis.direct_judgment.max_length":          "direct_judgment and current_state are at most {N} characters each",
	"synthesis.current_state.max_length":            "direct_judgment and current_state are at most {N} characters each",
	"synthesis.deterministic_answer.max_length":     "deterministic_answer at most {N}",
	"synthesis.driver.driver_id.max_length":         identifierLengthPhrase,
	"synthesis.finding.finding_id.max_length":       identifierLengthPhrase,
}

// identifierLengthPhrase is the ONE sentence that states the model-minted
// identifier length, shared by driver_id, finding_id and claim_id because
// the prompt genuinely states them together (codex round-9 F3).
//
// They previously anchored to "each at most {N} characters, and at most",
// which is the path_ids/claimed_fact_ids clause: it carries the same number
// 256 for an unrelated reason and never names an identifier, so all three
// identifier bounds were being "proved" by a sentence about something else.
const identifierLengthPhrase = "every driver_id, finding_id, and claim_id MUST be at least 8 and at most {N} characters"

// boundFields names the JSON field each bound actually governs, so an anchor
// can be checked STRUCTURALLY instead of trusted (codex round-9 F3, the
// third round this same class returned).
//
// A phrase can carry the right number, be unique, and still be attached to
// the wrong sentence: driver_id, finding_id and claim_id anchored to the
// path_ids/claimed_fact_ids clause, which states 256 for a different reason
// and never names an identifier at all. Uniqueness and value checks both
// passed, because neither asks the only question that catches it -- does the
// sentence the model reads for this bound actually mention this field?
//
// TestEveryPromptAnchorNamesTheFieldItBounds answers it mechanically, and
// TestBoundFieldsAreRealContractFields keeps this column honest by requiring
// every value to be a JSON field name contracts/v1 actually declares, so a
// mis-anchored bound cannot be rescued by inventing a word for it.
var boundFields = map[string]string{
	"interpretation.requested_judgment.max_length":               "requested_judgment",
	"interpretation.subject_terms.max_count":                     "subject_terms",
	"interpretation.comparison_terms.max_count":                  "comparison_terms",
	"interpretation.subject_term.max_length":                     "subject_terms",
	"interpretation.fact_requirements.max_count":                 "fact_requirements",
	"interpretation.fact_requirement.parameter_key.max_length":   "parameters",
	"interpretation.fact_requirement.parameter_value.max_length": "parameters",
	"interpretation.fact_requirement.parameters.max_count":       "parameters",
	"interpretation.clarification_reason.max_length":             "clarification_reason",

	"synthesis.driver.title.max_length":             "title",
	"synthesis.driver.summary.max_length":           "summary",
	"synthesis.driver.qualification.max_length":     "qualification",
	"synthesis.driver.affected_subjects.max_count":  "affected_subjects",
	"synthesis.finding.kind.max_length":             "kind",
	"synthesis.finding.summary.max_length":          "summary",
	"synthesis.finding.subjects.max_count":          "subjects",
	"synthesis.claimed_fact.field.max_length":       "field",
	"synthesis.driver.path_ids.max_count":           "path_ids",
	"synthesis.driver.claimed_fact_ids.max_count":   "claimed_fact_ids",
	"synthesis.finding.claimed_fact_ids.max_count":  "claimed_fact_ids",
	"synthesis.claimed_fact.claim_id.max_length":    "claim_id",
	"synthesis.driver.evidence_ref_ids.max_count":   "evidence_ref_ids",
	"synthesis.finding.evidence_ref_ids.max_count":  "evidence_ref_ids",
	"synthesis.evidence_ref_ids.max_count":          "evidence_ref_ids",
	"synthesis.drivers.max_count":                   "drivers",
	"synthesis.strongest_pressures.max_count":       "strongest_pressures",
	"synthesis.strongest_pressures.item_max_length": "strongest_pressures",
	"synthesis.remaining_work.max_count":            "remaining_work",
	"synthesis.readiness_gaps.max_count":            "readiness_gaps",
	"synthesis.conflicts.max_count":                 "conflicts",
	"synthesis.limitations.max_count":               "limitations",
	"synthesis.limitations.item_max_length":         "limitations",
	"synthesis.warnings.max_count":                  "warnings",
	"synthesis.warnings.item_max_length":            "warnings",
	"synthesis.claimed_facts.max_count":             "claimed_facts",
	"synthesis.direct_judgment.max_length":          "direct_judgment",
	"synthesis.current_state.max_length":            "current_state",
	"synthesis.deterministic_answer.max_length":     "deterministic_answer",
	"synthesis.driver.driver_id.max_length":         "driver_id",
	"synthesis.finding.finding_id.max_length":       "finding_id",
}

// promptClause returns the sentence or semicolon-delimited clause of prompt
// that contains phrase -- the span of text a reader takes as one statement,
// which is the unit an anchor is either right or wrong about.
func promptClause(prompt, phrase string) (string, bool) {
	start := strings.Index(prompt, phrase)
	if start < 0 {
		return "", false
	}
	end := start + len(phrase)
	// Walk outward to the nearest clause boundary on each side. The phrase
	// itself may contain a boundary (one anchor ends in ";"), so the search
	// starts from the phrase's own edges.
	left := 0
	for _, delimiter := range []string{". ", "; ", "\n"} {
		if index := strings.LastIndex(prompt[:start], delimiter); index >= 0 && index+len(delimiter) > left {
			left = index + len(delimiter)
		}
	}
	right := len(prompt)
	for _, delimiter := range []string{". ", "; ", "\n"} {
		if index := strings.Index(prompt[end:], delimiter); index >= 0 && end+index < right {
			right = end + index
		}
	}
	return prompt[left:right], true
}

// TestEveryPromptAnchorNamesTheFieldItBounds requires each bound's anchor to
// sit in a clause that names the field it bounds.
//
// This is the check that catches a correctly-valued, unique anchor pinned to
// the wrong sentence -- the shape that survived two previous rounds.
func TestEveryPromptAnchorNamesTheFieldItBounds(t *testing.T) {
	for _, testCase := range modelFacingBounds() {
		t.Run(testCase.registryName, func(t *testing.T) {
			field, ok := boundFields[testCase.registryName]
			if !ok {
				t.Fatalf("%s declares no field, so its anchor cannot be checked structurally", testCase.registryName)
			}
			phrase, ok := boundPhrases[testCase.registryName]
			if !ok {
				t.Fatalf("%s has no prompt phrase", testCase.registryName)
			}
			resolved := strings.ReplaceAll(phrase, "{N}", strconv.Itoa(testCase.limit))
			clause, found := promptClause(testCase.prompt, resolved)
			if !found {
				t.Fatalf("the prompt does not contain %q", resolved)
			}
			if !strings.Contains(clause, field) {
				t.Errorf("%s anchors to a clause that never names %q, so the number is pinned to the wrong statement:\n  anchor: %q\n  clause: %q",
					testCase.registryName, field, resolved, clause)
			}
		})
	}
}

// TestBoundFieldsAreRealContractFields keeps boundFields from becoming free
// text: every field named there must be a JSON field contracts/v1 actually
// declares on a model-facing shape.
func TestBoundFieldsAreRealContractFields(t *testing.T) {
	declared := map[string]struct{}{}
	for _, shape := range []any{
		contractsv1.ContextFabricInterpretedQuestion{},
		contractsv1.ContextFabricFactRequirement{},
		contractsv1.ContextFabricDriverJudgment{},
		contractsv1.ContextFabricFinding{},
		contractsv1.ContextFabricClaimedFact{},
		contractsv1.ContextFabricInvestigationResult{},
	} {
		shapeType := reflect.TypeOf(shape)
		for i := 0; i < shapeType.NumField(); i++ {
			if name := strings.Split(shapeType.Field(i).Tag.Get("json"), ",")[0]; name != "" && name != "-" {
				declared[name] = struct{}{}
			}
		}
	}
	for registryName, field := range boundFields {
		if _, ok := declared[field]; !ok {
			t.Errorf("boundFields[%q] names %q, which is not a JSON field any model-facing contracts/v1 shape declares", registryName, field)
		}
	}
}

// TestEveryRegistryBoundDeclaresItsField is the completeness half: a bound
// with no declared field would silently skip the structural check above.
func TestEveryRegistryBoundDeclaresItsField(t *testing.T) {
	for _, bound := range contractsv1.ContextFabricModelFacingBounds {
		if _, ok := boundFields[bound.Name]; !ok {
			t.Errorf("registry bound %q declares no entry in boundFields, so its anchor is unchecked", bound.Name)
		}
	}
	for registryName := range boundFields {
		if _, ok := boundPhrases[registryName]; !ok {
			t.Errorf("boundFields declares %q, which has no prompt phrase", registryName)
		}
	}
}

func TestPromptsStateEveryModelFacingBound(t *testing.T) {
	for _, testCase := range modelFacingBounds() {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.atLimit(); err != nil {
				t.Fatalf("a value at the documented limit was rejected (%v); the limit in this table is smaller than the validator's, so the prompt is understating it", err)
			}
			if err := testCase.over(); err == nil {
				t.Fatal("a value past the documented limit was accepted; the limit in this table is larger than the validator's, so the prompt is overstating it")
			}
			for _, mention := range testCase.mentions {
				if !strings.Contains(testCase.prompt, mention) {
					t.Errorf("the prompt never states %q, so the model cannot honour this bound", mention)
				}
			}
			// The NUMBER is derived from the validator-backed limit, never
			// written into this table (codex round-4 F6). A literal here
			// is the defect, not the safeguard: when the bound moved from
			// 250 to 100 the prompt kept saying 250 and this proof stayed
			// green, because it was searching for the stale string it had
			// been told to expect. Deriving it means the proof can only
			// pass when the prompt states what the validator enforces.
			// Anchored to the SENTENCE that names this bound, not to the
			// whole prompt (codex round-5 R5-7). A prompt-wide search
			// passes as soon as the number appears anywhere, so a stale
			// phrase can be validated by an unrelated bound that happens
			// to share a value -- the proof goes green while the sentence
			// the model actually reads still lies.
			if len(testCase.mentions) == 0 {
				t.Fatalf("%s states no phrase to anchor its limit check to", testCase.registryName)
			}
			// The prompt must contain a phrase that is unique to THIS
			// bound and carries its number (codex round-6 F5). A
			// proximity window still let a neighbour satisfy the check:
			// the interpretation prompt holds 128, 1000 and 32 in one
			// clause about parameters, so a stale parameter-count could
			// be "proved" by the key-length number sitting beside it.
			phrase, ok := boundPhrases[testCase.registryName]
			if !ok {
				t.Fatalf("%s has no unique prompt phrase registered", testCase.registryName)
			}
			want := strings.ReplaceAll(phrase, "{N}", strconv.Itoa(testCase.limit))
			if !strings.Contains(testCase.prompt, want) {
				t.Errorf("the prompt does not contain the phrase that states %s:\n  want: %q\nThe number must appear in a phrase unique to this bound, not merely somewhere nearby.",
					testCase.registryName, want)
			}
		})
	}
}

// TestModelFacingBoundRegistryIsFullyCovered is the mechanical completeness
// half of the oracle: every entry contracts/v1.ContextFabricModelFacingBounds
// declares must have a matching, correctly-valued case in modelFacingBounds()
// here. A registry entry with no matching case fails loudly by name, rather
// than the omission passing silently the way a purely hand-maintained table
// would. Combined with TestPromptsStateEveryModelFacingBound's atLimit/over/
// mentions checks on every covered case, this is what makes "a NEW validator
// bound cannot ship unstated" an enforced property instead of a convention:
// widening contracts/v1's registry without adding the matching wired case
// here fails this test; adding the case without a prompt statement fails the
// case's own mentions assertion above.
func TestModelFacingBoundRegistryIsFullyCovered(t *testing.T) {
	byName := make(map[string]boundCase, len(modelFacingBounds()))
	for _, testCase := range modelFacingBounds() {
		if _, exists := byName[testCase.registryName]; exists {
			t.Fatalf("modelFacingBounds() has more than one case for registry entry %q", testCase.registryName)
		}
		byName[testCase.registryName] = testCase
	}
	seen := make(map[string]struct{}, len(contractsv1.ContextFabricModelFacingBounds))
	for _, bound := range contractsv1.ContextFabricModelFacingBounds {
		seen[bound.Name] = struct{}{}
		testCase, ok := byName[bound.Name]
		if !ok {
			t.Errorf("contracts/v1 registers model-facing bound %q (limit %d) with no matching case in modelFacingBounds() -- add one, wired against contractsv1.ContextFabricModelFacingBounds so it cannot drift, before this bound can ship", bound.Name, bound.Limit)
			continue
		}
		if testCase.limit != bound.Limit {
			t.Errorf("modelFacingBounds() case %q pins limit %d, but contracts/v1 registers %d for %q -- the case must read the bound from contractsv1.ContextFabricModelFacingBounds (or the constant it is built from), not a bare literal", testCase.name, testCase.limit, bound.Limit, bound.Name)
		}
	}
	for name := range byName {
		if _, exists := seen[name]; !exists {
			t.Errorf("modelFacingBounds() has a case for registry name %q, which contracts/v1.ContextFabricModelFacingBounds does not declare -- rename it to match or remove it", name)
		}
	}
}

type boundCase struct {
	name string
	// registryName is the contractsv1.ContextFabricModelFacingBound.Name
	// this case is wired against -- see TestModelFacingBoundRegistryIsFullyCovered.
	registryName string
	// limit is the numeric value this case pins, always read from a
	// contractsv1 constant (never a bare literal) so it cannot drift from
	// what the validator itself enforces.
	limit    int
	prompt   string
	mentions []string
	atLimit  func() error
	over     func() error
}

func modelFacingBounds() []boundCase {
	return []boundCase{
		{
			name: "interpretation/requested_judgment max length", registryName: "interpretation.requested_judgment.max_length",
			limit: contractsv1.ContextFabricRequestedJudgmentMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"requested_judgment"},
			atLimit: func() error {
				return interpretationWithJudgment(contractsv1.ContextFabricRequestedJudgmentMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithJudgment(contractsv1.ContextFabricRequestedJudgmentMaxLength + 1).Validate()
			},
		},
		{
			name: "interpretation/subject_terms max count", registryName: "interpretation.subject_terms.max_count",
			limit: contractsv1.ContextFabricSubjectTermsMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"subject_terms"},
			atLimit: func() error {
				return interpretationWithSubjectTerms(contractsv1.ContextFabricSubjectTermsMaxCount).Validate()
			},
			over: func() error {
				return interpretationWithSubjectTerms(contractsv1.ContextFabricSubjectTermsMaxCount + 1).Validate()
			},
		},
		{
			name: "interpretation/subject term max length", registryName: "interpretation.subject_term.max_length",
			limit: contractsv1.ContextFabricSubjectOrComparisonTermMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"subject_term"},
			atLimit: func() error {
				return interpretationWithTermLength(contractsv1.ContextFabricSubjectOrComparisonTermMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithTermLength(contractsv1.ContextFabricSubjectOrComparisonTermMaxLength + 1).Validate()
			},
		},
		{
			name: "interpretation/comparison_terms max count", registryName: "interpretation.comparison_terms.max_count",
			limit: contractsv1.ContextFabricComparisonTermsMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"comparison_terms"},
			atLimit: func() error {
				return interpretationWithComparisonTerms(contractsv1.ContextFabricComparisonTermsMaxCount).Validate()
			},
			over: func() error {
				return interpretationWithComparisonTerms(contractsv1.ContextFabricComparisonTermsMaxCount + 1).Validate()
			},
		},
		{
			name: "interpretation/clarification_reason max length", registryName: "interpretation.clarification_reason.max_length",
			limit: contractsv1.ContextFabricClarificationReasonMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"clarification_reason"},
			atLimit: func() error {
				return interpretationWithClarification(contractsv1.ContextFabricClarificationReasonMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithClarification(contractsv1.ContextFabricClarificationReasonMaxLength + 1).Validate()
			},
		},
		{
			// The count bound is now DERIVED from the fact-kind vocabulary
			// (codex round-9 F1), so it is exactly the achievable ceiling:
			// ContextFabricInterpretedQuestion.Validate rejects a duplicate
			// Kind, and there are only ContextFabricFactKindCount distinct
			// kinds to spend. atLimit builds that many distinct kinds and
			// proves the bound does not block the real ceiling; over proves
			// the bound is still the enforced ceiling, using cycled
			// (duplicate) kinds -- valid here only because the length check
			// short-circuits the whole boolean expression before the
			// per-Kind uniqueness loop ever runs once the count itself
			// already exceeds the bound.
			//
			// mentions carries NO number (self-found while fixing F6): a
			// literal here is the same defect the derived phrase check
			// exists to prevent -- it said "At most 64 fact_requirements"
			// and would have gone on passing against a prompt stating a
			// bound nothing enforced. The number is asserted by
			// boundPhrases, which substitutes the validator-backed limit.
			name: "interpretation/fact_requirements max count", registryName: "interpretation.fact_requirements.max_count",
			limit: contractsv1.ContextFabricFactRequirementsMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"fact_requirements"},
			atLimit: func() error {
				return interpretationWithDistinctFactRequirements(len(contextFabricAllFactKinds)).Validate()
			},
			over: func() error {
				return interpretationWithCycledFactRequirements(contractsv1.ContextFabricFactRequirementsMaxCount + 1).Validate()
			},
		},
		{
			name: "interpretation/fact requirement parameter value max length", registryName: "interpretation.fact_requirement.parameter_value.max_length",
			limit: contractsv1.ContextFabricFactRequirementParameterValueMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"parameters"},
			atLimit: func() error {
				return interpretationWithParameterValue(contractsv1.ContextFabricFactRequirementParameterValueMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithParameterValue(contractsv1.ContextFabricFactRequirementParameterValueMaxLength + 1).Validate()
			},
		},
		{
			name: "interpretation/fact requirement parameter key max length", registryName: "interpretation.fact_requirement.parameter_key.max_length",
			limit: contractsv1.ContextFabricFactRequirementParameterKeyMaxLength, prompt: interpretationSystemPrompt,
			mentions: []string{"parameters key"},
			atLimit: func() error {
				return interpretationWithParameterKey(contractsv1.ContextFabricFactRequirementParameterKeyMaxLength).Validate()
			},
			over: func() error {
				return interpretationWithParameterKey(contractsv1.ContextFabricFactRequirementParameterKeyMaxLength + 1).Validate()
			},
		},
		{
			// CHAOS-3770 F3 residual (codex round 2): distinct from the
			// per-key/per-value LENGTH bounds above -- this is how many
			// parameter entries ONE fact_requirements[] item may carry.
			name: "interpretation/fact requirement parameters max count", registryName: "interpretation.fact_requirement.parameters.max_count",
			limit: contractsv1.ContextFabricFactRequirementParametersMaxCount, prompt: interpretationSystemPrompt,
			mentions: []string{"parameters"},
			atLimit: func() error {
				return interpretationWithParameterCount(contractsv1.ContextFabricFactRequirementParametersMaxCount).Validate()
			},
			over: func() error {
				return interpretationWithParameterCount(contractsv1.ContextFabricFactRequirementParametersMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/driver_id max length", registryName: "synthesis.driver.driver_id.max_length",
			limit: contractsv1.ContextFabricModelMintedIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"driver_id"},
			atLimit:  func() error { return driverWithID(contractsv1.ContextFabricModelMintedIDMaxLength).Validate() },
			over:     func() error { return driverWithID(contractsv1.ContextFabricModelMintedIDMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/driver title max length", registryName: "synthesis.driver.title.max_length",
			limit: contractsv1.ContextFabricDriverTitleMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"title"},
			atLimit:  func() error { return driverWithTitle(contractsv1.ContextFabricDriverTitleMaxLength).Validate() },
			over:     func() error { return driverWithTitle(contractsv1.ContextFabricDriverTitleMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/driver summary max length", registryName: "synthesis.driver.summary.max_length",
			limit: contractsv1.ContextFabricDriverSummaryMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"summary"},
			atLimit:  func() error { return driverWithSummary(contractsv1.ContextFabricDriverSummaryMaxLength).Validate() },
			over:     func() error { return driverWithSummary(contractsv1.ContextFabricDriverSummaryMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/driver qualification max length", registryName: "synthesis.driver.qualification.max_length",
			limit: contractsv1.ContextFabricDriverQualificationMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"qualification"},
			atLimit: func() error {
				return driverWithQualification(contractsv1.ContextFabricDriverQualificationMaxLength).Validate()
			},
			over: func() error {
				return driverWithQualification(contractsv1.ContextFabricDriverQualificationMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/driver affected_subjects max count", registryName: "synthesis.driver.affected_subjects.max_count",
			limit: contractsv1.ContextFabricDriverAffectedSubjectsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"affected_subjects"},
			atLimit: func() error {
				return driverWithSubjects(contractsv1.ContextFabricDriverAffectedSubjectsMaxCount).Validate()
			},
			over: func() error {
				return driverWithSubjects(contractsv1.ContextFabricDriverAffectedSubjectsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-3 R3-1: enforced (SubjectRef.Validate(), via
			// uniqueSubjects in ContextFabricDriverJudgment.Validate()) but
			// had no registry entry, no case, and no prompt statement until
			// this fix -- the class (a) exclusion this bound was
			// (incorrectly) covered by is documented and retracted in
			// ContextFabricModelFacingBounds's doc comment.
			name: "synthesis/driver affected_subjects item canonical_id max length", registryName: "synthesis.driver.affected_subjects.item_canonical_id_max_length",
			limit: contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"canonical ID at most 256"},
			atLimit: func() error {
				return driverWithAffectedSubjectCanonicalIDLength(contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength).Validate()
			},
			over: func() error {
				return driverWithAffectedSubjectCanonicalIDLength(contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength + 1).Validate()
			},
		},
		{
			// See the matching comment on the canonical_id case above.
			name: "synthesis/driver affected_subjects item label max length", registryName: "synthesis.driver.affected_subjects.item_label_max_length",
			limit: contractsv1.ContextFabricSubjectRefLabelMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"label at most 512"},
			atLimit: func() error {
				return driverWithAffectedSubjectLabelLength(contractsv1.ContextFabricSubjectRefLabelMaxLength).Validate()
			},
			over: func() error {
				return driverWithAffectedSubjectLabelLength(contractsv1.ContextFabricSubjectRefLabelMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/driver path_ids max count", registryName: "synthesis.driver.path_ids.max_count",
			limit: contractsv1.ContextFabricDriverPathIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids"},
			atLimit:  func() error { return driverWithPathIDs(contractsv1.ContextFabricDriverPathIDsMaxCount).Validate() },
			over:     func() error { return driverWithPathIDs(contractsv1.ContextFabricDriverPathIDsMaxCount + 1).Validate() },
		},
		{
			name: "synthesis/driver claimed_fact_ids max count", registryName: "synthesis.driver.claimed_fact_ids.max_count",
			limit: contractsv1.ContextFabricDriverClaimedFactIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"claimed_fact_ids"},
			atLimit: func() error {
				return driverWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount).Validate()
			},
			over: func() error {
				return driverWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-2 R2-2: enforced (validate_context_fabric_result.go's
			// uniqueTrimmedStrings(d.PathIDs, ContextFabricIdentifierRefMaxLength))
			// and already stated in the prompt ("each at most 256 characters"),
			// but had no registry entry and no case here until this fix.
			name: "synthesis/driver path_id item max length", registryName: "synthesis.driver.path_ids.item_max_length",
			limit: contractsv1.ContextFabricIdentifierRefMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids and at most 250 claimed_fact_ids, each at most 256"},
			atLimit: func() error {
				return driverWithPathIDLength(contractsv1.ContextFabricIdentifierRefMaxLength).Validate()
			},
			over: func() error {
				return driverWithPathIDLength(contractsv1.ContextFabricIdentifierRefMaxLength + 1).Validate()
			},
		},
		{
			// See the matching comment on the path_id item-length case above.
			name: "synthesis/driver claimed_fact_id item max length", registryName: "synthesis.driver.claimed_fact_ids.item_max_length",
			limit: contractsv1.ContextFabricIdentifierRefMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids and at most 250 claimed_fact_ids, each at most 256"},
			atLimit: func() error {
				return driverWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength).Validate()
			},
			over: func() error {
				return driverWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/driver evidence_ref_ids max count", registryName: "synthesis.driver.evidence_ref_ids.max_count",
			limit: contractsv1.ContextFabricNestedEvidenceRefIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids"},
			atLimit: func() error {
				return driverWithEvidenceRefIDs(contractsv1.ContextFabricNestedEvidenceRefIDsMaxCount).Validate()
			},
			over: func() error {
				return driverWithEvidenceRefIDs(contractsv1.ContextFabricNestedEvidenceRefIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-3 R3-1: enforced (boundedEvidenceRefs's
			// stringLengthBetween, called from ContextFabricDriverJudgment.Validate())
			// but had no registry entry, no case, and no prompt statement
			// until this fix -- same retracted class (a) exclusion as the
			// affected_subjects item-length bounds above.
			name: "synthesis/driver evidence_ref_id item max length", registryName: "synthesis.driver.evidence_ref_ids.item_max_length",
			limit: contractsv1.ContextFabricEvidenceRefIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids, each at most 256"},
			atLimit: func() error {
				return driverWithEvidenceRefIDLength(contractsv1.ContextFabricEvidenceRefIDMaxLength).Validate()
			},
			over: func() error {
				return driverWithEvidenceRefIDLength(contractsv1.ContextFabricEvidenceRefIDMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/finding_id max length", registryName: "synthesis.finding.finding_id.max_length",
			limit: contractsv1.ContextFabricModelMintedIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"finding_id"},
			atLimit:  func() error { return findingWithID(contractsv1.ContextFabricModelMintedIDMaxLength).Validate() },
			over:     func() error { return findingWithID(contractsv1.ContextFabricModelMintedIDMaxLength + 1).Validate() },
		},
		{
			// Finding.Kind became a CLOSED VOCABULARY in codex round 12, so
			// no filler string of any length is a valid kind and the length
			// bound can no longer be probed at its own value on the write
			// path. Same shape as interpretation.fact_requirements.max_count:
			// atLimit proves the bound does not block the real achievable
			// ceiling -- the longest legal vocabulary member -- rather than
			// pretending a 128-character kind is constructible.
			//
			// over's rejection is now OVERDETERMINED on this path (too long
			// AND not a member). The length bound's own proof lives where it
			// is still the operative check: contracts/v1's
			// TestFindingKindStaysReadableForStoredRows asserts an
			// over-length kind is rejected even on the lenient stored-read
			// path, where the vocabulary is deliberately not enforced.
			//
			// mentions carries no number (self-found, same defect as the
			// round-9 fact_requirements case): a literal here is what the
			// derived phrase check exists to prevent.
			name: "synthesis/finding kind max length", registryName: "synthesis.finding.kind.max_length",
			limit: contractsv1.ContextFabricFindingKindMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"kind is at most"},
			atLimit:  func() error { return findingWithLongestLegalKind().Validate() },
			over:     func() error { return findingWithKind(contractsv1.ContextFabricFindingKindMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/finding summary max length", registryName: "synthesis.finding.summary.max_length",
			limit: contractsv1.ContextFabricFindingSummaryMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"summary"},
			atLimit:  func() error { return findingWithSummary(contractsv1.ContextFabricFindingSummaryMaxLength).Validate() },
			over: func() error {
				return findingWithSummary(contractsv1.ContextFabricFindingSummaryMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/finding subjects max count", registryName: "synthesis.finding.subjects.max_count",
			limit: contractsv1.ContextFabricFindingSubjectsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"subjects"},
			atLimit:  func() error { return findingWithSubjects(contractsv1.ContextFabricFindingSubjectsMaxCount).Validate() },
			over: func() error {
				return findingWithSubjects(contractsv1.ContextFabricFindingSubjectsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-3 R3-1: see the matching driver-side comment.
			name: "synthesis/finding subjects item canonical_id max length", registryName: "synthesis.finding.subjects.item_canonical_id_max_length",
			limit: contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"canonical ID at most 256"},
			atLimit: func() error {
				return findingWithSubjectCanonicalIDLength(contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength).Validate()
			},
			over: func() error {
				return findingWithSubjectCanonicalIDLength(contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/finding subjects item label max length", registryName: "synthesis.finding.subjects.item_label_max_length",
			limit: contractsv1.ContextFabricSubjectRefLabelMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"label at most 512"},
			atLimit: func() error {
				return findingWithSubjectLabelLength(contractsv1.ContextFabricSubjectRefLabelMaxLength).Validate()
			},
			over: func() error {
				return findingWithSubjectLabelLength(contractsv1.ContextFabricSubjectRefLabelMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/finding evidence_ref_ids max count", registryName: "synthesis.finding.evidence_ref_ids.max_count",
			limit: contractsv1.ContextFabricNestedEvidenceRefIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids"},
			atLimit: func() error {
				return findingWithEvidenceRefIDs(contractsv1.ContextFabricNestedEvidenceRefIDsMaxCount).Validate()
			},
			over: func() error {
				return findingWithEvidenceRefIDs(contractsv1.ContextFabricNestedEvidenceRefIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-3 R3-1: see the matching driver-side comment.
			name: "synthesis/finding evidence_ref_id item max length", registryName: "synthesis.finding.evidence_ref_ids.item_max_length",
			limit: contractsv1.ContextFabricEvidenceRefIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids, each at most 256"},
			atLimit: func() error {
				return findingWithEvidenceRefIDLength(contractsv1.ContextFabricEvidenceRefIDMaxLength).Validate()
			},
			over: func() error {
				return findingWithEvidenceRefIDLength(contractsv1.ContextFabricEvidenceRefIDMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/finding claimed_fact_ids max count", registryName: "synthesis.finding.claimed_fact_ids.max_count",
			limit: contractsv1.ContextFabricDriverClaimedFactIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"claimed_fact_ids"},
			atLimit: func() error {
				return findingWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount).Validate()
			},
			over: func() error {
				return findingWithClaimedFactIDs(contractsv1.ContextFabricDriverClaimedFactIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-2 R2-2: see the matching driver-side case
			// above -- Finding.Validate() enforces this the same way
			// (uniqueTrimmedStrings(f.ClaimedFactIDs, ContextFabricIdentifierRefMaxLength)),
			// and the SAME prompt sentence covers both driver and finding
			// (prompts.go states them together).
			name: "synthesis/finding claimed_fact_id item max length", registryName: "synthesis.finding.claimed_fact_ids.item_max_length",
			limit: contractsv1.ContextFabricIdentifierRefMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"path_ids and at most 250 claimed_fact_ids, each at most 256"},
			atLimit: func() error {
				return findingWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength).Validate()
			},
			over: func() error {
				return findingWithClaimedFactIDLength(contractsv1.ContextFabricIdentifierRefMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/claim_id max length", registryName: "synthesis.claimed_fact.claim_id.max_length",
			limit: contractsv1.ContextFabricModelMintedIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"claim_id"},
			atLimit:  func() error { return claimWithID(contractsv1.ContextFabricModelMintedIDMaxLength).Validate() },
			over:     func() error { return claimWithID(contractsv1.ContextFabricModelMintedIDMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/claimed fact field max length", registryName: "synthesis.claimed_fact.field.max_length",
			limit: contractsv1.ContextFabricClaimedFieldMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"field"},
			atLimit:  func() error { return claimWithField(contractsv1.ContextFabricClaimedFieldMaxLength).Validate() },
			over:     func() error { return claimWithField(contractsv1.ContextFabricClaimedFieldMaxLength + 1).Validate() },
		},
		{
			// CHAOS-3784 round-3 R3-1: see the matching driver-side comment.
			name: "synthesis/claimed fact subject canonical_id max length", registryName: "synthesis.claimed_fact.subject.canonical_id_max_length",
			limit: contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"canonical ID at most 256"},
			atLimit: func() error {
				return claimWithSubjectCanonicalIDLength(contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength).Validate()
			},
			over: func() error {
				return claimWithSubjectCanonicalIDLength(contractsv1.ContextFabricSubjectRefCanonicalIDMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/claimed fact subject label max length", registryName: "synthesis.claimed_fact.subject.label_max_length",
			limit: contractsv1.ContextFabricSubjectRefLabelMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"label at most 512"},
			atLimit: func() error {
				return claimWithSubjectLabelLength(contractsv1.ContextFabricSubjectRefLabelMaxLength).Validate()
			},
			over: func() error {
				return claimWithSubjectLabelLength(contractsv1.ContextFabricSubjectRefLabelMaxLength + 1).Validate()
			},
		},
		{
			// CHAOS-3784 round-3 R3-1: enforced (ContextFabricScalarValue.Validate())
			// but had no registry entry, no case, and no prompt statement
			// until this fix -- same retracted class (a) exclusion.
			name: "synthesis/claimed fact value max length", registryName: "synthesis.claimed_fact.value.max_length",
			limit: contractsv1.ContextFabricClaimedFactValueMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"string value is at most 4000"},
			atLimit: func() error {
				return claimWithValueLength(contractsv1.ContextFabricClaimedFactValueMaxLength).Validate()
			},
			over: func() error {
				return claimWithValueLength(contractsv1.ContextFabricClaimedFactValueMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/strongest_pressures max count", registryName: "synthesis.strongest_pressures.max_count",
			limit: contractsv1.ContextFabricStrongestPressuresMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"at most 50 strongest_pressures"},
			atLimit: func() error {
				return resultWithStrongestPressures(contractsv1.ContextFabricStrongestPressuresMaxCount).Validate()
			},
			over: func() error {
				return resultWithStrongestPressures(contractsv1.ContextFabricStrongestPressuresMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/strongest_pressures item max length", registryName: "synthesis.strongest_pressures.item_max_length",
			limit: contractsv1.ContextFabricStrongestPressureMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"strongest_pressures"},
			atLimit: func() error {
				return resultWithStrongestPressureLength(contractsv1.ContextFabricStrongestPressureMaxLength).Validate()
			},
			over: func() error {
				return resultWithStrongestPressureLength(contractsv1.ContextFabricStrongestPressureMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/drivers max count", registryName: "synthesis.drivers.max_count",
			limit: contractsv1.ContextFabricDriversMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"at most 50 drivers"},
			atLimit:  func() error { return resultWithDrivers(contractsv1.ContextFabricDriversMaxCount).Validate() },
			over:     func() error { return resultWithDrivers(contractsv1.ContextFabricDriversMaxCount + 1).Validate() },
		},
		{
			name: "synthesis/remaining_work max count", registryName: "synthesis.remaining_work.max_count",
			limit: contractsv1.ContextFabricRemainingWorkMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"remaining_work"},
			atLimit: func() error {
				return resultWithFindings("remaining_work", contractsv1.ContextFabricRemainingWorkMaxCount).Validate()
			},
			over: func() error {
				return resultWithFindings("remaining_work", contractsv1.ContextFabricRemainingWorkMaxCount+1).Validate()
			},
		},
		{
			name: "synthesis/readiness_gaps max count", registryName: "synthesis.readiness_gaps.max_count",
			limit: contractsv1.ContextFabricReadinessGapsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"readiness_gaps"},
			atLimit: func() error {
				return resultWithFindings("readiness_gaps", contractsv1.ContextFabricReadinessGapsMaxCount).Validate()
			},
			over: func() error {
				return resultWithFindings("readiness_gaps", contractsv1.ContextFabricReadinessGapsMaxCount+1).Validate()
			},
		},
		{
			name: "synthesis/conflicts max count", registryName: "synthesis.conflicts.max_count",
			limit: contractsv1.ContextFabricConflictsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"conflicts"},
			atLimit: func() error {
				return resultWithFindings("conflicts", contractsv1.ContextFabricConflictsMaxCount).Validate()
			},
			over: func() error {
				return resultWithFindings("conflicts", contractsv1.ContextFabricConflictsMaxCount+1).Validate()
			},
		},
		{
			name: "synthesis/direct_judgment max length", registryName: "synthesis.direct_judgment.max_length",
			limit: contractsv1.ContextFabricDirectJudgmentMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"direct_judgment"},
			atLimit: func() error {
				return resultWithJudgment(contractsv1.ContextFabricDirectJudgmentMaxLength).Validate()
			},
			over: func() error {
				return resultWithJudgment(contractsv1.ContextFabricDirectJudgmentMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/current_state max length", registryName: "synthesis.current_state.max_length",
			limit: contractsv1.ContextFabricCurrentStateMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"current_state"},
			atLimit: func() error {
				return resultWithCurrentState(contractsv1.ContextFabricCurrentStateMaxLength).Validate()
			},
			over: func() error {
				return resultWithCurrentState(contractsv1.ContextFabricCurrentStateMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/deterministic_answer max length", registryName: "synthesis.deterministic_answer.max_length",
			limit: contractsv1.ContextFabricDeterministicAnswerMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"deterministic_answer"},
			atLimit: func() error {
				return resultWithDeterministicAnswer(contractsv1.ContextFabricDeterministicAnswerMaxLength).Validate()
			},
			over: func() error {
				return resultWithDeterministicAnswer(contractsv1.ContextFabricDeterministicAnswerMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/limitations max count", registryName: "synthesis.limitations.max_count",
			limit: contractsv1.ContextFabricLimitationsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"limitations"},
			atLimit:  func() error { return resultWithLimitations(contractsv1.ContextFabricLimitationsMaxCount).Validate() },
			over: func() error {
				return resultWithLimitations(contractsv1.ContextFabricLimitationsMaxCount + 1).Validate()
			},
		},
		{
			name: "synthesis/limitations item max length", registryName: "synthesis.limitations.item_max_length",
			limit: contractsv1.ContextFabricLimitationMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"limitation"},
			atLimit: func() error {
				return resultWithLimitationLength(contractsv1.ContextFabricLimitationMaxLength).Validate()
			},
			over: func() error {
				return resultWithLimitationLength(contractsv1.ContextFabricLimitationMaxLength + 1).Validate()
			},
		},
		{
			name: "synthesis/warnings max count", registryName: "synthesis.warnings.max_count",
			limit: contractsv1.ContextFabricWarningsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"warnings"},
			atLimit:  func() error { return resultWithWarnings(contractsv1.ContextFabricWarningsMaxCount).Validate() },
			over:     func() error { return resultWithWarnings(contractsv1.ContextFabricWarningsMaxCount + 1).Validate() },
		},
		{
			name: "synthesis/warnings item max length", registryName: "synthesis.warnings.item_max_length",
			limit: contractsv1.ContextFabricWarningMaxLength, prompt: synthesisSystemPrompt,
			mentions: []string{"warning"},
			atLimit:  func() error { return resultWithWarningLength(contractsv1.ContextFabricWarningMaxLength).Validate() },
			over:     func() error { return resultWithWarningLength(contractsv1.ContextFabricWarningMaxLength + 1).Validate() },
		},
		{
			name: "synthesis/evidence_ref_ids (top-level) max count", registryName: "synthesis.evidence_ref_ids.max_count",
			limit: contractsv1.ContextFabricEvidenceRefIDsMaxCount, prompt: synthesisSystemPrompt,
			mentions: []string{"evidence_ref_ids"},
			atLimit: func() error {
				return resultWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount).Validate()
			},
			over: func() error {
				return resultWithEvidenceRefIDs(contractsv1.ContextFabricEvidenceRefIDsMaxCount + 1).Validate()
			},
		},
		{
			// CHAOS-3770 F3 residual (codex round 2): the model decides how
			// MANY claimed facts to write (unlike claimed_fact_ids on a
			// driver/finding, which only reference entries in this list and
			// are already covered above).
			name: "synthesis/claimed_facts max count", registryName: "synthesis.claimed_facts.max_count",
			limit: contractsv1.ContextFabricClaimedFactsMaxCount, prompt: synthesisSystemPrompt,
			// A single precise phrase, not independent "claimed_facts"/"250"
			// substrings: both already appear elsewhere in this prompt for
			// unrelated reasons (the claimed_fact_ids reference bound, the
			// closed-vocabulary sentence), so loose substrings alone would
			// pass without an actual dedicated statement of THIS bound.
			mentions: []string{"at most 250 claimed_facts"},
			atLimit: func() error {
				return resultWithClaimedFacts(contractsv1.ContextFabricClaimedFactsMaxCount).Validate()
			},
			over: func() error {
				return resultWithClaimedFacts(contractsv1.ContextFabricClaimedFactsMaxCount + 1).Validate()
			},
		},
	}
}

func filler(n int) string { return strings.Repeat("a", n) }

func boundsSubject(index int) contractsv1.ContextFabricSubjectRef {
	return contractsv1.ContextFabricSubjectRef{
		Kind:        contractsv1.ContextFabricSubjectProject,
		CanonicalID: fmt.Sprintf("project_%04d", index),
		Label:       fmt.Sprintf("Project %04d", index),
	}
}

func baseInterpretation() contractsv1.ContextFabricInterpretedQuestion {
	return contractsv1.ContextFabricInterpretedQuestion{
		Shape:             contractsv1.ContextFabricShapeOpen,
		RequestedJudgment: "actual status and current drivers",
		TimeContext:       contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
		FactRequirements:  []contractsv1.ContextFabricFactRequirement{},
	}
}

func interpretationWithJudgment(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.RequestedJudgment = filler(length)
	return question
}

func interpretationWithSubjectTerms(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.SubjectTerms = uniqueTerms(count)
	return question
}

func interpretationWithComparisonTerms(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.ComparisonTerms = uniqueTerms(count)
	return question
}

func interpretationWithTermLength(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.SubjectTerms = []string{filler(length)}
	return question
}

func interpretationWithClarification(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.ClarificationNeeded = true
	question.ClarificationReason = filler(length)
	return question
}

// contextFabricAllFactKinds derives from contracts/v1's exported closed
// vocabulary instead of restating it (codex round-9 F1). The restated copy
// was a second list to drift against, and it carried the claim that the
// vocabulary is "deliberately fewer" than the count bound -- which was
// exactly the confusion: the bound is now DERIVED from this vocabulary, so
// the two can no longer disagree.
//
// interpretationWithDistinctFactRequirements and
// interpretationWithCycledFactRequirements remain two constructors because
// at-limit needs distinct kinds and over-limit cannot have them.
var contextFabricAllFactKinds = func() []contractsv1.ContextFabricFactKind {
	vocabulary := contractsv1.ContextFabricFactKindVocabulary()
	return vocabulary[:]
}()

// interpretationWithDistinctFactRequirements builds count DISTINCT
// fact_requirement kinds, and panics if count exceeds the vocabulary's
// size -- ContextFabricInterpretedQuestion.Validate rejects a duplicate
// kind, so this constructor can only ever reach as many requirements as
// there are legal kinds.
func interpretationWithDistinctFactRequirements(count int) contractsv1.ContextFabricInterpretedQuestion {
	if count > len(contextFabricAllFactKinds) {
		panic(fmt.Sprintf("interpretationWithDistinctFactRequirements(%d): only %d distinct fact kinds exist", count, len(contextFabricAllFactKinds)))
	}
	question := baseInterpretation()
	requirements := make([]contractsv1.ContextFabricFactRequirement, 0, count)
	for i := 0; i < count; i++ {
		requirements = append(requirements, contractsv1.ContextFabricFactRequirement{Kind: contextFabricAllFactKinds[i]})
	}
	question.FactRequirements = requirements
	return question
}

// interpretationWithCycledFactRequirements builds count fact_requirements,
// cycling through the vocabulary (so kinds repeat once count exceeds its
// size). It is valid ONLY for a count that already exceeds
// ContextFabricFactRequirementsMaxCount: ContextFabricInterpretedQuestion.Validate's
// length check short-circuits the whole boolean expression before the
// per-Kind uniqueness loop below it ever runs, so the repeated kinds this
// produces never actually reach that loop.
func interpretationWithCycledFactRequirements(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	requirements := make([]contractsv1.ContextFabricFactRequirement, 0, count)
	for i := 0; i < count; i++ {
		requirements = append(requirements, contractsv1.ContextFabricFactRequirement{Kind: contextFabricAllFactKinds[i%len(contextFabricAllFactKinds)]})
	}
	question.FactRequirements = requirements
	return question
}

func interpretationWithParameterValue(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: map[string]string{"scope": filler(length)},
	}}
	return question
}

func interpretationWithParameterKey(length int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: map[string]string{filler(length): "scope"},
	}}
	return question
}

// interpretationWithParameterCount builds ONE fact_requirements[] entry
// carrying count distinct parameter keys -- see
// "interpretation/fact requirement parameters max count".
func interpretationWithParameterCount(count int) contractsv1.ContextFabricInterpretedQuestion {
	question := baseInterpretation()
	params := make(map[string]string, count)
	for i := 0; i < count; i++ {
		params[fmt.Sprintf("key_%04d", i)] = "value"
	}
	question.FactRequirements = []contractsv1.ContextFabricFactRequirement{{
		Kind:       contractsv1.ContextFabricFactStatus,
		Parameters: params,
	}}
	return question
}

func uniqueTerms(count int) []string {
	terms := make([]string, 0, count)
	for i := 0; i < count; i++ {
		terms = append(terms, fmt.Sprintf("term_%04d", i))
	}
	return terms
}

// baseDriver is deliberately in the relationship category: relationship and
// narrative are the two categories that require no claimed fact, so these
// cases isolate the bound under test from value-level closure.
func baseDriver() contractsv1.ContextFabricDriverJudgment {
	return contractsv1.ContextFabricDriverJudgment{
		DriverID:         "driver_00000001",
		Standing:         contractsv1.ContextFabricDriverPrincipal,
		Category:         string(contractsv1.ContextFabricDriverCategoryRelationship),
		Title:            "Release acceptance is open",
		Summary:          "The release acceptance work item is still open.",
		AffectedSubjects: []contractsv1.ContextFabricSubjectRef{boundsSubject(0)},
		PathIDs:          []string{"path_00000001"},
		EvidenceRefIDs:   []string{},
		Derivation:       contractsv1.ContextFabricDerivationCanonicalStructured,
		EpistemicStatus:  contractsv1.ContextFabricEpistemicObserved,
		Confidence:       0.5,
	}
}

// driverN returns a valid driver identical to baseDriver() except for a
// unique DriverID -- for exercising a RESULT-level collection count bound
// (Drivers), where each entry must independently validate AND all DriverIDs
// in the list must be unique (validateDrivers).
func driverN(index int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.DriverID = fmt.Sprintf("driver_%08d", index)
	driver.PathIDs = []string{fmt.Sprintf("path_%08d", index)}
	return driver
}

func driverWithID(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.DriverID = filler(length)
	return driver
}

func driverWithTitle(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Title = filler(length)
	return driver
}

func driverWithSummary(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Summary = filler(length)
	return driver
}

func driverWithQualification(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.Qualification = filler(length)
	return driver
}

func driverWithSubjects(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	subjects := make([]contractsv1.ContextFabricSubjectRef, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, boundsSubject(i))
	}
	driver.AffectedSubjects = subjects
	return driver
}

func driverWithPathIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.PathIDs = uniqueTerms(count)
	return driver
}

func driverWithClaimedFactIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.ClaimedFactIDs = uniqueTerms(count)
	return driver
}

// driverWithPathIDLength and driverWithClaimedFactIDLength are the
// per-item length counterparts of driverWithPathIDs/driverWithClaimedFactIDs
// above (which vary COUNT): a single path_id/claimed_fact_id of the given
// length, isolating the item-length bound from the count bound
// (CHAOS-3784 round-2 R2-2).
func driverWithPathIDLength(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.PathIDs = []string{filler(length)}
	return driver
}

func driverWithClaimedFactIDLength(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.ClaimedFactIDs = []string{filler(length)}
	return driver
}

func evidenceRefs(count int) []string {
	refs := make([]string, 0, count)
	for i := 0; i < count; i++ {
		refs = append(refs, fmt.Sprintf("evidence_%08d", i))
	}
	return refs
}

func driverWithEvidenceRefIDs(count int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.EvidenceRefIDs = evidenceRefs(count)
	return driver
}

// driverWithEvidenceRefIDLength, driverWithAffectedSubjectCanonicalIDLength,
// and driverWithAffectedSubjectLabelLength are CHAOS-3784 round-3 R3-1
// additions: the per-item length counterparts of driverWithEvidenceRefIDs/
// driverWithSubjects above (which vary COUNT).
func driverWithEvidenceRefIDLength(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.EvidenceRefIDs = []string{filler(length)}
	return driver
}

func driverWithAffectedSubjectCanonicalIDLength(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.AffectedSubjects = []contractsv1.ContextFabricSubjectRef{{
		Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: filler(length), Label: "Project",
	}}
	return driver
}

func driverWithAffectedSubjectLabelLength(length int) contractsv1.ContextFabricDriverJudgment {
	driver := baseDriver()
	driver.AffectedSubjects = []contractsv1.ContextFabricSubjectRef{{
		Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_0000", Label: filler(length),
	}}
	return driver
}

func baseFinding() contractsv1.ContextFabricFinding {
	return contractsv1.ContextFabricFinding{
		FindingID: "finding_00000001",
		Kind:      string(contractsv1.ContextFabricDriverCategoryNarrative),
		Summary:   "Release acceptance remains open.",
		Subjects:  []contractsv1.ContextFabricSubjectRef{boundsSubject(0)},
		// Unlike a driver, a finding may not have an empty evidence list:
		// boundedEvidenceRefs is called with allowEmpty=false for findings
		// and allowEmpty=true for drivers.
		EvidenceRefIDs: []string{"evidence_00000001"},
	}
}

// findingN returns a valid finding identical to baseFinding() except for a
// unique FindingID -- see driverN.
func findingN(index int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.FindingID = fmt.Sprintf("finding_%08d", index)
	return finding
}

func findingWithID(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.FindingID = filler(length)
	return finding
}

// findingWithLongestLegalKind builds a finding carrying the longest member of
// the closed driver-category vocabulary -- the largest kind a model can
// legally produce, and therefore the real ceiling the length bound must not
// block.
func findingWithLongestLegalKind() contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Kind = ""
	for _, category := range contractsv1.ContextFabricDriverCategoryVocabulary() {
		if len(string(category)) > len(finding.Kind) {
			finding.Kind = string(category)
		}
	}
	// The longest member is canonical-fact-shaped, so it requires a claimed
	// fact for value-level closure. Supplying one keeps this probe measuring
	// the LENGTH ceiling rather than failing on an unrelated rule.
	if _, required := contractsv1.ContextFabricDriverCategoryRequiresClaimedFact(contractsv1.ContextFabricDriverCategory(finding.Kind)); required && len(finding.ClaimedFactIDs) == 0 {
		finding.ClaimedFactIDs = []string{"claim_probe_00001"}
	}
	return finding
}

func findingWithKind(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Kind = filler(length)
	return finding
}

func findingWithSummary(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Summary = filler(length)
	return finding
}

func findingWithSubjects(count int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	subjects := make([]contractsv1.ContextFabricSubjectRef, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, boundsSubject(i))
	}
	finding.Subjects = subjects
	return finding
}

func findingWithEvidenceRefIDs(count int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.EvidenceRefIDs = evidenceRefs(count)
	return finding
}

// findingWithEvidenceRefIDLength, findingWithSubjectCanonicalIDLength, and
// findingWithSubjectLabelLength are the finding-side counterparts of the
// driver-side helpers above (CHAOS-3784 round-3 R3-1).
func findingWithEvidenceRefIDLength(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.EvidenceRefIDs = []string{filler(length)}
	return finding
}

func findingWithSubjectCanonicalIDLength(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Subjects = []contractsv1.ContextFabricSubjectRef{{
		Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: filler(length), Label: "Project",
	}}
	return finding
}

func findingWithSubjectLabelLength(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.Subjects = []contractsv1.ContextFabricSubjectRef{{
		Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_0000", Label: filler(length),
	}}
	return finding
}

func findingWithClaimedFactIDs(count int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.ClaimedFactIDs = uniqueTerms(count)
	return finding
}

// findingWithClaimedFactIDLength is driverWithClaimedFactIDLength's
// finding-side counterpart (CHAOS-3784 round-2 R2-2).
func findingWithClaimedFactIDLength(length int) contractsv1.ContextFabricFinding {
	finding := baseFinding()
	finding.ClaimedFactIDs = []string{filler(length)}
	return finding
}

func claimWithID(length int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: filler(length),
		Kind:    contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0),
		Field:   "status",
		Value:   contractsv1.ContextFabricScalarValue{String: &value},
	}
}

func claimWithField(length int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: "claim_00000001",
		Kind:    contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0),
		Field:   filler(length),
		Value:   contractsv1.ContextFabricScalarValue{String: &value},
	}
}

// claimWithSubjectCanonicalIDLength, claimWithSubjectLabelLength, and
// claimWithValueLength are CHAOS-3784 round-3 R3-1 additions: the
// remaining fields ContextFabricClaimedFact.Validate() bounds by length.
func claimWithSubjectCanonicalIDLength(length int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: "claim_00000001", Kind: contractsv1.ContextFabricFactStatus,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: filler(length), Label: "Project"},
		Field:   "status", Value: contractsv1.ContextFabricScalarValue{String: &value},
	}
}

func claimWithSubjectLabelLength(length int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: "claim_00000001", Kind: contractsv1.ContextFabricFactStatus,
		Subject: contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project_0000", Label: filler(length)},
		Field:   "status", Value: contractsv1.ContextFabricScalarValue{String: &value},
	}
}

func claimWithValueLength(length int) contractsv1.ContextFabricClaimedFact {
	value := filler(length)
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: "claim_00000001", Kind: contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0), Field: "status",
		Value: contractsv1.ContextFabricScalarValue{String: &value},
	}
}

// baseResult builds a minimally valid ContextFabricInvestigationResult --
// every collection non-nil and empty, every required sub-object valid -- so
// that varying exactly ONE collection's size (below) isolates the bound
// under test from every other one ContextFabricInvestigationResult.Validate
// enforces.
func baseResult() contractsv1.ContextFabricInvestigationResult {
	project := boundsSubject(0)
	return contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      "result_12345678",
		RequestID:     "request_12345678",
		GeneratedAt:   time.Now().UTC(),
		Status:        contractsv1.ContextFabricInvestigationComplete,
		Question:      "What is the actual status of Ask Dev and what is driving it?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeOpen, RequestedJudgment: "status_and_drivers",
			TimeContext:      contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution:  contractsv1.ContextFabricSubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{project}},
		DirectJudgment:     "Ask Dev is not release-ready.",
		CurrentState:       "Required work remains.",
		StrongestPressures: []string{},
		Drivers:            []contractsv1.ContextFabricDriverJudgment{},
		RemainingWork:      []contractsv1.ContextFabricFinding{},
		ReadinessGaps:      []contractsv1.ContextFabricFinding{},
		Paths:              []contractsv1.ContextFabricRelationshipPath{},
		Conflicts:          []contractsv1.ContextFabricFinding{},
		Limitations:        []string{},
		EvidenceRefIDs:     []string{},
		ClaimedFacts:       []contractsv1.ContextFabricClaimedFact{},
		Coverage:           contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}, DegradedReasons: []string{}},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "acr-v1", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "test",
			ProjectionVersion: "projection-v1", QueryVersion: "query-v1", InterpretationVersion: "interpret-v1",
			SynthesisVersion: "synthesis-v1", CanonicalServiceVersion: "ops-v1", ModelIdentity: "test/model-v1",
		},
		DeterministicAnswer: "Ask Dev is not release-ready because required work remains.",
		Warnings:            []string{},
	}
}

func resultWithStrongestPressures(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.StrongestPressures = uniqueTerms(count)
	return result
}

func resultWithStrongestPressureLength(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.StrongestPressures = []string{filler(length)}
	return result
}

func resultWithDrivers(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	drivers := make([]contractsv1.ContextFabricDriverJudgment, 0, count)
	for i := 0; i < count; i++ {
		drivers = append(drivers, driverN(i))
	}
	result.Drivers = drivers
	return result
}

func resultWithFindings(field string, count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	findings := make([]contractsv1.ContextFabricFinding, 0, count)
	for i := 0; i < count; i++ {
		findings = append(findings, findingN(i))
	}
	switch field {
	case "remaining_work":
		result.RemainingWork = findings
	case "readiness_gaps":
		result.ReadinessGaps = findings
	case "conflicts":
		result.Conflicts = findings
	default:
		panic("resultWithFindings: unknown field " + field)
	}
	return result
}

func resultWithJudgment(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.DirectJudgment = filler(length)
	return result
}

func resultWithCurrentState(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.CurrentState = filler(length)
	return result
}

func resultWithDeterministicAnswer(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.DeterministicAnswer = filler(length)
	return result
}

func resultWithLimitations(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Limitations = uniqueTerms(count)
	return result
}

func resultWithLimitationLength(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Limitations = []string{filler(length)}
	return result
}

func resultWithWarnings(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Warnings = uniqueTerms(count)
	return result
}

func resultWithWarningLength(length int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.Warnings = []string{filler(length)}
	return result
}

func resultWithEvidenceRefIDs(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	result.EvidenceRefIDs = evidenceRefs(count)
	return result
}

// claimN returns a valid claimed fact identical in shape to claimWithField's
// baseline except for a unique ClaimID -- for exercising the RESULT-level
// claimed_facts count bound, where every entry must independently validate
// AND all ClaimIDs in the list must be unique (validateClaimedFacts).
func claimN(index int) contractsv1.ContextFabricClaimedFact {
	value := "in_progress"
	return contractsv1.ContextFabricClaimedFact{
		ClaimID: fmt.Sprintf("claim_%08d", index),
		Kind:    contractsv1.ContextFabricFactStatus,
		Subject: boundsSubject(0),
		Field:   "status",
		Value:   contractsv1.ContextFabricScalarValue{String: &value},
	}
}

func resultWithClaimedFacts(count int) contractsv1.ContextFabricInvestigationResult {
	result := baseResult()
	claims := make([]contractsv1.ContextFabricClaimedFact, 0, count)
	for i := 0; i < count; i++ {
		claims = append(claims, claimN(i))
	}
	result.ClaimedFacts = claims
	return result
}

// TestChangingOnePromptNumberFailsExactlyOneBound is the codex round-6 F5
// mutation check.
//
// The phrase map is only trustworthy if each phrase is genuinely unique to
// its bound. This mutates ONE number in a prompt and requires exactly the
// assertions tied to that phrase to stop matching. If a mutation broke
// nothing, that bound's phrase was not actually pinned; if it broke an
// unrelated bound, two bounds share a phrase and one is being proved by the
// other's number -- which is the defect this replaced.
func TestChangingOnePromptNumberFailsExactlyOneBound(t *testing.T) {
	for _, mutation := range []struct {
		name         string
		prompt       string
		from, to     string
		wantAffected []string
	}{
		{
			name: "driver title length", prompt: synthesisSystemPrompt,
			from: "A driver's title is at most 512 characters", to: "A driver's title is at most 999 characters",
			wantAffected: []string{"synthesis.driver.title.max_length"},
		},
		{
			name: "limitations count", prompt: synthesisSystemPrompt,
			from: "at most 100 limitations", to: "at most 999 limitations",
			wantAffected: []string{"synthesis.limitations.max_count"},
		},
		{
			name: "parameter value length", prompt: interpretationSystemPrompt,
			from: "each value at most 1000", to: "each value at most 999",
			wantAffected: []string{"interpretation.fact_requirement.parameter_value.max_length"},
		},
		{
			name: "subject terms count", prompt: interpretationSystemPrompt,
			from: "At most 50 subject_terms", to: "At most 999 subject_terms",
			wantAffected: []string{"interpretation.subject_terms.max_count"},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if !strings.Contains(mutation.prompt, mutation.from) {
				t.Fatalf("the prompt does not contain %q, so this mutation proves nothing", mutation.from)
			}
			mutated := strings.Replace(mutation.prompt, mutation.from, mutation.to, 1)

			affected := make([]string, 0, 2)
			for _, testCase := range modelFacingBounds() {
				if testCase.prompt != mutation.prompt {
					continue
				}
				phrase, ok := boundPhrases[testCase.registryName]
				if !ok {
					continue
				}
				want := strings.ReplaceAll(phrase, "{N}", strconv.Itoa(testCase.limit))
				if !strings.Contains(mutated, want) {
					affected = append(affected, testCase.registryName)
				}
			}
			sort.Strings(affected)
			expected := append([]string(nil), mutation.wantAffected...)
			sort.Strings(expected)
			if !reflect.DeepEqual(affected, expected) {
				t.Errorf("mutating %q broke %v, want exactly %v.\nA bound broken by an unrelated mutation shares a phrase; a bound broken by nothing is not pinned.",
					mutation.from, affected, expected)
			}
		})
	}
}

// exemptPromptNumerals are numbers a prompt states that are NOT
// registry-backed bounds, each with the reason it is not one. Anything not
// listed and not registry-derived fails TestEveryPromptNumeralIsAccounted.
//
// An exemption is a claim that the number is not a validated bound. It is
// deliberately awkward to add, because the failure this test exists to
// catch is exactly a bound hiding as prose (codex round-7 F5).
// exemptPromptNumerals ties each exemption to the exact PHRASE it appears
// in, not to its bare value (codex round-8 F4). Exempting the value 1
// globally meant any new "at most 1" cap anywhere was silently accepted; an
// occurrence-anchored exemption only excuses the clause it was written for,
// so a new occurrence of the same number is unclassified until claimed.
var exemptPromptNumerals = []struct {
	phrase string
	why    string
}{
	{"at least 8 and at most", "identifier MINIMUM length: a floor, not a cap the registry governs"},
	{"at least 1 and at most 250 affected_subjects", "affected_subjects minimum: the model must name at least one"},
	{"confidence MUST be a number between 0 and 1 inclusive", "confidence range: a fixed unit interval, not a sized bound"},
}

// TestEveryPromptNumeralIsAccounted ships the enumeration that was
// previously run by hand (codex round-7 F5).
//
// Every numeral in a prompt is either the value of a registry-backed bound
// or an explicitly classified exemption. Hand-running this found an
// unpinned deterministic_answer limit; shipping it means the next one
// cannot survive to a review round.
func TestEveryPromptNumeralIsAccounted(t *testing.T) {
	registryValues := map[int]bool{}
	for _, bound := range contractsv1.ContextFabricModelFacingBounds {
		registryValues[bound.Limit] = true
	}

	for name, prompt := range map[string]string{
		"interpretation": interpretationSystemPrompt,
		"synthesis":      synthesisSystemPrompt,
	} {
		t.Run(name, func(t *testing.T) {
			unaccounted := make([]int, 0, 4)
			for _, numeral := range promptNumerals(prompt) {
				if registryValues[numeral] {
					continue
				}
				if exemptedByPhrase(prompt, numeral) {
					continue
				}
				unaccounted = append(unaccounted, numeral)
			}
			if len(unaccounted) > 0 {
				t.Errorf("prompt states %v, which is neither a registry-backed bound nor a classified exemption.\nA number in a prompt that nothing validates is a bound hiding as prose.", unaccounted)
			}
		})
	}
}

// exemptedByPhrase reports whether every occurrence of numeral in the
// prompt sits inside a phrase claimed by an exemption. A numeral that also
// appears somewhere unclaimed is NOT exempt.
func exemptedByPhrase(prompt string, numeral int) bool {
	needle := strconv.Itoa(numeral)
	claimed := false
	for _, exemption := range exemptPromptNumerals {
		if strings.Contains(exemption.phrase, needle) && strings.Contains(prompt, exemption.phrase) {
			claimed = true
			break
		}
	}
	if !claimed {
		return false
	}
	// Count occurrences outside the claimed phrases; any leftover means a
	// new, unclaimed use of the same number.
	stripped := prompt
	for _, exemption := range exemptPromptNumerals {
		stripped = strings.ReplaceAll(stripped, exemption.phrase, "")
	}
	for _, field := range strings.FieldsFunc(stripped, func(r rune) bool { return r < '0' || r > '9' }) {
		if field == needle {
			return false
		}
	}
	return true
}

func promptNumerals(prompt string) []int {
	var found []int
	seen := map[int]bool{}
	for _, field := range strings.FieldsFunc(prompt, func(r rune) bool { return r < '0' || r > '9' }) {
		value, err := strconv.Atoi(field)
		if err != nil || seen[value] {
			continue
		}
		seen[value] = true
		found = append(found, value)
	}
	sort.Ints(found)
	return found
}

// TestEveryRegistryBoundHasAUniquePromptAnchor is the codex round-7 F3
// closure: the mutation set is GENERATED from the registry rather than
// sampled by hand.
//
// For every registry-derived assertion, it changes that bound's number in
// the prompt and requires exactly that bound's assertions to stop matching.
// A bound broken by an unrelated mutation shares an anchor with it -- which
// is how claimed_fact_ids, evidence refs and the identifier bounds were
// quietly proving each other.
func TestEveryRegistryBoundHasAUniquePromptAnchor(t *testing.T) {
	cases := modelFacingBounds()
	for _, subject := range cases {
		phrase, ok := boundPhrases[subject.registryName]
		if !ok {
			t.Errorf("%s has no prompt phrase", subject.registryName)
			continue
		}
		t.Run(subject.registryName, func(t *testing.T) {
			original := strings.ReplaceAll(phrase, "{N}", strconv.Itoa(subject.limit))
			if !strings.Contains(subject.prompt, original) {
				t.Fatalf("prompt does not contain %q", original)
			}
			// A value no other bound in this prompt uses, so the mutation
			// cannot accidentally satisfy a neighbour.
			mutated := strings.Replace(subject.prompt, original,
				strings.ReplaceAll(phrase, "{N}", "424242"), 1)

			broken := make([]string, 0, 2)
			for _, other := range cases {
				if other.prompt != subject.prompt {
					continue
				}
				otherPhrase, ok := boundPhrases[other.registryName]
				if !ok {
					continue
				}
				want := strings.ReplaceAll(otherPhrase, "{N}", strconv.Itoa(other.limit))
				if !strings.Contains(mutated, want) {
					broken = append(broken, other.registryName)
				}
			}
			// Bounds that legitimately SHARE a sentence share a phrase by
			// construction (driver and finding claimed_fact_ids are one
			// clause). Those are expected to break together; what must not
			// happen is a bound breaking that does not share the phrase.
			for _, name := range broken {
				if boundPhrases[name] != phrase {
					t.Errorf("mutating %s also broke %s, which uses a DIFFERENT phrase: the anchors overlap and one bound is proving another",
						subject.registryName, name)
				}
			}
			if len(broken) == 0 {
				t.Errorf("mutating %s broke nothing: its anchor is not actually pinning it", subject.registryName)
			}
		})
	}
}
