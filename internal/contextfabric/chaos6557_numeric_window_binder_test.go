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
	Surface    string
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

// TestCHAOS6557_StatedNumericWindowDrivesCommittedWindow replays the exact
// prod request body (02-q1.json args: question only) through a real Engine:
// the question itself names 30 days, so the window is the one the question
// names (trailing_30d, not the class table's trailing_90d) and it is
// COMMITTED (question_stated) -- no confirmation turn (chris ruling
// 2026-09-25; the explicit-supplied class is pinned in
// chaos6557_explicit_window_commits_test.go).
//
// Surface-agnostic BY DESIGN (codex r1 on #667 pinned this): the binder runs
// over the question text before any surface rule, so a workbench caller
// asking "over the last 30 days" gets 30 days too. Both surfaces are pinned
// so that contract is asserted, not incidental; the binder telemetry line
// carries the surface so an operator can tell them apart.
func TestCHAOS6557_StatedNumericWindowDrivesCommittedWindow(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"mcp", "workbench"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			telemetry := &recordingTelemetry{}
			project := acceptanceProject()
			facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
				return bootstrapFactBundle(project), nil
			})
			graph := &acceptanceGraphReader{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
				context:    bootstrapGraphContext(project),
			}
			engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), newMapResultStore(), telemetry)

			request := validInvestigationRequest()
			request.Question = "What is the team investment mix over the last 30 days?"
			request.Consumer.Surface = surface

			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status == InvestigationClarificationRequired {
				t.Fatalf("Status = %q, want an answer: the question states its window", result.Status)
			}
			window := result.EffectiveEvidenceWindow
			if window == nil {
				t.Fatal("EffectiveEvidenceWindow = nil, want the question's own 30-day window")
			}
			if window.RelativeID != RelativeWindowTrailing30D {
				t.Fatalf("EffectiveEvidenceWindow.RelativeID = %q, want %q -- the question states 30 days", window.RelativeID, RelativeWindowTrailing30D)
			}
			if window.Provenance != WindowQuestionStated {
				t.Fatalf("EffectiveEvidenceWindow.Provenance = %q, want %q", window.Provenance, WindowQuestionStated)
			}
			if len(telemetry.windowBinderProposals) != 1 {
				t.Fatalf("binder telemetry = %#v, want exactly one outcome", telemetry.windowBinderProposals)
			}
			if got := telemetry.windowBinderProposals[0]; got.Reason != WindowBindRoutedInferred || got.RelativeID != RelativeWindowTrailing30D || got.Grammar != "trailing_30_days" {
				t.Fatalf("binder telemetry = %#v, want routed_inferred trailing_30d via trailing_30_days", got)
			}
			if len(telemetry.windowGatedForConfirmation) != 0 {
				t.Fatalf("gated-window telemetry = %#v, want none: a stated window is never gated", telemetry.windowGatedForConfirmation)
			}
			if got := telemetry.windowCanonicalizationOutcomes; len(got) != 1 || got[0] != WindowCanonicalizationRequestStated {
				t.Fatalf("window canonicalization outcomes = %#v, want [request_stated]", got)
			}
		})
	}
}

// The two slog lines carry the closed fields an operator needs to read
// which window a clarification turn offered, and why.
func TestCHAOS6557_WindowTelemetryLinesCarryResolvedWindow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	principal := storage.Principal{OrgID: "org_6557"}
	telemetry.RecordWindowBinderOutcome(context.Background(), principal, "mcp", ProposeWindowFromSpans("Which teams need attention over the last 30 days?"))
	telemetry.RecordWindowGatedForConfirmation(context.Background(), principal, "mcp", WindowCanonicalizationGatedClassDefault,
		contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing30D, Provenance: WindowInferredDefault, WindowClass: WindowClassTrendAssessment})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2: %s", len(lines), buf.String())
	}
	want := []map[string]any{
		{"msg": "context fabric window binder outcome", "surface": "mcp", "reason": "binder_span_routed_inferred", "relative_id": "trailing_30d", "grammar": "trailing_30_days", "spans_bound": float64(1)},
		{"msg": "context fabric window gated for confirmation", "surface": "mcp", "origin": "gated_class_default", "relative_id": "trailing_30d", "provenance": "inferred_default", "window_class": "trend_assessment"},
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

// chris 2026-09-25: "in the last month" is trailing; a bare "last month" is
// the previous calendar month and is never committed by the trailing grammar.
func TestCHAOS6557_BinderSeparatesTrailingFromCalendarPhrases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		trailing bool
	}{
		{"What is the team investment mix in the last month?", true},
		{"What changed over the last quarter?", true},
		{"What changed within the last year?", true},
		{"What changed during the past month?", true},
		{"What changed in the past month?", true},
		{"What changed during the last month?", true},
		{"What changed for the last month?", true},
		{"What changed since last month?", true},
		{"What changed over the last 30 days?", true},
		{"Which teams need attention over the last 90 days?", true},
		{"Which repository carried the most operational/support work last month and why?", false},
		{"Which repository carried the most work last month?", false},
		{"Last month, which team shipped the most?", false},
		{"What changed last quarter?", false},
		{"What did the team ship for last month?", false},
		{"What did the team ship over last month?", false},
		{"What did the team ship in last month?", false},
	}
	for _, tc := range cases {
		got := ProposeWindowFromSpans(tc.question)
		if got.Trailing != tc.trailing {
			t.Fatalf("ProposeWindowFromSpans(%q) = %#v, want Trailing=%v", tc.question, got, tc.trailing)
		}
	}
}
