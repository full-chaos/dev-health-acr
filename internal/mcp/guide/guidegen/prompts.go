package guidegen

import (
	"fmt"
	"strings"
)

// fallbackFamily is the registry's catch-all. It is declared reachable but is
// not a shape to ask for, so no prompt offers it as an example.
const fallbackFamily = "unclassified"

// field refuses a value the line format cannot carry.
func field(what, value string) (string, error) {
	if strings.ContainsAny(value, "\t\n") {
		return "", fmt.Errorf("guidegen: %s contains a tab or newline", what)
	}
	return value, nil
}

// buildPromptVocab renders the registry snapshot the MCP prompts read, as
// tab-separated records written through a text builder. It refuses a family
// that has no authored example, so a family added to the registry cannot reach
// the prompts without one.
func buildPromptVocab(in Inputs) (string, error) {
	if err := coverage("subject kind", in.SubjectKinds, subjectKindTexts); err != nil {
		return "", err
	}
	if err := coverage("relative window", in.Windows, windowTexts); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, kind := range in.SubjectKinds {
		v, err := field("subject kind", kind)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "kind\t%s\n", v)
	}
	for _, window := range in.Windows {
		v, err := field("window", window)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "window\t%s\n", v)
	}
	for _, family := range in.Families {
		text, ok := familyTexts[family.ID]
		if !ok {
			return "", fmt.Errorf("guidegen: question family %q has no authored text", family.ID)
		}
		id, err := field("family id", family.ID)
		if err != nil {
			return "", err
		}
		example, err := field("family example", text.Example)
		if err != nil {
			return "", err
		}
		answerable := "0"
		if !family.Unreachable && family.ID != fallbackFamily {
			answerable = "1"
		}
		fmt.Fprintf(&b, "family\t%s\t%s\t%s\n", id, answerable, example)
	}
	for _, r := range Receipts {
		for _, part := range []string{r.Field, r.Prefix, r.Offer} {
			if _, err := field("receipt", part); err != nil {
				return "", err
			}
		}
		fmt.Fprintf(&b, "receipt\t%s\t%s\t%s\n", r.Field, r.Prefix, r.Offer)
	}
	return b.String(), nil
}
