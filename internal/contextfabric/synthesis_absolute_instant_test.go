package contextfabric

import (
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestDraftFreeTextStatingAnInstantTheInputDoesNotHoldIsRejected(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*SynthesisDraft){
		"direct_judgment":      func(d *SynthesisDraft) { d.DirectJudgment = "Delivery slowed between 2031-03-04 and 2031-04-01." },
		"current_state":        func(d *SynthesisDraft) { d.CurrentState = "As of 2031-03-04T10:00:00Z the backlog is stable." },
		"strongest_pressures":  func(d *SynthesisDraft) { d.StrongestPressures = []string{"Pressure rose on 2031-03-04."} },
		"limitations":          func(d *SynthesisDraft) { d.Limitations = []string{"Data ends 2031-03-04."} },
		"warnings":             func(d *SynthesisDraft) { d.Warnings = []string{"Window starts 2031-03-04."} },
		"deterministic_answer": func(d *SynthesisDraft) { d.DeterministicAnswer = "Window 2031-03-04 to 2031-04-01." },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input, draft := closureFixture()
			mutate(&draft)
			if err := draft.ValidateAgainst(input); err == nil {
				t.Fatalf("ValidateAgainst() = nil, want a rejection for an absolute instant the input does not hold in %s", name)
			}
		})
	}
}

func TestDraftFreeTextInstantForms(t *testing.T) {
	t.Parallel()
	input, _ := closureFixture()
	held := time.Date(2031, 3, 4, 10, 0, 0, 0, time.UTC)
	input.Facts.Facts[0].ObservedAt = &held
	end := time.Date(2031, 4, 2, 0, 0, 0, 0, time.UTC)
	start := time.Date(2031, 1, 10, 0, 0, 0, 0, time.UTC)
	input.EvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end}
	input.ReadTimeClamp.At = time.Date(2031, 6, 15, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name, text string
		reject     bool
	}{
		{"iso day held", "Seen on 2031-03-04.", false},
		{"iso datetime day held", "Seen at 2031-03-04T23:59:00Z.", false},
		{"iso day not held", "Seen on 2031-03-05.", true},
		{"iso datetime day not held", "Seen at 2031-03-05T01:00:00Z.", true},
		{"prose held", "Seen on March 4, 2031.", false},
		{"prose day first held", "Seen on 4 Mar 2031.", false},
		{"prose yearless held", "Seen on Mar 4.", false},
		{"prose ordinal held", "Seen on 4th March.", false},
		{"prose not held", "Seen on March 5, 2031.", true},
		{"prose day first not held", "Seen on 5 March 2031.", true},
		{"prose yearless not held", "Seen on Oct 7.", true},
		{"prose wrong year", "Seen on March 4, 2030.", true},
		{"exclusive end day before", "Through 2031-04-01.", false},
		{"exclusive end day itself", "Through 2031-04-02.", false},
		{"window start held", "From 2031-01-10.", false},
		{"clock instant held", "As of June 15, 2031.", false},
		{"impossible calendar day is no day", "Key 2031-02-30 is an id.", false},
		{"bare month is out of scope", "Seen in September 2031.", false},
		{"no instant", "Seen 3 times, 12 PRs.", false},
		{"impossible date ignored", "Ticket 2031-13-45 is a key.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, draft := closureFixture()
			draft.CurrentState = tc.text
			err := draft.ValidateAgainst(input)
			var rejection *SynthesisRejection
			switch {
			case tc.reject && !errors.As(err, &rejection):
				t.Fatalf("error = %v, want a rejection", err)
			case tc.reject && rejection.Reason != RejectionReasonFreeTextInstantUngrounded:
				t.Fatalf("reason = %q, want %q", rejection.Reason, RejectionReasonFreeTextInstantUngrounded)
			case !tc.reject && err != nil:
				t.Fatalf("error = %v, want admitted", err)
			}
		})
	}
}

func TestDriverAndFindingTextInstantRejected(t *testing.T) {
	t.Parallel()
	mutations := map[string]func(*SynthesisDraft){
		"driver summary":       func(d *SynthesisDraft) { d.Drivers[0].Summary = "Since 2031-03-04." },
		"driver title":         func(d *SynthesisDraft) { d.Drivers[0].Title = "Since 2031-03-04" },
		"driver qualification": func(d *SynthesisDraft) { d.Drivers[0].Qualification = "Since 2031-03-04." },
		"remaining work": func(d *SynthesisDraft) {
			d.RemainingWork = []Finding{{FindingID: "finding_one_1", Kind: "readiness", Summary: "Due 2031-03-04.", EvidenceRefIDs: []string{"evidence_release_1234"}, ClaimedFactIDs: []string{"claim_readiness_1"}}}
		},
		"readiness gap": func(d *SynthesisDraft) {
			d.ReadinessGaps = []Finding{{FindingID: "finding_one_1", Kind: "readiness", Summary: "Due 2031-03-04.", EvidenceRefIDs: []string{"evidence_release_1234"}, ClaimedFactIDs: []string{"claim_readiness_1"}}}
		},
		"conflict": func(d *SynthesisDraft) {
			d.Conflicts = []Finding{{FindingID: "finding_one_1", Kind: "readiness", Summary: "Due 2031-03-04.", EvidenceRefIDs: []string{"evidence_release_1234"}, ClaimedFactIDs: []string{"claim_readiness_1"}}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input, draft := closureFixture()
			mutate(&draft)
			err := draft.ValidateAgainst(input)
			var rejection *SynthesisRejection
			if !errors.As(err, &rejection) || rejection.Reason != RejectionReasonFreeTextInstantUngrounded {
				t.Fatalf("error = %v, want %q", err, RejectionReasonFreeTextInstantUngrounded)
			}
		})
	}
}
