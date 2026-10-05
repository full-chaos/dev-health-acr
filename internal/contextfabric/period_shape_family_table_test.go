package contextfabric

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// periodShapeQuestionKind is how a family-table question states its period: a
// trailing phrase the binder bounds, a period it cannot bound (a bare calendar
// "last month", or a possessive "project's last 30 days"), or none.
type periodShapeQuestionKind int

const (
	periodQuestionStated periodShapeQuestionKind = iota
	periodQuestionNamed
	periodQuestionNone
)

func (k periodShapeQuestionKind) String() string {
	switch k {
	case periodQuestionNamed:
		return "named"
	case periodQuestionNone:
		return "none"
	}
	return "stated"
}

// periodShapeQuestions are the questions of each frame temporal intent, by how
// they state the period. No frame asks the current-state questions.
var periodShapeQuestions = map[TemporalIntent]map[periodShapeQuestionKind]string{
	"": {
		periodQuestionStated: "How is the Ask Dev project doing in the last 30 days?",
		periodQuestionNamed:  "How was the Ask Dev project doing last month?",
		periodQuestionNone:   "How is the Ask Dev project doing?",
	},
	TemporalIntentTimeSeries: {
		periodQuestionStated: "How has the Ask Dev project's throughput changed over the last 30 days?",
		periodQuestionNamed:  "How did the Ask Dev project's throughput change last month?",
		periodQuestionNone:   "How has the Ask Dev project's throughput changed?",
	},
	TemporalIntentPeriodComparison: {
		periodQuestionStated: "How does the Ask Dev project's throughput over the last 30 days compare to the previous 30 days?",
		periodQuestionNamed:  "How does the Ask Dev project's last 30 days compare to the previous 30 days?",
		periodQuestionNone:   "How does the Ask Dev project's throughput compare with before?",
	},
}

func periodShapeQuestion(temporal TemporalIntent, kind periodShapeQuestionKind) string {
	if temporal == TemporalIntentCurrent || temporal == TemporalIntentBoundedWindow {
		temporal = ""
	}
	return periodShapeQuestions[temporal][kind]
}

// periodShapeTableCell is one family-table cell.
type periodShapeTableCell struct {
	cell suppliedRangeCell
	kind periodShapeQuestionKind
}

// periodShapeTableCells is the family table: every reachable family, unframed
// and under each frame temporal intent, by how the question states its period
// and by the time context the client sends.
func periodShapeTableCells() []periodShapeTableCell {
	var out []periodShapeTableCell
	frames := []struct {
		framed   bool
		temporal TemporalIntent
	}{{false, ""}, {true, TemporalIntentCurrent}, {true, TemporalIntentBoundedWindow}, {true, TemporalIntentTimeSeries}, {true, TemporalIntentPeriodComparison}}
	for _, frame := range frames {
		for _, kind := range []periodShapeQuestionKind{periodQuestionStated, periodQuestionNamed, periodQuestionNone} {
			for _, variant := range []suppliedTimeVariant{suppliedRangeEqual, suppliedRangeDifferent, suppliedRangeAbsent} {
				for _, cell := range reachableFamilyCells(WindowClassExplicitWindow, frame.framed, frame.temporal, periodShapeQuestion(frame.temporal, kind)) {
					cell.time = variant
					out = append(out, periodShapeTableCell{cell: cell, kind: kind})
				}
			}
		}
	}
	return out
}

func periodShapeTableKey(c periodShapeTableCell) string {
	temporal := "none"
	if c.cell.frame != nil {
		temporal = string(c.cell.frame.Temporal)
	}
	return fmt.Sprintf("%s|%s|%s|%s", c.cell.family, temporal, c.kind, c.cell.time)
}

