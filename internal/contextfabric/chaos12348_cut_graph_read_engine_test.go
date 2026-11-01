package contextfabric

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12348 (lead ruling, option d): a degrading cut graph read row floors a
// complete answer to partial and keeps the answer. The answer says why in one
// limitation sentence per cause, and states no narrative-withheld sentence.
//
// What the model wrote stays verbatim. The two lead fields are server-composed
// and state the status: a complete head states nothing, and the serving step
// restateServedStatusHead adds the partial sentence to a head the server
// composed. That sentence is the one change to them; current_state is untouched.

const cutReadModelCaveat = "The model's own caveat stays."

func cutReadAnswer(t *testing.T, details ...CoverageDetail) InvestigationResult {
	t.Helper()
	project := acceptanceProject()
	graphContext := bootstrapGraphContext(project)
	graphContext.Coverage = Coverage{Sources: []SourceObservation{}, Partial: len(details) > 0, DegradedReasons: []string{}, Details: details}
	for _, detail := range details {
		graphContext.Coverage.DegradedReasons = append(graphContext.Coverage.DegradedReasons, detail.Raw)
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    graphContext,
	}
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	draft := bootstrapDraft(project)
	draft.Limitations = []string{cutReadModelCaveat}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), draft, nil, &recordingTelemetry{})
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("served result does not validate: %v", err)
	}
	return result
}

type cutReadCase struct {
	detail     CoverageDetail
	limitation string
}

func hopWalkCutCase() cutReadCase {
	return cutReadCase{
		detail:     detailFixture(contractsv1.ContextFabricCoverageDetailGraphHopWalkTruncated, "context-fabric:graph", true, "hop_walk_truncated", nil),
		limitation: contractsv1.ContextFabricGraphHopWalkTruncatedLimitation,
	}
}

func undecidedPathsCase() cutReadCase {
	return cutReadCase{
		detail: detailFixture(contractsv1.ContextFabricCoverageDetailGraphTeamNarrowingUndecidedPaths, "context-fabric:graph", true,
			"team_narrowing_undecided_paths:2", withCount(2)),
		limitation: contractsv1.ContextFabricGraphTeamNarrowingUndecidedPathsLimitation,
	}
}

func cutReadCases() map[string]cutReadCase {
	return map[string]cutReadCase{"a cut hop walk": hopWalkCutCase(), "undecided path endpoints": undecidedPathsCase()}
}

// assertAnswerKept: everything the model wrote is what the whole read served,
// current_state is unchanged, and a lead field gained the status sentence and
// nothing else.
func assertAnswerKept(t *testing.T, whole, floored InvestigationResult) {
	t.Helper()
	for name, fields := range map[string][2]any{
		"current_state":       {whole.CurrentState, floored.CurrentState},
		"strongest_pressures": {whole.StrongestPressures, floored.StrongestPressures},
		"drivers":             {whole.Drivers, floored.Drivers},
		"remaining_work":      {whole.RemainingWork, floored.RemainingWork},
		"readiness_gaps":      {whole.ReadinessGaps, floored.ReadinessGaps},
		"conflicts":           {whole.Conflicts, floored.Conflicts},
		"claimed_facts":       {whole.ClaimedFacts, floored.ClaimedFacts},
	} {
		if !reflect.DeepEqual(fields[0], fields[1]) {
			t.Errorf("%s changed: %+v, was %+v", name, fields[1], fields[0])
		}
	}
	sentence := statusSentence(InvestigationPartial, SubjectResolution{})
	for name, heads := range map[string][2]string{
		"direct_judgment":      {whole.DirectJudgment, floored.DirectJudgment},
		"deterministic_answer": {whole.DeterministicAnswer, floored.DeterministicAnswer},
	} {
		if !strings.Contains(heads[1], sentence) {
			t.Errorf("%s = %q, want the status sentence %q", name, heads[1], sentence)
		}
		if got := strings.TrimSpace(strings.Replace(heads[1], sentence, "", 1)); got != heads[0] {
			t.Errorf("%s = %q, want %q and the status sentence alone", name, heads[1], heads[0])
		}
	}
	if !slices.Contains(floored.Limitations, cutReadModelCaveat) {
		t.Errorf("limitations = %v, lost the model's own caveat", floored.Limitations)
	}
	if slices.Contains(floored.Limitations, synthesisNarrativeWithheldLimitation) {
		t.Errorf("limitations = %v, want no narrative-withheld sentence: nothing was withheld", floored.Limitations)
	}
}

