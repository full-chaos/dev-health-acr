package contextfabric

// r2 CLASS pins: one real-handler forgery pin per family the r2 review
// round found unswept (stored ids, org_id-family), exercised through the
// production sink, never a direct function call. graphrank's Subject
// CanonicalID family and falkorgraph's graph request-id family get their
// own pins in their own packages (graphrank/chaos5544_r2_forge_pins_test.go,
// falkorgraph/chaos5544_r2_forge_pins_test.go) since this file cannot reach
// unexported sinks there.
import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestRecordAnswerReuseServedRequestIDSanitizesStoredIDs is the r2 pin for
// telemetry.go's served_request_id (a value READ BACK from a stored
// result, never passed through observability.WithRequestID's own format
// check the way a live request id is -- r1 P1 finding, corrected here).
func TestRecordAnswerReuseServedRequestIDSanitizesStoredIDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, control string }{
		{"lf", "\n"}, {"cr", "\r"}, {"ansi_escape", "\x1b[31m"}, {"nul", "\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			forged := "stored_result_1" + tc.control + "fake_field=1"
			records := captureSlogJSON(t, func(logger *slog.Logger) {
				NewSlogEngineTelemetry(logger).RecordAnswerReuseServedRequestID(
					context.Background(), storage.Principal{OrgID: "org_1"}, forged, true)
			})
			if len(records) != 1 {
				t.Fatalf("got %d records, want exactly 1", len(records))
			}
			got, _ := records[0]["served_request_id"].(string)
			if strings.Contains(got, tc.control) {
				t.Fatalf("the raw control byte %q survived into served_request_id: %q", tc.control, got)
			}
			if !strings.HasPrefix(got, "stored_result_1") {
				t.Fatalf("served_request_id = %q lost its correlation prefix", got)
			}
		})
	}
}

// TestRecordPlanCarrySanitizesSourceResultID is the r2 pin for
// telemetry.go's source_result_id (RecordPlanCarry and RecordPlanCarryOutcome
// share the shape; one call site pins both since they route through the
// same SanitizeLogAttr call after the mechanical fix).
func TestRecordPlanCarrySanitizesSourceResultID(t *testing.T) {
	t.Parallel()
	forged := "result_1\r\nlevel=ERROR msg=\"forged\""
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordPlanCarry(context.Background(), storage.Principal{OrgID: "org_1"}, PlanCarryEvent{
			FamilyReplaced: QuestionFamilyUnclassified,
			FamilyCarried:  QuestionFamilyGroupedCohortStatus,
			SourceResultID: forged,
		})
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(records))
	}
	got, _ := records[0]["source_result_id"].(string)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("a line break survived into source_result_id: %q", got)
	}
	if !strings.HasPrefix(got, "result_1") {
		t.Fatalf("source_result_id = %q lost its correlation prefix", got)
	}
}

// TestRecordPriorSubjectReceiptsSkippedSanitizesOrgID is the r2 pin for the
// general org_id family in this file (~50 sites, mechanically fixed):
// picks one representative site and forges org_id itself, through the real
// production sink.
func TestRecordPriorSubjectReceiptsSkippedSanitizesOrgID(t *testing.T) {
	t.Parallel()
	forged := "org_1\nlevel=ERROR msg=\"forged org line\""
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordPriorSubjectReceiptsSkipped(
			context.Background(), storage.Principal{OrgID: forged}, 3)
	})
	if len(records) != 1 {
		t.Fatalf("got %d records, want exactly 1", len(records))
	}
	got, _ := records[0]["org_id"].(string)
	if strings.Contains(got, "\n") {
		t.Fatalf("a raw newline survived into org_id: %q", got)
	}
	if !strings.HasPrefix(got, "org_1") {
		t.Fatalf("org_id = %q lost its correlation prefix", got)
	}
}
