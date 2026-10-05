package contextfabric

import (
	"regexp"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The comparison disclosure tells a client how to read the period the answer
// did not read. A client that does exactly what the sentence says must get an
// answer: a hint a literal follower cannot use is worse than none.
//
// The test is that client. It sees only the served limitation, takes the
// evidence window bounds the sentence names, asks about ONE period as the
// sentence says (no comparison wording), and the second answer must read those
// bounds.
var comparisonHintWindow = regexp.MustCompile(`evidence_window start (\S+?) and end (\S+?)[; ]`)

func TestAClientThatFollowsTheComparisonHintLiterallyGetsTheOtherPeriod(t *testing.T) {
	day := 24 * time.Hour
	now := suppliedRangeRigNow
	start := now.Add(-30 * day)
	interpreted, frame := periodShapeInterpreted([]InvestigationGoal{GoalAssessState}, TemporalIntentPeriodComparison, []FactKind{FactFlow}, TimeContext{Axis: TemporalCurrent})
	first := runPeriodShapeTurn(t, suppliedInterpretedPath, measureComparisonQuestion, interpreted, frame)

	var hint string
	for _, limitation := range first.result.Limitations {
		if contractsv1.IsContextFabricComparisonPeriodUnreadLimitation(limitation) {
			hint = limitation
		}
	}
	if hint == "" {
		t.Fatalf("the comparison answer carries no comparison disclosure: %q", first.result.Limitations)
	}
	bounds := comparisonHintWindow.FindStringSubmatch(hint + " ")
	if bounds == nil {
		t.Fatalf("the hint names no evidence_window start and end a client can send: %q", hint)
	}
	secondStart, err := time.Parse(time.RFC3339Nano, bounds[1])
	if err != nil {
		t.Fatalf("hint start %q is not a time: %v", bounds[1], err)
	}
	secondEnd, err := time.Parse(time.RFC3339Nano, bounds[2])
	if err != nil {
		t.Fatalf("hint end %q is not a time: %v", bounds[2], err)
	}
	if !secondEnd.Equal(start) || !secondStart.Equal(start.Add(-30*day)) {
		t.Fatalf("hint names %s..%s, want the 30 days before the stated period", secondStart, secondEnd)
	}

	// What the hint says to send: one period, in the single-period form of the
	// question, with the named window. Nothing else is taken from the first call.
	for _, phrase := range []string{"one period", "without the comparison"} {
		if !strings.Contains(hint, phrase) {
			t.Errorf("the hint does not say to ask about %q: %q", phrase, hint)
		}
	}
	single, singleFrame := periodShapeInterpreted([]InvestigationGoal{GoalAssessState}, TemporalIntentCurrent, []FactKind{FactFlow}, TimeContext{Axis: TemporalCurrent})
	second := runPeriodShapeTurnWith(t, suppliedInterpretedPath, "how was project Ask Dev's throughput?", single, singleFrame, periodShapeTurnOptions{request: func(r *InvestigationRequest) {
		r.TimeContext.EvidenceWindow = &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &secondStart, End: &secondEnd}
	}})
	window := second.result.EffectiveEvidenceWindow
	if window == nil || window.Provenance != contractsv1.ContextFabricWindowQuestionStated || !sameInstant(window.Start, secondStart) || !sameInstant(window.End, secondEnd) {
		t.Fatalf("the second call did not read the hint's bounds as stated: window %+v status %s limitations %q", window, second.result.Status, second.result.Limitations)
	}
	if second.result.Status == InvestigationClarificationRequired || limitationsContain(second.result.Limitations, "compares two periods") {
		t.Errorf("the second call was refused or read as a comparison: status %s limitations %q", second.result.Status, second.result.Limitations)
	}
}
