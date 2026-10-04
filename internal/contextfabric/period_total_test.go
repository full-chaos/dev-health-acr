package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A count about one named subject over a period is answered with the total of
// the subject's stored daily rows. The expected sum is computed here, from the
// seeded rows, never from the production function.

var periodTotalNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func dailyColumns() []FactColumnDeclaration {
	return []FactColumnDeclaration{
		{Name: "day", Type: FactFieldString},
		{Name: "commits_count", Type: FactFieldInteger, Unit: "count", Additivity: FactAdditive},
		{Name: "prs_merged", Type: FactFieldInteger, Unit: "count", Additivity: FactAdditive},
		{Name: "bus_factor", Type: FactFieldInteger, Unit: "count", Additivity: FactNonAdditive},
		{Name: "change_failure_rate", Type: FactFieldNumber, Unit: "ratio", Additivity: FactNonAdditive},
	}
}

func periodCapabilities() []FactCapability {
	return []FactCapability{
		{Kind: FactMetrics, Fields: []FactFieldDeclaration{
			{Name: "daily_metrics", Type: FactFieldTable, DailySeries: true, Columns: dailyColumns()},
		}},
		{Kind: FactHealth, Fields: []FactFieldDeclaration{
			{Name: "daily_health", Type: FactFieldTable, DailySeries: true, Columns: []FactColumnDeclaration{
				{Name: "day", Type: FactFieldString},
				{Name: "compounding_risk", Type: FactFieldNumber, Additivity: FactNonAdditive},
			}},
		}},
	}
}

type periodFactReader struct {
	capabilities []FactCapability
	facts        []CanonicalFact
	read         FactReadSubjects
}

func (r periodFactReader) Capabilities() []FactCapability { return r.capabilities }

func (r periodFactReader) ReadFacts(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
	return CanonicalFactBundle{
		Facts: r.facts, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
		ReadSubjects: r.read,
	}, nil
}

type dayRow struct {
	day     string
	commits int64
	merged  int64
}

func metricsFactFor(subject SubjectRef, rows []dayRow) CanonicalFact {
	valueRows := make([]FactValueRow, 0, len(rows))
	for _, row := range rows {
		valueRows = append(valueRows, FactValueRow{Fields: map[string]FactValue{
			"day":                 StringFactValue(row.day),
			"commits_count":       IntegerFactValue(row.commits),
			"prs_merged":          IntegerFactValue(row.merged),
			"bus_factor":          IntegerFactValue(3),
			"change_failure_rate": NumberFactValue(0.25),
		}})
	}
	return CanonicalFact{
		Kind: FactMetrics, Subject: subject, EvidenceRefIDs: []string{"evidence_1"},
		SourceState: SourceAvailable, Source: "devhealthfacts.metrics", SourceVersion: "v1",
		Fields: map[string]FactValue{"daily_metrics": TableFactValue(FactTable{
			Shape: FactTableTimeSeries, Key: []string{"day"},
			Measures: []string{"commits_count", "prs_merged", "bus_factor", "change_failure_rate"},
			Rows:     valueRows,
		})},
	}
}

// expectedDays is the period the engine resolved, listed independently of the
// production day arithmetic: both ends included.
func expectedDays(start, end time.Time) []string {
	var days []string
	for day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC); !day.After(end); day = day.Add(24 * time.Hour) {
		days = append(days, day.Format("2006-01-02"))
	}
	return days
}

func runPeriodCell(t *testing.T, kind SubjectKind, reader periodFactReader, subject SubjectRef) InvestigationResult {
	t.Helper()
	cell := scopeCell{
		frame: countingFrame(kind), family: QuestionFamilyScopedCohortStatus,
		resolution: SubjectResolution{Committed: []SubjectRef{subject}, Candidates: []SubjectCandidate{namedMatch(subject)}},
		cohort:     kindCohort(kind, 5), status: InvestigationComplete,
		facts: reader, now: periodTotalNow,
	}
	engine := newScopeEngine(t, cell, &recordingTelemetry{})
	request := validInvestigationRequestWithConfirmedWindow()
	request.TimeContext.EvidenceWindow = &RequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D}
	request.RequestID = "request_57750001"
	request.Question = "how many commits in the last 30 days"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return result
}

