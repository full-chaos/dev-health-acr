package contextfabric

import (
	"context"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3900 window classification post-pass (design brief v5.2, §2/§2.1).
//
// W0 shipped this as a SHADOW-ONLY file in package graphrank (not
// contextfabric, where the engine itself lives): graphrank already imports
// contextfabric, so at the time nothing here was invoked from engine.go, and
// putting it in graphrank kept it beside the companion binder's shared
// grammar-registry discipline (CHAOS-3896/3899's handle grammar).
//
// W1 makes canonicalizeEvidenceWindow (window.go) a REAL pre-tryReuse engine
// step that must call this post-pass directly -- which package graphrank's
// existing "graphrank imports contextfabric" direction would turn into an
// import cycle. This file therefore moved INTO package contextfabric
// unchanged in behavior; graphrank.ClassifyWindow/DefaultRelativeID (if any
// external caller still names them) no longer exist -- callers use
// contextfabric.ClassifyWindow/DefaultRelativeID directly.

// WindowClassSource names whether a WindowClassOutcome's Class came from
// the model's own pick or the deterministic fallback table (design brief
// §2's "Unset class... deterministic fallback" / "Class incompatible with
// structural signals... downgrade" rules) -- the "class_source: model|
// fallback" field the §7 measurement plan's per-case row names.
type WindowClassSource string

const (
	WindowClassSourceModel    WindowClassSource = "model"
	WindowClassSourceFallback WindowClassSource = "fallback"
	// WindowClassSourceNone means neither the model's pick nor the
	// deterministic fallback could name a class -- "anything else -> NO
	// window" (§2): refuse to guess, never a wrong constraint.
	WindowClassSourceNone WindowClassSource = "none"
)

// WindowClassOutcome is the SHADOW-ONLY engine post-pass verdict for one
// interpreted question.
type WindowClassOutcome struct {
	Class      WindowClass
	Confidence WindowConfidence
	Source     WindowClassSource
	// Downgraded is true iff the model asserted a class that conflicted
	// with the structural signals and this post-pass overrode it with the
	// structurally compatible fallback class (§2's "divergence is a
	// counted telemetry event").
	Downgraded bool
	// Basis is the rule that decided Class (CHAOS-12595).
	Basis WindowClassBasis
}

// classStructurallyCompatible reports whether class's own structural
// signal (design brief §2.1's "signals" column, the Shape-derived half of
// it) is present on interpreted.
//
// Deliberately conservative for W0: only InterpretedQuestion.Shape is
// consulted. The §2.1 table also names a "grammar-bound handle present"
// signal for recent_activity_lookup -- available in this package (
// BindHandles) but NOT wired into this check, because a handle's presence
// is resolved from graphrank.ResolveSubjects' own search machinery
// downstream of Interpret in the real Investigate flow, not from question
// text alone; using BindHandles here would silently diverge from what
// "handle bound" means everywhere else it is checked. shape=single_subject
// alone -- the FIRST-listed, strongest signal in that table row -- is
// therefore this slice's whole structural check for that class; the
// residual (a single_subject question with no bound handle still counting
// as compatible) is reach-preserving, never wrong-constraint-producing,
// matching this file's own refuse-don't-guess discipline elsewhere.
//
// state_snapshot has no Shape-derived signal in §2.1's table at all
// ("ownership/config/membership judgments; fact kinds that are pure
// current state" -- a fact-kind-shape check this slice does not
// implement), so it is always treated as structurally compatible: an
// accepted-but-windowless class is harmless by construction (§2.1: "NO
// window" is state_snapshot's own default).
//
// explicit_window is, by the class rule itself (§2.1: "the CALLER sent
// axis=range... NEVER a model assertion"), never a legitimate MODEL pick
// -- it is always treated as incompatible here, which routes it through
// the same downgrade-to-fallback path as any other structural mismatch.
func classStructurallyCompatible(class WindowClass, interpreted InterpretedQuestion) bool {
	switch class {
	case WindowClassTrendAssessment:
		switch interpreted.Shape {
		case ShapeDiscoveredCohort,
			ShapeExplicitCohort,
			ShapeOpen:
			return true
		default:
			return false
		}
	case WindowClassRecentActivityLookup:
		return interpreted.Shape == ShapeSingleSubject
	case WindowClassStateSnapshot:
		return true
	default:
		return false
	}
}

// fallbackClass implements §2's "Unset class (model omitted, or sanitized
// away) -> deterministic fallback" rule, restated: shape=discovered_cohort
// -> trend_assessment; single_subject -> recent_activity_lookup (the
// bound-handle half of the design's own fallback condition is dropped for
// the SAME reason classStructurallyCompatible drops it above -- see that
// function's doc comment); anything else -> no class at all (ok=false),
// never a guess.
func fallbackClass(interpreted InterpretedQuestion) (WindowClass, bool) {
	switch interpreted.Shape {
	case ShapeDiscoveredCohort:
		return WindowClassTrendAssessment, true
	case ShapeSingleSubject:
		return WindowClassRecentActivityLookup, true
	default:
		return "", false
	}
}

// ClassifyWindow is the whole §2 post-pass: given the interpreted question
// and the model's own (already-sanitized, closed-vocabulary-or-empty)
// class/confidence pick, produce the outcome the class-to-default table
// (DefaultRelativeID, below) consumes. modelClass must already be sanitized
// (SanitizeWindowClass) -- this function trusts "" as
// genuinely unset and any non-empty value as a vocabulary member; it does
// not re-validate.
func ClassifyWindow(interpreted InterpretedQuestion, modelClass WindowClass, modelConfidence WindowConfidence) WindowClassOutcome {
	if modelClass == "" {
		return classifyFallback(interpreted, false)
	}
	if modelClass == WindowClassExplicitWindow || !classStructurallyCompatible(modelClass, interpreted) {
		return classifyFallback(interpreted, true)
	}
	confidence := modelConfidence
	if confidence == "" {
		confidence = WindowConfidenceLow
	}
	return WindowClassOutcome{Class: modelClass, Confidence: confidence, Source: WindowClassSourceModel, Basis: WindowClassBasisModel}
}

// WindowClassBasis names which rule decided a WindowClassOutcome: the model's
// own pick, the shape fallback table, the handle-state rule, or none. Closed
// vocabulary; it is the decision basis the classification telemetry line
// carries (CHAOS-12595).
type WindowClassBasis string

const (
	WindowClassBasisModel         WindowClassBasis = "model"
	WindowClassBasisShapeFallback WindowClassBasis = "shape_fallback"
	WindowClassBasisHandleState   WindowClassBasis = "handle_state"
	WindowClassBasisNone          WindowClassBasis = "none"
)

// WindowClassBasisVocabulary is the closed basis vocabulary, in published
// order.
func WindowClassBasisVocabulary() []string {
	return []string{string(WindowClassBasisModel), string(WindowClassBasisShapeFallback), string(WindowClassBasisHandleState), string(WindowClassBasisNone)}
}

// WindowClassSourceVocabulary is the closed source vocabulary, in published
// order.
func WindowClassSourceVocabulary() []string {
	return []string{string(WindowClassSourceModel), string(WindowClassSourceFallback), string(WindowClassSourceNone)}
}

// handleStateKinds are the subject kinds one handle names: a question about
// the current state of one of them needs no evidence window (CHAOS-12595).
var handleStateKinds = [...]SubjectKind{SubjectPullRequest, SubjectWorkItem, contractsv1.ContextFabricSubjectCIRun, SubjectDeployment, SubjectIncident}

// frameAsksHandleState reports whether the frame asks only for the current
// state of one named subject whose stated kind is a handle kind: a
// named_subject expression with a stated handle kind, a current temporal
// intent, and assess_state as its only goal. This is the state_snapshot
// signal §2.1 names ("pure current state") that ClassifyWindow's shape-only
// check does not implement. Every clause is required: a trend, a change or a
// driver question about the same pull request keeps its window.
func frameAsksHandleState(frame *QuestionFrame) bool {
	if frame == nil || frame.Temporal != TemporalIntentCurrent || len(frame.Goals) == 0 {
		return false
	}
	for _, goal := range frame.Goals {
		if goal != GoalAssessState {
			return false
		}
	}
	expression := frame.SubjectExpression
	if expression.Kind != SubjectExpressionNamed || expression.Named == nil || expression.Named.ExpectedKind == nil {
		return false
	}
	for _, kind := range handleStateKinds {
		if *expression.Named.ExpectedKind == kind {
			return true
		}
	}
	return false
}

// classifyWindowFor is ClassifyWindow with the accepted frame in hand. A
// frame that asks only for the current state of one handle (frameAsksHandleState)
// classifies as state_snapshot, whatever the model picked: a model pick of
// state_snapshot is kept as the model's, any other pick (or none) is
// replaced and marked Downgraded when there was a pick. Every other frame,
// and a nil one, gets exactly ClassifyWindow's outcome.
func classifyWindowFor(interpreted InterpretedQuestion, frame *QuestionFrame) WindowClassOutcome {
	modelClass, modelConfidence := interpreted.WindowClass, interpreted.WindowConfidence
	if !frameAsksHandleState(frame) {
		return ClassifyWindow(interpreted, modelClass, modelConfidence)
	}
	if modelClass == WindowClassStateSnapshot {
		outcome := ClassifyWindow(interpreted, modelClass, modelConfidence)
		outcome.Basis = WindowClassBasisModel
		return outcome
	}
	return WindowClassOutcome{
		Class: WindowClassStateSnapshot, Confidence: WindowConfidenceHigh,
		Source: WindowClassSourceFallback, Downgraded: modelClass != "", Basis: WindowClassBasisHandleState,
	}
}

// recordWindowClassification reports one class decision; nil-safe.
func (e *Engine) recordWindowClassification(ctx context.Context, principal storage.Principal, interpreted InterpretedQuestion, outcome WindowClassOutcome) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordWindowClassification(ctx, principal, interpreted.WindowClass, outcome)
}