func TestACutNonCohortReadIsPartialAndKeepsTheAnswer(t *testing.T) {
	t.Parallel()
	whole := cutReadAnswer(t)
	if whole.Status != InvestigationComplete || whole.Coverage.Partial {
		t.Fatalf("control: with no row the answer is %q, partial=%v, want complete and not partial", whole.Status, whole.Coverage.Partial)
	}
	for name, tc := range cutReadCases() {
		t.Run(name, func(t *testing.T) {
			result := cutReadAnswer(t, tc.detail)
			if result.Status != InvestigationPartial || !result.Coverage.Partial {
				t.Fatalf("status = %q, partial = %v, want partial over a cut read", result.Status, result.Coverage.Partial)
			}
			if !slices.Contains(result.Coverage.DegradedReasons, tc.detail.Raw) {
				t.Fatalf("degraded_reasons = %v, want %q", result.Coverage.DegradedReasons, tc.detail.Raw)
			}
			served := false
			for _, row := range result.Coverage.Details {
				served = served || (row.Code == tc.detail.Code && row.Degrading && row.Raw == tc.detail.Raw && row.Label == tc.detail.Label)
			}
			if !served {
				t.Fatalf("coverage details = %+v, want the %s row", result.Coverage.Details, tc.detail.Code)
			}
			if !slices.Contains(result.Limitations, tc.limitation) {
				t.Fatalf("limitations = %v, want %q", result.Limitations, tc.limitation)
			}
			for _, other := range cutReadCases() {
				if other.limitation != tc.limitation && slices.Contains(result.Limitations, other.limitation) {
					t.Fatalf("limitations = %v, states the other cause", result.Limitations)
				}
			}
			// Completeness counts outcome rows, which do not state a cut read,
			// so it may stay above the status; terminal_status follows it.
			if want := ComputeAnswerCompleteness(result); result.Completeness.State != want.State || result.Completeness.TerminalStatus != InvestigationPartial {
				t.Fatalf("completeness = %q / terminal %q, want the outcome rows' %q and terminal partial", result.Completeness.State, result.Completeness.TerminalStatus, want.State)
			}
			assertAnswerKept(t, whole, result)
		})
	}
}

func TestBothCutReadCausesEachStateTheirSentence(t *testing.T) {
	t.Parallel()
	hop, team := hopWalkCutCase(), undecidedPathsCase()
	result := cutReadAnswer(t, hop.detail, team.detail)
	if result.Status != InvestigationPartial {
		t.Fatalf("status = %q, want partial", result.Status)
	}
	if !slices.Contains(result.Limitations, hop.limitation) || !slices.Contains(result.Limitations, team.limitation) {
		t.Fatalf("limitations = %v, want both sentences", result.Limitations)
	}
	var degrading []string
	for _, row := range result.Coverage.Details {
		if row.Degrading {
			degrading = append(degrading, row.Raw)
		}
	}
	if len(degrading) != 2 || !slices.Equal(degrading, result.Coverage.DegradedReasons) {
		t.Fatalf("degrading rows %v, degraded_reasons %v, want both, paired in order", degrading, result.Coverage.DegradedReasons)
	}
}

