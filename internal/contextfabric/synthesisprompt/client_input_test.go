package synthesisprompt

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const (
	decisionObservation  = "omitted: the time the turn read a source"
	decisionClockWritten = "omitted only when the turn's clock wrote it; a stated instant is a fact"
	decisionFact         = "kept: a fact about the data or a stated bound"
)

// timeValueDecisions names every time-typed path of the model input and what
// the client input does with it. A new time-typed field fails
// TestEveryTimeValueOfTheModelInputHasADecision until it is decided here.
var timeValueDecisions = map[string]string{
	"coverage.sources[].observed_at":                    decisionObservation,
	"interpretation.time_context.as_of":                 decisionClockWritten,
	"interpretation.time_context.start":                 decisionClockWritten,
	"interpretation.time_context.end":                   decisionClockWritten,
	"interpretation.time_context.evidence_window.start": decisionFact,
	"interpretation.time_context.evidence_window.end":   decisionFact,
	"paths[].edges[].observed_at":                       decisionFact,
	"paths[].edges[].valid_from":                        decisionFact,
	"paths[].edges[].valid_to":                          decisionFact,
	"canonical_facts[].observed_at":                     decisionFact,
	"canonical_facts[].event_at":                        decisionFact,
}

var (
	timeType = reflect.TypeOf(time.Time{})
	rawType  = reflect.TypeOf(json.RawMessage{})
)

// timeTypedPaths walks a type by its JSON names. It reports every time-typed
// path, and every opaque path (raw JSON, interface, a map key that is not a
// string) because a time could hide there unseen.
func timeTypedPaths(t reflect.Type, path string, depth map[reflect.Type]int, times, opaque *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == timeType:
		*times = append(*times, path)
		return
	case t == rawType:
		*opaque = append(*opaque, path)
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		if depth[t] > 1 {
			return
		}
		depth[t]++
		defer func() { depth[t]-- }()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" && field.Anonymous {
				timeTypedPaths(field.Type, path, depth, times, opaque)
				continue
			}
			if name == "" {
				name = field.Name
			}
			next := name
			if path != "" {
				next = path + "." + name
			}
			timeTypedPaths(field.Type, next, depth, times, opaque)
		}
	case reflect.Slice, reflect.Array:
		timeTypedPaths(t.Elem(), path+"[]", depth, times, opaque)
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			*opaque = append(*opaque, path)
			return
		}
		timeTypedPaths(t.Elem(), path+"{}", depth, times, opaque)
	case reflect.Interface:
		*opaque = append(*opaque, path)
	}
}

func TestEveryTimeValueOfTheModelInputHasADecision(t *testing.T) {
	var times, opaque []string
	timeTypedPaths(reflect.TypeOf(Input{}), "", map[reflect.Type]int{}, &times, &opaque)
	if len(opaque) > 0 {
		t.Fatalf("the model input has opaque paths a time could hide in: %v", opaque)
	}
	walked := map[string]bool{}
	for _, path := range times {
		walked[path] = true
		if _, decided := timeValueDecisions[path]; !decided {
			t.Errorf("time-typed path %q of the model input has no decision: decide it in timeValueDecisions and, when it is an observation, in clientInputObservationPaths", path)
		}
	}
	for path := range timeValueDecisions {
		if !walked[path] {
			t.Errorf("decided path %q is not a time-typed path of the model input", path)
		}
	}
	var omitted []string
	for path, decision := range timeValueDecisions {
		if decision != decisionFact {
			omitted = append(omitted, path)
		}
	}
	sort.Strings(omitted)
	listed := ClientInputObservationPaths()
	sort.Strings(listed)
	if !slices.Equal(omitted, listed) {
		t.Fatalf("clientInputObservationPaths = %v, want the paths decided as omitted %v", listed, omitted)
	}
}

func instant(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		panic(err)
	}
	return &parsed
}

// populatedInput sets a value at every time-typed path of the model input.
func populatedInput(timeContext contextfabric.TimeContext, readTimeClamps []time.Time) contextfabric.SynthesisInput {
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_quartz_ledger", Label: "Quartz Ledger"}
	item := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_quartz_gate", Label: "Quartz gate"}
	timeContext.EvidenceWindow = &contextfabric.RequestedEvidenceWindow{Start: instant("2026-07-01T00:00:00Z"), End: instant("2026-07-31T00:00:00Z")}
	return contextfabric.SynthesisInput{
		Request:        contextfabric.InvestigationRequest{Question: "What blocks the quartz ledger?"},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject, TimeContext: timeContext},
		Graph: contextfabric.GraphContext{
			Resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{project}},
			Paths: []contextfabric.RelationshipPath{{
				PathID: "path_quartz_0001", Nodes: []contextfabric.SubjectRef{project, item},
				Edges: []contextfabric.RelationshipEdge{{
					Type: "BLOCKS", From: project, To: item, EvidenceRefIDs: []string{"evidence_quartz_0001"},
					ObservedAt: instant("2026-08-01T10:00:00Z"), ValidFrom: instant("2026-07-15T00:00:00Z"), ValidTo: instant("2026-09-01T00:00:00Z"),
				}},
				EvidenceRefIDs: []string{"evidence_quartz_0001"},
			}},
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{
				{Source: "context-fabric:graph", State: contextfabric.SourceAvailable, ObservedAt: instant("2026-08-12T12:00:01.123456789Z")},
			}},
		},
		Facts: contextfabric.CanonicalFactBundle{
			Facts: []contextfabric.CanonicalFact{{
				Kind: contextfabric.FactStatus, Subject: project, Fields: map[string]contextfabric.FactValue{"status": contextfabric.StringFactValue("blocked")},
				ObservedAt: instant("2026-08-11T09:00:00Z"), EventAt: instant("2026-08-10T08:00:00Z"),
				EvidenceRefIDs: []string{"evidence_quartz_0002"}, SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1",
			}},
			Coverage: contextfabric.Coverage{Sources: []contextfabric.SourceObservation{
				{Source: "canonical_fact:status", State: contextfabric.SourceAvailable, ObservedAt: instant("2026-08-12T12:00:02Z"), Watermark: "2026-08-12T06:00:00Z"},
			}},
		},
		ReadTimeClamps: readTimeClamps,
	}
}

