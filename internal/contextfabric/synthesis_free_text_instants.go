package contextfabric

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type calendarDay struct {
	year  int
	month time.Month
	day   int
}

type monthDay struct {
	month time.Month
	day   int
}

type instantDays struct {
	full     map[calendarDay]struct{}
	yearless map[monthDay]struct{}
}

const monthNamePattern = `(Jan(?:uary)?|Feb(?:ruary)?|Mar(?:ch)?|Apr(?:il)?|May|June?|July?|Aug(?:ust)?|Sep(?:t(?:ember)?)?|Oct(?:ober)?|Nov(?:ember)?|Dec(?:ember)?)`

var (
	isoDayPattern       = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})(?:\b|T)`)
	isoDatetimePattern  = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})`)
	monthFirstPattern   = regexp.MustCompile(`\b` + monthNamePattern + `\.?\s+(\d{1,2})(?:st|nd|rd|th)?\b(?:,?\s+(\d{4})\b)?`)
	dayFirstPattern     = regexp.MustCompile(`\b(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?` + monthNamePattern + `\b\.?(?:,?\s+(\d{4})\b)?`)
	monthNumberByPrefix = map[string]time.Month{
		"Jan": time.January, "Feb": time.February, "Mar": time.March, "Apr": time.April,
		"May": time.May, "Jun": time.June, "Jul": time.July, "Aug": time.August,
		"Sep": time.September, "Oct": time.October, "Nov": time.November, "Dec": time.December,
	}
)

func newInstantDays() instantDays {
	return instantDays{full: map[calendarDay]struct{}{}, yearless: map[monthDay]struct{}{}}
}

func (d instantDays) add(year int, month time.Month, day int) {
	if month < time.January || month > time.December || day < 1 || day > 31 {
		return
	}
	if year == 0 {
		d.yearless[monthDay{month, day}] = struct{}{}
		return
	}
	d.full[calendarDay{year, month, day}] = struct{}{}
	d.yearless[monthDay{month, day}] = struct{}{}
}

func (d instantDays) addTime(t time.Time) {
	u := t.UTC()
	d.add(u.Year(), u.Month(), u.Day())
}

// addAdjacent grounds the day an instant falls on and the days either side:
// a draft may state the same instant in a local zone, whose day differs from
// the UTC day by at most one.
func (d instantDays) addAdjacent(t time.Time) {
	for _, offset := range []int{-1, 0, 1} {
		d.addTime(t.AddDate(0, 0, offset))
	}
}

func (d instantDays) holds(year int, month time.Month, day int) bool {
	if year == 0 {
		_, ok := d.yearless[monthDay{month, day}]
		return ok
	}
	_, ok := d.full[calendarDay{year, month, day}]
	return ok
}

// scanInstantDays calls emit for every calendar day the text states, as an
// ISO date or datetime or as a month-name date. A year of 0 means none was
// stated. A bare month or year names no day and is not visited.
func scanInstantDays(text string, emit func(year int, month time.Month, day int)) {
	visit := func(year int, month time.Month, day int) {
		if year != 0 && time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Day() != day {
			return
		}
		emit(year, month, day)
	}
	for _, m := range isoDayPattern.FindAllStringSubmatch(text, -1) {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		visit(year, time.Month(month), day)
	}
	for _, m := range monthFirstPattern.FindAllStringSubmatch(text, -1) {
		day, _ := strconv.Atoi(m[2])
		year, _ := strconv.Atoi(m[3])
		visit(year, monthNumberByPrefix[m[1][:3]], day)
	}
	for _, m := range dayFirstPattern.FindAllStringSubmatch(text, -1) {
		day, _ := strconv.Atoi(m[1])
		year, _ := strconv.Atoi(m[3])
		visit(year, monthNumberByPrefix[m[2][:3]], day)
	}
}

func synthesisInputInstantDays(input SynthesisInput) (instantDays, error) {
	days := newInstantDays()
	encoded, err := json.Marshal(input)
	if err != nil {
		return days, fmt.Errorf("synthesis input could not be read for instants: %w", err)
	}
	scanInstantDays(string(encoded), days.add)
	for _, m := range isoDatetimePattern.FindAllStringSubmatch(string(encoded), -1) {
		if at, err := time.Parse(time.RFC3339, m[1]+"Z"); err == nil {
			days.addAdjacent(at)
		}
	}
	if window := input.EvidenceWindow; window != nil {
		if window.Start != nil {
			days.addTime(*window.Start)
		}
		if window.End != nil {
			days.addTime(*window.End)
			days.addTime(window.End.Add(-time.Nanosecond))
		}
	}
	if !input.ReadTimeClamp.At.IsZero() {
		days.addAdjacent(input.ReadTimeClamp.At)
	}
	return days, nil
}

func (d SynthesisDraft) freeText() []string {
	texts := []string{d.DirectJudgment, d.CurrentState, d.DeterministicAnswer}
	texts = append(texts, d.StrongestPressures...)
	texts = append(texts, d.Limitations...)
	texts = append(texts, d.Warnings...)
	for _, driver := range d.Drivers {
		texts = append(texts, driver.Title, driver.Summary, driver.Qualification)
	}
	for _, section := range [][]Finding{d.RemainingWork, d.ReadinessGaps, d.Conflicts} {
		for _, finding := range section {
			texts = append(texts, finding.Summary)
		}
	}
	for _, disclosure := range d.CoverageDisclosures {
		texts = append(texts, disclosure.Text)
	}
	return texts
}

// requireGroundedInstants rejects a draft whose free text states a calendar
// day the input does not hold. The text of the rejection never carries the
// day the draft wrote.
func (d SynthesisDraft) requireGroundedInstants(input SynthesisInput) error {
	held, err := synthesisInputInstantDays(input)
	if err != nil {
		return rejectSynthesis(RejectionReasonFreeTextInstantUngrounded, "%w", err)
	}
	ungrounded := 0
	for _, text := range d.freeText() {
		if strings.TrimSpace(text) == "" {
			continue
		}
		scanInstantDays(text, func(year int, month time.Month, day int) {
			if month < time.January || month > time.December || day < 1 || day > 31 {
				return
			}
			if !held.holds(year, month, day) {
				ungrounded++
			}
		})
	}
	if ungrounded > 0 {
		return rejectSynthesis(RejectionReasonFreeTextInstantUngrounded, "synthesis free text states %d calendar day(s) the investigation input does not hold", ungrounded)
	}
	return nil
}
