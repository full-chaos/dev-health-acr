package falkorgraph

import "testing"

func TestCutLandedMeasuresTheDeadlineNotTheReturnTime(t *testing.T) {
	const deadline = int64(1_000_050)
	for name, tc := range map[string]struct {
		sent    int64
		landing probeLanding
		want    cutVerdict
	}{
		"sent before, committed after: a cut, however late the caller returned": {1_000_001, probeLanding{1, 1_000_400}, cutMidCommand},
		"committed one ms after the deadline":                                   {1_000_001, probeLanding{1, deadline + 1}, cutMidCommand},
		"committed at the deadline is not a cut":                                {1_000_001, probeLanding{1, deadline}, cutCommittedBeforeDeadline},
		"committed before the deadline is not a cut":                            {1_000_001, probeLanding{1, deadline - 1}, cutCommittedBeforeDeadline},
		"sent and never landed":                                                 {1_000_001, probeLanding{0, 0}, cutNotLanded},
		"sent after the deadline: no cut was measured":                          {deadline + 5, probeLanding{1, deadline + 400}, cutNeverSent},
		"sent at the deadline: no cut was measured":                             {deadline, probeLanding{1, deadline + 400}, cutNeverSent},
		"never sent": {0, probeLanding{0, 0}, cutNeverSent},
	} {
		if got := classifyCut(tc.sent, deadline, tc.landing); got != tc.want {
			t.Errorf("%s: verdict %d, want %d", name, got, tc.want)
		}
	}
}

// A recording gate stamps the first send and holds nothing back.
func TestRecordingGateStampsTheSendAndHoldsNoReply(t *testing.T) {
	gate := newReplyGate()
	gate.record()
	if _, holding := gate.holding(); holding {
		t.Fatal("a recording gate holds replies back")
	}
	if !gate.recording() {
		t.Fatal("a recording gate is not recording")
	}
	gate.arm()
	if _, holding := gate.holding(); !holding {
		t.Fatal("an armed gate does not hold replies back")
	}
	gate.open()
}
