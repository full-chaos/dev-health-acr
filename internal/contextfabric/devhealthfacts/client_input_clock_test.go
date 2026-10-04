package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

var clockTestFixedInstant = time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC)

type stringRule struct {
	match   string
	values  []string
	numbers []float64
}

type universalClient struct {
	subjectID string
	rows      int
	rules     []stringRule
	spy       func(statement string, dest []any)
	// order serves row i as the rows[order[i]] of the static set, and a non-nil
	// order also makes the rows differ from each other (their numbers carry the
	// static index), so a changed order is a changed provider answer for the
	// same rows. noise scales every float by (1+noise): the last-digit
	// difference two ClickHouse aggregations of the same rows can carry.
	order []int
	noise float64
}

func (c *universalClient) Query(_ context.Context, statement string, _ []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	collapsed := strings.Join(strings.Fields(statement), " ")
	scanner := &universalScanner{client: c, statement: collapsed, remaining: c.rows, total: c.rows}
	for _, rule := range c.rules {
		if strings.Contains(collapsed, rule.match) {
			scanner.values = rule.values
			scanner.numbers = rule.numbers
			break
		}
	}
	return scanner, nil
}

type universalScanner struct {
	client    *universalClient
	statement string
	values    []string
	numbers   []float64
	remaining int
	total     int
	served    int
	current   int
}

func (s *universalScanner) Next() bool {
	if s.remaining <= 0 {
		return false
	}
	s.remaining--
	s.current = s.served
	if s.client.order != nil && s.served < len(s.client.order) {
		s.current = s.client.order[s.served]
	}
	s.served++
	return true
}

func (s *universalScanner) Scan(dest ...any) error {
	if s.client.spy != nil {
		s.client.spy(s.statement, dest)
	}
	position, numberPosition := 0, 0
	for _, target := range dest {
		value := reflect.ValueOf(target)
		if value.Kind() != reflect.Pointer || value.IsNil() {
			return fmt.Errorf("universal scanner: destination %T is not a pointer", target)
		}
		fillDeterministic(value.Elem(), s.rowOffset(), s.client.noise, func() string {
			defer func() { position++ }()
			if position < len(s.values) {
				if s.values[position] == "$id" {
					return s.client.subjectID
				}
				return s.values[position]
			}
			return s.client.subjectID
		}, func() (float64, bool) {
			defer func() { numberPosition++ }()
			if numberPosition < len(s.numbers) {
				return s.numbers[numberPosition], true
			}
			return 0, false
		})
	}
	return nil
}

// rowOffset is the per-row number offset of an ordered client: 0 for the
// plain one-row client, the static row index otherwise.
func (s *universalScanner) rowOffset() int {
	if s.client.order == nil {
		return 0
	}
	return s.current
}

func (s *universalScanner) Err() error { return nil }

func (s *universalScanner) Close() error { return nil }

func fillDeterministic(value reflect.Value, offset int, noise float64, nextString func() string, nextNumber func() (float64, bool)) {
	switch value.Kind() {
	case reflect.String:
		value.SetString(nextString())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if number, ok := nextNumber(); ok {
			value.SetInt(int64(number))
			return
		}
		value.SetInt(int64(7 + offset))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if number, ok := nextNumber(); ok {
			value.SetUint(uint64(number))
			return
		}
		if value.Kind() == reflect.Uint8 {
			value.SetUint(1)
			return
		}
		value.SetUint(uint64(7 + offset))
	case reflect.Float32, reflect.Float64:
		if number, ok := nextNumber(); ok {
			value.SetFloat(number * (1 + noise))
			return
		}
		value.SetFloat((0.75 + 0.125*float64(offset)) * (1 + noise))
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Pointer:
		value.Set(reflect.New(value.Type().Elem()))
		fillDeterministic(value.Elem(), offset, noise, nextString, nextNumber)
	case reflect.Slice:
		slice := reflect.MakeSlice(value.Type(), 1, 1)
		fillDeterministic(slice.Index(0), offset, noise, nextString, nextNumber)
		value.Set(slice)
	case reflect.Map:
		entry := reflect.MakeMap(value.Type())
		key := reflect.New(value.Type().Key()).Elem()
		if key.Kind() == reflect.String {
			key.SetString("feature_delivery")
		} else {
			fillDeterministic(key, offset, noise, nextString, nextNumber)
		}
		elem := reflect.New(value.Type().Elem()).Elem()
		fillDeterministic(elem, offset, noise, nextString, nextNumber)
		entry.SetMapIndex(key, elem)
		value.Set(entry)
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(time.Time{}) {
			value.Set(reflect.ValueOf(clockTestFixedInstant))
			return
		}
		for index := 0; index < value.NumField(); index++ {
			if value.Field(index).CanSet() {
				fillDeterministic(value.Field(index), offset, noise, nextString, nextNumber)
			}
		}
	}
}

