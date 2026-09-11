package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE PAIRWISE KNOB TABLE.
//
// The per-field input-domain table cannot see a knob that is correct on its
// own and wrong beside another. This change has two config-bearing knobs that
// meet on one turn: the item budget (MaxItems, against the profile's synthesis
// headroom) and the per-bundle fact cap (maxCanonicalFactsPerBundle, now shared
// by two reads). Each cell below crosses them through PRODUCTION code, and the
// expectation in each cell is computed independently of the function under
// test (literal headrooms, literal counts), never by calling it.

// TestPairwiseMaxItemsByHeadroomAgreesWithTheClampFlag crosses the item budget
// with every headroom profile through planBudget -- the function that
// PUBLISHES the budget -- and asks whether the clamp flag the allowance line
// reports agrees with the arithmetic done here from the literal headroom.
//
// planBudget also rewrites SynthesisHeadroom down to MaxItems when the
// profile reserves more than the whole budget, so a flag computed from the
// PUBLISHED budget could disagree with the one computed from the profile.
// These cells are where that would show.
func TestPairwiseMaxItemsByHeadroomAgreesWithTheClampFlag(t *testing.T) {
	t.Parallel()

	profiles := []struct {
		profile  PlanBudgetProfile
		headroom int
	}{
		{PlanBudgetGroupedCohort, 20},
		{PlanBudgetFlatCohort, 16},
		{PlanBudgetSingleSubject, 12},
		{PlanBudgetUnbounded, 0},
	}
	var table strings.Builder
	table.WriteString("\n| profile | max_items | published headroom | max_members | clamped (production) | clamped (independent) | verdict |\n|---|---|---|---|---|---|---|\n")
	for _, p := range profiles {
		seen := map[int]bool{}
		for _, maxItems := range []int{0, 1, p.headroom - 1, p.headroom, p.headroom + 1, 30, 45} {
			if maxItems < 0 || seen[maxItems] {
				continue
			}
			seen[maxItems] = true
			planned := planBudget(p.profile, ResponseBudget{MaxItems: maxItems, MaxSerializedBytes: 262144}, 1000)
			got := cohortMemberAllowanceClamped(planned)
			want := maxItems > 0 && maxItems-p.headroom < 1
			verdict := "ok"
			if got != want {
				verdict = "MISMATCH"
				t.Errorf("profile %q max_items=%d: clamped=%v, want %v (the floor supplied the allowance iff max_items-%d < 1)", p.profile, maxItems, got, want, p.headroom)
			}
			if want && planned.MaxMembers != 1 {
				verdict = "MISMATCH"
				t.Errorf("profile %q max_items=%d: clamped but max_members=%d, want the floor 1", p.profile, maxItems, planned.MaxMembers)
			}
			table.WriteString(fmt.Sprintf("| %s | %d | %d | %d | %v | %v | %s |\n", p.profile, maxItems, planned.SynthesisHeadroom, planned.MaxMembers, got, want, verdict))
		}
	}
	t.Logf("PAIRWISE max_items x headroom%s", table.String())
}

