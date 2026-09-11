package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupedRetryTurn drives the REAL grouped retry -- six members in two groups
// of three, a budget above the grouped headroom, five claims per member, so
// the first answer goes over on claims and the one bounded retry narrows the
// members and fits -- with a group read that returns evidence for ONE of the
// two groups. It returns the served document and the per-pass synthesis
// inputs, and fails the test if the retry did not actually run.
//
// NOT safe for t.Parallel(): it changes the fixture's claims-per-member and
// reads the shared synthesis captures.
func groupedRetryTurn(t *testing.T, telemetry EngineTelemetry) (InvestigationResult, []groupReadSynthesisPass) {
	t.Helper()
	previousClaims := groupReadClaimsPerMember
	groupReadClaimsPerMember = 5
	groupReadSynthesisPasses = nil
	t.Cleanup(func() {
		groupReadClaimsPerMember = previousClaims
		groupReadSynthesisPasses = nil
	})

	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Facts = []CanonicalFact{groupKindFact("team_security", FactHealth), groupKindFact("team_security", FactWorkload)}
				bundle.Coverage.Sources = []SourceObservation{
					{Source: "canonical_fact:health", State: SourceAvailable},
					{Source: "canonical_fact:workload", State: SourceAvailable},
				}
				return bundle
			}
		}
		for index, id := range groupReadRetryMemberIDs() {
			team := "team_security"
			if index >= 3 {
				team = "team_platform"
			}
			bundle.Facts = append(bundle.Facts, teamScopedFact(id, team, team))
		}
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	members := make([]CohortMember, 0, 6)
	for index, id := range groupReadRetryMemberIDs() {
		members = append(members, CohortMember{
			Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: index + 1, InclusionReasons: []string{"matched"},
		})
	}
	synthesisCalls := 0
	options := EngineOptions{MaxItems: 26, SynthesisDeadlineReserve: time.Hour}
	engine, request := groupReadEngineFixtureFull(t, telemetry, recorder, members, nil, SubjectProject, &options, &synthesisCalls)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v -- this fixture must FIT after one retry", err)
	}
	if synthesisCalls != 2 || len(groupReadSynthesisPasses) != 2 {
		t.Fatalf("synthesis calls = %d (captured passes %d), want 2 -- without a real retry there is no first pass to confuse with the served one, and this probe measures nothing",
			synthesisCalls, len(groupReadSynthesisPasses))
	}
	return result, append([]groupReadSynthesisPass(nil), groupReadSynthesisPasses...)
}

// membersReadIn counts, INDEPENDENTLY of the evaluator, how many of members
// carry at least one available fact of kind in facts -- the `at_least_one`
// standard of the fixture's member row.
func membersReadIn(members []SubjectRef, facts []CanonicalFact, kind FactKind) int {
	read := 0
	for _, member := range members {
		for _, fact := range facts {
			if fact.Kind == kind && fact.Subject == member && fact.SourceState == SourceAvailable {
				read++
				break
			}
		}
	}
	return read
}

// groupsReadIn counts how many groups carry facts of at least two distinct
// kinds in facts -- the `corroborated` standard of the fixture's group row.
func groupsReadIn(groups []SubjectRef, facts []CanonicalFact) int {
	read := 0
	for _, group := range groups {
		kinds := map[FactKind]struct{}{}
		for _, fact := range facts {
			if SubjectMapKey(fact.Subject) == SubjectMapKey(group) {
				kinds[fact.Kind] = struct{}{}
			}
		}
		if len(kinds) >= 2 {
			read++
		}
	}
	return read
}

// TestWrongIf_FinalCountsReflectFirstPassEvidence is stage 5's first "wrong
// if": *final counts reflect first-pass evidence rather than the served
// document.*
//
// A retried turn synthesizes twice, and only the second document is served.
// Every count the served document publishes must be computed from what THAT
// document was built from -- the narrowed cohort and the facts the second
// synthesis received -- or the reader is told "6 of 6 members read" about an
// answer that carries three.
//
// The expectations are counted HERE, from the captured synthesis inputs,
// never by calling the evaluator: an expectation computed by the thing under
// test cannot fail.
//
// DISCRIMINATING BY CONSTRUCTION. The probe first requires that the two
// passes actually DISAGREE -- the first saw six members, the second three --
// so the first-pass count and the served count are different numbers, and a
// document carrying the first-pass one fails the equality below.
func TestWrongIf_FinalCountsReflectFirstPassEvidence(t *testing.T) {
	result, passes := groupedRetryTurn(t, &recordingTelemetry{})
	first, final := passes[0], passes[1]

	firstMembers := membersReadIn(first.Members, first.Facts, FactMetrics)
	finalMembers := membersReadIn(final.Members, final.Facts, FactMetrics)
	finalGroups := groupsReadIn(final.Groups, final.Facts)
	t.Logf("pass 1: members=%d read=%d | pass 2 (served): members=%d read=%d groups=%d groups_read=%d | served cohort members=%d",
		len(first.Members), firstMembers, len(final.Members), finalMembers, len(final.Groups), finalGroups, len(result.Cohort.Members))
	if firstMembers == finalMembers {
		t.Fatalf("the two passes read the same member count (%d), so a document carrying the first-pass count would pass -- the probe is vacuous on this fixture", firstMembers)
	}
	if len(result.Cohort.Members) != len(final.Members) {
		t.Fatalf("served cohort carries %d members but the served pass synthesized %d -- the served document is not the second pass's", len(result.Cohort.Members), len(final.Members))
	}

	_, memberRows := servedRequirementRows(t, result, CompletionScopeEachMember)
	_, groupRows := servedRequirementRows(t, result, CompletionScopeEachGroup)
	if len(memberRows) != 1 || len(groupRows) != 1 {
		t.Fatalf("assembled-result rows: each_member=%d each_group=%d, want 1 each", len(memberRows), len(groupRows))
	}
	t.Logf("served each_member row: %q %d/%d; each_group row: %q %d/%d",
		memberRows[0].Outcome, memberRows[0].Served, memberRows[0].Declared, groupRows[0].Outcome, groupRows[0].Served, groupRows[0].Declared)
	if memberRows[0].Served != finalMembers {
		t.Errorf("each_member served = %d, want %d (the served pass's own count); the first pass counted %d -- the served document reports evidence for an answer nobody received",
			memberRows[0].Served, finalMembers, firstMembers)
	}
	if groupRows[0].Served != finalGroups || groupRows[0].Declared != len(final.Groups) {
		t.Errorf("each_group row = %d/%d, want %d/%d from the served pass", groupRows[0].Served, groupRows[0].Declared, finalGroups, len(final.Groups))
	}
}

