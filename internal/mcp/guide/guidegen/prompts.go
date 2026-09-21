package guidegen

import (
	"encoding/json"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

// fallbackFamily is the registry's catch-all. It is declared reachable but is
// not a shape to ask for, so no prompt offers it as an example.
const fallbackFamily = "unclassified"

// buildPromptVocab renders the registry snapshot the MCP prompts read. It
// refuses a family that has no authored example, so a family added to the
// registry cannot reach the prompts without one.
func buildPromptVocab(in Inputs) (string, error) {
	if err := coverage("subject kind", in.SubjectKinds, subjectKindTexts); err != nil {
		return "", err
	}
	if err := coverage("relative window", in.Windows, windowTexts); err != nil {
		return "", err
	}
	vocab := guide.PromptVocab{
		SubjectKinds: append([]string{}, in.SubjectKinds...),
		Windows:      append([]string{}, in.Windows...),
	}
	for _, family := range in.Families {
		text, ok := familyTexts[family.ID]
		if !ok {
			return "", fmt.Errorf("guidegen: question family %q has no authored text", family.ID)
		}
		vocab.Families = append(vocab.Families, guide.PromptFamily{
			ID:         family.ID,
			Example:    text.Example,
			Answerable: !family.Unreachable && family.ID != fallbackFamily,
		})
	}
	for _, r := range Receipts {
		vocab.Receipts = append(vocab.Receipts, guide.PromptReceipt{Field: r.Field, Prefix: r.Prefix, Offer: r.Offer})
	}
	data, err := json.MarshalIndent(vocab, "", "  ")
	if err != nil {
		return "", fmt.Errorf("guidegen: encode prompt vocabulary: %w", err)
	}
	return string(data) + "\n", nil
}