// Neither row is a cohort terminal: it says nothing about a member set, so it
// must not turn a model no_match into a served cohort outcome.
func TestACutNonCohortReadIsNotACohortTerminal(t *testing.T) {
	t.Parallel()
	for name, tc := range cutReadCases() {
		if cohortTerminalCoverageCode(tc.detail.Code) {
			t.Errorf("%s: %s is a cohort terminal code", name, tc.detail.Code)
		}
	}
}

// storedWithRows is a stored complete answer that carries rows, as a row saved
// before the floor existed does.
func storedWithRows(t *testing.T, rows ...CoverageDetail) InvestigationResult {
	t.Helper()
	stored := cutReadAnswer(t)
	stored.Coverage.Details = append([]CoverageDetail{}, rows...)
	stored.Coverage.DegradedReasons = []string{}
	for _, row := range rows {
		if row.Degrading {
			stored.Coverage.DegradedReasons = append(stored.Coverage.DegradedReasons, row.Raw)
			stored.Coverage.Partial = true
		}
	}
	stored.Completeness = ComputeAnswerCompleteness(stored)
	return stored
}

// The apply step every serving point runs, on a stored answer.
func TestAStoredCompleteAnswerOverACutReadIsFlooredOnRead(t *testing.T) {
	t.Parallel()
	for name, tc := range cutReadCases() {
		t.Run(name, func(t *testing.T) {
			stored := storedWithRows(t, tc.detail)
			if stored.Status != InvestigationComplete {
				t.Fatalf("fixture: stored status = %q, want complete", stored.Status)
			}
			served := ApplyServedStatusFloors(stored)
			if served.Status != InvestigationPartial || !served.Coverage.Partial || served.Completeness.TerminalStatus != InvestigationPartial {
				t.Fatalf("served %q, partial %v, terminal %q, want partial", served.Status, served.Coverage.Partial, served.Completeness.TerminalStatus)
			}
			if !slices.Contains(served.Limitations, tc.limitation) || slices.Contains(served.Limitations, synthesisNarrativeWithheldLimitation) {
				t.Fatalf("limitations = %v, want %q and no narrative-withheld sentence", served.Limitations, tc.limitation)
			}
			if stored.Status != InvestigationComplete || slices.Contains(stored.Limitations, tc.limitation) {
				t.Fatal("the floor rewrote the stored document")
			}
			again := ApplyServedStatusFloors(served)
			if !reflect.DeepEqual(again.Limitations, served.Limitations) || again.Status != InvestigationPartial {
				t.Fatalf("a second apply changed the answer: %q, %v", again.Status, again.Limitations)
			}
			// The served head: what the completeness authority's apply step
			// makes of it, on every serving point.
			restated := restateServedStatusHead(served)
			assertAnswerKept(t, stored, restated)
		})
	}
}

// A head the server did not compose is left alone, by the floor and by the
// serving step. floorNoMatchTo would write over it.
func TestTheCutReadFloorLeavesAHeadTheServerDidNotCompose(t *testing.T) {
	t.Parallel()
	for name, head := range map[string]string{
		"a client written answer": contractsv1.ContextFabricClientSynthesisAnswer,
		"any other head":          "The head a stored answer carries, word for word.",
	} {
		t.Run(name, func(t *testing.T) {
			stored := storedWithRows(t, hopWalkCutCase().detail)
			stored.DirectJudgment, stored.DeterministicAnswer = head, head
			served := restateServedStatusHead(ApplyServedStatusFloors(stored))
			if served.Status != InvestigationPartial {
				t.Fatalf("status = %q, want partial", served.Status)
			}
			if served.DirectJudgment != head || served.DeterministicAnswer != head {
				t.Fatalf("heads = %q / %q, want %q verbatim", served.DirectJudgment, served.DeterministicAnswer, head)
			}
		})
	}
}