func classifyFallback(interpreted InterpretedQuestion, downgraded bool) WindowClassOutcome {
	class, ok := fallbackClass(interpreted)
	if !ok {
		return WindowClassOutcome{Source: WindowClassSourceNone, Confidence: WindowConfidenceLow, Downgraded: downgraded, Basis: WindowClassBasisNone}
	}
	return WindowClassOutcome{Class: class, Confidence: WindowConfidenceLow, Source: WindowClassSourceFallback, Downgraded: downgraded, Basis: WindowClassBasisShapeFallback}
}

// WindowDefaultPolicy names ONE of the two DW0 candidate default widths
// (design brief §7/§9 decision matrix, row DW0). W0 measures BOTH
// candidates per case in the D2(b) rerun (§7's own per-kind row carries
// count_90d AND count_365d together) -- this type exists so the
// class-distribution secondary instrument (which policy a case's class
// would have picked) can be reported per policy without hardcoding either
// width into ClassifyWindow itself. Neither ships as a live default in W0.
type WindowDefaultPolicy struct {
	Name                 string
	TrendAssessment      RelativeWindowID
	RecentActivityLookup RelativeWindowID
}

var (
	// WindowDefaultPolicy90D is the DW0-preferred candidate (chris:
	// "quarter-to-year... quarter is the tighter cardinality lever").
	WindowDefaultPolicy90D = WindowDefaultPolicy{
		Name: "90d", TrendAssessment: RelativeWindowTrailing90D, RecentActivityLookup: RelativeWindowTrailing30D,
	}
	// WindowDefaultPolicy365D is the DW0 wider fallback candidate.
	WindowDefaultPolicy365D = WindowDefaultPolicy{
		Name: "365d", TrendAssessment: RelativeWindowTrailing365D, RecentActivityLookup: RelativeWindowTrailing90D,
	}
)

// DefaultRelativeID applies policy's class-to-default table (§2.1) to
// outcome. state_snapshot and WindowClassSourceNone both legitimately have
// NO default (§2.1: "NO window" / "refuse to guess") -- ok=false, never a
// zero RelativeWindowID standing in for absence.
func DefaultRelativeID(outcome WindowClassOutcome, policy WindowDefaultPolicy) (RelativeWindowID, bool) {
	switch outcome.Class {
	case WindowClassTrendAssessment:
		return policy.TrendAssessment, true
	case WindowClassRecentActivityLookup:
		return policy.RecentActivityLookup, true
	default:
		return "", false
	}
}