// periodShapeOutcome is a cell's served outcome in one line: status, executed
// axis (with a range's bounds in days from now), the evidence window, and
// which period disclosures were served.
func periodShapeOutcome(result InvestigationResult, now time.Time) string {
	tc := result.Interpretation.TimeContext
	axis := string(tc.Axis)
	if tc.Axis == TemporalRange && tc.Start != nil && tc.End != nil {
		axis += fmt.Sprintf(" %s..%s", dayOffset(*tc.Start, now), dayOffset(*tc.End, now))
	}
	window := "no window"
	if w := result.EffectiveEvidenceWindow; w != nil {
		class := string(w.WindowClass)
		if class == "" {
			class = "none"
		}
		relative := string(w.RelativeID)
		if relative == "" {
			relative = "calendar"
		}
		window = string(w.Provenance) + " " + relative + " class " + class
	}
	var flags []string
	if limitationsContain(result.Limitations, "which is not the period the question states") {
		flags = append(flags, "+conflict")
	}
	if limitationsContain(result.Limitations, "period it is compared with") {
		flags = append(flags, "+unread")
	}
	out := fmt.Sprintf("%s %s %s", result.Status, axis, window)
	if len(flags) > 0 {
		out += " " + strings.Join(flags, " ")
	}
	return out
}

func dayOffset(at, now time.Time) string {
	return fmt.Sprintf("%dd", int(at.Sub(now).Round(time.Hour).Hours()/24))
}

// periodShapeRuleRead is the rule written from its statement, not from the
// production predicates: a series or a comparison frame with a period the
// question states reads that period on the range axis. A bound trailing phrase
// gives it from now (a different client range is disclosed); a period the
// grammar cannot bound gives it through the client's range. A comparison
// names the equal period before it as not read. ok false when the rule does
// not decide the cell.
func periodShapeRuleRead(c periodShapeTableCell, now time.Time) (start, end time.Time, conflict, unread, ok bool) {
	if c.cell.frame == nil {
		return
	}
	comparison := c.cell.frame.Temporal == TemporalIntentPeriodComparison
	if !comparison && c.cell.frame.Temporal != TemporalIntentTimeSeries {
		return
	}
	day := 24 * time.Hour
	switch {
	case c.kind == periodQuestionStated:
		start, end, conflict = now.Add(-30*day), now, c.cell.time == suppliedRangeDifferent
	case c.kind == periodQuestionNamed && c.cell.time == suppliedRangeEqual:
		start, end = now.Add(-30*day), now
	case c.kind == periodQuestionNamed && c.cell.time == suppliedRangeDifferent:
		start, end = now.Add(-7*day), now
	default:
		return
	}
	return start, end, conflict, comparison, true
}

// The family table: each cell is routed to its own family, the served outcome
// is pinned by hand, and the rule written from its statement must agree for
// every series and comparison cell it decides. No current-state cell reads a
// stated range or carries the comparison disclosure.
func TestPeriodShapeFamilyTable(t *testing.T) {
	now := suppliedRangeRigNow
	cells := periodShapeTableCells()
	seen := map[string]bool{}
	for _, c := range cells {
		key := periodShapeTableKey(c)
		if seen[key] {
			t.Fatalf("duplicate cell %s", key)
		}
		seen[key] = true
		result, family := runSuppliedRangeCell(t, c.cell, mcpSurface)
		got := periodShapeOutcome(result, now)
		t.Logf("ROW|%s|%s", key, got)
		if family != c.cell.family {
			t.Errorf("%s: resolved family %s, want the cell's own family", key, family)
		}
		if want, ok := periodShapeServedOutcome[key]; !ok || got != want {
			t.Errorf("%s: served %q, want %q (hand-pinned)", key, got, want)
		}
		tc := result.Interpretation.TimeContext
		start, end, conflict, unread, decided := periodShapeRuleRead(c, now)
		if decided {
			if tc.Axis != TemporalRange || tc.Start == nil || !tc.Start.Equal(start) || tc.End == nil || !tc.End.Equal(end) || result.EffectiveEvidenceWindow != nil {
				t.Errorf("%s: axis %s %v..%v window %+v, want the range %s..%s and no window", key, tc.Axis, tc.Start, tc.End, result.EffectiveEvidenceWindow, start, end)
			}
			if limitationsContain(result.Limitations, "which is not the period the question states") != conflict {
				t.Errorf("%s: limitations %q, want the range conflict disclosed=%v", key, result.Limitations, conflict)
			}
			if unread && !limitationsContain(result.Limitations, comparisonPeriodUnreadLimitation(start, end)) {
				t.Errorf("%s: limitations %q, want %q", key, result.Limitations, comparisonPeriodUnreadLimitation(start, end))
			}
		}
		if !unread && limitationsContain(result.Limitations, "period it is compared with") {
			t.Errorf("%s: limitations %q, want no comparison disclosure", key, result.Limitations)
		}
	}
	if len(cells) != len(periodShapeServedOutcome) {
		t.Errorf("table has %d cells, the hand-pinned outcomes %d", len(cells), len(periodShapeServedOutcome))
	}
	// Another surface keeps the client's range.
	other := periodShapeTableCells()[0].cell
	result, _ := runSuppliedRangeCell(t, other, "api")
	if result.Interpretation.TimeContext.Axis != TemporalRange {
		t.Errorf("api surface: axis=%s, want the client's range kept", result.Interpretation.TimeContext.Axis)
	}
}

