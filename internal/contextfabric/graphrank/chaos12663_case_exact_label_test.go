package graphrank

// CHAOS-12663: two subjects whose labels differ only in case ("Fullchaos", a
// linear team, and "fullchaos", a gitlab team). The term-to-label match folds
// case, so each term exact-matched both and every question naming either one
// clarified -- a comparison of the two could never publish. A unique
// case-exact label match now wins over its case-folded twins, and the answer
// says which subject the term was read as and names each twin.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// caseTwinNode is a node with one provider attribute, which is what the
// candidate's provider cue (and so the disclosure's provider word) reads.
func caseTwinNode(kind contextfabric.SubjectKind, canonicalID, label, provider string) CandidateNode {
	node := candidateNode(kind, canonicalID, label, 0.8, "*")
	if provider != "" {
		node.Attributes["provider_"+provider] = canonicalID
	}
	return node
}

func caseTwinPair() (linear, gitlab CandidateNode) {
	return caseTwinNode(contextfabric.SubjectTeam, "team:linear:CHAOS", "Fullchaos", "linear"),
		caseTwinNode(contextfabric.SubjectTeam, "team:gl:full.chaos", "fullchaos", "gitlab")
}

func resolveCaseTwinTerms(t *testing.T, backend *fakeGraphBackend, terms ...string) contextfabric.SubjectResolution {
	t.Helper()
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(terms...), backend.deps(), nil, nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	return resolution
}

func TestCHAOS12663_ASingleSubjectTermCommitsItsCaseExactLabelAndNamesTheTwin(t *testing.T) {
	t.Parallel()
	linear, gitlab := caseTwinPair()
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"fullchaos": {linear, gitlab}}}
	resolution := resolveCaseTwinTerms(t, backend, "fullchaos")
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "team:gl:full.chaos" {
		t.Fatalf("committed = %#v, want the gitlab team whose label is the term with its case", resolution.Committed)
	}
	if resolution.ClarificationPrompt != "" {
		t.Fatalf("clarification = %q, want none once the case-exact label committed", resolution.ClarificationPrompt)
	}
	want := []string{`"fullchaos" was read as the team "fullchaos" (gitlab). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`}
	if !slices.Equal(resolution.CaseTwinReadingLimitations, want) {
		t.Fatalf("case-twin lines = %q, want %q", resolution.CaseTwinReadingLimitations, want)
	}
}

func TestCHAOS12663_EveryCaseTwinIsNamed(t *testing.T) {
	t.Parallel()
	linear, gitlab := caseTwinPair()
	jira := caseTwinNode(contextfabric.SubjectProject, "project:jira:FC", "FULLCHAOS", "jira")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Fullchaos": {linear, gitlab, jira}}}
	resolution := resolveCaseTwinTerms(t, backend, "Fullchaos")
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "team:linear:CHAOS" {
		t.Fatalf("committed = %#v, want the linear team", resolution.Committed)
	}
	want := []string{`"Fullchaos" was read as the team "Fullchaos" (linear). The project "FULLCHAOS" (jira) and the team "fullchaos" (gitlab) have the same name apart from case; name one with its own case to ask about it.`}
	if !slices.Equal(resolution.CaseTwinReadingLimitations, want) {
		t.Fatalf("case-twin lines = %q, want %q", resolution.CaseTwinReadingLimitations, want)
	}
}