// TestPairwiseFactCapByItemBudget crosses the shared fact cap (no pressure /
// the member read four short of the cap) with the item budget (below the
// grouped headroom, one above it, and a budget that forces the bounded retry)
// through the real engine.
//
// Every cell must hold the same four properties whatever the other knob says:
// synthesis never receives more than the cap on ANY pass; the truncation
// disclosure appears exactly when the cap trimmed; exactly two fact-service
// calls; and the decision line's cap_omitted is the count this table expects.
// A budget that narrows members runs retention over the combined bundle, and
// a budget that retries runs synthesis twice -- both are where a cap that
// holds on one pass could fail on another.
//
// NOT t.Parallel(): it reads the shared synthesis captures.
func TestPairwiseFactCapByItemBudget(t *testing.T) {
	var table strings.Builder
	table.WriteString("\n| cap pressure | max_items | synthesis passes | max facts on a pass | truncated kinds | fact-service calls | cap_omitted | verdict |\n|---|---|---|---|---|---|---|---|\n")
	// max_items 6 clamps the allowance; 21 is one above the grouped headroom;
	// 26 with six members at five claims each forces the one bounded RETRY,
	// so the cap is measured on a second synthesis pass too.
	previousClaims := groupReadClaimsPerMember
	t.Cleanup(func() { groupReadClaimsPerMember = previousClaims })
	for _, pressure := range []bool{false, true} {
		for _, maxItems := range []int{6, 21, 26} {
			groupReadSynthesisPasses = nil
			logs := captureEngineLogger(t)
			members := groupReadRetryMemberIDs()[:2]
			groupReadClaimsPerMember = 1
			if maxItems == 26 {
				members = groupReadRetryMemberIDs()
				groupReadClaimsPerMember = 5
			}
			memberFacts := make([]CanonicalFact, 0, len(members))
			for index, id := range members {
				team := "team_security"
				if index >= len(members)/2 {
					team = "team_platform"
				}
				memberFacts = append(memberFacts, teamScopedFact(id, team, team))
			}
			recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
				bundle := emptyFactBundle()
				for _, subject := range request.Subjects {
					if subject.Kind == SubjectTeam {
						bundle.Facts = combinedCapGroupFacts()
						bundle.Coverage.Sources = []SourceObservation{
							{Source: "canonical_fact:health", State: SourceAvailable}, {Source: "canonical_fact:landscape", State: SourceAvailable},
							{Source: "canonical_fact:readiness", State: SourceAvailable}, {Source: "canonical_fact:workload", State: SourceAvailable},
						}
						return bundle
					}
				}
				bundle.Facts = append([]CanonicalFact(nil), memberFacts...)
				for index := len(bundle.Facts); pressure && index < combinedCapMemberFacts; index++ {
					member := members[index%len(members)]
					bundle.Facts = append(bundle.Facts, CanonicalFact{
						Kind: FactMetrics, Subject: SubjectRef{Kind: SubjectProject, CanonicalID: member, Label: member},
						Fields: map[string]FactValue{"sample": StringFactValue(fmt.Sprintf("s%04d", index))}, SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
					})
				}
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
				return bundle
			}}
			cohortMembers := make([]CohortMember, 0, len(members))
			for index, id := range members {
				cohortMembers = append(cohortMembers, CohortMember{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}, Rank: index + 1, InclusionReasons: []string{"matched"}})
			}
			options := EngineOptions{MaxItems: maxItems, SynthesisDeadlineReserve: time.Hour}
			engine, request := groupReadEngineFixtureFull(t, logs.telemetry, recorder, cohortMembers, nil, SubjectProject, &options, nil)
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
			verdict := "ok"
			fail := func(format string, args ...any) {
				verdict = "MISMATCH"
				t.Errorf("pressure=%v max_items=%d: "+format, append([]any{pressure, maxItems}, args...)...)
			}
			if err != nil {
				fail("Investigate() error = %v", err)
				continue
			}
			maxOnPass := 0
			for _, pass := range groupReadSynthesisPasses {
				if len(pass.Facts) > maxOnPass {
					maxOnPass = len(pass.Facts)
				}
			}
			truncated := []string{}
			for _, source := range result.Coverage.Sources {
				if source.State == SourceTruncated {
					truncated = append(truncated, source.Source)
				}
			}
			lines := linesWithMessage(t, logs.configured.String(), "context fabric cohort group read")
			capOmitted := -1
			if len(lines) == 1 {
				if value, ok := lines[0]["group_facts_cap_omitted"].(float64); ok {
					capOmitted = int(value)
				}
			}
			wantOmitted, wantTruncated := 0, 0
			if pressure {
				wantOmitted, wantTruncated = 3, 2
			}
			if maxItems == 26 && len(groupReadSynthesisPasses) != 2 {
				fail("synthesis passes = %d, want 2 -- this cell exists to measure the cap on a retry", len(groupReadSynthesisPasses))
			}
			if maxOnPass > maxCanonicalFactsPerBundle {
				fail("a synthesis pass received %d facts, over the cap %d", maxOnPass, maxCanonicalFactsPerBundle)
			}
			if len(truncated) != wantTruncated {
				fail("truncated kinds = %v, want %d", truncated, wantTruncated)
			}
			if len(recorder.requests) != 2 {
				fail("fact-service calls = %d, want 2", len(recorder.requests))
			}
			if capOmitted != wantOmitted {
				fail("cap_omitted = %d, want %d", capOmitted, wantOmitted)
			}
			table.WriteString(fmt.Sprintf("| %v | %d | %d | %d | %s | %d | %d | %s |\n",
				pressure, maxItems, len(groupReadSynthesisPasses), maxOnPass, strings.Join(truncated, " "), len(recorder.requests), capOmitted, verdict))
		}
	}
	groupReadSynthesisPasses = nil
	t.Logf("PAIRWISE fact cap x item budget%s", table.String())
}
