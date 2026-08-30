package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4085 / CHAOS-4089 -- SINK-LEVEL telemetry tests.
//
// THE LESSON THIS FILE EXISTS TO ENFORCE: a field existing on a telemetry
// struct, and being populated by the code that builds it, must NEVER again
// pass for that field reaching an operator.
//
// CHAOS-4085's trace addition shipped with the CommitBasis/TiedStatisticalTop
// fields defined, populated at every emission site, and covered by tests --
// and none of it reached production, because the production slog sink simply
// did not log them and no test looked at the sink. Worse, the whole commit
// retraction event was invisible: CommitAffirmationTelemetry is an OPTIONAL
// interface, and nothing in production implemented it, so every retraction
// failed a type assertion and disappeared. Both were caught only by a
// retroactive codex pass.
//
// The shape of the miss is what matters: every test involved read the
// telemetry value DIRECTLY through an in-memory double, which is precisely
// the kind of test that cannot observe "nothing downstream consumes this".
// So the tests below deliberately assert against the PRODUCTION sink's real
// output bytes -- the log line an operator would actually receive -- rather
// than against any struct field.
//
// If you add a field to a telemetry or trace event, add its assertion here
// too. A test that reads the field off the struct proves nothing about
// whether anyone will ever see it.

// captureSlogJSON runs emit against a JSON slog logger at Debug level and
// returns the decoded records, so an assertion can name the exact key an
// operator would grep for.
func captureSlogJSON(t *testing.T, emit func(*slog.Logger)) []map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	emit(logger)

	records := make([]map[string]any, 0, 2)
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("telemetry emitted a line that is not valid JSON: %v (%q)", err, line)
		}
		records = append(records, record)
	}
	return records
}

// TestChaos4085_ProductionTelemetryEmitsARetraction is the sink-level pin
// for the hole this file's header describes: SlogEngineTelemetry must
// actually implement CommitAffirmationTelemetry, and the record must carry
// every field the outcome does.
//
// The interface assertion is the load-bearing half. recordCommitAffirmation
// reaches the sink through an optional type assertion, so a production
// telemetry that merely FAILS to implement this method is not a compile
// error anywhere -- it is a silent, total loss of the signal. That is
// exactly what shipped.
func TestChaos4085_ProductionTelemetryEmitsARetraction(t *testing.T) {
	// Compile-time proof that the production sink satisfies the optional
	// interface. If someone removes the method, this line fails to build
	// instead of the signal quietly disappearing at runtime.
	var _ CommitAffirmationTelemetry = SlogEngineTelemetry{}

	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCommitAffirmationRetraction(
			context.Background(),
			storage.Principal{OrgID: "org_sink_test"},
			CommitAffirmationOutcome{
				Basis:                CommitBasisStatistical,
				SubjectKind:          SubjectWorkItem,
				WinningMechanism:     "lexical",
				ProvisionalCommitted: 2,
				FinalCommitted:       1,
			},
		)
	})

	if len(records) != 1 {
		t.Fatalf("a retraction must produce exactly one log record, got %d", len(records))
	}
	record := records[0]
	for key, want := range map[string]any{
		"org_id":            "org_sink_test",
		"commit_basis":      string(CommitBasisStatistical),
		"subject_kind":      string(SubjectWorkItem),
		"winning_mechanism": "lexical",
	} {
		if got, ok := record[key]; !ok || got != want {
			t.Fatalf("record[%q] = %v (present=%v), want %v -- an operator greps for this key", key, got, ok, want)
		}
	}
	// JSON numbers decode as float64.
	for key, want := range map[string]float64{"provisional_committed": 2, "final_committed": 1} {
		if got, ok := record[key].(float64); !ok || got != want {
			t.Fatalf("record[%q] = %v, want %v", key, record[key], want)
		}
	}
}