// The negatives keep today's behaviour: no case-exact label, more than one,
// exact matches from two different terms, two spellings of one term, and the
// alias and provider-key classes (which stay case-insensitive).
func TestCHAOS12663_AnythingButOneCaseExactLabelStaysAmbiguous(t *testing.T) {
	t.Parallel()
	linear, gitlab := caseTwinPair()
	cells := []struct {
		name    string
		results map[string][]CandidateNode
		terms   []string
	}{
		{"no case-exact label", map[string][]CandidateNode{"FULLCHAOS": {linear, gitlab}}, []string{"FULLCHAOS"}},
		{"two case-exact labels", map[string][]CandidateNode{"Fullchaos": {linear, gitlab,
			caseTwinNode(contextfabric.SubjectTeam, "team:jira:FC", "Fullchaos", "jira")}}, []string{"Fullchaos"}},
		{"exact matches from two different terms", map[string][]CandidateNode{
			"fullchaos": {linear, gitlab},
			"Acme":      {caseTwinNode(contextfabric.SubjectTeam, "team:github:acme", "Acme", "github")},
		}, []string{"fullchaos", "Acme"}},
		{"two spellings of one term", map[string][]CandidateNode{"Fullchaos": {linear, gitlab}, "fullchaos": {linear, gitlab}}, []string{"Fullchaos", "fullchaos"}},
		// A sentence too long for the limitation bound is no sentence, and a
		// pick that cannot state its sentence is not made.
		{"a reading too long to state", map[string][]CandidateNode{strings.Repeat("a", 500): {
			caseTwinNode(contextfabric.SubjectTeam, "team:linear:long", strings.Repeat("a", 500), "linear"),
			caseTwinNode(contextfabric.SubjectTeam, "team:gl:long", strings.Repeat("A", 500), "gitlab"),
			caseTwinNode(contextfabric.SubjectTeam, "team:jira:long", "A"+strings.Repeat("a", 499), "jira"),
		}}, []string{strings.Repeat("a", 500)}},
		// The node NAME matches the term, so the match is exact, but the
		// served label is another name: it is no case twin to name.
		{"an exact match through the node name", map[string][]CandidateNode{"fullchaos": {gitlab,
			withAttribute(withName(caseTwinNode(contextfabric.SubjectTeam, "team:linear:FCP", "Fullchaos Platform", "linear"), "Fullchaos"), "label", "Fullchaos Platform"),
		}}, []string{"fullchaos"}},
		{"alias collision", map[string][]CandidateNode{"fullchaos": {
			withAttribute(caseTwinNode(contextfabric.SubjectRepository, "repo:github:one", "One", "github"), "aliases", []string{"fullchaos"}),
			withAttribute(caseTwinNode(contextfabric.SubjectRepository, "repo:gitlab:two", "Two", "gitlab"), "aliases", []string{"FullChaos"}),
		}}, []string{"fullchaos"}},
		{"provider-key collision", map[string][]CandidateNode{"github:fullchaos": {
			withAttribute(caseTwinNode(contextfabric.SubjectRepository, "repo:github:one", "One", "github"), "provider_aliases", []string{"github:fullchaos"}),
			withAttribute(caseTwinNode(contextfabric.SubjectRepository, "repo:github:two", "Two", "github"), "provider_aliases", []string{"GitHub:FullChaos"}),
		}}, []string{"github:fullchaos"}},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			resolution := resolveCaseTwinTerms(t, &fakeGraphBackend{searchResults: cell.results}, cell.terms...)
			if len(resolution.Committed) != 0 {
				t.Fatalf("committed = %#v, want none", resolution.Committed)
			}
			if resolution.ClarificationPrompt == "" {
				t.Fatal("no clarification for an ambiguous term")
			}
			if len(resolution.CaseTwinReadingLimitations) != 0 {
				t.Fatalf("case-twin lines = %q on a resolution that read no term by case", resolution.CaseTwinReadingLimitations)
			}
		})
	}
}

func withName(node CandidateNode, name string) CandidateNode {
	node.Name = name
	return node
}

func withAttribute(node CandidateNode, key string, value interface{}) CandidateNode {
	node.Attributes[key] = value
	return node
}

