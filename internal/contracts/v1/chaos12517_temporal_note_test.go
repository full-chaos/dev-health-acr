package v1

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// CHAOS-12517.

// temporalNoteRow builds a label. A value holding " to " is a range.
type temporalNoteRow struct {
	requested, effective string
	grain                ContextFabricTemporalGrain
	complete             bool
}

func temporalInstant(t *testing.T, value string) *time.Time {
	t.Helper()
	for _, layout := range []string{time.DateOnly, "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	t.Fatalf("bad time %q", value)
	return nil
}

func temporalContext(t *testing.T, value string) ContextFabricTimeContext {
	t.Helper()
	if start, end, isRange := strings.Cut(value, " to "); isRange {
		return ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: temporalInstant(t, start), End: temporalInstant(t, end)}
	}
	return ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: temporalInstant(t, value)}
}

func (r temporalNoteRow) label(t *testing.T) ContextFabricTemporalLabel {
	t.Helper()
	return ContextFabricTemporalLabel{Requested: temporalContext(t, r.requested), Effective: temporalContext(t, r.effective), Grain: r.grain, CoverageComplete: r.complete}
}

func TestTheTemporalNoteSaysOnlyWhatTheLabelProves(t *testing.T) {
	for name, row := range map[string]struct {
		temporalNoteRow
		want string
	}{
		"the day asked, complete": {temporalNoteRow{"2026-03-01", "2026-03-01", ContextFabricGrainDay, true}, "This answer speaks for 2026-03-01."},
		"the day asked, not complete": {temporalNoteRow{"2026-03-01", "2026-03-01", ContextFabricGrainDay, false},
			"This answer speaks for 2026-03-01, and not every source covers it."},
		"an earlier day": {temporalNoteRow{"2026-03-01", "2026-02-28", ContextFabricGrainDay, true}, "This answer speaks for 2026-02-28, not the 2026-03-01 you asked about."},
		"an instant read at day grain": {temporalNoteRow{"2026-03-01T14:30", "2026-03-01", ContextFabricGrainDay, true},
			"This answer speaks for 2026-03-01, not the 2026-03-01 14:30 UTC you asked about."},
		"an exact instant":              {temporalNoteRow{"2026-03-01T14:30", "2026-03-01T14:30", ContextFabricGrainInstant, true}, "This answer speaks for 2026-03-01 14:30 UTC."},
		"an exact instant with seconds": {temporalNoteRow{"2026-03-01T14:30:05", "2026-03-01T14:30:05", ContextFabricGrainInstant, true}, "This answer speaks for 2026-03-01 14:30:05 UTC."},
		"an exact midnight":             {temporalNoteRow{"2026-03-01", "2026-03-01", ContextFabricGrainInstant, true}, "This answer speaks for 2026-03-01 00:00 UTC."},
		"an exact range in one minute": {temporalNoteRow{"2026-03-01T14:30:10 to 2026-03-01T14:30:50", "2026-03-01T14:30:10 to 2026-03-01T14:30:50", ContextFabricGrainInstant, true},
			"This answer speaks for 2026-03-01 14:30:10 UTC to 2026-03-01 14:30:50 UTC."},
		"seconds asked at day grain": {temporalNoteRow{"2026-03-01T14:30:05", "2026-03-01", ContextFabricGrainDay, true},
			"This answer speaks for 2026-03-01, not the 2026-03-01 14:30:05 UTC you asked about."},
		"no source spoke for a range in one minute": {temporalNoteRow{"2026-03-01T14:30:10 to 2026-03-01T14:30:50", "2026-03-01T14:30:10 to 2026-03-01T14:30:50", ContextFabricGrainNone, false},
			"No source could speak for 2026-03-01 14:30:10 UTC to 2026-03-01 14:30:50 UTC."},
		"only the start moved": {temporalNoteRow{"2026-01-01T09:30 to 2026-02-01", "2026-01-02 to 2026-02-01", ContextFabricGrainDay, true},
			"This answer speaks for 2026-01-02 to 2026-02-01, not the 2026-01-01 09:30 UTC to 2026-02-01 you asked about."},
		"only the end moved": {temporalNoteRow{"2026-01-01 to 2026-02-01T17:45", "2026-01-01 to 2026-02-01", ContextFabricGrainDay, true},
			"This answer speaks for 2026-01-01 to 2026-02-01, not the 2026-01-01 to 2026-02-01 17:45 UTC you asked about."},
		"no source spoke": {temporalNoteRow{"2026-03-01", "2026-03-01", ContextFabricGrainNone, false}, "No source could speak for 2026-03-01."},
		"a range":         {temporalNoteRow{"2026-01-01 to 2026-02-01", "2026-01-01 to 2026-02-01", ContextFabricGrainDay, true}, "This answer speaks for 2026-01-01 to 2026-02-01."},
		"a narrowed range": {temporalNoteRow{"2026-01-01T09:30 to 2026-02-01T17:45", "2026-01-02 to 2026-02-01", ContextFabricGrainDay, true},
			"This answer speaks for 2026-01-02 to 2026-02-01, not the 2026-01-01 09:30 UTC to 2026-02-01 17:45 UTC you asked about."},
		"one day asked as a zero-width range": {temporalNoteRow{"2026-01-01 to 2026-01-01", "2026-01-01 to 2026-01-01", ContextFabricGrainDay, true}, "This answer speaks for 2026-01-01."},
		"an instant asked as a zero-width range": {temporalNoteRow{"2026-01-01T09:00 to 2026-01-01T09:00", "2026-01-01T09:00 to 2026-01-01T09:00", ContextFabricGrainDay, true},
			"No full day of 2026-01-01 09:00 UTC could be read."},
		"part of a day from its start": {temporalNoteRow{"2026-01-01 to 2026-01-01T17:00", "2026-01-01 to 2026-01-01", ContextFabricGrainDay, true},
			"No full day of 2026-01-01 to 2026-01-01 17:00 UTC could be read."},
		"no source spoke on a range": {temporalNoteRow{"2026-01-01 to 2026-02-01", "2026-01-01 to 2026-02-01", ContextFabricGrainNone, false}, "No source could speak for 2026-01-01 to 2026-02-01."},
		"a span with no full day": {temporalNoteRow{"2026-01-01T09:00 to 2026-01-01T17:00", "2026-01-01T17:00 to 2026-01-01T17:00", ContextFabricGrainDay, true},
			"No full day of 2026-01-01 09:00 UTC to 2026-01-01 17:00 UTC could be read."},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := ContextFabricTemporalNote(row.label(t))
			if !ok || got != row.want {
				t.Fatalf("note = %q (%v), want %q", got, ok, row.want)
			}
		})
	}
}