// outcomeRowMultiplicity counts the served document's outcome rows per (stage,
// requirement). It is the S5b detector, and TestTheDuplicateRowDetectorCanFire
// proves it can see a duplicate at all.
func outcomeRowMultiplicity(rows []RequirementOutcomeRow) map[string]int {
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[string(row.Stage)+"|"+row.Requirement]++
	}
	return counts
}

// TestWrongIf_RetriesDuplicateOutcomeRows is stage 5's second "wrong if":
// *retries duplicate outcome rows.*
//
// Finalization runs once per synthesis pass, and the row set is append-only.
// A retried turn that carried its first pass's rows into the second -- or
// re-seeded an already-seeded set -- would publish two rows for one
// requirement, and a reader summing or choosing between them gets a
// different answer depending on which one they read. Counted, not looked up:
// a test that only checks the expected row exists cannot detect a surplus.
//
// The decision lines are counted too. The group read, the member allowance
// and each retention stage decide ONCE per turn, and a retry that re-emitted
// them would double every count an operator aggregates.
func TestWrongIf_RetriesDuplicateOutcomeRows(t *testing.T) {
	logs := captureEngineLogger(t)
	result, _ := groupedRetryTurn(t, logs.telemetry)

	counts := outcomeRowMultiplicity(result.Completeness.Outcomes)
	t.Logf("row multiplicity on a retried turn: %v", counts)
	sawGroupRow := false
	for key, count := range counts {
		if count != 1 {
			t.Errorf("outcome rows for %s = %d, want 1 -- the retry duplicated an outcome row", key, count)
		}
		if key == string(contractsv1.ContextFabricOutcomeStageAssembledResult)+"|state/group/team" {
			sawGroupRow = true
		}
	}
	if !sawGroupRow {
		t.Fatalf("the retried document carries no assembled-result each_group row, so there was nothing to duplicate -- the probe is vacuous")
	}

	raw := logs.configured.String()
	for _, message := range []string{"context fabric cohort group read", "context fabric cohort member allowance"} {
		if got := len(linesWithMessage(t, raw, message)); got != 1 {
			t.Errorf("%q lines on a retried turn = %d, want 1", message, got)
		}
	}
	perStage := map[string]int{}
	for _, line := range linesWithMessage(t, raw, "context fabric fact retention") {
		stage, _ := line["stage"].(string)
		perStage[stage]++
	}
	t.Logf("retention lines per stage: %v", perStage)
	if len(perStage) == 0 {
		t.Fatalf("no retention line on a retried turn that narrowed members -- nothing to count")
	}
	for stage, count := range perStage {
		if count != 1 {
			t.Errorf("retention lines at stage %q = %d, want 1", stage, count)
		}
	}
}

// TestTheDuplicateRowDetectorCanFire is the NON-VACUOUS CONTROL for the S5b
// probe: the same served document with one row appended twice must be seen.
func TestTheDuplicateRowDetectorCanFire(t *testing.T) {
	result, _ := groupedRetryTurn(t, &recordingTelemetry{})
	_, groupRows := servedRequirementRows(t, result, CompletionScopeEachGroup)
	if len(groupRows) != 1 {
		t.Fatalf("CONTROL BROKEN: each_group rows = %d", len(groupRows))
	}
	duplicated := append(append([]RequirementOutcomeRow(nil), result.Completeness.Outcomes...), groupRows[0])
	key := string(contractsv1.ContextFabricOutcomeStageAssembledResult) + "|" + groupRows[0].Requirement
	if got := outcomeRowMultiplicity(duplicated)[key]; got != 2 {
		t.Fatalf("CONTROL BROKEN: the detector counts %d for a row appended twice, want 2", got)
	}
}