func periodOf(t *testing.T, result InvestigationResult) []string {
	t.Helper()
	window := result.EffectiveEvidenceWindow
	if window == nil || window.Start == nil || window.End == nil {
		t.Fatalf("result carries no bounded window: %+v", window)
	}
	return expectedDays(window.Start.UTC(), window.End.UTC())
}

func TestASingleSubjectCountIsAnsweredWithThePeriodTotalOfItsDailyRows(t *testing.T) {
	t.Parallel()
	kinds := []SubjectKind{SubjectRepository, SubjectTeam, SubjectProject}
	for _, kind := range kinds {
		kind := kind
		t.Run(string(kind)+"/full coverage", func(t *testing.T) {
			t.Parallel()
			subject := singleSubjectOf(kind)
			probe := runPeriodCell(t, kind, periodFactReader{capabilities: periodCapabilities()}, subject)
			period := periodOf(t, probe)
			var rows []dayRow
			var wantCommits, wantMerged int64
			for i, day := range period {
				row := dayRow{day: day, commits: int64(3 + i%5), merged: int64(i % 3)}
				rows = append(rows, row)
				wantCommits += row.commits
				wantMerged += row.merged
			}
			result := runPeriodCell(t, kind, periodFactReader{capabilities: periodCapabilities(), facts: []CanonicalFact{metricsFactFor(subject, rows)}}, subject)
			want := fmt.Sprintf("Total of commits count over the period: %d, summed from %d of %d days", wantCommits, len(period), len(period))
			if !strings.Contains(result.DeterministicAnswer, want) {
				t.Errorf("answer %q lacks %q", result.DeterministicAnswer, want)
			}
			if !strings.Contains(result.DeterministicAnswer, fmt.Sprintf("Total of prs merged over the period: %d,", wantMerged)) {
				t.Errorf("answer %q lacks the prs merged total %d", result.DeterministicAnswer, wantMerged)
			}
			for _, banned := range []string{"bus factor", "change failure", "Partial total", "compounding"} {
				if strings.Contains(result.DeterministicAnswer, banned) {
					t.Errorf("answer states a non-additive or partial value %q: %q", banned, result.DeterministicAnswer)
				}
			}
			if claim := cardinalityClaimOf(result); claim != nil {
				t.Errorf("a member count claim was served: %+v", *claim)
			}
		})
	}
}

func TestASingleSubjectPeriodTotalNamesTheDaysWithNoRowAndDoesNotZeroFillThem(t *testing.T) {
	t.Parallel()
	subject := singleSubjectOf(SubjectRepository)
	probe := runPeriodCell(t, SubjectRepository, periodFactReader{capabilities: periodCapabilities()}, subject)
	period := periodOf(t, probe)
	missing := map[int]bool{4: true, 11: true}
	var rows []dayRow
	var want int64
	for i, day := range period {
		if missing[i] {
			continue
		}
		row := dayRow{day: day, commits: int64(7 + i), merged: 1}
		rows = append(rows, row)
		want += row.commits
	}
	result := runPeriodCell(t, SubjectRepository, periodFactReader{capabilities: periodCapabilities(), facts: []CanonicalFact{metricsFactFor(subject, rows)}}, subject)
	wantText := fmt.Sprintf("Partial total of commits count over the period: %d, summed from the %d of %d days that have a stored row; no row is stored for %s, %s",
		want, len(period)-2, len(period), period[4], period[11])
	if !strings.Contains(result.DeterministicAnswer, wantText) {
		t.Errorf("answer %q lacks %q", result.DeterministicAnswer, wantText)
	}
	if strings.Contains(result.DeterministicAnswer, "Total of commits count over the period") {
		t.Errorf("a partial total is stated as certified: %q", result.DeterministicAnswer)
	}
}

