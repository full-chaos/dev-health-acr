package mcp

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	elicitToolInvestigateWindow = "investigate_question_window"
	windowChoiceCalendar        = "previous_calendar_month"
	windowChoiceTrailing30      = "trailing_30d"
)

func logElicitWindowOutcome(ctx context.Context, cfg *ProcessConfig, outcome string, asked bool, offered int) {
	if cfg == nil || cfg.diagnostics == nil {
		return
	}
	fields := eventspec.NewMCPElicitationOutcomeFields(elicitToolInvestigateWindow, outcome, asked, offered)
	cfg.diagnostics.InfoContext(ctx, eventspec.MCPElicitationOutcome.Msg, fields.SlogArgs()...)
}

// chooseWindow returns the evidence window of the hosted request: the caller's
// own, unchanged, on every path except an accepted trailing_30d, which names
// that window. Decline, cancel, timeout, error, a value outside the offered
// choices, no capability and a malformed capability all return it unchanged, so
// the request (and the answer) is today's.
func chooseWindow(ctx context.Context, cfg *ProcessConfig, req *mcpsdk.CallToolRequest, input contractsv1.MCPInvestigateQuestionRequest) *contractsv1.ContextFabricRequestedEvidenceWindow {
	unchanged := input.EvidenceWindow
	if !contextfabric.WindowPhraseIsCalendarMonth(input.Question) {
		return unchanged
	}
	const offered = 2
	if !contextfabric.WindowPhraseNeedsChoice(input.Question) ||
		input.EvidenceWindow != nil || len(input.PriorWindowReceipts) > 0 || input.ParentResultID != "" ||
		(input.AllowClarification != nil && !*input.AllowClarification) {
		logElicitWindowOutcome(ctx, cfg, elicitOutcomeNotEligible, false, 0)
		return unchanged
	}
	t := newElicitTransport(cfg, req)
	if t == nil {
		logElicitWindowOutcome(ctx, cfg, elicitOutcomeUnavailable, false, offered)
		return unchanged
	}
	res := elicit(ctx, t, elicitChoice{
		Message: "\"last month\" can mean the previous calendar month or the last 30 days. Which do you mean?",
		Field:   "window",
		Title:   "Window",
		Options: []elicitOption{
			{Value: windowChoiceCalendar, Title: "The previous calendar month (" + contextfabric.PreviousCalendarMonthLabel(time.Now()) + ")"},
			{Value: windowChoiceTrailing30, Title: "The last 30 days"},
		},
	})
	switch res.Outcome {
	case elicitDeclined:
		logElicitWindowOutcome(ctx, cfg, elicitOutcomeDeclined, true, offered)
		return unchanged
	case elicitAccepted:
		logElicitWindowOutcome(ctx, cfg, elicitOutcomeAccepted, true, offered)
		if res.Value == windowChoiceTrailing30 {
			return &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D}
		}
		return unchanged
	default:
		logElicitWindowOutcome(ctx, cfg, elicitOutcomeUnavailable, res.Answered, offered)
		return unchanged
	}
}