// presentTimePaths decodes encoded JSON and returns, by the walk's path
// spelling, every path whose value is present.
func presentTimePaths(t *testing.T, encoded []byte) map[string]bool {
	t.Helper()
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	present := map[string]bool{}
	var walk func(value any, path string)
	walk = func(value any, path string) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				next := key
				if path != "" {
					next = path + "." + key
				}
				walk(child, next)
			}
		case []any:
			for _, child := range typed {
				walk(child, path+"[]")
			}
		default:
			if _, decided := timeValueDecisions[path]; decided {
				present[path] = true
			}
		}
	}
	walk(decoded, "")
	return present
}

func TestTheClientInputLeavesOutOnlyTheObservationValues(t *testing.T) {
	clamp := *instant("2026-08-12T12:00:00.5Z")
	cases := map[string]struct {
		timeContext contextfabric.TimeContext
		clamps      []time.Time
		omitted     []string
	}{
		"a range whose end the clock wrote": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: &clamp},
			clamps:      []time.Time{clamp},
			omitted:     []string{"coverage.sources[].observed_at", "interpretation.time_context.end"},
		},
		"a range wholly clamped": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &clamp, End: &clamp},
			clamps:      []time.Time{clamp},
			omitted:     []string{"coverage.sources[].observed_at", "interpretation.time_context.end", "interpretation.time_context.start"},
		},
		"an as-of the clock wrote": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &clamp},
			clamps:      []time.Time{clamp},
			omitted:     []string{"coverage.sources[].observed_at", "interpretation.time_context.as_of"},
		},
		"a stated range": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-10T00:00:00Z")},
			clamps:      []time.Time{clamp},
			omitted:     []string{"coverage.sources[].observed_at"},
		},
		"a stated as-of with no clamp": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &clamp},
			omitted:     []string{"coverage.sources[].observed_at"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := populatedInput(tc.timeContext, tc.clamps)
			client, err := ClientPayload("org-quartz", input, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			server, err := UserPayload("org-quartz", input, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			inClient, inServer := presentTimePaths(t, client), presentTimePaths(t, server)
			var missing []string
			for path := range inServer {
				if !inClient[path] {
					missing = append(missing, path)
				}
			}
			sort.Strings(missing)
			want := append([]string(nil), tc.omitted...)
			sort.Strings(want)
			if !slices.Equal(missing, want) {
				t.Fatalf("client input left out %v, want %v", missing, want)
			}
			for path := range timeValueDecisions {
				if timeValueDecisions[path] == decisionFact && !inClient[path] {
					t.Errorf("fact path %q is missing from the client input", path)
				}
			}
			if !bytes.Contains(client, []byte(`"watermark":"2026-08-12T06:00:00Z"`)) {
				t.Fatal("the source watermark is missing from the client input")
			}
			if !inServer["coverage.sources[].observed_at"] {
				t.Fatal("the service's own model input lost the source observation time")
			}
		})
	}
}

func TestTheServiceModelInputIsUnchanged(t *testing.T) {
	clamp := *instant("2026-08-12T12:00:00.5Z")
	input := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: &clamp}, []time.Time{clamp})
	got, err := UserPayload("org-quartz", input, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(InputFromDomain("org-quartz", input))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the service model input differs from InputFromDomain:\n%s\n%s", got, want)
	}
}

func TestTwoReadsOfTheSameFactsGiveOneClientInput(t *testing.T) {
	first := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-12T12:00:00.5Z")}, []time.Time{*instant("2026-08-12T12:00:00.5Z")})
	second := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-12T12:07:30Z")}, []time.Time{*instant("2026-08-12T12:07:30Z")})
	second.Graph.Coverage.Sources[0].ObservedAt = instant("2026-08-12T12:07:31.2Z")
	second.Facts.Coverage.Sources[0].ObservedAt = instant("2026-08-12T12:07:32Z")
	a, err := ClientPayload("org-quartz", first, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ClientPayload("org-quartz", second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("two reads of the same facts gave two client inputs:\n%s\n%s", a, b)
	}
	second.Facts.Facts[0].Fields["status"] = contextfabric.StringFactValue("open")
	c, err := ClientPayload("org-quartz", second, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("a changed fact value gave the same client input")
	}
}

func TestClientInputDoesNotChangeTheDomainInput(t *testing.T) {
	clamp := *instant("2026-08-12T12:00:00.5Z")
	input := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: &clamp}, []time.Time{clamp})
	ClientInputFromDomain("org-quartz", input)
	if input.Graph.Coverage.Sources[0].ObservedAt == nil || input.Facts.Coverage.Sources[0].ObservedAt == nil || input.Interpretation.TimeContext.End == nil {
		t.Fatal("building the client input cleared a value of the domain input")
	}
}
