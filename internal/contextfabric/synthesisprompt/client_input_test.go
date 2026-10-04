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
	"evidence_window.start":                             decisionClockWritten,
	"evidence_window.end":                               decisionClockWritten,
}

// stringWindowEchoPaths are the string-typed paths of the closed list: a type
// walk cannot see them, the executed clock tests over the real producers do.
var stringWindowEchoPaths = []string{
	"canonical_facts[].fields." + contextfabric.FactFieldWindowStart,
	"canonical_facts[].fields." + contextfabric.FactFieldWindowEnd,
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
	timeTypedPaths(reflect.TypeOf(ClientInput{}), "", map[reflect.Type]int{}, &times, &opaque)
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
	omitted = append(omitted, stringWindowEchoPaths...)
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
func populatedInput(timeContext contextfabric.TimeContext, clamp contextfabric.ReadTimeClamp) contextfabric.SynthesisInput {
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
		ReadTimeClamp: clamp,
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
	at := *instant("2026-08-12T12:00:00.5Z")
	cases := map[string]struct {
		timeContext contextfabric.TimeContext
		clamp       contextfabric.ReadTimeClamp
		omitted     []string
	}{
		"a range whose end the clock wrote": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: &at},
			clamp:       contextfabric.ReadTimeClamp{At: at, End: true},
			omitted:     []string{"coverage.sources[].observed_at", "interpretation.time_context.end"},
		},
		"a range wholly clamped": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &at, End: &at},
			clamp:       contextfabric.ReadTimeClamp{At: at, Start: true, End: true},
			omitted:     []string{"coverage.sources[].observed_at", "interpretation.time_context.end", "interpretation.time_context.start"},
		},
		"an as-of the clock wrote": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &at},
			clamp:       contextfabric.ReadTimeClamp{At: at, AsOf: true},
			omitted:     []string{"coverage.sources[].observed_at", "interpretation.time_context.as_of"},
		},
		"a stated start equal to the instant the clock wrote into the end": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &at, End: &at},
			clamp:       contextfabric.ReadTimeClamp{At: at, End: true},
			omitted:     []string{"coverage.sources[].observed_at", "interpretation.time_context.end"},
		},
		"a stated range": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-10T00:00:00Z")},
			clamp:       contextfabric.ReadTimeClamp{At: at, End: true},
			omitted:     []string{"coverage.sources[].observed_at"},
		},
		"a stated as-of with no clamp": {
			timeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &at},
			omitted:     []string{"coverage.sources[].observed_at"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := populatedInput(tc.timeContext, tc.clamp)
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
	at := *instant("2026-08-12T12:00:00.5Z")
	input := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: &at}, contextfabric.ReadTimeClamp{At: at, End: true})
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
	first := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-12T12:00:00.5Z")}, contextfabric.ReadTimeClamp{At: *instant("2026-08-12T12:00:00.5Z"), End: true})
	second := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-12T12:07:30Z")}, contextfabric.ReadTimeClamp{At: *instant("2026-08-12T12:07:30Z"), End: true})
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
	at := *instant("2026-08-12T12:00:00.5Z")
	input := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: instant("2026-08-01T00:00:00Z"), End: &at}, contextfabric.ReadTimeClamp{At: at, End: true})
	ClientInputFromDomain("org-quartz", input)
	if input.Graph.Coverage.Sources[0].ObservedAt == nil || input.Facts.Coverage.Sources[0].ObservedAt == nil || input.Interpretation.TimeContext.End == nil {
		t.Fatal("building the client input cleared a value of the domain input")
	}
}

// populateTimes sets every time-typed value reachable in v to at, allocating
// only the pointers, slices and maps that lead to one.
func populateTimes(v reflect.Value, at time.Time, depth map[reflect.Type]int) {
	t := v.Type()
	if !reachesTime(t, map[reflect.Type]bool{}) || depth[t] > 1 {
		return
	}
	depth[t]++
	defer func() { depth[t]-- }()
	switch {
	case t == timeType:
		v.Set(reflect.ValueOf(at))
		return
	}
	switch t.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(t.Elem()))
		}
		populateTimes(v.Elem(), at, depth)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.IsExported() && field.Tag.Get("json") != "-" {
				populateTimes(v.Field(i), at, depth)
			}
		}
	case reflect.Slice:
		if v.Len() == 0 {
			v.Set(reflect.MakeSlice(t, 1, 1))
		}
		for i := 0; i < v.Len(); i++ {
			populateTimes(v.Index(i), at, depth)
		}
	case reflect.Map:
		element := reflect.New(t.Elem()).Elem()
		populateTimes(element, at, depth)
		if v.IsNil() {
			v.Set(reflect.MakeMap(t))
		}
		v.SetMapIndex(reflect.ValueOf("key").Convert(t.Key()), element)
	}
}

