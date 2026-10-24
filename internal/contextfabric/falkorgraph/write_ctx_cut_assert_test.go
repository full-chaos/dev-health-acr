package falkorgraph

import (
	"testing"
	"time"
)

func TestCutLandedMeasuresTheDeadlineNotTheReturnTime(t *testing.T) {
	deadline := time.UnixMilli(1_000_050).Add(500 * time.Microsecond)
	deadlineMs := deadline.UnixMilli()
	at := func(ms int64) time.Time { return time.UnixMilli(ms) }
	for name, tc := range map[string]struct {
		sent    time.Time
		landing probeLanding
		want    cutVerdict
	}{
		"sent before, committed after: a cut, however late the caller returned": {at(1_000_001), probeLanding{1, 1_000_400}, cutMidCommand},
		"committed one ms after the deadline":                                   {at(1_000_001), probeLanding{1, deadlineMs + 1}, cutMidCommand},
		"committed at the deadline is not a cut":                                {at(1_000_001), probeLanding{1, deadlineMs}, cutCommittedBeforeDeadline},
		"committed before the deadline is not a cut":                            {at(1_000_001), probeLanding{1, deadlineMs - 1}, cutCommittedBeforeDeadline},
		"sent and never landed":                                                 {at(1_000_001), probeLanding{0, 0}, cutNotLanded},
		"sent after the deadline: no cut was measured":                          {deadline.Add(5 * time.Millisecond), probeLanding{1, deadlineMs + 400}, cutNeverSent},
		"sent at the deadline: no cut was measured":                             {deadline, probeLanding{1, deadlineMs + 400}, cutNeverSent},
		"never sent": {time.Time{}, probeLanding{0, 0}, cutNeverSent},
		"sent in the deadline's millisecond but before it: a cut, committed after": {deadline.Add(-200 * time.Microsecond), probeLanding{1, deadlineMs + 300}, cutMidCommand},
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