// Each clause of the match and of the status rule.
func TestTheCutReadFloorMatchesADegradingRowAndNeverPromotes(t *testing.T) {
	t.Parallel()
	hop, team := hopWalkCutCase(), undecidedPathsCase()
	notDegrading := hop.detail
	notDegrading.Degrading = false
	cases := map[string]struct {
		detail CoverageDetail
		status InvestigationStatus
		refuse bool
		want   InvestigationStatus
	}{
		"the hop walk row lowers complete":   {hop.detail, InvestigationComplete, false, InvestigationPartial},
		"the undecided row lowers complete":  {team.detail, InvestigationComplete, false, InvestigationPartial},
		"a row that does not degrade":        {notDegrading, InvestigationComplete, false, InvestigationComplete},
		"another graph row":                  {detailFixture(contractsv1.ContextFabricCoverageDetailGraphEndpointLookupFailed, "context-fabric:graph", true, "endpoint_lookup_failed:1", withCount(1)), InvestigationComplete, false, InvestigationComplete},
		"partial stays partial":              {hop.detail, InvestigationPartial, false, InvestigationPartial},
		"degraded stays degraded":            {hop.detail, InvestigationDegraded, false, InvestigationDegraded},
		"degraded stays degraded, undecided": {team.detail, InvestigationDegraded, false, InvestigationDegraded},
		"no_match stays no_match":            {hop.detail, InvestigationNoMatch, false, InvestigationNoMatch},
		"no_match stays no_match, undecided": {team.detail, InvestigationNoMatch, false, InvestigationNoMatch},
		"clarification stays as it is":       {hop.detail, InvestigationClarificationRequired, false, InvestigationClarificationRequired},
		"a refusal keeps its status":         {hop.detail, InvestigationComplete, true, InvestigationComplete},
		"a refusal keeps it, undecided":      {team.detail, InvestigationComplete, true, InvestigationComplete},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stored := storedWithRows(t, tc.detail)
			stored.Status = tc.status
			if tc.refuse {
				stored.RefusalBasis = "planted refusal basis"
			}
			stored.Completeness = ComputeAnswerCompleteness(stored)
			served := ApplyServedStatusFloors(stored)
			if served.Status != tc.want {
				t.Fatalf("served status = %q, want %q", served.Status, tc.want)
			}
			if tc.want == tc.status && !reflect.DeepEqual(served.Limitations, stored.Limitations) {
				t.Fatalf("limitations = %v, want them unchanged from %v: this floor did not move the status", served.Limitations, stored.Limitations)
			}
		})
	}
}

// With no row, a complete answer stays complete.
func TestNoCutReadRowLeavesACompleteAnswerComplete(t *testing.T) {
	t.Parallel()
	stored := storedWithRows(t)
	if served := ApplyServedStatusFloors(stored); served.Status != InvestigationComplete || !reflect.DeepEqual(served.Limitations, stored.Limitations) {
		t.Fatalf("served %q, limitations %v, want the stored answer", served.Status, served.Limitations)
	}
}

// A sibling floor keeps its own swap: a read that also lowers by a floor that
// withholds the narrative is served as that floor serves it.
func TestACutReadRowLeavesASiblingFloorItsOwnSwap(t *testing.T) {
	t.Parallel()
	stored := storedWithRows(t, unverifiedNarrowingDetail(), hopWalkCutCase().detail)
	served := ApplyServedStatusFloors(stored)
	if served.Status != InvestigationPartial || !slices.Contains(served.Limitations, synthesisNarrativeWithheldLimitation) {
		t.Fatalf("served %q, limitations %v, want the sibling floor's own withheld sentence", served.Status, served.Limitations)
	}
}