func clockTestSubjectID(kind contextfabric.SubjectKind) (canonicalID, rowID string) {
	switch kind {
	case contextfabric.SubjectRepository:
		return "repository:r1", "r1"
	case contextfabric.SubjectTeam:
		return "team:t1", "t1"
	case contextfabric.SubjectProject:
		return "project.v2:linear:P1", "linear:P1"
	case contextfabric.SubjectOrganization:
		return "organization:org-x", "org-x"
	case contractsv1.ContextFabricSubjectPullRequestReview:
		return "pull_request_review.v2:r1:7:review1", "review1"
	case contextfabric.SubjectWorkItem, contextfabric.SubjectDeployment, contractsv1.ContextFabricSubjectCIRun:
		return string(kind) + ".v2:r1:item1", "item1"
	case contextfabric.SubjectPullRequest:
		return "pull_request:r1:7", "r1"
	default:
		return string(kind) + ":x1", "x1"
	}
}

// clockTestRules gives the static string columns of the statements whose row
// keys must line up with the subject the read asked for.
func clockTestRules() []stringRule {
	const day = "2026-09-10"
	return []stringRule{
		{match: "SELECT w.work_item_id, toString(w.repo_id), ifNull(r.repo", values: []string{"item1", "r1", "repo-one"}},
		{match: "SELECT toString(repo_id), toString(day), toInt64(commits_count)", values: []string{"r1", day}},
		{match: "SELECT scope_id, toString(day), toString(severity)", values: []string{"$id", day, "low"}},
		{match: "SELECT scope_id, toString(severity)", values: []string{"$id", "low", day + " 08:30:00", day}},
		{match: "SELECT project_key, scope, scope_id, scope_name, severity", values: []string{"linear:P1", "team", "t1", "Team One", "low", day + " 08:30:00", day}},
		{match: "SELECT project_key, toString(day), toUInt8(isNotNull(max(risk)))", values: []string{"linear:P1", day, "low"}},
		{match: "SELECT concat(p.provider, ':', p.id), ec.has_team", values: []string{"linear:P1", "T1", "Team One", "scope1", "github", day}},
		{match: "SELECT concat(p.provider, ':', p.id), toString(ec.day)", values: []string{"linear:P1", day}},
		{match: "SELECT concat(p.provider, ':', p.id), wm.team_id", values: []string{"linear:P1", "t1"}},
		{match: "SELECT concat(p.provider, ':', p.id), toString(wm.day)", values: []string{"linear:P1", day}},
		{"SELECT w.work_item_id, ifNull(w.status", []string{"item1", "open", "r1", "jira"}, nil},
		{"SELECT w.work_item_id, isNotNull(w.completed_at", []string{"item1", "r1"}, nil},
		{"SELECT w.work_item_id, ifNull(w.title", []string{"item1", "a title", "r1"}, nil},
		{"SELECT concat(p.provider, ':', p.id), count()", []string{"linear:P1"}, []float64{10, 1, 0, 5}},
		{"SELECT d.source_work_item_id, d.target_work_item_id, toString(t.repo_id)", []string{"item2", "item1", "r1", "r1"}, nil},
		{"SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type", []string{"item1", "item2", "parent_of", "r1", "r1"}, nil},
		{"SELECT toString(p.repo_id), p.number", []string{"r1", "open"}, nil},
		{"SELECT r.review_id", []string{"review1", "approved", "r1"}, nil},
		{"SELECT c.run_id", []string{"item1", "success", "r1"}, nil},
		{"SELECT d.deployment_id", []string{"item1", "success", "production", "r1"}, nil},
		{"SELECT toUInt8(win) AS window", []string{"r1"}, []float64{0, 0.5, 3}},
		{"SELECT team_id, ifNull(work_scope_id", []string{"t1", "scope1", day + " 08:30:00"}, nil},
		{"SELECT toString(team_id), toString(toDate(computed_at))", []string{"t1", day}, nil},
		{"SELECT concat(p.provider, ':', p.id), cf.has_team", []string{"linear:P1", "T1", "Team One", "scope1", day + " 08:30:00"}, nil},
		{"SELECT concat(p.provider, ':', p.id), toString(toDate(cf.computed_at))", []string{"linear:P1", day}, nil},
		{"SELECT DISTINCT team_id, repo_key", []string{"t1", "r1"}, nil},
		{"SELECT team_id, work_scope_id, provider, toString(day)", []string{"t1", "scope1", "github", day}, nil},
		{"SELECT toString(team_id), toString(day)", []string{"t1", day}, nil},
		{"SELECT toString(team_id), map_name, toString(as_of_day)", []string{"t1", "map-1", day}, nil},
	}
}