// TestChaos4085_RetractionTelemetryLeaksNoIdentityAndStaysAtWarn holds the
// content-safety line every method on this sink already holds, and pins the
// level.
//
// The oracle is an ALLOW-LIST, not a denylist (codex pre-merge review, LOW):
// a denylist of forbidden substrings only catches the leaks someone thought
// of, so a future field named subject_id or subject carrying a canonical
// value would sail through one. Requiring every key to be explicitly
// permitted inverts that -- a new field fails this test by default and has
// to be argued for. Given this file exists because a telemetry gap shipped
// unnoticed, the default must be "fail" rather than "pass unless it looks
// suspicious".
func TestChaos4085_RetractionTelemetryLeaksNoIdentityAndStaysAtWarn(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCommitAffirmationRetraction(
			context.Background(),
			storage.Principal{OrgID: "org_sink_test"},
			CommitAffirmationOutcome{
				Basis: CommitBasisUnknown, SubjectKind: SubjectRepository,
				WinningMechanism: "vector", ProvisionalCommitted: 1, FinalCommitted: 0,
			},
		)
	})
	if len(records) != 1 {
		t.Fatalf("expected one record, got %d", len(records))
	}
	record := records[0]

	// Every key this record is permitted to carry. slog's own envelope
	// (time/level/msg) plus the org id, the closed enums, and the counts.
	// request_id is permitted but only appears when ctx carries one.
	permitted := map[string]struct{}{
		"time": {}, "level": {}, "msg": {},
		"org_id": {}, "commit_basis": {}, "subject_kind": {},
		"winning_mechanism": {}, "provisional_committed": {}, "final_committed": {},
		"request_id": {},
	}
	for key := range record {
		if _, ok := permitted[key]; !ok {
			t.Fatalf("retraction telemetry emits unpermitted key %q -- if this field is genuinely safe, add it to the allow-list deliberately", key)
		}
	}

	// The VALUES must be closed-vocabulary too: an allow-listed key holding
	// a canonical id would defeat the key check.
	if got := record["subject_kind"]; got != string(SubjectRepository) {
		t.Fatalf("subject_kind = %v, want the closed kind enum %q", got, SubjectRepository)
	}
	if got := record["commit_basis"]; got != string(CommitBasisUnknown) {
		t.Fatalf("commit_basis = %v, want the closed basis enum (empty for unknown)", got)
	}

	// WARN, pinned: a retraction is a designed outcome, but a RATE change in
	// it is the signal that retrieval quality or synthesis grounding moved.
	// Demoting it to INFO would bury that under the reuse-outcome stream,
	// and nothing else in the suite would notice.
	if got := record["level"]; got != "WARN" {
		t.Fatalf("level = %v, want WARN -- see RecordCommitAffirmationRetraction's own doc comment for why", got)
	}
}

// TestChaos4579_ProductionTelemetryEmitsTheCohortStructureGate is this
// file's own rule applied to CHAOS-4579's new event: "If you add a field to
// a telemetry or trace event, add its assertion here too. A test that reads
// the field off the struct proves nothing about whether anyone will ever
// see it."
//
// codex round 2 (P3) caught that the gate's tests inspected only the
// in-memory recordingTelemetry double. Confirmed by mutation before this
// test was written: deleting `shape` from the sink's slog arguments left
// the ENTIRE contextfabric package green, while production could no longer
// tell an `applied` on a discovered_cohort question from an `applied` on
// anything else -- which is the whole point of the event.
//
// `shape` is the load-bearing half here. `outcome` alone cannot answer the
// question the gate exists to make countable ("cohort clarification vs
// subject clarification"), because subject_bearing is emitted for three
// different shapes.
func TestChaos4579_ProductionTelemetryEmitsTheCohortStructureGate(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		telemetry := NewSlogEngineTelemetry(logger)
		telemetry.RecordCohortStructureGate(context.Background(), storage.Principal{OrgID: "org_sink_test"}, CohortStructureGateApplied, ShapeDiscoveredCohort)
		telemetry.RecordCohortStructureGate(context.Background(), storage.Principal{OrgID: "org_sink_test"}, CohortStructureGateSubjectBearing, ShapeSingleSubject)
	})

	if len(records) != 2 {
		t.Fatalf("two gate decisions must produce exactly two log records, got %d", len(records))
	}
	for i, want := range []map[string]any{
		{"org_id": "org_sink_test", "outcome": string(CohortStructureGateApplied), "shape": string(ShapeDiscoveredCohort)},
		{"org_id": "org_sink_test", "outcome": string(CohortStructureGateSubjectBearing), "shape": string(ShapeSingleSubject)},
	} {
		for key, value := range want {
			got, ok := records[i][key]
			if !ok || got != value {
				t.Fatalf("record[%d][%q] = %v (present=%v), want %v -- an operator greps for this key; without `shape` the two sides of the class decision are indistinguishable in production", i, key, got, ok, value)
			}
		}
	}
}

// The content-safety line every method on this sink holds, applied to the
// gate: both arguments are closed enums, so the record must carry NOTHING
// else that could name a question, a subject, or an offer. An allow-list,
// matching this file's own oracle discipline -- a denylist only catches the
// leaks someone thought of.
func TestChaos4579_CohortStructureGateTelemetryLeaksNoContent(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordCohortStructureGate(context.Background(), storage.Principal{OrgID: "org_sink_test"}, CohortStructureGateApplied, ShapeDiscoveredCohort)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	allowed := map[string]bool{"time": true, "level": true, "msg": true, "org_id": true, "outcome": true, "shape": true, "request_id": true}
	for key := range records[0] {
		if !allowed[key] {
			t.Fatalf("cohort structure gate record carries unexpected key %q -- this event is closed enums and an org id only, never question text, a subject identifier, or an offer label", key)
		}
	}
	if records[0]["level"] != "INFO" {
		t.Fatalf("level = %v, want INFO: a class decision is a normal, designed outcome, not a fault", records[0]["level"])
	}
}
