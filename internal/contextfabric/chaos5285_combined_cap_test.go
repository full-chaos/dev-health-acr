package contextfabric

import (
	"context"
	"fmt"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// combinedCapMemberFacts is how many facts the MEMBER read returns in the
// row-6 fixture: four short of the per-bundle cap, so exactly four group facts
// fit and the rest must not.
const combinedCapMemberFacts = maxCanonicalFactsPerBundle - 4

// combinedCapGroupFacts is the group read's answer for the row-6 fixture:
// SEVEN facts over two teams and four kinds. Seven, four admitted and three
// omitted are pairwise distinct, and none of them equals the two groups or the
// two members -- so an emitter that swapped one count for another would put a
// wrong number on the line rather than a coincidentally right one.
func combinedCapGroupFacts() []CanonicalFact {
	return []CanonicalFact{
		// Deliberately NOT in the registry's canonical order, so an admission
		// that simply took the provider's first four would admit a different
		// set from one that orders them first.
		groupKindFact("team_platform", FactLandscape),
		groupKindFact("team_security", FactReadiness),
		groupKindFact("team_security", FactWorkload),
		groupKindFact("team_platform", FactReadiness),
		groupKindFact("team_security", FactHealth),
		groupKindFact("team_platform", FactWorkload),
		groupKindFact("team_platform", FactHealth),
	}
}

func combinedCapRecorder() *groupReadRecorder {
	return &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind != SubjectTeam {
				continue
			}
			bundle.Facts = combinedCapGroupFacts()
			bundle.Coverage.Sources = []SourceObservation{
				{Source: "canonical_fact:health", State: SourceAvailable},
				{Source: "canonical_fact:landscape", State: SourceAvailable},
				{Source: "canonical_fact:readiness", State: SourceAvailable},
				{Source: "canonical_fact:workload", State: SourceAvailable},
			}
			return bundle
		}
		facts := groupReadMemberFacts()
		for index := len(facts); index < combinedCapMemberFacts; index++ {
			member := "project_a"
			if index%2 == 1 {
				member = "project_b"
			}
			facts = append(facts, CanonicalFact{
				Kind:        FactMetrics,
				Subject:     SubjectRef{Kind: SubjectProject, CanonicalID: member, Label: member},
				Fields:      map[string]FactValue{"sample": StringFactValue(fmt.Sprintf("s%04d", index))},
				SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
			})
		}
		bundle.Facts = facts
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
}