type clockTestCase struct {
	name string
	// interpretationCarriesWindow: the engine hands synthesis the interpretation
	// as the interpreter produced it; only the fact-read copy carries the
	// effective window (factReadQuestion), so a derived window stays out of it.
	interpretationCarriesWindow bool
	requested                   func(now time.Time) *contextfabric.RequestedEvidenceWindow
	effective                   func(now time.Time) *contextfabric.EffectiveEvidenceWindow
}

func clockTestCases() []clockTestCase {
	statedStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	statedEnd := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	return []clockTestCase{
		{
			name: "relative_window",
			requested: func(now time.Time) *contextfabric.RequestedEvidenceWindow {
				start, end := now.UTC().Add(-30*24*time.Hour), now.UTC()
				return &contextfabric.RequestedEvidenceWindow{Start: &start, End: &end, RelativeID: contextfabric.RelativeWindowTrailing30D}
			},
			effective: func(now time.Time) *contextfabric.EffectiveEvidenceWindow {
				start, end := now.UTC().Add(-30*24*time.Hour), now.UTC()
				return &contextfabric.EffectiveEvidenceWindow{
					Start: &start, End: &end, RelativeID: contextfabric.RelativeWindowTrailing30D,
					WindowClass: contextfabric.WindowClassTrendAssessment, Provenance: contextfabric.WindowInferredDefault,
					Confidence: contextfabric.WindowConfidenceHigh,
				}
			},
		},
		{
			name:      "provider_default_window",
			requested: func(time.Time) *contextfabric.RequestedEvidenceWindow { return nil },
			effective: func(time.Time) *contextfabric.EffectiveEvidenceWindow { return nil },
		},
		{
			name:                        "stated_window",
			interpretationCarriesWindow: true,
			requested: func(time.Time) *contextfabric.RequestedEvidenceWindow {
				start, end := statedStart, statedEnd
				return &contextfabric.RequestedEvidenceWindow{Start: &start, End: &end}
			},
			effective: func(time.Time) *contextfabric.EffectiveEvidenceWindow {
				start, end := statedStart, statedEnd
				return &contextfabric.EffectiveEvidenceWindow{
					Start: &start, End: &end,
					WindowClass: contextfabric.WindowClassExplicitWindow, Provenance: contextfabric.WindowQuestionStated,
					Confidence: contextfabric.WindowConfidenceHigh,
				}
			},
		},
	}
}

func clockTestPayload(t *testing.T, provider contextfabric.FactProvider, subject contextfabric.SubjectRef, kind contextfabric.FactKind, tc clockTestCase, now time.Time) ([]byte, int, error) {
	t.Helper()
	restore := devhealthfacts.SetClockForTest(func() time.Time { return now })
	defer restore()
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{Now: func() time.Time { return now }})
	if err != nil {
		return nil, 0, fmt.Errorf("registry: %w", err)
	}
	interpretation := contextfabric.InterpretedQuestion{
		RequestedJudgment: "clock stability",
		TimeContext:       contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	}
	if tc.interpretationCarriesWindow {
		interpretation.TimeContext.EvidenceWindow = tc.requested(now)
	}
	question := interpretation
	question.TimeContext.EvidenceWindow = tc.requested(now)
	bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org-x", RepositoryScopes: []string{"*"}}, contextfabric.CanonicalFactRequest{
		Question:     question,
		Subjects:     []contextfabric.SubjectRef{subject},
		Requirements: []contextfabric.FactRequirement{{Kind: kind, Subjects: []contextfabric.SubjectRef{subject}}},
	})
	if err != nil {
		return nil, 0, fmt.Errorf("ReadFacts: %w", err)
	}
	input := contextfabric.SynthesisInput{
		Request:                 contextfabric.InvestigationRequest{Question: "clock stability"},
		Interpretation:          interpretation,
		Facts:                   bundle,
		EvidenceWindow:          tc.effective(now),
		EvidenceWindowFromClock: tc.name == "relative_window",
	}
	payload, err := synthesisprompt.ClientPayload("org-x", input, 1<<20)
	if err != nil {
		return nil, 0, fmt.Errorf("ClientPayload: %w", err)
	}
	if len(bundle.Facts) == 0 {
		var reasons []string
		for _, detail := range bundle.Coverage.Details {
			reasons = append(reasons, string(detail.SourceState)+": "+detail.Raw)
		}
		return payload, 0, fmt.Errorf("no facts; %s", strings.Join(reasons, "; "))
	}
	return payload, len(bundle.Facts), nil
}

