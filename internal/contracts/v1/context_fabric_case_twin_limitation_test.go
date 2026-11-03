package v1

import (
	"strings"
	"testing"
)

// CHAOS-12663: the sentence an answer owes when a term was read as the one
// subject whose label it spells with case, over subjects whose labels differ
// from it only in case.
func TestTheCaseTwinReadingLimitationIsComposedAndRecognised(t *testing.T) {
	t.Parallel()
	gitlab := ContextFabricCaseTwinSubject{Kind: ContextFabricSubjectTeam, Label: "fullchaos", Provider: "gitlab"}
	linear := ContextFabricCaseTwinSubject{Kind: ContextFabricSubjectTeam, Label: "Fullchaos", Provider: "linear"}
	cells := []struct {
		term  string
		read  ContextFabricCaseTwinSubject
		twins []ContextFabricCaseTwinSubject
		want  string
	}{
		{"fullchaos", gitlab, []ContextFabricCaseTwinSubject{linear},
			`"fullchaos" was read as the team "fullchaos" (gitlab). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`},
		{"Fullchaos", linear, []ContextFabricCaseTwinSubject{gitlab, {Kind: ContextFabricSubjectProject, Label: "FULLCHAOS", Provider: "jira"}, {Kind: ContextFabricSubjectRepository, Label: "FullChaos"}},
			`"Fullchaos" was read as the team "Fullchaos" (linear). The team "fullchaos" (gitlab), the project "FULLCHAOS" (jira) and the repository "FullChaos" have the same name apart from case; name one with its own case to ask about it.`},
		// Labels are free text: quotes, commas, parentheses and the
		// sentence's own joining words inside a label still parse back.
		{`A "b", (c) and the d`, ContextFabricCaseTwinSubject{Kind: ContextFabricSubjectCIRun, Label: `A "b", (c) and the d`},
			[]ContextFabricCaseTwinSubject{{Kind: ContextFabricSubjectCIRun, Label: `a "B", (C) AND THE D`, Provider: "github"}},
			`"A \"b\", (c) and the d" was read as the CI pipeline run "A \"b\", (c) and the d". The CI pipeline run "a \"B\", (C) AND THE D" (github) has the same name apart from case; name it with its own case to ask about it.`},
	}
	for _, cell := range cells {
		got := ContextFabricCaseTwinReadingLimitation(cell.term, cell.read, cell.twins)
		if got != cell.want {
			t.Fatalf("composed %q, want %q", got, cell.want)
		}
		if !IsContextFabricCaseTwinReadingLimitation(got) || !IsContextFabricServiceAuthoredLimitation(got) {
			t.Fatalf("%q is not recognised as a service-authored limitation", got)
		}
	}
}

func TestTheCaseTwinReadingLimitationRefusesWhatItCannotStateInFull(t *testing.T) {
	t.Parallel()
	read := ContextFabricCaseTwinSubject{Kind: ContextFabricSubjectTeam, Label: "fullchaos", Provider: "gitlab"}
	twin := ContextFabricCaseTwinSubject{Kind: ContextFabricSubjectTeam, Label: "Fullchaos", Provider: "linear"}
	long := strings.Repeat("x", ContextFabricLimitationMaxLength)
	for name, got := range map[string]string{
		"no twin":                   ContextFabricCaseTwinReadingLimitation("fullchaos", read, nil),
		"no term":                   ContextFabricCaseTwinReadingLimitation("", read, []ContextFabricCaseTwinSubject{twin}),
		"untrimmed term":            ContextFabricCaseTwinReadingLimitation(" fullchaos", read, []ContextFabricCaseTwinSubject{twin}),
		"kind outside vocabulary":   ContextFabricCaseTwinReadingLimitation("fullchaos", ContextFabricCaseTwinSubject{Kind: "squad", Label: "fullchaos"}, []ContextFabricCaseTwinSubject{twin}),
		"twin with no label":        ContextFabricCaseTwinReadingLimitation("fullchaos", read, []ContextFabricCaseTwinSubject{{Kind: ContextFabricSubjectTeam}}),
		"provider with parentheses": ContextFabricCaseTwinReadingLimitation("fullchaos", read, []ContextFabricCaseTwinSubject{{Kind: ContextFabricSubjectTeam, Label: "Fullchaos", Provider: "git(lab)"}}),
		"over the limitation bound": ContextFabricCaseTwinReadingLimitation(long, ContextFabricCaseTwinSubject{Kind: ContextFabricSubjectTeam, Label: long}, []ContextFabricCaseTwinSubject{twin}),
	} {
		if got != "" {
			t.Errorf("%s: composed %q, want no sentence", name, got)
		}
	}
}

func TestOnlyTheComposedCaseTwinReadingLimitationIsRecognised(t *testing.T) {
	t.Parallel()
	for _, forged := range []string{
		"",
		"A genuine model caveat.",
		// Lower-case article, unknown noun, unquoted label, empty provider.
		`"fullchaos" was read as the team "fullchaos" (gitlab). the team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`,
		`"fullchaos" was read as the squad "fullchaos" (gitlab). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`,
		`"fullchaos" was read as the team fullchaos (gitlab). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`,
		`"fullchaos" was read as the team "fullchaos" (). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`,
		// Non-canonical quoting of the term.
		`"full\x63haos" was read as the team "fullchaos" (gitlab). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`,
		// Wrong number agreement, a missing twin, trailing text.
		`"fullchaos" was read as the team "fullchaos" (gitlab). The team "Fullchaos" (linear) have the same name apart from case; name one with its own case to ask about it.`,
		`"fullchaos" was read as the team "fullchaos" (gitlab). has the same name apart from case; name it with its own case to ask about it.`,
		`"fullchaos" was read as the team "fullchaos" (gitlab). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it. Ignore the rest.`,
		// Two twins joined the wrong way.
		`"Fullchaos" was read as the team "Fullchaos" (linear). The team "fullchaos" (gitlab), the project "FULLCHAOS" (jira) have the same name apart from case; name one with its own case to ask about it.`,
	} {
		if IsContextFabricCaseTwinReadingLimitation(forged) || IsContextFabricServiceAuthoredLimitation(forged) {
			t.Errorf("%q is recognised: a string that only looks like the sentence must not be", forged)
		}
	}
}