func TestASingleSubjectWithNoDailyRowsStatesNoTotalAndWhy(t *testing.T) {
	t.Parallel()
	subject := singleSubjectOf(SubjectRepository)
	read := FactReadSubjects{}
	read.add(FactMetrics, []SubjectRef{subject})
	result := runPeriodCell(t, SubjectRepository, periodFactReader{capabilities: periodCapabilities(), read: read}, subject)
	if !strings.Contains(result.DeterministicAnswer, "No stored metrics row exists for any day of the period, so no total is stated.") {
		t.Errorf("answer %q does not name the reason", result.DeterministicAnswer)
	}
	if strings.Contains(result.DeterministicAnswer, "Total of") || strings.Contains(result.DeterministicAnswer, "Partial total") {
		t.Errorf("a total is stated without rows: %q", result.DeterministicAnswer)
	}
}

func TestASingleSubjectWithOnlyNonAdditiveDailyFactsStatesNoTotal(t *testing.T) {
	t.Parallel()
	subject := singleSubjectOf(SubjectRepository)
	probe := runPeriodCell(t, SubjectRepository, periodFactReader{capabilities: periodCapabilities()}, subject)
	period := periodOf(t, probe)
	var rows []FactValueRow
	for _, day := range period {
		rows = append(rows, FactValueRow{Fields: map[string]FactValue{"day": StringFactValue(day), "compounding_risk": NumberFactValue(0.4)}})
	}
	health := CanonicalFact{
		Kind: FactHealth, Subject: subject, EvidenceRefIDs: []string{"evidence_1"},
		SourceState: SourceAvailable, Source: "devhealthfacts.health", SourceVersion: "v1",
		Fields: map[string]FactValue{"daily_health": TableFactValue(FactTable{
			Shape: FactTableTimeSeries, Key: []string{"day"}, Measures: []string{"compounding_risk"}, Rows: rows,
		})},
	}
	result := runPeriodCell(t, SubjectRepository, periodFactReader{capabilities: periodCapabilities(), facts: []CanonicalFact{health}}, subject)
	if strings.Contains(result.DeterministicAnswer, "otal of") || strings.Contains(result.DeterministicAnswer, "No stored") {
		t.Errorf("a non-additive series produced a total sentence: %q", result.DeterministicAnswer)
	}
}

func TestASingleSubjectPeriodTotalIsTheSameWhenAskedTwice(t *testing.T) {
	t.Parallel()
	subject := singleSubjectOf(SubjectRepository)
	probe := runPeriodCell(t, SubjectRepository, periodFactReader{capabilities: periodCapabilities()}, subject)
	var rows []dayRow
	for i, day := range periodOf(t, probe) {
		rows = append(rows, dayRow{day: day, commits: int64(i), merged: 2})
	}
	reader := periodFactReader{capabilities: periodCapabilities(), facts: []CanonicalFact{metricsFactFor(subject, rows)}}
	first := runPeriodCell(t, SubjectRepository, reader, subject)
	second := runPeriodCell(t, SubjectRepository, reader, subject)
	if first.DeterministicAnswer != second.DeterministicAnswer || !strings.Contains(first.DeterministicAnswer, "Total of commits count") {
		t.Errorf("answers differ or lack the total:\n%q\n%q", first.DeterministicAnswer, second.DeterministicAnswer)
	}
	if first.Versions != second.Versions {
		t.Errorf("version sets differ: %+v vs %+v", first.Versions, second.Versions)
	}
}