func diffJSON(path string, a, b any, out *[]string) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, fmt.Sprintf("%s: %v != %v", path, a, b))
			return
		}
		keys := map[string]struct{}{}
		for key := range av {
			keys[key] = struct{}{}
		}
		for key := range bv {
			keys[key] = struct{}{}
		}
		sorted := make([]string, 0, len(keys))
		for key := range keys {
			sorted = append(sorted, key)
		}
		sort.Strings(sorted)
		for _, key := range sorted {
			left, lok := av[key]
			right, rok := bv[key]
			switch {
			case !lok:
				*out = append(*out, fmt.Sprintf("%s.%s: <absent> != %v", path, key, right))
			case !rok:
				*out = append(*out, fmt.Sprintf("%s.%s: %v != <absent>", path, key, left))
			default:
				diffJSON(path+"."+key, left, right, out)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s: %v != %v", path, a, b))
			return
		}
		for index := range av {
			diffJSON(fmt.Sprintf("%s[%d]", path, index), av[index], bv[index], out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, fmt.Sprintf("%s: %v != %v", path, a, b))
		}
	}
}

func TestClientInputIsClockIndependentForEveryFactKind(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 123456789, time.UTC)
	clocks := []time.Time{t0, t0.Add(3 * time.Second), t0.Add(7 * time.Minute)}
	providers := devhealthfacts.NewProviders(&universalClient{})
	produced := map[string]bool{}
	refusals := map[string][]string{}
	var kinds, labels []string

	for _, base := range providers {
		capability := base.Capability()
		kind := capability.Kind
		kinds = append(kinds, string(kind))
		for _, subjectKind := range capability.SupportedSubjectKinds {
			canonicalID, rowID := clockTestSubjectID(subjectKind)
			subject := contextfabric.SubjectRef{Kind: subjectKind, CanonicalID: canonicalID, Label: "subject-1"}
			provider := findProvider(t, devhealthfacts.NewProviders(&universalClient{subjectID: rowID, rows: 1, rules: clockTestRules()}), kind)
			for _, tc := range clockTestCases() {
				label := fmt.Sprintf("%s/%s/%s", kind, subjectKind, tc.name)
				labels = append(labels, label)
				var first []byte
				for index, now := range clocks {
					payload, facts, err := clockTestPayload(t, provider, subject, kind, tc, now)
					if err != nil && !strings.HasPrefix(err.Error(), "no facts") {
						refusals[label] = append(refusals[label], err.Error())
						break
					}
					if err != nil && index == 0 {
						refusals[label] = append(refusals[label], err.Error())
					}
					if facts > 0 {
						produced[label] = true
					}
					if index == 0 {
						first = payload
						continue
					}
					if bytes.Equal(first, payload) {
						continue
					}
					var left, right any
					if err := json.Unmarshal(first, &left); err != nil {
						t.Fatalf("%s: decode: %v", label, err)
					}
					if err := json.Unmarshal(payload, &right); err != nil {
						t.Fatalf("%s: decode: %v", label, err)
					}
					var diffs []string
					diffJSON("$", left, right, &diffs)
					t.Errorf("%s: client input differs between clock %s and %s (%d facts):\n  %s", label, clocks[0].Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), facts, strings.Join(diffs, "\n  "))
				}
			}
		}
	}

	t.Logf("kinds enumerated: %s", strings.Join(kinds, ", "))
	for _, label := range labels {
		if !produced[label] {
			t.Errorf("%s produced no fact, so the clock is not tested there; refusals: %s", label, strings.Join(refusals[label], " | "))
		}
	}
}