// The rule is provider-agnostic and kind-agnostic: every provider pairing and
// every alias-lookup kind takes the case-exact label.
func TestCHAOS12663_TheCaseExactRuleHoldsAcrossProvidersAndKinds(t *testing.T) {
	t.Parallel()
	nouns := map[contextfabric.SubjectKind]string{
		contextfabric.SubjectTeam: "team", contextfabric.SubjectRepository: "repository", contextfabric.SubjectProject: "project",
	}
	cells := []struct {
		kind            contextfabric.SubjectKind
		exact, twin     string
		exactProvider   string
		twinProvider    string
		exactID, twinID string
	}{
		{contextfabric.SubjectTeam, "Platform", "platform", "jira", "github", "team:jira:PLAT", "team:github:platform"},
		{contextfabric.SubjectTeam, "platform", "Platform", "gitlab", "linear", "team:gl:platform", "team:linear:PLAT"},
		{contextfabric.SubjectTeam, "PLATFORM", "Platform", "github", "jira", "team:github:PLATFORM", "team:jira:PLAT"},
		{contextfabric.SubjectTeam, "Platform", "PLATFORM", "linear", "gitlab", "team:linear:PLAT", "team:gl:PLATFORM"},
		{contextfabric.SubjectRepository, "Dev-Health", "dev-health", "github", "gitlab", "repo:github:Dev-Health", "repo:gitlab:dev-health"},
		{contextfabric.SubjectProject, "Ask Dev", "ask dev", "jira", "linear", "project:jira:AD", "project:linear:ad"},
	}
	for _, cell := range cells {
		t.Run(fmt.Sprintf("%s %s over %s", cell.kind, cell.exactProvider, cell.twinProvider), func(t *testing.T) {
			t.Parallel()
			exact := caseTwinNode(cell.kind, cell.exactID, cell.exact, cell.exactProvider)
			twin := caseTwinNode(cell.kind, cell.twinID, cell.twin, cell.twinProvider)
			resolution := resolveCaseTwinTerms(t, &fakeGraphBackend{searchResults: map[string][]CandidateNode{cell.exact: {twin, exact}}}, cell.exact)
			if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != cell.exactID {
				t.Fatalf("committed = %#v, want %s", resolution.Committed, cell.exactID)
			}
			noun := nouns[cell.kind]
			want := []string{fmt.Sprintf(`%q was read as the %s %q (%s). The %s %q (%s) has the same name apart from case; name it with its own case to ask about it.`,
				cell.exact, noun, cell.exact, cell.exactProvider, noun, cell.twin, cell.twinProvider)}
			if !slices.Equal(resolution.CaseTwinReadingLimitations, want) {
				t.Fatalf("case-twin lines = %q, want %q", resolution.CaseTwinReadingLimitations, want)
			}
		})
	}
}

// caseTwinComparisonFrame is the live turn's frame: an explicit set of two
// named team operands whose terms differ only in case.
func caseTwinComparisonFrame(first, second string) *contextfabric.QuestionFrame {
	frame := contextfabric.DeriveFrameObligations(contextfabric.QuestionFrame{
		Goals: []contextfabric.InvestigationGoal{contextfabric.GoalCompare},
		SubjectExpression: contextfabric.SubjectExpression{
			Kind: contextfabric.SubjectExpressionExplicitSet,
			Explicit: &contextfabric.ExplicitSetExpression{Operands: []contextfabric.SubjectOperand{
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{
					Terms: []string{first}, ExpectedKind: slotGateKindPointer(contextfabric.SubjectTeam)}},
				{Kind: contextfabric.SubjectOperandNamed, Named: &contextfabric.NamedSubjectExpression{
					Terms: []string{second}, ExpectedKind: slotGateKindPointer(contextfabric.SubjectTeam)}},
			}},
		},
		Temporal: contextfabric.TemporalIntentCurrent,
		Version:  contextfabric.QuestionFrameVersion,
	}, nil)
	return &frame
}

