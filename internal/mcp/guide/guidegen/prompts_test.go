package guidegen

import (
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide"
)

// promptGaps renders the shipped prompts and lists every registry member the
// prompt guidance does not carry.
func promptGaps(t *testing.T, in Inputs) []string {
	t.Helper()
	vocab, err := guide.LoadPromptVocab()
	if err != nil {
		t.Fatal(err)
	}
	investigate, err := guide.RenderPrompt(vocab, guide.PromptInvestigate, map[string]string{guide.ArgQuestion: "q"})
	if err != nil {
		t.Fatal(err)
	}
	continued, err := guide.RenderPrompt(vocab, guide.PromptContinue, map[string]string{guide.ArgQuestion: "q", guide.ArgParentResultID: "result_0001"})
	if err != nil {
		t.Fatal(err)
	}
	var gaps []string
	for _, family := range in.Families {
		answerable := !family.Unreachable && family.ID != fallbackFamily
		if in := strings.Contains(investigate.Text, "`"+family.ID+"`"); in != answerable {
			gaps = append(gaps, "family "+family.ID)
		}
	}
	var argText string
	for _, def := range guide.PromptDefs(vocab) {
		for _, arg := range def.Args {
			argText += " " + def.Name + "/" + arg.Name + ": " + arg.Description
		}
	}
	for _, kind := range in.SubjectKinds {
		if !strings.Contains(argText, "`"+kind+"`") {
			gaps = append(gaps, "kind "+kind)
		}
	}
	for _, window := range in.Windows {
		if !strings.Contains(argText, "`"+window+"`") {
			gaps = append(gaps, "window "+window)
		}
	}
	for _, receipt := range Receipts {
		for name, text := range map[string]string{"investigate": investigate.Text, "continue": continued.Text} {
			if !strings.Contains(text, "`"+receipt.Field+"`") {
				gaps = append(gaps, "receipt field "+receipt.Field+" in "+name)
			}
		}
	}
	slices.Sort(gaps)
	return gaps
}

func TestPromptGuidanceCoversEveryRegistryMember(t *testing.T) {
	if gaps := promptGaps(t, FromRegistries()); len(gaps) > 0 {
		t.Fatalf("prompt guidance lacks registry members: %v", gaps)
	}
}

// Planted defects: a member added to a registry but absent from the shipped
// prompt guidance must be reported.
func TestPromptGuidanceDetectsRegistryAdditions(t *testing.T) {
	base := FromRegistries()
	cases := map[string]func(*Inputs){
		"family":       func(in *Inputs) { in.Families = append(slices.Clone(in.Families), FamilyRow{ID: "planted_family"}) },
		"subject kind": func(in *Inputs) { in.SubjectKinds = append(slices.Clone(in.SubjectKinds), "planted_kind") },
		"window":       func(in *Inputs) { in.Windows = append(slices.Clone(in.Windows), "planted_window") },
		"reachability": func(in *Inputs) {
			in.Families = slices.Clone(in.Families)
			in.Families[0].Unreachable = !in.Families[0].Unreachable
		},
	}
	for name, plant := range cases {
		in := base
		plant(&in)
		if len(promptGaps(t, in)) == 0 {
			t.Errorf("planted %s went undetected", name)
		}
	}
}

// A registry family without authored text cannot be generated into the prompt
// vocabulary, so the generator fails before stale guidance can ship.
func TestPromptVocabBuildRefusesUnauthoredMembers(t *testing.T) {
	in := FromRegistries()
	in.Families = append(slices.Clone(in.Families), FamilyRow{ID: "planted_family"})
	if _, err := buildPromptVocab(in); err == nil {
		t.Fatal("a family without authored text must fail the build")
	}
	in = FromRegistries()
	in.SubjectKinds = append(slices.Clone(in.SubjectKinds), "planted_kind")
	if _, err := buildPromptVocab(in); err == nil {
		t.Fatal("a subject kind without authored text must fail the build")
	}
	in = FromRegistries()
	in.Windows = append(slices.Clone(in.Windows), "planted_window")
	if _, err := buildPromptVocab(in); err == nil {
		t.Fatal("a window without authored text must fail the build")
	}
}

func TestPromptVocabReceiptsMatchGuideTable(t *testing.T) {
	vocab, err := guide.LoadPromptVocab()
	if err != nil {
		t.Fatal(err)
	}
	if len(vocab.Receipts) != len(Receipts) {
		t.Fatalf("vocab has %d receipts, guide table %d", len(vocab.Receipts), len(Receipts))
	}
	for i, r := range Receipts {
		if vocab.Receipts[i].Field != r.Field || vocab.Receipts[i].Prefix != r.Prefix {
			t.Errorf("receipt %d differs: vocab %+v, table %+v", i, vocab.Receipts[i], r)
		}
	}
}
