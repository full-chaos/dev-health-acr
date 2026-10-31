package contextfabric

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var serverDefaultReadAt = time.Date(2026, 10, 30, 18, 4, 5, 0, time.FixedZone("PDT", -7*3600))

func readOf(kinds ...FactKind) FactReadSubjects {
	read := FactReadSubjects{}
	for _, kind := range kinds {
		read.add(kind, []SubjectRef{{Kind: SubjectTeam, CanonicalID: "team:linear:CHAOS"}})
	}
	return read
}

// TestServerDefaultWindowStatesTheDefaultTrailingWindowAWindowedReadUsed: a
// completed read of each window-consuming kind gives the platform default
// trailing window, with the bounds the devhealthfacts default_trailing window
// reads over: [start of the UTC day - 90 days, start of the UTC day).
func TestServerDefaultWindowStatesTheDefaultTrailingWindowAWindowedReadUsed(t *testing.T) {
	t.Parallel()
	utc := serverDefaultReadAt.UTC()
	wantEnd := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	wantStart := wantEnd.Add(-90 * 24 * time.Hour)
	for _, kind := range ServerDefaultWindowKinds() {
		got := serverDefaultWindow(TemporalCurrent, nil, readOf(FactHealth, kind), serverDefaultReadAt)
		if got == nil || got.Provenance != WindowServerDefault || got.RelativeID != RelativeWindowTrailing90D || got.AsOf != nil ||
			got.Start == nil || got.End == nil || !got.Start.Equal(wantStart) || !got.End.Equal(wantEnd) || got.WindowClass != "" || got.Confidence != "" {
			t.Errorf("%s: window = %+v, want server_default trailing_90d %s..%s", kind, got, wantStart, wantEnd)
		}
	}
}

// TestServerDefaultWindowIsASnapshotWithoutAWindowedRead: reads that count no
// window (a latest value, a status) give the instant of the read, not a window
// no read used.
func TestServerDefaultWindowIsASnapshotWithoutAWindowedRead(t *testing.T) {
	t.Parallel()
	for name, read := range map[string]FactReadSubjects{
		"snapshot kinds": readOf(FactHealth, FactReadiness, FactWorkload, FactStatus, FactFlow),
		"no read":        nil,
	} {
		got := serverDefaultWindow(TemporalCurrent, nil, read, serverDefaultReadAt)
		if got == nil || got.Provenance != WindowServerDefault || got.AsOf == nil || !got.AsOf.Equal(serverDefaultReadAt) || got.AsOf.Location() != time.UTC ||
			got.WindowClass != contractsv1.ContextFabricWindowClassStateSnapshot || got.Start != nil || got.End != nil || got.RelativeID != "" {
			t.Errorf("%s: window = %+v, want server_default state_snapshot as_of %s", name, got, serverDefaultReadAt.UTC())
		}
	}
}

// TestServerDefaultWindowOfAnAnswerWhoseOnlyWindowedReadIsInvestment
// (CHAOS-12142): a current-axis investment read with no committed window
// counts over the default trailing 90 days, so an answer whose only windowed
// read is investment states that window, never as_of.
func TestServerDefaultWindowOfAnAnswerWhoseOnlyWindowedReadIsInvestment(t *testing.T) {
	t.Parallel()
	utc := serverDefaultReadAt.UTC()
	wantEnd := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	wantStart := wantEnd.Add(-90 * 24 * time.Hour)
	for name, read := range map[string]FactReadSubjects{
		"investment alone":            readOf(FactInvestment),
		"investment beside snapshots": readOf(FactHealth, FactInvestment, FactStatus, FactWorkload),
	} {
		got := serverDefaultWindow(TemporalCurrent, nil, read, serverDefaultReadAt)
		if got == nil || got.Provenance != WindowServerDefault || got.RelativeID != RelativeWindowTrailing90D || got.AsOf != nil ||
			got.Start == nil || got.End == nil || !got.Start.Equal(wantStart) || !got.End.Equal(wantEnd) {
			t.Errorf("%s: window = %+v, want server_default trailing_90d %s..%s, not as_of", name, got, wantStart, wantEnd)
		}
	}
}

// TestServerDefaultWindowLeavesAStatedWindowAndOtherAxesAlone: a window the
// request established is never replaced, and no axis but current carries one.
func TestServerDefaultWindowLeavesAStatedWindowAndOtherAxesAlone(t *testing.T) {
	t.Parallel()
	stated := &EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowQuestionStated}
	if got := serverDefaultWindow(TemporalCurrent, stated, readOf(FactIncidents), serverDefaultReadAt); got != stated {
		t.Fatalf("a stated window was replaced by %+v", got)
	}
	for _, axis := range []TemporalAxis{contractsv1.ContextFabricTemporalValidTime, contractsv1.ContextFabricTemporalObservedTime, contractsv1.ContextFabricTemporalRange} {
		if got := serverDefaultWindow(axis, nil, readOf(FactIncidents), serverDefaultReadAt); got != nil {
			t.Errorf("axis %s: window = %+v, want none", axis, got)
		}
	}
	if got := serverDefaultWindow(TemporalCurrent, nil, readOf(FactIncidents), time.Time{}); got != nil {
		t.Errorf("no read instant: window = %+v, want none rather than an invented instant", got)
	}
}

// TestServerDefaultWindowIsNoCallerAuthority: a server_default window is never
// a carry source for a later turn and never reads as a canonicalized window.
func TestServerDefaultWindowIsNoCallerAuthority(t *testing.T) {
	t.Parallel()
	for name, window := range map[string]*EffectiveEvidenceWindow{
		"trailing": serverDefaultWindow(TemporalCurrent, nil, readOf(FactDeployments), serverDefaultReadAt),
		"as_of":    serverDefaultWindow(TemporalCurrent, nil, nil, serverDefaultReadAt),
	} {
		if callerWindow(window) {
			t.Errorf("%s: callerWindow = true", name)
		}
		if carried := carriableWindow(window); carried != nil {
			t.Errorf("%s: carriableWindow = %+v, want no carry source", name, carried)
		}
		if got := windowCanonicalizationOutcome(requestWindowCanonicalization{}, window, false); got != WindowCanonicalizationNone {
			t.Errorf("%s: canonicalization outcome = %s, want none", name, got)
		}
	}
	confirmed := &EffectiveEvidenceWindow{RelativeID: RelativeWindowAllTime, Provenance: WindowClarificationConfirmed}
	if carriableWindow(confirmed) == nil || !callerWindow(confirmed) {
		t.Fatal("a confirmed window is no longer a carry source; the guard above proves nothing")
	}
}
