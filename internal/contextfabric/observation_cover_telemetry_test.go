package contextfabric

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The observability bar: from the trace alone a reader must be able to rebuild
// the decision graph -- pre-entry, pre-decision, decision + reason,
// post-decision -- at Info, with values.
//
// This asserts the EMITTED LINE through the REAL slog handler at PRODUCTION
// LEVEL, driven by the production function, not a hand-called formatter. Every
// value asserted is non-trivial: a field checked at its zero value would pin
// nothing, and the whole point of this line is the DELTA between a kind count
// and a cover.
//
// Not parallel: it installs a process-wide default logger and restores it.
func TestTheObservationCoverDecisionIsEmittedAtInfoWithValues(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// The collapse case, at real declared values: deficiencies and health at
	// team are ONE observation, so two served kinds cover one -- and a
	// `corroborated` threshold of 2 is NOT met by them.
	requirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "principal_drivers",
		Obligation:  "drivers",
		Subject:     contractsv1.ContextFabricSubjectKind(SubjectTeam),
	}
	evidence := readEvidence{
		Observed:      2,
		Served:        2,
		ObservedKinds: []FactKind{FactOperationalDeficiencies, FactHealth},
		ServedKinds:   []FactKind{FactOperationalDeficiencies, FactHealth},
	}
	populations := readPopulationEvidence{assignment: teamAssignment()}

	readRequirementOutcomeRow(requirement, 2, evidence, populations)

	var line map[string]any
	found := false
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		var candidate map[string]any
		if err := json.Unmarshal(raw, &candidate); err != nil {
			continue
		}
		if candidate["msg"] == "context fabric observation cover" {
			line, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatalf("no \"context fabric observation cover\" line reached the handler; the cover decision is unobservable.\ncaptured: %s", buf.String())
	}
	if got := line["level"]; got != "INFO" {
		t.Fatalf("level = %v, want INFO -- a decision an operator cannot see at production level is not observable", got)
	}

	// THE DELTA IS THE ASSERTION. served_kinds=2 with served_cover=1 is the
	// whole finding this line exists to make visible; asserting either alone
	// would pass against a line that reported only one of them.
	for field, want := range map[string]float64{
		"threshold":              2,
		"observed_kinds":         2,
		"served_kinds":           2,
		"observed_cover":         1,
		"served_cover":           1,
		"collapsed_observations": 1,
		"tainted_observations":   0,
		"declared":               2,
	} {
		got, ok := line[field].(float64)
		if !ok {
			t.Fatalf("%s missing from the emitted line (present fields: %v)", field, keysOf(line))
		}
		if got != want {
			t.Fatalf("%s = %v, want %v", field, got, want)
		}
	}
	if line["meets_threshold"] != false {
		t.Fatalf("meets_threshold = %v, want false -- two kinds that are one observation must not satisfy a corroborated threshold", line["meets_threshold"])
	}
	if line["declared_raised_to_standard"] != true {
		t.Fatalf("declared_raised_to_standard = %v, want true -- the cover is 1 and the standard demands 2, so the row must publish the standard's demand", line["declared_raised_to_standard"])
	}
	if line["subject_kind"] != string(SubjectTeam) {
		t.Fatalf("subject_kind = %v, want %q", line["subject_kind"], SubjectTeam)
	}

	// THE DISCRIMINATING CONTROL. Without it every assertion above would also
	// pass against a line that hard-coded these numbers. Two INDEPENDENT
	// kinds at the same subject kind must emit cover 2, not cover 1.
	buf.Reset()
	independent := readEvidence{
		Observed: 2, Served: 2,
		ObservedKinds: []FactKind{FactHealth, FactFlow},
		ServedKinds:   []FactKind{FactHealth, FactFlow},
	}
	readRequirementOutcomeRow(requirement, 2, independent, populations)
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var candidate map[string]any
		if json.Unmarshal(raw, &candidate) != nil || candidate["msg"] != "context fabric observation cover" {
			continue
		}
		if candidate["served_cover"] != float64(2) {
			t.Fatalf("control: served_cover = %v for two INDEPENDENT kinds, want 2", candidate["served_cover"])
		}
		if candidate["collapsed_observations"] != float64(0) {
			t.Fatalf("control: collapsed_observations = %v, want 0", candidate["collapsed_observations"])
		}
		if candidate["meets_threshold"] != true {
			t.Fatalf("control: meets_threshold = %v, want true", candidate["meets_threshold"])
		}
		return
	}
	t.Fatal("control emitted no cover line")
}

// TestTheEmittedServedCoverIsNotTheObservedCover is the pin for a SURVIVOR the
// first version of the certification above could not kill.
//
// That fixture had nothing lost, so observed_cover and served_cover were both
// 1 -- and an emitter that published the OBSERVED cover under the
// `served_cover` key passed every assertion in it. The two numbers answer
// different questions ("how many observations were in play" vs "how many were
// actually served"), and a line that conflates them makes the shortfall the
// row publishes unexplainable from the trace, which is the whole failure this
// telemetry exists to prevent.
//
// The fixture therefore forces them APART: three independent observations
// observed, one served.
func TestTheEmittedServedCoverIsNotTheObservedCover(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	requirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "principal_drivers",
		Obligation:  "drivers",
		Subject:     contractsv1.ContextFabricSubjectKind(SubjectTeam),
	}
	// health{risk}, flow{throughput}, metrics{sustainability} at team are three
	// INDEPENDENT observations. Only health served, so flow's and metrics'
	// observations are tainted and health's `risk` survives.
	evidence := readEvidence{
		Observed:      3,
		Served:        1,
		Narrowed:      2,
		ObservedKinds: []FactKind{FactHealth, FactFlow, FactMetrics},
		ServedKinds:   []FactKind{FactHealth},
	}
	readRequirementOutcomeRow(requirement, 2, evidence, readPopulationEvidence{assignment: teamAssignment()})

	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var line map[string]any
		if json.Unmarshal(raw, &line) != nil || line["msg"] != "context fabric observation cover" {
			continue
		}
		observed, served := line["observed_cover"], line["served_cover"]
		if observed != float64(3) {
			t.Fatalf("observed_cover = %v, want 3 -- three independent observations were in play", observed)
		}
		if served != float64(1) {
			t.Fatalf("served_cover = %v, want 1 -- only health's observation was served", served)
		}
		if observed == served {
			t.Fatal("observed_cover == served_cover on a fixture built to separate them; an emitter publishing one under the other's key would be invisible")
		}
		if line["tainted_observations"] != float64(2) {
			t.Fatalf("tainted_observations = %v, want 2 -- flow's and metrics' observations were lost", line["tainted_observations"])
		}
		return
	}
	t.Fatal("no cover line reached the handler")
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