// TestTheSecondReadSharesTheTurnsFactBudget is CHAOS-5285 test-table row 6,
// and it closes stage 4's first "wrong if": *synthesis exceeds the cap.*
//
// The per-bundle cap bounds what ONE fact read may hand synthesis, and the
// registry enforces it at the merge point every provider result passes
// through. A second read is a second bundle with its own cap, so without a
// combined bound the turn's synthesis input is up to twice the ceiling the
// cap exists to hold -- and the model input is exactly what that ceiling is
// for.
//
// Four properties, each a separate way to get this wrong:
//   - BOUNDED: synthesis receives at most the cap, across both reads.
//   - FIRST READ PRESERVED: every member fact survives; the group read is
//     what yields, never the evidence already gathered.
//   - DETERMINISTIC: the admitted group facts are the ones the registry's own
//     order puts first, not whichever the provider happened to list first.
//   - DISCLOSED: the omitted kinds reach the served coverage as truncated,
//     through the same machinery the registry uses when its own cap trims,
//     and the decision line carries the counts at Info.
//
// NOT t.Parallel(): it reads the shared synthesis-fact capture and installs
// the process default logger.
func TestTheSecondReadSharesTheTurnsFactBudget(t *testing.T) {
	groupReadSynthesisFacts = nil
	t.Cleanup(func() { groupReadSynthesisFacts = nil })
	logs := captureEngineLogger(t)

	engine, request := groupReadEngineFixture(t, logs.telemetry, combinedCapRecorder())
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	memberFacts, groupFacts := 0, map[string]bool{}
	for _, fact := range groupReadSynthesisFacts {
		if fact.Subject.Kind == SubjectTeam {
			groupFacts[string(fact.Kind)+"@"+fact.Subject.CanonicalID] = true
			continue
		}
		memberFacts++
	}
	t.Logf("synthesis received %d facts (cap %d): member=%d group=%v",
		len(groupReadSynthesisFacts), maxCanonicalFactsPerBundle, memberFacts, groupFacts)

	// BOUNDED.
	if len(groupReadSynthesisFacts) > maxCanonicalFactsPerBundle {
		t.Errorf("synthesis received %d facts, over the per-bundle cap %d -- a second read that brings its own cap doubles the ceiling the cap exists to hold",
			len(groupReadSynthesisFacts), maxCanonicalFactsPerBundle)
	}
	// FIRST READ PRESERVED.
	if memberFacts != combinedCapMemberFacts {
		t.Errorf("member facts reaching synthesis = %d, want all %d -- the group read yields to the evidence already gathered, never the reverse",
			memberFacts, combinedCapMemberFacts)
	}
	// DETERMINISTIC: the registry's canonical order (fact kind, then subject)
	// puts health then workload first, for team_platform then team_security.
	want := map[string]bool{
		"health@" + TeamCanonicalID("team_platform"):   true,
		"health@" + TeamCanonicalID("team_security"):   true,
		"workload@" + TeamCanonicalID("team_platform"): true,
		"workload@" + TeamCanonicalID("team_security"): true,
	}
	if len(groupFacts) != len(want) {
		t.Errorf("group facts admitted = %d %v, want the %d that fit in the remaining capacity", len(groupFacts), groupFacts, len(want))
	}
	for key := range want {
		if !groupFacts[key] {
			t.Errorf("group fact %s was not admitted -- admission into the remaining capacity must follow the registry's own order, not the provider's", key)
		}
	}

	// DISCLOSED ON THE DOCUMENT, through the existing truncation state.
	states := map[string]SourceState{}
	for _, source := range result.Coverage.Sources {
		states[source.Source] = source.State
	}
	t.Logf("served coverage states = %v", states)
	for _, kind := range []FactKind{FactLandscape, FactReadiness} {
		if got := states["canonical_fact:"+string(kind)]; got != SourceTruncated {
			t.Errorf("served coverage for %s = %q, want %q -- the kind the combined cap trimmed must say so where every consumer already reads truncation",
				kind, got, SourceTruncated)
		}
	}
	for _, kind := range []FactKind{FactHealth, FactWorkload} {
		if got := states["canonical_fact:"+string(kind)]; got != SourceAvailable {
			t.Errorf("served coverage for %s = %q, want %q -- a kind the cap did not touch must not be reported trimmed", kind, got, SourceAvailable)
		}
	}
	_, rows := servedRequirementRows(t, result, CompletionScopeEachGroup)
	for _, row := range rows {
		t.Logf("each_group row: outcome=%q impact=%q served=%d declared=%d cause=%q", row.Outcome, row.Impact, row.Served, row.Declared, row.CauseCoverage)
		if row.Outcome == contractsv1.ContextFabricRequirementSatisfied {
			t.Errorf("each_group row is satisfied with two of its kinds trimmed by the cap -- the omission must reach the requirement it cost")
		}
	}

	// DISCLOSED ON THE TRACE, on the engine's configured logger, with values.
	lines := linesWithMessage(t, logs.configured.String(), "context fabric cohort group read")
	if len(lines) != 1 {
		t.Fatalf("group-read decision lines on the configured logger = %d, want 1", len(lines))
	}
	line := lines[0]
	t.Logf("decision line: level=%v returned=%v cap_omitted=%v merged=%v bundle_cap=%v",
		line["level"], line["group_facts_returned"], line["group_facts_cap_omitted"], line["group_facts_merged"], line["fact_bundle_cap"])
	if line["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", line["level"])
	}
	for field, want := range map[string]float64{
		"group_facts_returned":    7,
		"group_facts_cap_omitted": 3,
		"group_facts_merged":      4,
		"fact_bundle_cap":         maxCanonicalFactsPerBundle,
	} {
		if got, ok := line[field].(float64); !ok || got != want {
			t.Errorf("%s = %v, want %v", field, line[field], want)
		}
	}
	if stray := linesWithMessage(t, logs.fallback.String(), "context fabric cohort group read"); len(stray) != 0 {
		t.Errorf("%d decision line(s) reached the PROCESS DEFAULT logger, which acr-api's JSON handler never reads", len(stray))
	}
}

// TestAGroupReadUnderTheCapIsAdmittedWhole is the DISCRIMINATING CONTROL for
// row 6: the same group answer with a member read far below the cap is
// admitted in full and nothing is reported trimmed.
//
// Without it, an admission that always trimmed -- or always reported a
// truncation -- would pass the pin above.
func TestAGroupReadUnderTheCapIsAdmittedWhole(t *testing.T) {
	groupReadSynthesisFacts = nil
	t.Cleanup(func() { groupReadSynthesisFacts = nil })
	logs := captureEngineLogger(t)

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := combinedCapRecorder().facts(request)
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		return bundle
	}}
	engine, request := groupReadEngineFixture(t, logs.telemetry, recorder)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v", err)
	}
	group := 0
	for _, fact := range groupReadSynthesisFacts {
		if fact.Subject.Kind == SubjectTeam {
			group++
		}
	}
	if group != len(combinedCapGroupFacts()) {
		t.Fatalf("CONTROL BROKEN: %d of %d group facts reached synthesis far below the cap", group, len(combinedCapGroupFacts()))
	}
	for _, source := range result.Coverage.Sources {
		if source.State == SourceTruncated {
			t.Fatalf("CONTROL BROKEN: %s reported truncated far below the cap", source.Source)
		}
	}
	lines := linesWithMessage(t, logs.configured.String(), "context fabric cohort group read")
	if len(lines) != 1 {
		t.Fatalf("CONTROL BROKEN: decision lines = %d, want 1", len(lines))
	}
	if got, _ := lines[0]["group_facts_cap_omitted"].(float64); got != 0 {
		t.Fatalf("CONTROL BROKEN: group_facts_cap_omitted = %v far below the cap", lines[0]["group_facts_cap_omitted"])
	}
	if got, _ := lines[0]["group_facts_merged"].(float64); got != 7 {
		t.Fatalf("CONTROL BROKEN: group_facts_merged = %v, want 7", lines[0]["group_facts_merged"])
	}
}