func TestALabelThatNamesNoPastTimeHasNoNote(t *testing.T) {
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	valid := ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime, AsOf: &day}
	for name, label := range map[string]ContextFabricTemporalLabel{
		"zero value":          {},
		"current axis":        {Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent}, Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalCurrent}, Grain: ContextFabricGrainDay},
		"no as-of asked":      {Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime}, Effective: valid, Grain: ContextFabricGrainDay},
		"no effective time":   {Requested: valid, Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalValidTime}, Grain: ContextFabricGrainDay},
		"a range with no end": {Requested: ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &day}, Effective: ContextFabricTimeContext{Axis: ContextFabricTemporalRange, Start: &day}, Grain: ContextFabricGrainDay},
		"an unknown grain":    {Requested: valid, Effective: valid, Grain: "a_future_grain"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := ContextFabricTemporalNote(label); ok {
				t.Fatalf("no note expected, got %q", got)
			}
		})
	}
}

func TestTheTemporalNoteIsPublished(t *testing.T) {
	names := ContextFabricAnswerNoteNames()
	if !slices.Contains(names[:], ContextFabricAnswerNoteTemporal) {
		t.Fatalf("temporal is not in the published notes: %v", names)
	}
	if got := ContextFabricAnswerNoteTemporal.Key(); got != "temporal:note" {
		t.Fatalf("key = %q", got)
	}
}

// A stored label is not revalidated before its words are composed, and a word must
// never cost an answer: mixed axes read as words, and nothing panics.
func TestALabelWithMixedAxesDoesNotPanic(t *testing.T) {
	label := temporalNoteRow{"2026-03-01", "2026-01-01 to 2026-01-01", ContextFabricGrainDay, true}.label(t)
	if got, ok := ContextFabricTemporalNote(label); !ok || got != "This answer speaks for 2026-01-01, not the 2026-03-01 you asked about." {
		t.Fatalf("note = %q (%v)", got, ok)
	}
	label = temporalNoteRow{"2026-01-01 to 2026-01-02", "2026-03-01", ContextFabricGrainDay, true}.label(t)
	if got, ok := ContextFabricTemporalNote(label); !ok || got != "This answer speaks for 2026-03-01, not the 2026-01-01 to 2026-01-02 you asked about." {
		t.Fatalf("note = %q (%v)", got, ok)
	}
}

// coverage_complete false is never shown as complete, on any row that claims to
// speak for a time.
func TestAnIncompleteLabelNeverReadsAsComplete(t *testing.T) {
	for _, row := range []temporalNoteRow{
		{"2026-03-01", "2026-03-01", ContextFabricGrainDay, false},
		{"2026-03-01", "2026-02-28", ContextFabricGrainDay, false},
		{"2026-03-01T14:30", "2026-03-01T14:30", ContextFabricGrainInstant, false},
		{"2026-01-01 to 2026-02-01", "2026-01-01 to 2026-02-01", ContextFabricGrainDay, false},
	} {
		got, ok := ContextFabricTemporalNote(row.label(t))
		if !ok || !strings.Contains(got, "not every source") {
			t.Errorf("%+v reads %q", row, got)
		}
	}
}

// The longest sentence a real label can reach stays a valid label: a word over
// the bound would fail validation for every such answer.
func TestTheLongestTemporalNoteIsAValidLabel(t *testing.T) {
	got, ok := ContextFabricTemporalNote(temporalNoteRow{"2026-01-01T09:30:05 to 2026-12-31T17:45:05", "2026-01-02 to 2026-12-31", ContextFabricGrainDay, false}.label(t))
	if !ok {
		t.Fatal("no note")
	}
	if length := utf8.RuneCountInString(got); length > ContextFabricCoverageDetailLabelMaxLength {
		t.Fatalf("%d runes, over the bound: %q", length, got)
	}
}
