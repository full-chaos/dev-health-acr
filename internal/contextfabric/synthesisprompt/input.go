package synthesisprompt

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// Input is the JSON document the synthesize call sends as the user message.
type Input struct {
	Question         string                            `json:"question"`
	Interpretation   contextfabric.InterpretedQuestion `json:"interpretation"`
	Resolution       contextfabric.SubjectResolution   `json:"subject_resolution"`
	Cohort           *contextfabric.Cohort             `json:"cohort,omitempty"`
	Paths            []contextfabric.RelationshipPath  `json:"paths"`
	DriverCandidates []contextfabric.DriverJudgment    `json:"driver_candidates"`
	Facts            []contextfabric.CanonicalFact     `json:"canonical_facts"`
	Coverage         contextfabric.Coverage            `json:"coverage"`
	// AnswerBudget is the per-request allowance the ONE item allocator
	// published. Omitted ENTIRELY when no budget is in force, so the model is
	// never shown a quota of zero where it should see no quota at all.
	AnswerBudget *AnswerBudget `json:"answer_budget,omitempty"`
}

// AnswerBudget is the model-facing view of the allocator's grants.
//
// COUNTS ONLY, and only the ones a model can act on. It names no group, no
// subject and no internal reason -- a prompt payload is answer-facing, and the
// operator-facing detail lives on the narrowing telemetry.
//
// WHY THE MODEL IS TOLD AT ALL. The model decides how many drivers, findings
// and claims to write, so an allowance it never sees can only be discovered
// afterwards, as an overrun. A number in the prompt is not an enforcement
// mechanism -- S7c owns enforcement -- but it is the only way prediction and
// outcome converge rather than being reconciled after the fact.
type AnswerBudget struct {
	// ItemsPerGroup is the per-group allowance, ABSENT when the answer has no
	// group axis and PRESENT-AND-ZERO when it has one whose allowance is
	// zero. It is derived from the group AND shared grants together, so an
	// item naming several groups is funded by the same allowance its usage
	// is measured against.
	//
	// A POINTER, and that is the whole point. As a plain int with omitempty
	// this field could not tell those two states apart: a grouped answer
	// whose allowance is zero serialized with no `items_per_group` at all,
	// identical on the wire to an answer with no groups -- while `groups: 1`
	// was still emitted beside it. The model was shown a group axis and no
	// allowance for it, and the prompt paragraph promising the field became
	// false for exactly the answers where the budget bites hardest.
	//
	// That is the same absence-versus-measured-zero distinction the
	// enforcement side keeps as `unavailable` versus `bounded_zero`, and the
	// same one this file's own ModelFacingAnswerBudget doc comment states
	// one level up about nil-versus-zeroed structs. It was being kept in two
	// places and broken in the third.
	ItemsPerGroup *int `json:"items_per_group,omitempty"`
	// Groups is how many groups that allowance is repeated across. Omitted
	// when there is no group axis, which is the SAME condition that makes
	// ItemsPerGroup nil -- the two are emitted and withheld together, never
	// one without the other.
	Groups int `json:"groups,omitempty"`
	// Global is the allowance for items belonging to the answer as a whole
	// rather than to any member or group.
	Global int `json:"global"`
	// PerMember is what is LEFT for drivers, findings and claims about
	// members: the member ROWS are committed off the top of the account
	// before any grant is published, so this number is already net of them.
	// The prompt states exactly that, and this is why the sentence is true.
	PerMember int `json:"per_member"`
}

// InputFromDomain composes the exact bounded-JSON payload
// SynthesizeAnswer sends the model. orgID (CHAOS-4690) feeds
// contextfabric.MergeCoverage's own fail-open reconcile WARN log only --
// never merge semantics; BuildSynthesisPrompt's prompt-preview path has no
// authenticated principal in scope and passes "".
func InputFromDomain(orgID string, input contextfabric.SynthesisInput) Input {
	return Input{
		Question: input.Request.Question, Interpretation: input.Interpretation,
		Resolution: input.Graph.Resolution, Cohort: input.Graph.Cohort,
		Paths: input.Graph.Paths, DriverCandidates: input.Graph.DriverCandidates,
		Facts: ModelFacingFacts(input.Facts.Facts), Coverage: contextfabric.MergeCoverage(orgID, input.Graph.Coverage, input.Facts.Coverage),
		AnswerBudget: ModelFacingAnswerBudget(input.Allocation),
	}
}

// ModelFacingAnswerBudget projects the allocator's grants into the payload, or
// nil when no budget is in force.
//
// NIL, NOT A ZEROED STRUCT. An omitted field says "no quota"; a present field
// full of zeros says "a quota of zero", and a model shown the latter has been
// told to write nothing at all. That is the same distinction the enforcement
// side keeps in its availability vocabulary, and it is worth keeping twice.
//
// Every number comes from an allocator METHOD, never from arithmetic here: the
// per-group allowance and the discretionary member grant have exactly one
// derivation each, and a second one at the prompt site is how the grants shown
// to producers and the predicates used downstream came to describe different
// permitted spending.
func ModelFacingAnswerBudget(allocation contextfabric.ItemAllocation) *AnswerBudget {
	if !allocation.InForce() {
		return nil
	}
	budget := &AnswerBudget{
		Groups:    allocation.Groups,
		Global:    allocation.Grant(contractsv1.ContextFabricItemBucketGlobal),
		PerMember: allocation.PerMemberGrant(),
	}
	// Present exactly when there IS a group axis, whatever the allowance
	// comes to -- including zero, which is a real instruction ("every group
	// item is over budget") and not an absence.
	if allocation.Groups > 0 {
		perGroup := allocation.GroupAllowance()
		budget.ItemsPerGroup = &perGroup
	}
	return budget
}

// ModelFacingFacts returns a copy of facts with every Rows-shaped field
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
func ModelFacingFacts(facts []contextfabric.CanonicalFact) []contextfabric.CanonicalFact {
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