func reachesTime(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == timeType {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return reachesTime(t.Elem(), seen)
	case reflect.Map:
		return t.Key().Kind() == reflect.String && reachesTime(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.IsExported() && field.Tag.Get("json") != "-" && reachesTime(field.Type, seen) {
				return true
			}
		}
	}
	return false
}

func TestEveryTimeValueTheClockWroteIsLeftOutAndEveryFactStays(t *testing.T) {
	at := time.Date(2031, 2, 3, 4, 5, 6, 789000000, time.UTC)
	var payload Input
	populateTimes(reflect.ValueOf(&payload).Elem(), at, map[reflect.Type]int{})
	client, err := json.Marshal(canonicalClientInput(payload, clientWindow{}, contextfabric.ReadTimeClamp{At: at, AsOf: true, Start: true, End: true}))
	if err != nil {
		t.Fatal(err)
	}
	server, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	stamp := at.Format(time.RFC3339Nano)
	inServer, inClient := pathsHolding(t, server, stamp), pathsHolding(t, client, stamp)
	for path, decision := range timeValueDecisions {
		if strings.HasPrefix(path, "evidence_window.") {
			continue
		}
		if !inServer[path] {
			t.Errorf("decided path %q was not populated, so this test does not cover it", path)
			continue
		}
		if omitted := !inClient[path]; omitted != (decision != decisionFact) {
			t.Errorf("path %q decided %q: left out of the client input = %v", path, decision, omitted)
		}
	}
	for path := range inServer {
		if _, decided := timeValueDecisions[path]; !decided {
			t.Errorf("populated time path %q has no decision", path)
		}
	}
}

// pathsHolding returns every path of encoded whose value is the string want.
func pathsHolding(t *testing.T, encoded []byte, want string) map[string]bool {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
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
		case string:
			if typed == want {
				found[path] = true
			}
		}
	}
	walk(decoded, "")
	return found
}

