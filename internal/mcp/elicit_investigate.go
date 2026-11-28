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
	elicitToolInvestigateWindow  = "investigate_question_window"
	elicitToolInvestigateSubject = "investigate_question_subject"
	windowChoiceCalendar         = "previous_calendar_month"
	windowChoiceTrailing30       = "trailing_30d"
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

func logElicitSubjectOutcome(ctx context.Context, cfg *ProcessConfig, outcome string, asked bool, offered int) {
	if cfg == nil || cfg.diagnostics == nil {
		return
	}
	fields := eventspec.NewMCPElicitationOutcomeFields(elicitToolInvestigateSubject, outcome, asked, offered)
	cfg.diagnostics.InfoContext(ctx, eventspec.MCPElicitationOutcome.Msg, fields.SlogArgs()...)
}

// chooseSubject asks which subject a clarification means, and on an accepted,
// offered choice returns the follow-up request: the same request with that
// candidate's receipt and the clarification as its parent. It asks only when
// the hosted API proved the candidates are the whole set (2 to 25 of them) and
// the request carries no parent result or receipt of its own. Every other path
// returns false, so the caller renders today's answer.
func chooseSubject(ctx context.Context, cfg *ProcessConfig, t elicitTransport, input contractsv1.MCPInvestigateQuestionRequest, result contractsv1.ContextFabricInvestigationResult) (contractsv1.MCPInvestigateQuestionRequest, bool) {
	if result.Status != contractsv1.ContextFabricInvestigationClarificationRequired {
		return input, false
	}
	candidates := result.SubjectResolution.Candidates
	if result.ResultID == "" || !result.SubjectResolution.CandidateSetProvenWhole() ||
		len(candidates) < 2 || len(candidates) > maxElicitedSubjects || carriesPriorTurn(input) {
		logElicitSubjectOutcome(ctx, cfg, elicitOutcomeNotEligible, false, 0)
		return input, false
	}
	options := make([]elicitOption, 0, len(candidates))
	receipts := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		id := candidate.Subject.CanonicalID
		if _, dup := receipts[id]; dup || id == "" || candidate.ReceiptID == "" {
			logElicitSubjectOutcome(ctx, cfg, elicitOutcomeNotEligible, false, 0)
			return input, false
		}
		receipts[id] = candidate.ReceiptID
		title := candidate.Subject.Label + " (" + string(candidate.Subject.Kind)
		if candidate.Provider != "" {
			title += ", " + candidate.Provider
		}
		options = append(options, elicitOption{Value: id, Title: title + ", " + id + ")"})
	}
	if t == nil {
		logElicitSubjectOutcome(ctx, cfg, elicitOutcomeUnavailable, false, len(options))
		return input, false
	}
	res := elicit(ctx, t, elicitChoice{
		Message: "More than one subject matches. Which one do you mean?",
		Field:   "subject",
		Title:   "Subject",
		Options: options,
	})
	switch res.Outcome {
	case elicitDeclined:
		logElicitSubjectOutcome(ctx, cfg, elicitOutcomeDeclined, true, len(options))
		return input, false
	case elicitAccepted:
	default:
		logElicitSubjectOutcome(ctx, cfg, elicitOutcomeUnavailable, res.Answered, len(options))
		return input, false
	}
	logElicitSubjectOutcome(ctx, cfg, elicitOutcomeAccepted, true, len(options))
	followUp := input
	followUp.ParentResultID = result.ResultID
	followUp.PriorSubjectReceipts = []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: result.ResultID, ReceiptID: receipts[res.Value]}}
	return followUp, true
}

// carriesPriorTurn reports whether the request already answers an earlier
// turn: a parent result or any receipt.
func carriesPriorTurn(input contractsv1.MCPInvestigateQuestionRequest) bool {
	return input.ParentResultID != "" || len(input.PriorSubjectReceipts) > 0 || len(input.PriorKindReceipts) > 0 ||
		len(input.PriorAnchorReceipts) > 0 || len(input.PriorHandleReceipts) > 0 || len(input.PriorWindowReceipts) > 0 ||
		len(input.PriorCandidateReceipts) > 0
}