// The executed defect, turn 1 (no selection) and turn 2 (both canonical ids
// carried as subject hints): each operand commits its case-exact team, the
// hints bind one to each operand, and the comparison publishes.
func TestCHAOS12663_TheCaseTwinComparisonPublishes(t *testing.T) {
	t.Parallel()
	for _, hinted := range []bool{false, true} {
		t.Run(fmt.Sprintf("subject hints=%t", hinted), func(t *testing.T) {
			t.Parallel()
			linear, gitlab := caseTwinPair()
			linearSubject, _ := NodeSubject(linear)
			gitlabSubject, _ := NodeSubject(gitlab)
			backend := &fakeGraphBackend{
				searchResults: map[string][]CandidateNode{"Fullchaos": {linear, gitlab}, "fullchaos": {linear, gitlab}},
				exactHints:    map[string]CandidateNode{SubjectKey(linearSubject): linear, SubjectKey(gitlabSubject): gitlab},
			}
			sink := &recordingBindingSink{}
			deps := backend.deps()
			deps.OperandResolutionSink = sink
			request := slotGateRequest()
			request.Question = "How do Fullchaos and fullchaos compare?"
			if hinted {
				request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{
					{Kind: contextfabric.SubjectTeam, ID: linearSubject.CanonicalID, Label: linearSubject.Label, Source: "candidate"},
					{Kind: contextfabric.SubjectTeam, ID: gitlabSubject.CanonicalID, Label: gitlabSubject.Label, Source: "candidate"},
				}
			}
			resolution, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(), storage.Principal{OrgID: "org_1"},
				request, testInterpreted("Fullchaos", "fullchaos"), deps, nil, nil, caseTwinComparisonFrame("Fullchaos", "fullchaos"), "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			if len(sink.bindings) != 1 || sink.bindings[0].UnboundCount != 0 {
				t.Fatalf("binding lines = %+v, want one with no unbound selection", sink.bindings)
			}
			if hinted && (sink.bindings[0].BoundCount != 2 || !slices.Equal(sink.bindings[0].BoundSlotPositions, []int{0, 1})) {
				t.Fatalf("binding = %+v, want each selection bound to the operand spelled with its label's case", sink.bindings[0])
			}
			if len(resolution.Committed) != 2 || resolution.Committed[0].CanonicalID != "team:linear:CHAOS" || resolution.Committed[1].CanonicalID != "team:gl:full.chaos" {
				t.Fatalf("committed = %#v, want the linear team then the gitlab team", resolution.Committed)
			}
			if resolution.ClarificationPrompt != "" {
				t.Fatalf("clarification = %q, want none for a published comparison", resolution.ClarificationPrompt)
			}
			var want []string
			if !hinted {
				// Each operand read its term by case, so each owes its line. A
				// selection is the user's own choice and owes none.
				want = []string{
					`"Fullchaos" was read as the team "Fullchaos" (linear). The team "fullchaos" (gitlab) has the same name apart from case; name it with its own case to ask about it.`,
					`"fullchaos" was read as the team "fullchaos" (gitlab). The team "Fullchaos" (linear) has the same name apart from case; name it with its own case to ask about it.`,
				}
			}
			if !slices.Equal(resolution.CaseTwinReadingLimitations, want) {
				t.Fatalf("case-twin lines = %q, want %q", resolution.CaseTwinReadingLimitations, want)
			}
		})
	}
}

