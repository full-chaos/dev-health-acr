package main

import (
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

// semanticProjection is TARGET-CONTRACT.md section 4. Every value is read
// from a production function's output (the parsed interpretation, the
// sanitized capture, the interpreter's receipt and family outcome); nothing
// here re-derives a production decision.
type semanticProjection struct {
	Contract        semanticContract             `json:"contract"`
	Accepted        bool                         `json:"accepted"`
	TurnAdmitted    bool                         `json:"turn_admitted"`
	Flat            *semanticFlat                `json:"flat"`
	Hints           *semanticHints               `json:"hints"`
	FrameProposal   *semanticFrameProposal       `json:"frame_proposal"`
	FrameResolution semanticFrameResolution      `json:"frame_resolution"`
	AcceptedFrame   *contextfabric.QuestionFrame `json:"accepted_frame"`
	Family          semanticFamily               `json:"family"`
}

type semanticContract struct {
	PromptVersion string `json:"prompt_version"`
	FrameVersion  string `json:"frame_version"`
}

type semanticTime struct {
	Axis  string  `json:"axis"`
	AsOf  *string `json:"as_of"`
	Start *string `json:"start"`
	End   *string `json:"end"`
}

type semanticFlat struct {
	Shape                            string       `json:"shape"`
	RequestedJudgment                string       `json:"requested_judgment"`
	RequestedJudgmentKind            string       `json:"requested_judgment_kind"`
	SubjectTerms                     []string     `json:"subject_terms"`
	ComparisonTerms                  []string     `json:"comparison_terms"`
	TimeContext                      semanticTime `json:"time_context"`
	TimeContextDefaulted             bool         `json:"time_context_defaulted"`
	FactRequirementKinds             []string     `json:"fact_requirement_kinds"`
	FactRequirementParametersPresent bool         `json:"fact_requirement_parameters_present"`
	ClarificationNeeded              bool         `json:"clarification_needed"`
	ClarificationReasonPresent       bool         `json:"clarification_reason_present"`
}

type semanticHints struct {
	GroupKind                        string `json:"group_kind"`
	GroupKindUnrecognized            bool   `json:"group_kind_unrecognized"`
	ScopeAnchorTerm                  string `json:"scope_anchor_term"`
	ScopeAnchorTermTruncated         bool   `json:"scope_anchor_term_truncated"`
	ScopeAnchorKind                  string `json:"scope_anchor_kind"`
	ScopeAnchorKindUnrecognized      bool   `json:"scope_anchor_kind_unrecognized"`
	RequestedSubjectKind             string `json:"requested_subject_kind"`
	RequestedSubjectKindUnrecognized bool   `json:"requested_subject_kind_unrecognized"`
	QuestionFamilyPick               string `json:"question_family_pick"`
	QuestionFamilyUnrecognized       bool   `json:"question_family_unrecognized"`
	WindowClass                      string `json:"window_class"`
	WindowConfidence                 string `json:"window_confidence"`
	WindowClassUnrecognized          bool   `json:"window_class_unrecognized"`
}

type semanticOperand struct {
	Kind            string   `json:"kind"`
	Terms           []string `json:"terms"`
	AnchorTerms     []string `json:"anchor_terms"`
	MemberKind      string   `json:"member_kind"`
	MemberQualifier string   `json:"member_qualifier"`
}

type semanticExpression struct {
	Kind            string            `json:"kind"`
	Terms           []string          `json:"terms"`
	AnchorTerms     []string          `json:"anchor_terms"`
	MemberKind      string            `json:"member_kind"`
	MemberQualifier string            `json:"member_qualifier"`
	GroupKind       string            `json:"group_kind"`
	Operands        []semanticOperand `json:"operands"`
}

type frameSanitize struct {
	GoalsDropped                int  `json:"goals_dropped"`
	TermsTruncated              int  `json:"terms_truncated"`
	KindUnrecognized            bool `json:"kind_unrecognized"`
	TemporalUnrecognized        bool `json:"temporal_unrecognized"`
	EmphasisDropped             int  `json:"emphasis_dropped"`
	DimensionsDropped           int  `json:"dimensions_dropped"`
	MemberKindUnrecognized      bool `json:"member_kind_unrecognized"`
	GroupKindUnrecognized       bool `json:"group_kind_unrecognized"`
	MemberQualifierUnrecognized bool `json:"member_qualifier_unrecognized"`
}

type semanticFrameProposal struct {
	Present           bool               `json:"present"`
	Goals             []string           `json:"goals"`
	Temporal          string             `json:"temporal"`
	Emphasis          []string           `json:"emphasis"`
	Dimensions        []string           `json:"dimensions"`
	SubjectExpression semanticExpression `json:"subject_expression"`
	Sanitize          frameSanitize      `json:"sanitize"`
}

type gateReport struct {
	Outcome            string `json:"outcome"`
	RefuseBasis        string `json:"refuse_basis"`
	DeclaredMemberKind string `json:"declared_member_kind"`
	FailedInvariant    string `json:"failed_invariant"`
	Refuses            bool   `json:"refuses"`
}

type semanticFrameResolution struct {
	ValidationOutcome      string `json:"validation_outcome"`
	FailedInvariant        string `json:"failed_invariant"`
	GateOutcome            string `json:"gate_outcome"`
	GateRefuseBasis        string `json:"gate_refuse_basis"`
	GateDeclaredMemberKind string `json:"gate_declared_member_kind"`
	GateFailedInvariant    string `json:"gate_failed_invariant"`
	JudgmentRepaired       bool   `json:"judgment_repaired"`
}

type semanticFamily struct {
	Family           string `json:"family"`
	Source           string `json:"source"`
	RouteFamily      string `json:"route_family"`
	RouteSource      string `json:"route_source"`
	RouteDisposition string `json:"route_disposition"`
	PrecedenceRow    string `json:"precedence_row"`
}

func gateFrom(gate contextfabric.FrameGate) gateReport {
	return gateReport{
		Outcome:            string(gate.Outcome),
		RefuseBasis:        string(gate.RefuseBasis),
		DeclaredMemberKind: string(gate.DeclaredMemberKind),
		FailedInvariant:    string(gate.FailedInvariant),
		Refuses:            gate.Refuses(),
	}
}

func sanitizeCounters(frame genkitruntime.InterpretationOutputFrame) frameSanitize {
	return frameSanitize{
		GoalsDropped: frame.GoalsDropped, TermsTruncated: frame.TermsTruncated, KindUnrecognized: frame.KindUnrecognized,
		TemporalUnrecognized: frame.TemporalUnrecognized, EmphasisDropped: frame.EmphasisDropped, DimensionsDropped: frame.DimensionsDropped,
		MemberKindUnrecognized: frame.MemberKindUnrecognized, GroupKindUnrecognized: frame.GroupKindUnrecognized,
		MemberQualifierUnrecognized: frame.MemberQualifierUnrecognized,
	}
}

func buildSemantic(tree *node, interpreted contextfabric.InterpretedQuestion, capture genkitruntime.InterpretationOutputCapture, replay replayResult, env Envelope) *semanticProjection {
	if !env.JSONOK {
		return nil
	}
	projection := &semanticProjection{
		Contract:     semanticContract{PromptVersion: genkitruntime.DefaultInterpretationPromptVersion, FrameVersion: contextfabric.QuestionFrameVersion},
		Accepted:     env.ProductionAccepts,
		TurnAdmitted: env.TurnAdmitted,
	}
	if env.DomainOK {
		projection.Flat = flatFrom(interpreted, rawTimeAxisEmpty(tree))
		projection.Hints = hintsFrom(capture)
		projection.FrameProposal = proposalFrom(capture.Frame)
	}
	if replay.Receipt != nil {
		projection.FrameResolution.ValidationOutcome = string(replay.Receipt.FrameOutcome)
		projection.FrameResolution.FailedInvariant = string(replay.Receipt.FrameFailedInvariant)
		projection.FrameResolution.JudgmentRepaired = replay.Receipt.RequestedJudgmentRepair != ""
	}
	if replay.Err == nil {
		gate := replay.Outcome.Gate
		projection.FrameResolution.GateOutcome = string(gate.Outcome)
		projection.FrameResolution.GateRefuseBasis = string(gate.RefuseBasis)
		projection.FrameResolution.GateDeclaredMemberKind = string(gate.DeclaredMemberKind)
		projection.FrameResolution.GateFailedInvariant = string(gate.FailedInvariant)
		if replay.Outcome.Frame != nil {
			frame := *replay.Outcome.Frame
			projection.AcceptedFrame = &frame
		}
		projection.Family = semanticFamily{
			Family: string(replay.Outcome.Family), Source: string(replay.Outcome.Source),
			RouteFamily: string(replay.Outcome.Route.Family), RouteSource: string(replay.Outcome.Route.Source),
			RouteDisposition: string(replay.Outcome.Route.Disposition), PrecedenceRow: string(replay.Outcome.Winner.Row),
		}
	}
	return projection
}

func flatFrom(q contextfabric.InterpretedQuestion, timeDefaulted bool) *semanticFlat {
	kinds := make([]string, 0, len(q.FactRequirements))
	parameters := false
	for _, requirement := range q.FactRequirements {
		kinds = append(kinds, string(requirement.Kind))
		if len(requirement.Parameters) > 0 {
			parameters = true
		}
	}
	return &semanticFlat{
		Shape:                            string(q.Shape),
		RequestedJudgment:                q.RequestedJudgment,
		RequestedJudgmentKind:            string(q.RequestedJudgmentKind),
		SubjectTerms:                     nonNil(append([]string(nil), q.SubjectTerms...)),
		ComparisonTerms:                  nonNil(append([]string(nil), q.ComparisonTerms...)),
		TimeContext:                      timeFrom(q.TimeContext),
		TimeContextDefaulted:             timeDefaulted,
		FactRequirementKinds:             sortedSet(kinds),
		FactRequirementParametersPresent: parameters,
		ClarificationNeeded:              q.ClarificationNeeded,
		ClarificationReasonPresent:       strings.TrimSpace(q.ClarificationReason) != "",
	}
}

// rawTimeAxisEmpty reports the toDomain default-fill condition: a blank or
// missing time_context.axis makes production substitute the request's own
// time context (runtime.go toDomain).
func rawTimeAxisEmpty(tree *node) bool {
	timeContext, ok := tree.lookup("time_context")
	if !ok {
		return true
	}
	axis, ok := timeContext.lookup("axis")
	return !ok || axis.Kind != nodeString || strings.TrimSpace(axis.String) == ""
}

// rawString returns the last string value for key, or "" when absent or
// not a string.
func rawString(tree *node, key string) string {
	value, ok := tree.lookup(key)
	if !ok || value.Kind != nodeString {
		return ""
	}
	return value.String
}

// rawListLen returns the number of items in the array at key, or 0.
func rawListLen(tree *node, key string) int {
	value, ok := tree.lookup(key)
	if !ok || value.Kind != nodeArray {
		return 0
	}
	return len(value.Items)
}

func timeFrom(t contextfabric.TimeContext) semanticTime {
	format := func(value *time.Time) *string {
		if value == nil || value.IsZero() {
			return nil
		}
		text := value.UTC().Format(time.RFC3339Nano)
		return &text
	}
	return semanticTime{Axis: string(t.Axis), AsOf: format(t.AsOf), Start: format(t.Start), End: format(t.End)}
}

func hintsFrom(capture genkitruntime.InterpretationOutputCapture) *semanticHints {
	family := capture.Family
	return &semanticHints{
		GroupKind: string(family.GroupKind), GroupKindUnrecognized: family.GroupKindUnrecognized,
		ScopeAnchorTerm: family.ScopeAnchorTerm, ScopeAnchorTermTruncated: family.ScopeAnchorTermTruncated,
		ScopeAnchorKind: string(family.ScopeAnchorKind), ScopeAnchorKindUnrecognized: family.ScopeAnchorKindUnrecognized,
		RequestedSubjectKind: string(family.RequestedKind), RequestedSubjectKindUnrecognized: family.RequestedKindUnrecognized,
		QuestionFamilyPick: string(family.Family), QuestionFamilyUnrecognized: family.FamilyUnrecognized,
		WindowClass: string(capture.Window.Class), WindowConfidence: string(capture.Window.Confidence),
		WindowClassUnrecognized: capture.Window.ClassUnrecognized,
	}
}

func proposalFrom(frame genkitruntime.InterpretationOutputFrame) *semanticFrameProposal {
	proposal := &semanticFrameProposal{
		Present:    frame.Present,
		Goals:      []string{},
		Emphasis:   []string{},
		Dimensions: []string{},
		SubjectExpression: semanticExpression{
			Terms: []string{}, AnchorTerms: []string{}, Operands: []semanticOperand{},
		},
		Sanitize: sanitizeCounters(frame),
	}
	if !frame.Present {
		return proposal
	}
	f := frame.Frame
	for _, goal := range f.Goals {
		proposal.Goals = append(proposal.Goals, string(goal))
	}
	proposal.Goals = sortedSet(proposal.Goals)
	proposal.Temporal = string(f.Temporal)
	for _, emphasis := range f.Emphasis {
		proposal.Emphasis = append(proposal.Emphasis, string(emphasis))
	}
	proposal.Emphasis = sortedSet(proposal.Emphasis)
	for _, dimension := range f.Dimensions {
		proposal.Dimensions = append(proposal.Dimensions, string(dimension))
	}
	proposal.Dimensions = sortedSet(proposal.Dimensions)
	proposal.SubjectExpression = expressionFrom(f.SubjectExpression)
	return proposal
}

// expressionFrom flattens the sanitized union back to the model-facing
// field names. Only the variant the discriminator names is populated
// (sanitizeFrameOutput builds exactly one), so fields the kind does not
// use read as empty even if the raw output carried them.
func expressionFrom(e contextfabric.SubjectExpression) semanticExpression {
	out := semanticExpression{Kind: string(e.Kind), Terms: []string{}, AnchorTerms: []string{}, Operands: []semanticOperand{}}
	switch {
	case e.Named != nil:
		out.Terms = nonNil(append([]string(nil), e.Named.Terms...))
	case e.Discovered != nil:
		out.MemberKind = string(e.Discovered.MemberKind)
	case e.Scoped != nil:
		out.AnchorTerms = nonNil(append([]string(nil), e.Scoped.AnchorTerms...))
		out.MemberKind = string(e.Scoped.MemberKind)
		out.MemberQualifier = string(e.Scoped.MemberQualifier)
	case e.Grouped != nil:
		out.GroupKind = string(e.Grouped.GroupKind)
		out.MemberKind = string(e.Grouped.MemberKind)
	case e.Org != nil:
		if e.Org.MemberKind != nil {
			out.MemberKind = string(*e.Org.MemberKind)
		}
	case e.Explicit != nil:
		for _, operand := range e.Explicit.Operands {
			item := semanticOperand{Kind: string(operand.Kind), Terms: []string{}, AnchorTerms: []string{}}
			if operand.Named != nil {
				item.Terms = nonNil(append([]string(nil), operand.Named.Terms...))
			}
			if operand.Scoped != nil {
				item.AnchorTerms = nonNil(append([]string(nil), operand.Scoped.AnchorTerms...))
				item.MemberKind = string(operand.Scoped.MemberKind)
				item.MemberQualifier = string(operand.Scoped.MemberQualifier)
			}
			out.Operands = append(out.Operands, item)
		}
	}
	return out
}

func sortedSet(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
