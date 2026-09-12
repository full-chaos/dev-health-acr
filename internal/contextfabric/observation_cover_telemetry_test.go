package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The observability bar: from the trace alone a reader must be able to rebuild
// the decision graph -- pre-entry, pre-decision, decision + reason,
// post-decision -- at Info, with values.
//
// THESE TESTS DRIVE THROUGH THE PORT, not through slog.SetDefault(). The
// production line used to reach ONLY slog.Default() -- Go's process-wide
// fallback logger, which is a TEXT handler on stderr at a fixed level in this
// service and never the JSON stream cmd/acr-api/main.go actually builds and
// injects as the engine's telemetry logger. A test that installed its own
// default handler proved the line was EMITTABLE, never that it reached the
// service's own configured stream -- which is exactly the defect. So every
// test here builds a SlogEngineTelemetry around a logger it owns and asserts
// the emitted line from that, the same convention this package's other
// telemetry tests already use.
//
// The pure evaluator, readRequirementOutcomeRow, no longer does any I/O at
// all: its third return is the ReadRequirementObservationCoverEvent value,
// and these tests hand that value to the production sink themselves.

func TestTheObservationCoverDecisionIsEmittedAtInfoWithValues(t *testing.T) {
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

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

	_, ok, cover := readRequirementOutcomeRow(requirement, 2, evidence, populations)
	if !ok || cover == nil {
		t.Fatalf("readRequirementOutcomeRow returned ok=%v cover=%v; the fixture built a row that must both serve and carry a cover diagnostic", ok, cover)
	}
	telemetry.RecordReadRequirementObservationCover(context.Background(), storage.Principal{}, *cover)

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
	// `pass` and `served` are asserted on the EMITTED LINE, not only on the
	// recorded event. The hosted battery dropped `"pass"` from the emitter and
	// NOTHING failed: the per-pass pin reads the telemetry double, so the field
	// could vanish from the log while every test stayed green. An emitted line
	// is the artefact an operator reads; the event is not.
	if _, ok := line["pass"]; !ok {
		t.Fatalf("the emitted line carries no `pass` field (present: %v) -- the pin on the recorded event cannot see a field dropped from the emitter", keysOf(line))
	}
	if _, ok := line["served"]; !ok {
		t.Fatalf("the emitted line carries no `served` field (present: %v)", keysOf(line))
	}
	for field, want := range map[string]float64{
		"pass":                   0,
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
	// requirement/obligation: the row's own identity, so a reader can tell
	// WHICH cell this decision is about without joining against another line.
	if line["requirement"] != requirement.Requirement {
		t.Fatalf("requirement = %v, want %q", line["requirement"], requirement.Requirement)
	}
	if line["obligation"] != requirement.Obligation {
		t.Fatalf("obligation = %v, want %q", line["obligation"], requirement.Obligation)
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
	_, ok, independentCover := readRequirementOutcomeRow(requirement, 2, independent, populations)
	if !ok || independentCover == nil {
		t.Fatalf("readRequirementOutcomeRow returned ok=%v cover=%v for the control fixture", ok, independentCover)
	}
	telemetry.RecordReadRequirementObservationCover(context.Background(), storage.Principal{}, *independentCover)
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
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))

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
	_, ok, cover := readRequirementOutcomeRow(requirement, 2, evidence, readPopulationEvidence{assignment: teamAssignment()})
	if !ok || cover == nil {
		t.Fatalf("readRequirementOutcomeRow returned ok=%v cover=%v", ok, cover)
	}
	telemetry.RecordReadRequirementObservationCover(context.Background(), storage.Principal{}, *cover)

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
		// ZERO, not two -- and the earlier expectation of 2 was this test
		// pinning a defect. tainted_observations counts observations the
		// mixed-state rule actually EXCLUDED FROM THE SERVED COVER. health
		// stands on `risk`; the lost flow and metrics stand on
		// `capacity_from_throughput` and `sustainability`, neither of which is
		// `risk`, so excluding them removed nothing and the honest count is 0.
		// The old value came from counting every lost key regardless of whether
		// it intersected anything served -- an adversarial round named it, and
		// this assertion had frozen it in place, which is the failure mode where
		// a green test makes a gap look covered.
		if line["tainted_observations"] != float64(0) {
			t.Fatalf("tainted_observations = %v, want 0 -- neither lost observation was one the served cover stood on", line["tainted_observations"])
		}
		return
	}
	t.Fatal("no cover line reached the handler")
}

