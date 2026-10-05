package contextfabric

import (
	"fmt"
	"sort"
	"strings"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A count question about one named subject over a period is answered with the
// total of the stored daily rows of every additive daily count fact the read
// returned for that subject.
//
// WHAT "NO ROW FOR A DAY" MEANS. The daily tables are written for a day only
// when the day had activity the job saw, and a day the job never ran has no row
// either, so an absent day is not a zero. The total therefore sums the days
// that have a row, states how many of the period's days those are, and is
// certified only when every day of the period has a row. A day without a row is
// named and never counted as zero.
//
// WHAT IS ADDITIVE is read from the fact's own declaration (FactAdditivity on
// the column of a DailySeries table), never from the column's name.

// PeriodTotal is the total of one additive column of one daily series over the
// stated period, with the coverage it rests on.
type PeriodTotal struct {
	Subject SubjectRef
	Kind    FactKind
	// Table and Column name the series and the additive column summed.
	Table  string
	Column string
	// Unit is the column's declared unit.
	Unit string
	// Sum is the sum of the column over the days that have a row.
	Sum int64
	// DaysWithRow is how many days of the period have a row; DaysInPeriod is
	// how many days the period spans, both ends included.
	DaysWithRow  int
	DaysInPeriod int
	// MissingDays are the days of the period with no row, ascending.
	MissingDays []string
	// Certified is true only when every day of the period has a row.
	Certified bool
}

// PeriodTotalAbsence says why a series that was read yields no total.
type PeriodTotalAbsence struct {
	Subject SubjectRef
	Kind    FactKind
	Table   string
	Reason  string
}

const (
	PeriodTotalAbsenceNoRows       = "no_rows_in_period"
	PeriodTotalAbsenceTruncated    = "series_truncated"
	PeriodTotalAbsenceWithheld     = "rows_withheld"
	PeriodTotalAbsenceInconsistent = "rows_inconsistent"
)

// maxPeriodDays is a sanity bound on the span listed for a period. A period
// longer than MaxFactValueRows days is listed but never totalled: one read
// returns at most that many daily rows, so it could not be covered.
const maxPeriodDays = 3660

// PeriodTotalAbsenceTooLong: the period has more days than one read returns
// daily rows.
const PeriodTotalAbsenceTooLong = "period_longer_than_row_cap"

const dayLayout = "2006-01-02"

// PeriodDays returns the UTC days a period total is summed over, ascending, or
// false when the window does not bound a period holding a whole day. It is the
// ONE place the rule lives.
//
// A trailing window ("the last N days", a relative id) is the N most recent
// COMPLETED UTC days: it ends at today 00:00 UTC, and today's partial day is
// not counted because its row is not final. A client that asks for the last 30
// days gets 30 days, whatever the hour it asks.
//
// Any other window (dates the caller stated) is every whole UTC day inside the
// stated bounds: a bound that cuts a day excludes that day.
//
// The reported effective window stays what the window contract says; the scope
// sentence names the days this rule chose so the two are not read as one.
func PeriodDays(window *contractsv1.ContextFabricEffectiveEvidenceWindow) ([]string, bool) {
	if window == nil || window.Start == nil || window.End == nil {
		return nil, false
	}
	first := ceilToDay(*window.Start)
	stop := truncateToDay(*window.End)
	if duration, trailing := relativeWindowDurations[window.RelativeID]; trailing {
		first = stop.Add(-duration)
	}
	if !first.Before(stop) {
		return nil, false
	}
	days := make([]string, 0, int(stop.Sub(first)/(24*time.Hour)))
	for day := first; day.Before(stop); day = day.AddDate(0, 0, 1) {
		days = append(days, day.Format(dayLayout))
		if len(days) > maxPeriodDays {
			return nil, false
		}
	}
	return days, true
}

// PeriodScopeSentence says which days a period total rests on, so a total over
// "the last 30 days" is read against its named range and not against the
// reported window.
func PeriodScopeSentence(window *contractsv1.ContextFabricEffectiveEvidenceWindow, period []string) string {
	if len(period) == 0 {
		return ""
	}
	if window != nil {
		if _, trailing := relativeWindowDurations[window.RelativeID]; trailing {
			return fmt.Sprintf("The period is the %d most recent completed UTC days, %s to %s; today's partial day is not in the total.",
				len(period), period[0], period[len(period)-1])
		}
	}
	return fmt.Sprintf("The period is the %d whole UTC days from %s to %s inside the stated range; a day the range cuts is not counted.",
		len(period), period[0], period[len(period)-1])
}

// PeriodTotalsForSubject totals every additive column of every daily series
// the facts carry for subject. It reads only what the declarations classify as
// additive, and it reads rows only inside the period.
func PeriodTotalsForSubject(capabilities []FactCapability, facts []CanonicalFact, subject SubjectRef, period []string) ([]PeriodTotal, []PeriodTotalAbsence) {
	if len(period) == 0 {
		return nil, nil
	}
	inPeriod := make(map[string]struct{}, len(period))
	for _, day := range period {
		inPeriod[day] = struct{}{}
	}
	byKind := map[FactKind]FactCapability{}
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	var totals []PeriodTotal
	var absences []PeriodTotalAbsence
	for _, fact := range facts {
		if fact.Subject.Kind != subject.Kind || fact.Subject.CanonicalID != subject.CanonicalID {
			continue
		}
		capability, ok := byKind[fact.Kind]
		if !ok {
			continue
		}
		for _, field := range capability.Fields {
			if !field.DailySeries || !field.AppliesTo(fact.Subject.Kind) {
				continue
			}
			value, present := fact.Fields[field.Name]
			if !present || value.Table == nil || value.Table.Shape != FactTableTimeSeries {
				continue
			}
			additive := additiveColumns(field)
			if len(additive) == 0 {
				continue
			}
			absence := func(reason string) {
				absences = append(absences, PeriodTotalAbsence{Subject: fact.Subject, Kind: fact.Kind, Table: field.Name, Reason: reason})
			}
			if withheld, ok := fact.Fields[field.Name+FactFieldRowsWithheldByGrantSuffix]; ok && withheld.Integer != nil && *withheld.Integer > 0 {
				absence(PeriodTotalAbsenceWithheld)
				continue
			}
			if len(period) > MaxFactValueRows {
				absence(PeriodTotalAbsenceTooLong)
				continue
			}
			rows := value.Table.Rows
			seen := map[string]FactValueRow{}
			consistent := true
			outside := 0
			for _, row := range rows {
				day, ok := rowDay(row)
				if !ok {
					consistent = false
					break
				}
				if _, in := inPeriod[day]; !in {
					outside++
					continue
				}
				if _, dup := seen[day]; dup {
					consistent = false
					break
				}
				seen[day] = row
			}
			if !consistent {
				absence(PeriodTotalAbsenceInconsistent)
				continue
			}
			// A series holds at most MaxFactValueRows rows and drops the oldest
			// beyond that. A full series whose rows all lie in the period is a
			// complete period of that many days; a full series that also holds
			// a day outside the period may have dropped days of the period.
			if len(rows) >= MaxFactValueRows && outside > 0 {
				absence(PeriodTotalAbsenceTruncated)
				continue
			}
			if len(seen) == 0 {
				absence(PeriodTotalAbsenceNoRows)
				continue
			}
			for _, column := range additive {
				total := PeriodTotal{
					Subject: fact.Subject, Kind: fact.Kind, Table: field.Name, Column: column.Name, Unit: column.Unit,
					DaysInPeriod: len(period),
				}
				states := make([]DayState, len(period))
				for i, day := range period {
					row, has := seen[day]
					cell, hasCell := row.Fields[column.Name]
					if has && hasCell && cell.Integer != nil {
						states[i] = DayRowPresent
						total.Sum += *cell.Integer
					}
				}
				total.DaysWithRow, total.MissingDays, total.Certified = PeriodCoverage(period, states)
				totals = append(totals, total)
			}
		}
	}
	sort.SliceStable(totals, func(i, j int) bool {
		if totals[i].Table != totals[j].Table {
			return totals[i].Table < totals[j].Table
		}
		return totals[i].Column < totals[j].Column
	})
	return totals, absences
}

// DayState is what is known of one day of the period.
type DayState int

const (
	// DayNoRow: no row is stored for the day. It is either a day with no
	// activity or a day that was not computed; the server cannot tell which.
	DayNoRow DayState = iota
	// DayRowPresent: a row is stored for the day.
	DayRowPresent
)

// PeriodCoverage is the ONE rule for what a period's days allow a total to
// claim: the days with a row, the days without, and whether the total is
// certified. It is certified only when every day has a row; a day without a
// row is never counted as zero. A per-day state that proves a day was computed
// with no activity would be added here, as a further DayState, and nowhere else.
func PeriodCoverage(period []string, states []DayState) (withRow int, missing []string, certified bool) {
	for i, day := range period {
		if i < len(states) && states[i] == DayRowPresent {
			withRow++
			continue
		}
		missing = append(missing, day)
	}
	return withRow, missing, len(period) > 0 && len(missing) == 0
}

func additiveColumns(field FactFieldDeclaration) []FactColumnDeclaration {
	var out []FactColumnDeclaration
	for _, column := range field.Columns {
		if column.Additivity == FactAdditive {
			out = append(out, column)
		}
	}
	return out
}

func rowDay(row FactValueRow) (string, bool) {
	value, ok := row.Fields[FactDayColumn]
	if !ok || value.String == nil {
		return "", false
	}
	text := strings.TrimSpace(*value.String)
	if len(text) >= len(dayLayout) {
		text = text[:len(dayLayout)]
	}
	if _, err := time.Parse(dayLayout, text); err != nil {
		return "", false
	}
	return text, true
}

// PeriodTotalSentences composes the answer prose for the totals, one sentence
// per total and one per absence. Deterministic: the same totals give the same
// text.
func PeriodTotalSentences(totals []PeriodTotal, absences []PeriodTotalAbsence) []string {
	var out []string
	for _, total := range totals {
		name := strings.ReplaceAll(total.Column, "_", " ")
		if total.Certified {
			out = append(out, fmt.Sprintf("Total of %s over the period: %d, summed from %d of %d days, every day of the period having a stored row.",
				name, total.Sum, total.DaysWithRow, total.DaysInPeriod))
			continue
		}
		out = append(out, fmt.Sprintf("Partial total of %s over the period: %d, summed from the %d of %d days that have a stored row; no row is stored for %s, and those days are not counted as zero, so this is not the total of the period.",
			name, total.Sum, total.DaysWithRow, total.DaysInPeriod, missingDaysText(total.MissingDays)))
	}
	for _, absence := range absences {
		table := strings.ReplaceAll(strings.TrimPrefix(absence.Table, "daily_"), "_", " ")
		switch absence.Reason {
		case PeriodTotalAbsenceNoRows:
			out = append(out, fmt.Sprintf("No stored %s row exists for any day of the period, so no total is stated.", table))
		case PeriodTotalAbsenceTruncated:
			out = append(out, fmt.Sprintf("The stored %s rows for the period reached the limit of %d rows one read returns and extend beyond the period, so some days of the period may be missing and no total is stated.", table, MaxFactValueRows))
		case PeriodTotalAbsenceTooLong:
			out = append(out, fmt.Sprintf("The period is longer than the %d daily rows one read returns, so no %s total is stated for it.", MaxFactValueRows, table))
		case PeriodTotalAbsenceWithheld:
			out = append(out, fmt.Sprintf("Some stored %s rows were withheld from this caller, so no total is stated.", table))
		default:
			out = append(out, fmt.Sprintf("The stored %s rows for the period could not be read as one row per day, so no total is stated.", table))
		}
	}
	return out
}

func missingDaysText(days []string) string {
	const shown = 10
	if len(days) <= shown {
		return strings.Join(days, ", ")
	}
	return fmt.Sprintf("%s and %d more days", strings.Join(days[:shown], ", "), len(days)-shown)
}

// singleSubjectPeriodSentences is the answer prose for the single_subject
// decision: the totals of the one committed subject's additive daily facts over
// the stated period. Empty when the period is not bounded or the subject has no
// additive daily series among the facts read.
func (e *Engine) singleSubjectPeriodSentences(params synthesisAssemblyParams) []string {
	if len(params.Resolution.Committed) != 1 {
		return nil
	}
	subject := params.Resolution.Committed[0]
	period, ok := PeriodDays(params.EffectiveWindow)
	if !ok {
		return nil
	}
	source, ok := e.facts.(capabilitySource)
	if !ok {
		return nil
	}
	capabilities := source.Capabilities()
	totals, absences := PeriodTotalsForSubject(capabilities, params.Facts.Facts, subject, period)
	absences = append(absences, unreadDailySeriesAbsences(capabilities, params.Facts, subject)...)
	sentences := PeriodTotalSentences(totals, absences)
	if len(sentences) == 0 {
		return nil
	}
	return append([]string{PeriodScopeSentence(params.EffectiveWindow, period)}, sentences...)
}

// unreadDailySeriesAbsences names a daily series whose read completed for the
// subject and returned no fact: no row exists for any day of the period.
func unreadDailySeriesAbsences(capabilities []FactCapability, bundle CanonicalFactBundle, subject SubjectRef) []PeriodTotalAbsence {
	var out []PeriodTotalAbsence
	for _, capability := range capabilities {
		if !bundle.ReadSubjects.covers(capability.Kind, subject) {
			continue
		}
		carried := false
		for _, fact := range bundle.Facts {
			if fact.Kind == capability.Kind && fact.Subject.Kind == subject.Kind && fact.Subject.CanonicalID == subject.CanonicalID {
				carried = true
				break
			}
		}
		if carried {
			continue
		}
		for _, field := range capability.Fields {
			if field.DailySeries && field.AppliesTo(subject.Kind) && len(additiveColumns(field)) > 0 {
				out = append(out, PeriodTotalAbsence{Subject: subject, Kind: capability.Kind, Table: field.Name, Reason: PeriodTotalAbsenceNoRows})
			}
		}
	}
	return out
}