func TestAPeriodTotalIsNotStatedFromACutOrWithheldSeries(t *testing.T) {
	t.Parallel()
	subject := singleSubjectOf(SubjectRepository)
	period := []string{"2026-09-01"}
	var many []dayRow
	for i := 0; i < MaxFactValueRows; i++ {
		many = append(many, dayRow{day: "2026-09-01", commits: 1})
	}
	cut := metricsFactFor(subject, many)
	totals, absences := PeriodTotalsForSubject(periodCapabilities(), []CanonicalFact{cut}, subject, period)
	if len(totals) != 0 || len(absences) != 1 || absences[0].Reason != PeriodTotalAbsenceTruncated {
		t.Errorf("cut series: totals %+v absences %+v", totals, absences)
	}
	withheld := metricsFactFor(subject, []dayRow{{day: "2026-09-01", commits: 4}})
	withheld.Fields["daily_metrics"+FactFieldRowsWithheldByGrantSuffix] = IntegerFactValue(2)
	totals, absences = PeriodTotalsForSubject(periodCapabilities(), []CanonicalFact{withheld}, subject, period)
	if len(totals) != 0 || len(absences) != 1 || absences[0].Reason != PeriodTotalAbsenceWithheld {
		t.Errorf("withheld series: totals %+v absences %+v", totals, absences)
	}
	dup := metricsFactFor(subject, []dayRow{{day: "2026-09-01", commits: 4}, {day: "2026-09-01", commits: 5}})
	totals, absences = PeriodTotalsForSubject(periodCapabilities(), []CanonicalFact{dup}, subject, period)
	if len(totals) != 0 || len(absences) != 1 || absences[0].Reason != PeriodTotalAbsenceInconsistent {
		t.Errorf("duplicate day: totals %+v absences %+v", totals, absences)
	}
}

func TestAnUnclassifiedDailyColumnIsRefusedAtDeclaration(t *testing.T) {
	t.Parallel()
	declare := func(columns ...FactColumnDeclaration) error {
		return validateFieldDeclarations(FactCapability{
			Kind: FactMetrics, SupportedSubjectKinds: []SubjectKind{SubjectRepository},
			Fields: []FactFieldDeclaration{{Name: "daily_x", Type: FactFieldTable, DailySeries: true, Columns: columns}},
		})
	}
	day := FactColumnDeclaration{Name: "day", Type: FactFieldString}
	if err := declare(day, FactColumnDeclaration{Name: "n", Type: FactFieldInteger}); err == nil {
		t.Error("an unclassified numeric daily column was accepted")
	}
	if err := declare(day, FactColumnDeclaration{Name: "r", Type: FactFieldNumber, Additivity: FactAdditive}); err == nil {
		t.Error("an additive non-integer column was accepted")
	}
	if err := declare(FactColumnDeclaration{Name: "n", Type: FactFieldInteger, Additivity: FactAdditive}); err == nil {
		t.Error("a daily series without a day column was accepted")
	}
	if err := declare(day, FactColumnDeclaration{Name: "n", Type: FactFieldInteger, Additivity: FactAdditive}); err != nil {
		t.Errorf("a classified daily series was refused: %v", err)
	}
}

func TestPeriodCoverageRule(t *testing.T) {
	t.Parallel()
	period := []string{"2026-09-01", "2026-09-02", "2026-09-03"}
	rows := []struct {
		name      string
		states    []DayState
		withRow   int
		missing   []string
		certified bool
	}{
		{"every day has a row", []DayState{DayRowPresent, DayRowPresent, DayRowPresent}, 3, nil, true},
		{"one day has no row", []DayState{DayRowPresent, DayNoRow, DayRowPresent}, 2, []string{"2026-09-02"}, false},
		{"no day has a row", []DayState{DayNoRow, DayNoRow, DayNoRow}, 0, period, false},
		{"states shorter than the period", []DayState{DayRowPresent}, 1, []string{"2026-09-02", "2026-09-03"}, false},
	}
	for _, row := range rows {
		withRow, missing, certified := PeriodCoverage(period, row.states)
		if withRow != row.withRow || certified != row.certified || fmt.Sprint(missing) != fmt.Sprint(row.missing) {
			t.Errorf("%s: got %d %v %t, want %d %v %t", row.name, withRow, missing, certified, row.withRow, row.missing, row.certified)
		}
	}
	if _, _, certified := PeriodCoverage(nil, nil); certified {
		t.Error("an empty period was certified")
	}
}