// TestTheObservationCoverLineReachesTheEnginesConfiguredLoggerNotTheProcessDefault
// is the regression test for the defect itself, not for the event's fields.
//
// It deliberately leaves slog.Default() pointing at a logger of its own that
// is NOT the engine's, drives the real production path (Engine.finalizeResult
// followed by Engine.emit -- finalizeResult only APPENDS the event onto the
// pending telemetry now, so emit is the call that actually publishes it, same
// as every other deferred event on assemblyTelemetry), with a telemetry sink
// built around a SECOND logger this test also owns, and asserts the line
// lands in the ENGINE's sink and never in the process default. Before this
// fix, recordObservationCoverDecision called slog.Default() directly -- the
// exact call this test would have caught, because the engine's own sink would
// have stayed empty while the process default (proven live on the rig to be a
// text handler on stderr, not this service's JSON stream) received it
// instead.
func TestTheObservationCoverLineReachesTheEnginesConfiguredLoggerNotTheProcessDefault(t *testing.T) {
	var defaultBuf, engineBuf bytes.Buffer
	previousDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&defaultBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previousDefault) })

	engine := &Engine{
		requirements: registryDeriver{},
		telemetry:    NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&engineBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))),
	}

	frame := teamStateFrame(t)
	requirement := readRequirement(CompletionQuantifierAtLeastOne)
	coverage := factCoverage(contractsv1.ContextFabricFactHealth, SourceAvailable)

	principal := storage.Principal{OrgID: "org_cover_sink"}
	pending := &assemblyTelemetry{}
	engine.finalizeResult(context.Background(), principal, InvestigationResult{
		Status:   InvestigationComplete,
		ResultID: "result_cover_engine_sink",
		Coverage: coverage,
	}, AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{requirement}}, &frame, CanonicalFactBundle{}, pending, answerPassFirst, 0)
	engine.emit(context.Background(), principal, *pending)
	engine.publishObservationCover(context.Background(), principal, pending.ObservationCover, true)

	const msg = "context fabric observation cover"
	if !bytes.Contains(engineBuf.Bytes(), []byte(msg)) {
		t.Fatalf("the observation-cover line did not reach the engine's own configured logger:\n%s", engineBuf.String())
	}
	if bytes.Contains(defaultBuf.Bytes(), []byte(msg)) {
		t.Fatalf("the observation-cover line reached the PROCESS DEFAULT logger -- exactly the defect this fix removes:\n%s", defaultBuf.String())
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestTaintedObservationsIsNonZeroWhenTheTaintActuallyBites is the companion
// the zero-assertion above cannot be trusted without.
//
// A field asserted only at its zero value pins nothing: an emitter that hard-
// coded 0, or dropped the field entirely, would satisfy every other test in
// this file. This drives the case where the mixed-state rule genuinely excludes
// an observation -- a lost kind standing on a key a SERVED kind also stands on
// -- and requires the count to be 1.
func TestTaintedObservationsIsNonZeroWhenTheTaintActuallyBites(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	sink := &recordingTelemetry{}
	requirement := contractsv1.ContextFabricPlanRequirement{
		Requirement: "principal_drivers",
		Obligation:  "drivers",
		Subject:     contractsv1.ContextFabricSubjectKind(SubjectTeam),
	}
	// deficiencies is SERVED and stands on {risk, recommendations, sustainability};
	// health is LOST and stands on {risk} -- which deficiencies also stands on.
	// So `risk` IS excluded from the served cover: the taint bites, and the
	// count must say so.
	evidence := readEvidence{
		Observed: 2, Served: 1, Narrowed: 1,
		ObservedKinds: []FactKind{FactOperationalDeficiencies, FactHealth},
		ServedKinds:   []FactKind{FactOperationalDeficiencies},
	}
	event := readRequirementObservationCoverEvent(
		requirement, 2,
		servedObservationCover(evidence, SubjectTeam, teamAssignment()),
		2, evidence, teamAssignment())
	if event == nil {
		t.Fatal("no cover event was built")
	}
	if event.TaintedObservations != 1 {
		t.Fatalf("TaintedObservations = %d, want 1 -- `risk` backs a lost kind AND a served one, so it is excluded from the served cover",
			event.TaintedObservations)
	}
	// The control: with nothing lost, the same pair taints nothing.
	clean := readEvidence{
		Observed: 2, Served: 2,
		ObservedKinds: []FactKind{FactOperationalDeficiencies, FactHealth},
		ServedKinds:   []FactKind{FactOperationalDeficiencies, FactHealth},
	}
	cleanEvent := readRequirementObservationCoverEvent(
		requirement, 2,
		servedObservationCover(clean, SubjectTeam, teamAssignment()),
		2, clean, teamAssignment())
	if cleanEvent.TaintedObservations != 0 {
		t.Fatalf("control: TaintedObservations = %d with nothing lost, want 0", cleanEvent.TaintedObservations)
	}
	_ = sink
}