// periodShapeServedOutcome hand-pins every family-table cell.
var periodShapeServedOutcome = map[string]string{
	"subject_investigation|none|stated|range 30d":                     "complete current question_stated trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|none|stated|range 30d":                 "complete current question_stated trailing_30d class trend_assessment",
	"scoped_cohort_status|none|stated|range 30d":                      "complete current question_stated trailing_30d class none",
	"grouped_cohort_status|none|stated|range 30d":                     "complete current question_stated trailing_30d class trend_assessment",
	"explicit_comparison|none|stated|range 30d":                       "complete current question_stated trailing_30d class none",
	"unclassified|none|stated|range 30d":                              "complete current question_stated trailing_30d class none",
	"subject_investigation|none|stated|range 7d":                      "complete current question_stated trailing_30d class recent_activity_lookup +conflict",
	"discovered_cohort_ranking|none|stated|range 7d":                  "complete current question_stated trailing_30d class trend_assessment +conflict",
	"scoped_cohort_status|none|stated|range 7d":                       "complete current question_stated trailing_30d class none +conflict",
	"grouped_cohort_status|none|stated|range 7d":                      "complete current question_stated trailing_30d class trend_assessment +conflict",
	"explicit_comparison|none|stated|range 7d":                        "complete current question_stated trailing_30d class none +conflict",
	"unclassified|none|stated|range 7d":                               "complete current question_stated trailing_30d class none +conflict",
	"subject_investigation|none|stated|current axis":                  "complete current question_stated trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|none|stated|current axis":              "complete current question_stated trailing_30d class trend_assessment",
	"scoped_cohort_status|none|stated|current axis":                   "complete current question_stated trailing_30d class none",
	"grouped_cohort_status|none|stated|current axis":                  "complete current question_stated trailing_30d class trend_assessment",
	"explicit_comparison|none|stated|current axis":                    "complete current question_stated trailing_30d class none",
	"unclassified|none|stated|current axis":                           "complete current question_stated trailing_30d class none",
	"subject_investigation|none|named|range 30d":                      "complete current question_stated calendar class none",
	"discovered_cohort_ranking|none|named|range 30d":                  "complete current question_stated calendar class none",
	"scoped_cohort_status|none|named|range 30d":                       "complete current question_stated calendar class none",
	"grouped_cohort_status|none|named|range 30d":                      "complete current question_stated calendar class none",
	"explicit_comparison|none|named|range 30d":                        "complete current question_stated calendar class none",
	"unclassified|none|named|range 30d":                               "complete current question_stated calendar class none",
	"subject_investigation|none|named|range 7d":                       "complete current question_stated calendar class none",
	"discovered_cohort_ranking|none|named|range 7d":                   "complete current question_stated calendar class none",
	"scoped_cohort_status|none|named|range 7d":                        "complete current question_stated calendar class none",
	"grouped_cohort_status|none|named|range 7d":                       "complete current question_stated calendar class none",
	"explicit_comparison|none|named|range 7d":                         "complete current question_stated calendar class none",
	"unclassified|none|named|range 7d":                                "complete current question_stated calendar class none",
	"subject_investigation|none|named|current axis":                   "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|none|named|current axis":               "clarification_required current inferred_default trailing_30d class trend_assessment",
	"scoped_cohort_status|none|named|current axis":                    "complete current no window",
	"grouped_cohort_status|none|named|current axis":                   "clarification_required current inferred_default trailing_30d class trend_assessment",
	"explicit_comparison|none|named|current axis":                     "complete current no window",
	"unclassified|none|named|current axis":                            "complete current no window",
	"subject_investigation|none|none|range 30d":                       "complete range -30d..0d no window",
	"discovered_cohort_ranking|none|none|range 30d":                   "complete range -30d..0d no window",
	"scoped_cohort_status|none|none|range 30d":                        "complete range -30d..0d no window",
	"grouped_cohort_status|none|none|range 30d":                       "complete range -30d..0d no window",
	"explicit_comparison|none|none|range 30d":                         "complete range -30d..0d no window",
	"unclassified|none|none|range 30d":                                "complete range -30d..0d no window",
	"subject_investigation|none|none|range 7d":                        "complete range -7d..0d no window",
	"discovered_cohort_ranking|none|none|range 7d":                    "complete range -7d..0d no window",
	"scoped_cohort_status|none|none|range 7d":                         "complete range -7d..0d no window",
	"grouped_cohort_status|none|none|range 7d":                        "complete range -7d..0d no window",
	"explicit_comparison|none|none|range 7d":                          "complete range -7d..0d no window",
	"unclassified|none|none|range 7d":                                 "complete range -7d..0d no window",
	"subject_investigation|none|none|current axis":                    "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|none|none|current axis":                "clarification_required current inferred_default trailing_90d class trend_assessment",
	"scoped_cohort_status|none|none|current axis":                     "complete current no window",
	"grouped_cohort_status|none|none|current axis":                    "clarification_required current inferred_default trailing_90d class trend_assessment",
	"explicit_comparison|none|none|current axis":                      "complete current no window",
	"unclassified|none|none|current axis":                             "complete current no window",
	"subject_investigation|current|stated|range 30d":                  "complete current question_stated trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|current|stated|range 30d":              "complete current question_stated trailing_30d class trend_assessment",
	"scoped_cohort_status|current|stated|range 30d":                   "no_match current question_stated trailing_30d class none",
	"grouped_cohort_status|current|stated|range 30d":                  "complete current question_stated trailing_30d class trend_assessment",
	"explicit_comparison|current|stated|range 30d":                    "complete current question_stated trailing_30d class none",
	"subject_investigation|current|stated|range 7d":                   "complete current question_stated trailing_30d class recent_activity_lookup +conflict",
	"discovered_cohort_ranking|current|stated|range 7d":               "complete current question_stated trailing_30d class trend_assessment +conflict",
	"scoped_cohort_status|current|stated|range 7d":                    "no_match current question_stated trailing_30d class none +conflict",
	"grouped_cohort_status|current|stated|range 7d":                   "complete current question_stated trailing_30d class trend_assessment +conflict",
	"explicit_comparison|current|stated|range 7d":                     "complete current question_stated trailing_30d class none +conflict",
	"subject_investigation|current|stated|current axis":               "complete current question_stated trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|current|stated|current axis":           "complete current question_stated trailing_30d class trend_assessment",
	"scoped_cohort_status|current|stated|current axis":                "no_match current question_stated trailing_30d class none",
	"grouped_cohort_status|current|stated|current axis":               "complete current question_stated trailing_30d class trend_assessment",
	"explicit_comparison|current|stated|current axis":                 "complete current question_stated trailing_30d class none",
	"subject_investigation|current|named|range 30d":                   "complete current question_stated calendar class none",
	"discovered_cohort_ranking|current|named|range 30d":               "complete current question_stated calendar class none",
	"scoped_cohort_status|current|named|range 30d":                    "no_match current question_stated calendar class none",
	"grouped_cohort_status|current|named|range 30d":                   "complete current question_stated calendar class none",
	"explicit_comparison|current|named|range 30d":                     "complete current question_stated calendar class none",
	"subject_investigation|current|named|range 7d":                    "complete current question_stated calendar class none",
	"discovered_cohort_ranking|current|named|range 7d":                "complete current question_stated calendar class none",
	"scoped_cohort_status|current|named|range 7d":                     "no_match current question_stated calendar class none",
	"grouped_cohort_status|current|named|range 7d":                    "complete current question_stated calendar class none",
	"explicit_comparison|current|named|range 7d":                      "complete current question_stated calendar class none",
	"subject_investigation|current|named|current axis":                "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|current|named|current axis":            "clarification_required current inferred_default trailing_30d class trend_assessment",
	"scoped_cohort_status|current|named|current axis":                 "no_match current no window",
	"grouped_cohort_status|current|named|current axis":                "clarification_required current inferred_default trailing_30d class trend_assessment",
	"explicit_comparison|current|named|current axis":                  "complete current no window",
	"subject_investigation|current|none|range 30d":                    "complete range -30d..0d no window",
	"discovered_cohort_ranking|current|none|range 30d":                "complete range -30d..0d no window",
	"scoped_cohort_status|current|none|range 30d":                     "no_match range -30d..0d no window",
	"grouped_cohort_status|current|none|range 30d":                    "complete range -30d..0d no window",
	"explicit_comparison|current|none|range 30d":                      "complete range -30d..0d no window",
	"subject_investigation|current|none|range 7d":                     "complete range -7d..0d no window",
	"discovered_cohort_ranking|current|none|range 7d":                 "complete range -7d..0d no window",
	"scoped_cohort_status|current|none|range 7d":                      "no_match range -7d..0d no window",
	"grouped_cohort_status|current|none|range 7d":                     "complete range -7d..0d no window",
	"explicit_comparison|current|none|range 7d":                       "complete range -7d..0d no window",
	"subject_investigation|current|none|current axis":                 "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|current|none|current axis":             "clarification_required current inferred_default trailing_90d class trend_assessment",
	"scoped_cohort_status|current|none|current axis":                  "no_match current no window",
	"grouped_cohort_status|current|none|current axis":                 "clarification_required current inferred_default trailing_90d class trend_assessment",
	"explicit_comparison|current|none|current axis":                   "complete current no window",
	"subject_investigation|bounded_window|stated|range 30d":           "complete current question_stated trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|bounded_window|stated|range 30d":       "complete current question_stated trailing_30d class trend_assessment",
	"scoped_cohort_status|bounded_window|stated|range 30d":            "no_match current question_stated trailing_30d class none",
	"grouped_cohort_status|bounded_window|stated|range 30d":           "complete current question_stated trailing_30d class trend_assessment",
	"explicit_comparison|bounded_window|stated|range 30d":             "complete current question_stated trailing_30d class none",
	"subject_investigation|bounded_window|stated|range 7d":            "complete current question_stated trailing_30d class recent_activity_lookup +conflict",
	"discovered_cohort_ranking|bounded_window|stated|range 7d":        "complete current question_stated trailing_30d class trend_assessment +conflict",
	"scoped_cohort_status|bounded_window|stated|range 7d":             "no_match current question_stated trailing_30d class none +conflict",
	"grouped_cohort_status|bounded_window|stated|range 7d":            "complete current question_stated trailing_30d class trend_assessment +conflict",
	"explicit_comparison|bounded_window|stated|range 7d":              "complete current question_stated trailing_30d class none +conflict",
	"subject_investigation|bounded_window|stated|current axis":        "complete current question_stated trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|bounded_window|stated|current axis":    "complete current question_stated trailing_30d class trend_assessment",
	"scoped_cohort_status|bounded_window|stated|current axis":         "no_match current question_stated trailing_30d class none",
	"grouped_cohort_status|bounded_window|stated|current axis":        "complete current question_stated trailing_30d class trend_assessment",
	"explicit_comparison|bounded_window|stated|current axis":          "complete current question_stated trailing_30d class none",
	"subject_investigation|bounded_window|named|range 30d":            "complete current question_stated calendar class none",
	"discovered_cohort_ranking|bounded_window|named|range 30d":        "complete current question_stated calendar class none",
	"scoped_cohort_status|bounded_window|named|range 30d":             "no_match current question_stated calendar class none",
	"grouped_cohort_status|bounded_window|named|range 30d":            "complete current question_stated calendar class none",
	"explicit_comparison|bounded_window|named|range 30d":              "complete current question_stated calendar class none",
	"subject_investigation|bounded_window|named|range 7d":             "complete current question_stated calendar class none",
	"discovered_cohort_ranking|bounded_window|named|range 7d":         "complete current question_stated calendar class none",
	"scoped_cohort_status|bounded_window|named|range 7d":              "no_match current question_stated calendar class none",
	"grouped_cohort_status|bounded_window|named|range 7d":             "complete current question_stated calendar class none",
	"explicit_comparison|bounded_window|named|range 7d":               "complete current question_stated calendar class none",
	"subject_investigation|bounded_window|named|current axis":         "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|bounded_window|named|current axis":     "clarification_required current inferred_default trailing_30d class trend_assessment",
	"scoped_cohort_status|bounded_window|named|current axis":          "no_match current no window",
	"grouped_cohort_status|bounded_window|named|current axis":         "clarification_required current inferred_default trailing_30d class trend_assessment",
	"explicit_comparison|bounded_window|named|current axis":           "complete current no window",
	"subject_investigation|bounded_window|none|range 30d":             "complete range -30d..0d no window",
	"discovered_cohort_ranking|bounded_window|none|range 30d":         "complete range -30d..0d no window",
	"scoped_cohort_status|bounded_window|none|range 30d":              "no_match range -30d..0d no window",
	"grouped_cohort_status|bounded_window|none|range 30d":             "complete range -30d..0d no window",
	"explicit_comparison|bounded_window|none|range 30d":               "complete range -30d..0d no window",
	"subject_investigation|bounded_window|none|range 7d":              "complete range -7d..0d no window",
	"discovered_cohort_ranking|bounded_window|none|range 7d":          "complete range -7d..0d no window",
	"scoped_cohort_status|bounded_window|none|range 7d":               "no_match range -7d..0d no window",
	"grouped_cohort_status|bounded_window|none|range 7d":              "complete range -7d..0d no window",
	"explicit_comparison|bounded_window|none|range 7d":                "complete range -7d..0d no window",
	"subject_investigation|bounded_window|none|current axis":          "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|bounded_window|none|current axis":      "clarification_required current inferred_default trailing_90d class trend_assessment",
	"scoped_cohort_status|bounded_window|none|current axis":           "no_match current no window",
	"grouped_cohort_status|bounded_window|none|current axis":          "clarification_required current inferred_default trailing_90d class trend_assessment",
	"explicit_comparison|bounded_window|none|current axis":            "complete current no window",
	"subject_investigation|time_series|stated|range 30d":              "complete range -30d..0d no window",
	"discovered_cohort_ranking|time_series|stated|range 30d":          "complete range -30d..0d no window",
	"scoped_cohort_status|time_series|stated|range 30d":               "no_match range -30d..0d no window",
	"grouped_cohort_status|time_series|stated|range 30d":              "complete range -30d..0d no window",
	"explicit_comparison|time_series|stated|range 30d":                "complete range -30d..0d no window",
	"subject_investigation|time_series|stated|range 7d":               "complete range -30d..0d no window +conflict",
	"discovered_cohort_ranking|time_series|stated|range 7d":           "complete range -30d..0d no window +conflict",
	"scoped_cohort_status|time_series|stated|range 7d":                "no_match range -30d..0d no window +conflict",
	"grouped_cohort_status|time_series|stated|range 7d":               "complete range -30d..0d no window +conflict",
	"explicit_comparison|time_series|stated|range 7d":                 "complete range -30d..0d no window +conflict",
	"subject_investigation|time_series|stated|current axis":           "complete range -30d..0d no window",
	"discovered_cohort_ranking|time_series|stated|current axis":       "complete range -30d..0d no window",
	"scoped_cohort_status|time_series|stated|current axis":            "no_match range -30d..0d no window",
	"grouped_cohort_status|time_series|stated|current axis":           "complete range -30d..0d no window",
	"explicit_comparison|time_series|stated|current axis":             "complete range -30d..0d no window",
	"subject_investigation|time_series|named|range 30d":               "complete range -30d..0d no window",
	"discovered_cohort_ranking|time_series|named|range 30d":           "complete range -30d..0d no window",
	"scoped_cohort_status|time_series|named|range 30d":                "no_match range -30d..0d no window",
	"grouped_cohort_status|time_series|named|range 30d":               "complete range -30d..0d no window",
	"explicit_comparison|time_series|named|range 30d":                 "complete range -30d..0d no window",
	"subject_investigation|time_series|named|range 7d":                "complete range -7d..0d no window",
	"discovered_cohort_ranking|time_series|named|range 7d":            "complete range -7d..0d no window",
	"scoped_cohort_status|time_series|named|range 7d":                 "no_match range -7d..0d no window",
	"grouped_cohort_status|time_series|named|range 7d":                "complete range -7d..0d no window",
	"explicit_comparison|time_series|named|range 7d":                  "complete range -7d..0d no window",
	"subject_investigation|time_series|named|current axis":            "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|time_series|named|current axis":        "clarification_required current inferred_default trailing_30d class trend_assessment",
	"scoped_cohort_status|time_series|named|current axis":             "no_match current no window",
	"grouped_cohort_status|time_series|named|current axis":            "clarification_required current inferred_default trailing_30d class trend_assessment",
	"explicit_comparison|time_series|named|current axis":              "complete current no window",
	"subject_investigation|time_series|none|range 30d":                "complete range -30d..0d no window",
	"discovered_cohort_ranking|time_series|none|range 30d":            "complete range -30d..0d no window",
	"scoped_cohort_status|time_series|none|range 30d":                 "no_match range -30d..0d no window",
	"grouped_cohort_status|time_series|none|range 30d":                "complete range -30d..0d no window",
	"explicit_comparison|time_series|none|range 30d":                  "complete range -30d..0d no window",
	"subject_investigation|time_series|none|range 7d":                 "complete range -7d..0d no window",
	"discovered_cohort_ranking|time_series|none|range 7d":             "complete range -7d..0d no window",
	"scoped_cohort_status|time_series|none|range 7d":                  "no_match range -7d..0d no window",
	"grouped_cohort_status|time_series|none|range 7d":                 "complete range -7d..0d no window",
	"explicit_comparison|time_series|none|range 7d":                   "complete range -7d..0d no window",
	"subject_investigation|time_series|none|current axis":             "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|time_series|none|current axis":         "clarification_required current inferred_default trailing_90d class trend_assessment",
	"scoped_cohort_status|time_series|none|current axis":              "no_match current no window",
	"grouped_cohort_status|time_series|none|current axis":             "clarification_required current inferred_default trailing_90d class trend_assessment",
	"explicit_comparison|time_series|none|current axis":               "complete current no window",
	"subject_investigation|period_comparison|stated|range 30d":        "complete range -30d..0d no window +unread",
	"discovered_cohort_ranking|period_comparison|stated|range 30d":    "complete range -30d..0d no window +unread",
	"scoped_cohort_status|period_comparison|stated|range 30d":         "no_match range -30d..0d no window +unread",
	"grouped_cohort_status|period_comparison|stated|range 30d":        "complete range -30d..0d no window +unread",
	"explicit_comparison|period_comparison|stated|range 30d":          "complete range -30d..0d no window +unread",
	"subject_investigation|period_comparison|stated|range 7d":         "complete range -30d..0d no window +conflict +unread",
	"discovered_cohort_ranking|period_comparison|stated|range 7d":     "complete range -30d..0d no window +conflict +unread",
	"scoped_cohort_status|period_comparison|stated|range 7d":          "no_match range -30d..0d no window +conflict +unread",
	"grouped_cohort_status|period_comparison|stated|range 7d":         "complete range -30d..0d no window +conflict +unread",
	"explicit_comparison|period_comparison|stated|range 7d":           "complete range -30d..0d no window +conflict +unread",
	"subject_investigation|period_comparison|stated|current axis":     "complete range -30d..0d no window +unread",
	"discovered_cohort_ranking|period_comparison|stated|current axis": "complete range -30d..0d no window +unread",
	"scoped_cohort_status|period_comparison|stated|current axis":      "no_match range -30d..0d no window +unread",
	"grouped_cohort_status|period_comparison|stated|current axis":     "complete range -30d..0d no window +unread",
	"explicit_comparison|period_comparison|stated|current axis":       "complete range -30d..0d no window +unread",
	"subject_investigation|period_comparison|named|range 30d":         "complete range -30d..0d no window +unread",
	"discovered_cohort_ranking|period_comparison|named|range 30d":     "complete range -30d..0d no window +unread",
	"scoped_cohort_status|period_comparison|named|range 30d":          "no_match range -30d..0d no window +unread",
	"grouped_cohort_status|period_comparison|named|range 30d":         "complete range -30d..0d no window +unread",
	"explicit_comparison|period_comparison|named|range 30d":           "complete range -30d..0d no window +unread",
	"subject_investigation|period_comparison|named|range 7d":          "complete range -7d..0d no window +unread",
	"discovered_cohort_ranking|period_comparison|named|range 7d":      "complete range -7d..0d no window +unread",
	"scoped_cohort_status|period_comparison|named|range 7d":           "no_match range -7d..0d no window +unread",
	"grouped_cohort_status|period_comparison|named|range 7d":          "complete range -7d..0d no window +unread",
	"explicit_comparison|period_comparison|named|range 7d":            "complete range -7d..0d no window +unread",
	"subject_investigation|period_comparison|named|current axis":      "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|period_comparison|named|current axis":  "clarification_required current inferred_default trailing_90d class trend_assessment",
	"scoped_cohort_status|period_comparison|named|current axis":       "no_match current no window",
	"grouped_cohort_status|period_comparison|named|current axis":      "clarification_required current inferred_default trailing_90d class trend_assessment",
	"explicit_comparison|period_comparison|named|current axis":        "complete current no window",
	"subject_investigation|period_comparison|none|range 30d":          "complete range -30d..0d no window",
	"discovered_cohort_ranking|period_comparison|none|range 30d":      "complete range -30d..0d no window",
	"scoped_cohort_status|period_comparison|none|range 30d":           "no_match range -30d..0d no window",
	"grouped_cohort_status|period_comparison|none|range 30d":          "complete range -30d..0d no window",
	"explicit_comparison|period_comparison|none|range 30d":            "complete range -30d..0d no window",
	"subject_investigation|period_comparison|none|range 7d":           "complete range -7d..0d no window",
	"discovered_cohort_ranking|period_comparison|none|range 7d":       "complete range -7d..0d no window",
	"scoped_cohort_status|period_comparison|none|range 7d":            "no_match range -7d..0d no window",
	"grouped_cohort_status|period_comparison|none|range 7d":           "complete range -7d..0d no window",
	"explicit_comparison|period_comparison|none|range 7d":             "complete range -7d..0d no window",
	"subject_investigation|period_comparison|none|current axis":       "clarification_required current inferred_default trailing_30d class recent_activity_lookup",
	"discovered_cohort_ranking|period_comparison|none|current axis":   "clarification_required current inferred_default trailing_90d class trend_assessment",
	"scoped_cohort_status|period_comparison|none|current axis":        "no_match current no window",
	"grouped_cohort_status|period_comparison|none|current axis":       "clarification_required current inferred_default trailing_90d class trend_assessment",
	"explicit_comparison|period_comparison|none|current axis":         "complete current no window",
}