func TestTheClientInputNamesTheWindowTheFactsWereReadOver(t *testing.T) {
	at := *instant("2026-08-12T12:00:00.5Z")
	start, end := instant("2026-07-13T12:00:00.5Z"), &at
	stated := &contextfabric.EffectiveEvidenceWindow{Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-10T00:00:00Z"), WindowClass: "recent_activity_lookup"}
	relative := &contextfabric.EffectiveEvidenceWindow{Start: start, End: end, RelativeID: "trailing_30d", WindowClass: "recent_activity_lookup"}
	cases := map[string]struct {
		window    *contextfabric.EffectiveEvidenceWindow
		fromClock bool
		clamp     contextfabric.ReadTimeClamp
		want      string
	}{
		"no window": {nil, false, contextfabric.ReadTimeClamp{}, ""},
		"a relative window resolved from the clock": {relative, true, contextfabric.ReadTimeClamp{}, `{"relative_id":"trailing_30d","window_class":"recent_activity_lookup"}`},
		"a frozen relative window":                  {relative, false, contextfabric.ReadTimeClamp{}, `{"relative_id":"trailing_30d","window_class":"recent_activity_lookup","start":"2026-07-13T12:00:00.5Z","end":"2026-08-12T12:00:00.5Z"}`},
		"all time":                                  {&contextfabric.EffectiveEvidenceWindow{RelativeID: "all_time", WindowClass: "historical_analysis"}, false, contextfabric.ReadTimeClamp{}, `{"relative_id":"all_time","window_class":"historical_analysis"}`},
		"a stated window":                           {stated, false, contextfabric.ReadTimeClamp{At: at, End: true}, `{"window_class":"recent_activity_lookup","start":"2026-08-01T00:00:00Z","end":"2026-08-10T00:00:00Z"}`},
		"a stated window whose end the clock wrote": {&contextfabric.EffectiveEvidenceWindow{Start: instant("2026-08-01T00:00:00Z"), End: &at, WindowClass: "recent_activity_lookup"}, false, contextfabric.ReadTimeClamp{At: at, WindowEnd: true},
			`{"window_class":"recent_activity_lookup","start":"2026-08-01T00:00:00Z"}`},
		"a stated start equal to the instant the clock wrote into the end": {&contextfabric.EffectiveEvidenceWindow{Start: &at, End: &at}, false, contextfabric.ReadTimeClamp{At: at, WindowEnd: true}, `{"start":"2026-08-12T12:00:00.5Z"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, tc.clamp)
			input.EvidenceWindow, input.EvidenceWindowFromClock = tc.window, tc.fromClock
			client, err := ClientPayload("org-quartz", input, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(client, &decoded); err != nil {
				t.Fatal(err)
			}
			if got := string(decoded["evidence_window"]); got != tc.want {
				t.Fatalf("evidence_window = %s, want %s", got, tc.want)
			}
			server, err := UserPayload("org-quartz", input, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			var serverKeys map[string]json.RawMessage
			if err := json.Unmarshal(server, &serverKeys); err != nil {
				t.Fatal(err)
			}
			if _, present := serverKeys["evidence_window"]; present {
				t.Fatalf("the service model input carries the window descriptor: %s", server)
			}
		})
	}
}

func windowedFact(basis, start, end string) contextfabric.CanonicalFact {
	fields := map[string]contextfabric.FactValue{"incident_count": contextfabric.IntegerFactValue(3)}
	if basis != "" {
		fields[contextfabric.FactFieldWindowBasis] = contextfabric.StringFactValue(basis)
	}
	if start != "" {
		fields[contextfabric.FactFieldWindowStart] = contextfabric.StringFactValue(start)
	}
	if end != "" {
		fields[contextfabric.FactFieldWindowEnd] = contextfabric.StringFactValue(end)
	}
	return contextfabric.CanonicalFact{
		Kind: contextfabric.FactIncidents, Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_quartz", Label: "Quartz"},
		Fields: fields, EvidenceRefIDs: []string{"evidence_quartz_0003"}, SourceState: contextfabric.SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

func TestTheClientInputLeavesOutOnlyTheWindowEchoesTheClockWrote(t *testing.T) {
	at := *instant("2026-08-12T12:00:00.5Z")
	relative := &contextfabric.EffectiveEvidenceWindow{Start: instant("2026-07-13T12:00:00.5Z"), End: &at, RelativeID: "trailing_30d"}
	stated := &contextfabric.EffectiveEvidenceWindow{Start: instant("2026-08-01T00:00:00Z"), End: instant("2026-08-10T00:00:00Z")}
	cases := map[string]struct {
		fact      contextfabric.CanonicalFact
		window    *contextfabric.EffectiveEvidenceWindow
		fromClock bool
		clamp     contextfabric.ReadTimeClamp
		wantStart bool
		wantEnd   bool
	}{
		"the provider's own default window":              {windowedFact("default_trailing", "2026-05-14T12:00:00Z", "2026-08-12T12:00:00Z"), nil, false, contextfabric.ReadTimeClamp{}, false, false},
		"the bounds of a relative window from the clock": {windowedFact("evidence_window", "2026-07-13T12:00:00Z", "2026-08-12T12:00:00Z"), relative, true, contextfabric.ReadTimeClamp{}, false, false},
		"the bounds of a frozen relative window":         {windowedFact("evidence_window", "2026-07-13T12:00:00Z", "2026-08-12T12:00:00Z"), relative, false, contextfabric.ReadTimeClamp{}, true, true},
		"the bounds of a stated window":                  {windowedFact("evidence_window", "2026-08-01T00:00:00Z", "2026-08-10T00:00:00Z"), stated, false, contextfabric.ReadTimeClamp{}, true, true},
		"an end the clamp wrote":                         {windowedFact("range", "2026-08-01T00:00:00Z", "2026-08-12T12:00:00Z"), nil, false, contextfabric.ReadTimeClamp{At: at, End: true}, true, false},
		"stored window fields with no basis":             {windowedFact("", "2026-08-12", "2026-08-12"), relative, true, contextfabric.ReadTimeClamp{}, true, true},
		"a stated bound under a relative window":         {windowedFact("evidence_window", "2026-08-01T00:00:00Z", "2026-08-10T00:00:00Z"), relative, true, contextfabric.ReadTimeClamp{}, true, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			input := populatedInput(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, tc.clamp)
			input.EvidenceWindow, input.EvidenceWindowFromClock = tc.window, tc.fromClock
			input.Facts.Facts = []contextfabric.CanonicalFact{tc.fact}
			client := ClientInputFromDomain("org-quartz", input)
			fields := client.Facts[0].Fields
			_, hasStart := fields[contextfabric.FactFieldWindowStart]
			_, hasEnd := fields[contextfabric.FactFieldWindowEnd]
			if hasStart != tc.wantStart || hasEnd != tc.wantEnd {
				t.Fatalf("window_start kept = %v, window_end kept = %v, want %v / %v", hasStart, hasEnd, tc.wantStart, tc.wantEnd)
			}
			if _, kept := fields["incident_count"]; !kept {
				t.Fatal("a fact value was left out")
			}
			if len(input.Facts.Facts[0].Fields) != len(tc.fact.Fields) {
				t.Fatal("building the client input changed the domain fact")
			}
		})
	}
}
