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

// TestChaos4085_RetractionTelemetryLeaksNoIdentity holds the content-safety
// line every method on this sink already holds: an org id, closed enums and
// counts, never a canonical id or a label. A reader who needs subject
// identity correlates by request_id with the resolution trace, which is
// where identity legitimately lives.
func TestChaos4085_RetractionTelemetryLeaksNoIdentity(t *testing.T) {
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
	// The outcome type carries no identifier field at all, so this is a
	// pin on that remaining true rather than a filter over what is logged:
	// a future field carrying an id would fail here.
	line, err := json.Marshal(records[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"canonical_id", "canonical", "label", "question", "result_id", "receipt"} {
		if strings.Contains(strings.ToLower(string(line)), forbidden) {
			t.Fatalf("retraction telemetry leaks %q: %s", forbidden, line)
		}
	}
}