// Binding reads the label the graph serves for the selected subject, never the
// label the caller sent with the hint, and still leaves a selection unbound
// when its label's case names neither operand or both.
func TestCHAOS12663_ASelectionBindsToTheOperandSpelledWithItsServedLabel(t *testing.T) {
	t.Parallel()
	linear, gitlab := caseTwinPair()
	linearSubject, _ := NodeSubject(linear)
	slot := func(position int, term string) contextfabric.ComparisonOperandSlot {
		return contextfabric.ComparisonOperandSlot{Position: position, Variant: contextfabric.ComparisonOperandNamed, Kind: contextfabric.SubjectTeam, Terms: []string{term}}
	}
	cells := []struct {
		name        string
		slots       []contextfabric.ComparisonOperandSlot
		hintLabel   string
		wantSlot    int
		wantUnbound int
	}{
		{"served label names operand one", []contextfabric.ComparisonOperandSlot{slot(0, "Fullchaos"), slot(1, "fullchaos")}, "Fullchaos", 0, 0},
		{"the caller's label is not read", []contextfabric.ComparisonOperandSlot{slot(0, "fullchaos"), slot(1, "Fullchaos")}, "fullchaos", 1, 0},
		{"served label names neither operand", []contextfabric.ComparisonOperandSlot{slot(0, "fullchaos"), slot(1, "FULLCHAOS")}, "Fullchaos", -1, 1},
		{"served label names both operands", []contextfabric.ComparisonOperandSlot{slot(0, "Fullchaos"), slot(1, "Fullchaos")}, "Fullchaos", -1, 1},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			t.Parallel()
			request := slotGateRequest()
			request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: contextfabric.SubjectTeam, ID: linearSubject.CanonicalID, Label: cell.hintLabel, Source: "candidate"}}
			backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{SubjectKey(linearSubject): linear, "unused": gitlab}}
			bound, unbound, err := bindReceiptsToSlots(context.Background(), storage.Principal{OrgID: "org_1"}, request, backend.deps(), cell.slots, nil, comparisonAliasClaimants{})
			if err != nil {
				t.Fatalf("bindReceiptsToSlots() error = %v", err)
			}
			if unbound != cell.wantUnbound {
				t.Fatalf("unbound = %d, want %d", unbound, cell.wantUnbound)
			}
			for index := range cell.slots {
				want := 0
				if index == cell.wantSlot {
					want = 1
				}
				if len(bound[index]) != want {
					t.Fatalf("slot %d bound %d, want %d (bound=%v)", index, len(bound[index]), want, bound)
				}
			}
		})
	}
}

// The pick commits on the exact-label tier or not at all. A keyed identity read
// that cannot prove it saw every same-label team refuses the pick as it refuses
// a lone exact label, and a refused pick states no reading: nothing was read.
func TestCHAOS12663_ARefusedPickCommitsNothingAndStatesNoReading(t *testing.T) {
	t.Parallel()
	linear, gitlab := caseTwinPair()
	backend := &fakeGraphBackend{
		searchResults:        map[string][]CandidateNode{"fullchaos": {linear, gitlab}},
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{},
		aliasLookupComplete:  false,
	}
	resolution := resolveCaseTwinTerms(t, backend, "fullchaos")
	if len(resolution.Committed) != 0 {
		t.Fatalf("committed = %#v, want none: an unproven read refuses the case-exact label", resolution.Committed)
	}
	if len(resolution.CaseTwinReadingLimitations) != 0 {
		t.Fatalf("case-twin lines = %q on a pick that committed nothing", resolution.CaseTwinReadingLimitations)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("no clarification for a refused pick")
	}
}

// Production also reads the keyed identity universe, which names both teams as
// label claimants of the folded term. A complete read proves no third
// same-name team is hidden, and the case-exact label still commits.
func TestCHAOS12663_ACompleteKeyedIdentityReadKeepsTheCaseExactCommit(t *testing.T) {
	t.Parallel()
	linear, gitlab := caseTwinPair()
	claimant := func(node CandidateNode) CandidateNode {
		node.Mechanism = contextfabric.MatchExact
		node.FromKeyedIdentityLookup = true
		return node
	}
	backend := &fakeGraphBackend{
		searchResults:        map[string][]CandidateNode{"fullchaos": {linear, gitlab}},
		enableAliasLookup:    true,
		aliasLookupClaimants: map[string][]CandidateNode{"fullchaos": {claimant(linear), claimant(gitlab)}},
		aliasLookupComplete:  true,
	}
	resolution := resolveCaseTwinTerms(t, backend, "fullchaos")
	if len(resolution.Committed) != 1 || resolution.Committed[0].CanonicalID != "team:gl:full.chaos" {
		t.Fatalf("committed = %#v, want the gitlab team", resolution.Committed)
	}
	if len(resolution.CaseTwinReadingLimitations) != 1 {
		t.Fatalf("case-twin lines = %q, want one", resolution.CaseTwinReadingLimitations)
	}
}
