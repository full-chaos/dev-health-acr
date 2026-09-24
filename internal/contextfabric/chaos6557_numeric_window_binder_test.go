package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// windowGatedRecord is recordingTelemetry's capture of one
// RecordWindowGatedForConfirmation call.
type windowGatedRecord struct {
	Origin     WindowCanonicalizationOutcome
	RelativeID RelativeWindowID
	Provenance WindowProvenance
}

// CHAOS-6557: the prod acceptance question (req lane-mcp-accept-d49b78d5,
// acr b28a82b2) "What is the team investment mix over the last 30 days?"
// bound no temporal span at all -- the closed binder grammar knew only
// "last/past month|quarter|year" -- so the class table's trend_assessment
// default (trailing_90d) was offered as the inferred window, although the
// question itself names 30 days. These cases pin the numeric trailing
// phrases whose width IS a registry RelativeID.
func TestCHAOS6557_BindWindowSpans_NumericTrailingPhrases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     RelativeWindowID
	}{
		{"What is the team investment mix over the last 30 days?", RelativeWindowTrailing30D},
		{"Which teams need attention over the last 30 days?", RelativeWindowTrailing30D},
		{"what shipped in the past 30 days", RelativeWindowTrailing30D},
		{"what shipped in the last thirty days", RelativeWindowTrailing30D},
		{"how did the team do over the last 90 days", RelativeWindowTrailing90D},
		{"how did the team do over the past ninety days", RelativeWindowTrailing90D},
		{"how did the team do over the last 3 months", RelativeWindowTrailing90D},
		{"how did the team do over the last three months", RelativeWindowTrailing90D},
		{"what changed in the last 365 days", RelativeWindowTrailing365D},
		{"what changed in the past 12 months", RelativeWindowTrailing365D},
		{"what changed in the past twelve months", RelativeWindowTrailing365D},
	}
	for _, tc := range cases {
		bound := BindWindowSpans(tc.question)
		if len(bound) != 1 {
			t.Fatalf("BindWindowSpans(%q) = %#v, want exactly 1 span", tc.question, bound)
		}
		if bound[0].RelativeID != tc.want {
			t.Fatalf("BindWindowSpans(%q)[0].RelativeID = %q, want %q", tc.question, bound[0].RelativeID, tc.want)
		}
		if got := ProposeWindowFromSpans(tc.question); got.Reason != WindowBindRoutedInferred || got.RelativeID != tc.want {
			t.Fatalf("ProposeWindowFromSpans(%q) = %#v, want routed_inferred %q", tc.question, got, tc.want)
		}
	}
}

// Widths with no registry RelativeID stay unbound: the grammar maps a span
// to a registry member or binds nothing, never the nearest width.
func TestCHAOS6557_BindWindowSpans_NumericOutsideRegistryStaysUnbound(t *testing.T) {
	t.Parallel()
	cases := []string{
		"what shipped in the last 14 days",
		"what shipped in the last 300 days", // must not bind "30"
		"what shipped in the last 7 days",
		"what shipped in the last 2 months",
		"what shipped in the last 4 weeks",
		"what shipped in the last 30 minutes",
		"what shipped in the last 3 years",
	}
	for _, question := range cases {
		if bound := BindWindowSpans(question); len(bound) != 0 {
			t.Fatalf("BindWindowSpans(%q) = %#v, want no spans bound", question, bound)
		}
	}
}

// TestCHAOS6557_StatedNumericWindowDrivesInferredDefault replays the exact
// prod request body (02-q1.json args: question only, MCP surface) through
// a real Engine: the class-default gate still holds (DP12(b)/CHAOS-4040
// are unchanged), but the offered effective window is the one the question
// names (trailing_30d), not the class table's trailing_90d.
func TestCHAOS6557_StatedNumericWindowDrivesInferredDefault(t *testing.T) {
	t.Parallel()
	interpretation := bootstrapInterpretation()
	interpretation.Shape = ShapeDiscoveredCohort
	interpretation.SubjectTerms = nil
	interpreter := &countingInterpreter{interpretation: interpretation}
	telemetry := &recordingTelemetry{}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, chaos4234GatedGraph(), &staticResultStore{results: map[string]InvestigationResult{}}, telemetry)

	request := validInvestigationRequest()
	request.Question = "What is the team investment mix over the last 30 days?"
	request.Consumer.Surface = "mcp"

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("Status = %q, want clarification_required (the CHAOS-4040 gate is unchanged)", result.Status)
	}
	window := result.EffectiveEvidenceWindow
	if window == nil {
		t.Fatal("EffectiveEvidenceWindow = nil, want the question's own 30-day window")
	}
	if window.RelativeID != RelativeWindowTrailing30D {
		t.Fatalf("EffectiveEvidenceWindow.RelativeID = %q, want %q -- the question states 30 days", window.RelativeID, RelativeWindowTrailing30D)
	}
	if window.Provenance != WindowInferredDefault {
		t.Fatalf("EffectiveEvidenceWindow.Provenance = %q, want %q (a binder proposal never mints question_stated)", window.Provenance, WindowInferredDefault)
	}
	if len(telemetry.windowBinderProposals) != 1 {
		t.Fatalf("binder telemetry = %#v, want exactly one outcome", telemetry.windowBinderProposals)
	}
	if got := telemetry.windowBinderProposals[0]; got.Reason != WindowBindRoutedInferred || got.RelativeID != RelativeWindowTrailing30D || got.Grammar != "trailing_30_days" {
		t.Fatalf("binder telemetry = %#v, want routed_inferred trailing_30d via trailing_30_days", got)
	}
	wantGated := []windowGatedRecord{{Origin: WindowCanonicalizationGatedClassDefault, RelativeID: RelativeWindowTrailing30D, Provenance: WindowInferredDefault}}
	if len(telemetry.windowGatedForConfirmation) != 1 || telemetry.windowGatedForConfirmation[0] != wantGated[0] {
		t.Fatalf("gated-window telemetry = %#v, want %#v", telemetry.windowGatedForConfirmation, wantGated)
	}
}

// The two slog lines carry the closed fields an operator needs to read
// which window a clarification turn offered, and why.
func TestCHAOS6557_WindowTelemetryLinesCarryResolvedWindow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	principal := storage.Principal{OrgID: "org_6557"}
	telemetry.RecordWindowBinderOutcome(context.Background(), principal, ProposeWindowFromSpans("Which teams need attention over the last 30 days?"))
	telemetry.RecordWindowGatedForConfirmation(context.Background(), principal, WindowCanonicalizationGatedExplicitUnconfirmed,
		contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowInferredDefault})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2: %s", len(lines), buf.String())
	}
	want := []map[string]any{
		{"msg": "context fabric window binder outcome", "reason": "binder_span_routed_inferred", "relative_id": "trailing_30d", "grammar": "trailing_30_days", "spans_bound": float64(1)},
		{"msg": "context fabric window gated for confirmation", "origin": "gated_explicit_unconfirmed", "relative_id": "trailing_30d", "provenance": "inferred_default"},
	}
	for i, line := range lines {
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		for key, value := range want[i] {
			if got[key] != value {
				t.Fatalf("line %d field %q = %#v, want %#v (line: %s)", i, key, got[key], value, line)
			}
		}
	}
}