// The helper's contract, with a sentence no caller uses: it takes the sentence
// from its caller and knows no cause.
func TestFloorKeepingNarrativeIsStatusOnly(t *testing.T) {
	t.Parallel()
	const sentence = "An arbitrary sentence from a caller."
	stored := storedWithRows(t)
	result := stored
	floorKeepingNarrative(&result, InvestigationPartial, sentence)
	if result.Status != InvestigationPartial || !result.Coverage.Partial {
		t.Fatalf("status %q, partial %v, want partial on both", result.Status, result.Coverage.Partial)
	}
	if want := append(append([]string{}, stored.Limitations...), sentence); !slices.Equal(result.Limitations, want) {
		t.Fatalf("limitations = %v, want %v", result.Limitations, want)
	}
	if result.DirectJudgment != stored.DirectJudgment || result.DeterministicAnswer != stored.DeterministicAnswer || result.CurrentState != stored.CurrentState {
		t.Fatalf("the helper changed prose: %q / %q / %q", result.DirectJudgment, result.DeterministicAnswer, result.CurrentState)
	}
	if !reflect.DeepEqual(result.Completeness, stored.Completeness) {
		t.Fatal("the helper changed completeness: ApplyServedStatusFloors recomputes it")
	}
	// It sets the floor it is given, and the guards are its caller's.
	floorKeepingNarrative(&result, InvestigationDegraded, sentence)
	if result.Status != InvestigationDegraded {
		t.Fatalf("status = %q, want the floor it was given", result.Status)
	}
	if count := len(result.Limitations); count != len(stored.Limitations)+1 {
		t.Fatalf("limitations = %v, want the sentence stated once", result.Limitations)
	}
}

// The bound: a full list gives way to the sentence, and the loss is counted.
func TestFloorKeepingNarrativeAppendsWithinTheLimitationBound(t *testing.T) {
	t.Parallel()
	const sentence = "An arbitrary sentence from a caller."
	result := storedWithRows(t)
	result.Limitations = nil
	for i := 0; i < contractsv1.ContextFabricLimitationsMaxCount; i++ {
		result.Limitations = append(result.Limitations, "A model caveat "+strings.Repeat("x", i+1)+".")
	}
	floorKeepingNarrative(&result, InvestigationPartial, sentence)
	if len(result.Limitations) != contractsv1.ContextFabricLimitationsMaxCount || !slices.Contains(result.Limitations, sentence) || result.LimitationsDisplaced != 1 {
		t.Fatalf("limitations %d, displaced %d, sentence kept %v, want the bound held and one displaced", len(result.Limitations), result.LimitationsDisplaced, slices.Contains(result.Limitations, sentence))
	}
}

// The reuse path: a stored complete answer that carries a row is served partial
// through the engine's late writers.
func TestAReusedCompleteAnswerOverACutReadIsServedPartial(t *testing.T) {
	t.Parallel()
	stored := runAuthorizationCutCohortQuestion(t, 4, Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, []InvestigationGoal{GoalAssessState})
	if stored.Status != InvestigationComplete {
		t.Fatalf("fixture: the answer is %q, want complete", stored.Status)
	}
	hop := hopWalkCutCase()
	stored.Coverage.Details = append(stored.Coverage.Details, hop.detail)
	stored.Coverage.DegradedReasons = append(stored.Coverage.DegradedReasons, hop.detail.Raw)
	stored.Coverage.Partial = true
	stored.Completeness = ComputeAnswerCompleteness(stored)
	committed := []SubjectRef{{Kind: SubjectOrganization, CanonicalID: "org_1", Label: "Org"}}
	for _, member := range stored.Cohort.Members {
		committed = append(committed, member.Subject)
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph:     graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed}},
		Results:   &resultStoreStub{},
		ReuseGate: readingReuseGate{stored: stored, frame: frameWithPointer([]InvestigationGoal{GoalAssessState}, scopedExpression(SubjectTeam))},
	})
	reused, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if !reused.Reused {
		t.Fatal("the fixture did not take the reuse path, so it proves nothing about it")
	}
	if reused.Status != InvestigationPartial || !slices.Contains(reused.Limitations, hop.limitation) {
		t.Fatalf("served by reuse %q, limitations %v, want partial and %q", reused.Status, reused.Limitations, hop.limitation)
	}
}
