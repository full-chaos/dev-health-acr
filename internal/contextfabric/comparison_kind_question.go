package contextfabric

import (
	"slices"
	"strings"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// soleStatedKind is the one kind a caller stated on this request, by the
// expected_kinds field. Two kinds state no one kind, and a kind receipt reaches
// here as this field (interpretRequestFor).
func soleStatedKind(request InvestigationRequest) SubjectKind {
	if len(request.ExpectedKinds) != 1 {
		return ""
	}
	return request.ExpectedKinds[0]
}

// interpretRequestFor is the request the interpreter sees. It holds only the
// validated receipts, and a redeemed kind option reads as the field the caller
// could have sent, so the frame takes one reading of "the caller stated a kind"
// (CHAOS-12548). The interpreter reads ExpectedKinds for nothing else.
func interpretRequestFor(request InvestigationRequest, validated []contractsv1.ContextFabricBoundSubjectReceipt, canon requestStructureCanonicalization) InvestigationRequest {
	interpret := request
	interpret.PriorSubjectReceipts = validated
	if confirmed := confirmedExpectedKind(canon.Confirmed); confirmed != nil {
		interpret.ExpectedKinds = []SubjectKind{confirmed.Kind}
	}
	return interpret
}

// askComparisonKind turns the question a bare-name comparison gets into the one
// it needs (CHAOS-12548). The user named both sides, so "which one did you mean"
// asks what they already said. What is missing is the kind, and the answer states
// it on both sides, so the comparison resolves each by its own terms.
//
// It changes a turn that is already asking, and only a named pair that states no
// kind. The candidate, anchor and handle offers go with the old prompt: they ask
// the caller to pick one side. Dropping the rows and the options together keeps
// the offer whole, as GateOffersByFamily does. A kind that does not fit a side
// still asks for that side, on the next turn.
func askComparisonKind(frame *QuestionFrame, resolution SubjectResolution, material StructureOfferMaterial) (SubjectResolution, StructureOfferMaterial) {
	if strings.TrimSpace(resolution.ClarificationPrompt) == "" || len(material.KindOptions) == 0 ||
		!slices.Contains(material.Missing, contractsv1.ContextFabricStructureNeedExpectedKind) {
		return resolution, material
	}
	operands := ClassifyComparisonOperands(frame)
	if operands.Admission != ComparisonOutOfCutUnstatedKind {
		return resolution, material
	}
	prompt, asked := comparisonKindPrompt(operands.Slots, material.KindOptions)
	if !asked {
		return resolution, material
	}
	resolution.ClarificationPrompt = prompt
	material.Missing = []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}
	material.AnchorOptions = nil
	material.AnchorOptionsRequireV2 = false
	material.HandleOptions = nil
	material.CandidateOptions = nil
	return resolution, material
}

// comparisonKindPrompt names both sides in the user's own terms and the served
// kind words. A prompt past its bound is not asked.
func comparisonKindPrompt(slots []ComparisonOperandSlot, options []contractsv1.ContextFabricKindOption) (string, bool) {
	sides := make([]string, 0, len(slots))
	for _, slot := range slots {
		if slot.Variant != ComparisonOperandNamed || len(slot.Terms) == 0 {
			return "", false
		}
		sides = append(sides, `"`+slot.Terms[0]+`"`)
	}
	words := make([]string, 0, len(options))
	for _, option := range options {
		words = append(words, option.Label)
	}
	last := len(words) - 1
	kinds := words[last]
	if last > 0 {
		kinds = strings.Join(words[:last], ", ") + " or " + words[last]
	}
	prompt := "Are " + strings.Join(sides, " and ") + " each " + kinds + "?"
	return prompt, utf8.RuneCountInString(prompt) <= contractsv1.ContextFabricProjectedClarificationPromptMaxLength
}
