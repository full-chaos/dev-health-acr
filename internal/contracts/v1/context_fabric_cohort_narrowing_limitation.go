package v1

import (
	"regexp"
	"strconv"
	"strings"
)

// CHAOS-6561: the cohort-narrowing disclosure.
//
// WHAT IT CLOSES. A cohort answer whose member list the plan narrowed to fit
// the response budget told the reader only `served 5 / declared 11` on a
// requirement row, with a coverage cause that read as "the evidence for the
// rest was not found". The two steps that actually cut the list -- the
// pre-read member-limit clamp and the stage-3 re-synthesis over fewer members
// -- were recorded on the plan and in telemetry, and nowhere a reader of the
// answer looks. North Star checks 5 and 12: a partial answer is useful only
// when the reader is told it is partial, and by how much, and why.
//
// INTERPOLATED WITH COUNTS, and that is a deliberate, narrower exception to the
// "no count" rule the grouping disclosures above follow. The reader needs the
// FROM and the TO to act on this sentence (ask a narrower question, or allow a
// larger budget), and the counts are the whole disclosure. The hazard the
// grouping disclosures avoid -- a recogniser that accepts arbitrary digit runs
// widens what a model caveat can impersonate -- is bounded here by a STRICT
// parse: every step is one of a closed set of fixed phrases, every number is a
// canonical decimal integer of bounded width, the subject kind must be a
// member of the closed kind vocabulary, and the counts must form a reducing
// chain from the declared total to the served total. A string that only looks
// like this sentence is not recognised.
//
// DETERMINISTIC. No model writes it; the engine composes it from the plan's own
// recorded narrowing steps and the served member count.

// ContextFabricCohortNarrowingDisclosureStep is ONE step of the member chain
// the disclosure states.
//
// Before and After are MEMBER counts -- how many members the list held either
// side of the step. LimitBefore and LimitAfter are the member LIMIT the
// pre-read cardinality clamp moved, and are read only for that stage: the
// clamp's own record is a pair of ceilings, not members, and the sentence says
// which is which. LimitBefore is zero when no limit was requested before the
// clamp set one.
type ContextFabricCohortNarrowingDisclosureStep struct {
	Stage       ContextFabricPlanNarrowingStage
	Overrun     ContextFabricBudgetOverrun
	Before      int
	After       int
	LimitBefore int
	LimitAfter  int
}

const (
	contextFabricCohortNarrowingPrefix     = "This answer lists "
	contextFabricCohortNarrowingOf         = " of the "
	contextFabricCohortNarrowingSubjects   = " subjects found, because the list was narrowed to fit the response budget: "
	contextFabricCohortNarrowingStepSep    = "; "
	contextFabricCohortNarrowingSuffix     = ". Ask a narrower question or allow a larger response budget to see the rest."
	contextFabricCohortNarrowingTo         = " to "
	contextFabricCohortNarrowingClampFrom  = " when the plan lowered the member limit from "
	contextFabricCohortNarrowingClampSet   = " when the plan set a member limit of "
	contextFabricCohortNarrowingPreRead    = " before retrieval"
	contextFabricCohortNarrowingSynthInput = " before synthesis, to leave room for the findings synthesis adds"
	contextFabricCohortNarrowingItems      = " after the assembled answer exceeded the item budget"
	contextFabricCohortNarrowingBytes      = " after the assembled answer exceeded the size budget"
	contextFabricCohortNarrowingResynth    = " when the assembled answer was re-synthesized over fewer members"
	// contextFabricCohortNarrowingCountMax bounds every interpolated count, so
	// the recogniser's digit runs are bounded too. Far above any cohort this
	// service can discover.
	contextFabricCohortNarrowingCountMax = 999999999
)

// ContextFabricCohortNarrowingLimitation composes the disclosure. THE SOLE
// COMPOSER: recognition is a parse over exactly what this writes.
//
// The second return is false -- and nothing is composed -- unless the input
// is a chain that the sentence can state truthfully: a member kind in the
// closed vocabulary, served strictly below declared, at least one step, every
// step a strict reduction on a known stage, the first step starting at
// declared, each step starting where the previous one ended, and the last
// step ending at served. An unreconciled chain is not disclosed with invented
// numbers; the caller logs it instead.
func ContextFabricCohortNarrowingLimitation(kind ContextFabricSubjectKind, declared, served int, steps []ContextFabricCohortNarrowingDisclosureStep) (string, bool) {
	if !contextFabricCohortNarrowingChainValid(kind, declared, served, steps) {
		return "", false
	}
	var builder strings.Builder
	builder.WriteString(contextFabricCohortNarrowingPrefix)
	builder.WriteString(strconv.Itoa(served))
	builder.WriteString(contextFabricCohortNarrowingOf)
	builder.WriteString(strconv.Itoa(declared))
	builder.WriteString(" ")
	builder.WriteString(string(kind))
	builder.WriteString(contextFabricCohortNarrowingSubjects)
	for index, step := range steps {
		if index > 0 {
			builder.WriteString(contextFabricCohortNarrowingStepSep)
		}
		builder.WriteString(strconv.Itoa(step.Before))
		builder.WriteString(contextFabricCohortNarrowingTo)
		builder.WriteString(strconv.Itoa(step.After))
		builder.WriteString(contextFabricCohortNarrowingStepCause(step))
	}
	builder.WriteString(contextFabricCohortNarrowingSuffix)
	return builder.String(), true
}

// contextFabricCohortNarrowingStepCause is the fixed phrase for one step. Its
// arms are the closed stage vocabulary; a stage outside it is refused by the
// chain check before this runs.
func contextFabricCohortNarrowingStepCause(step ContextFabricCohortNarrowingDisclosureStep) string {
	switch step.Stage {
	case ContextFabricPlanNarrowingCardinality:
		if step.LimitBefore > 0 {
			return contextFabricCohortNarrowingClampFrom + strconv.Itoa(step.LimitBefore) +
				contextFabricCohortNarrowingTo + strconv.Itoa(step.LimitAfter) + contextFabricCohortNarrowingPreRead
		}
		return contextFabricCohortNarrowingClampSet + strconv.Itoa(step.LimitAfter) + contextFabricCohortNarrowingPreRead
	case ContextFabricPlanNarrowingSynthesisInput:
		return contextFabricCohortNarrowingSynthInput
	default:
		switch step.Overrun {
		case ContextFabricBudgetOverrunItems:
			return contextFabricCohortNarrowingItems
		case ContextFabricBudgetOverrunBytes:
			return contextFabricCohortNarrowingBytes
		default:
			return contextFabricCohortNarrowingResynth
		}
	}
}

func contextFabricCohortNarrowingCountValid(value int) bool {
	return value >= 0 && value <= contextFabricCohortNarrowingCountMax
}

func contextFabricCohortNarrowingChainValid(kind ContextFabricSubjectKind, declared, served int, steps []ContextFabricCohortNarrowingDisclosureStep) bool {
	if !ValidContextFabricSubjectKind(kind) {
		return false
	}
	if !contextFabricCohortNarrowingCountValid(declared) || !contextFabricCohortNarrowingCountValid(served) || served >= declared {
		return false
	}
	if len(steps) == 0 || len(steps) > ContextFabricPlanNarrowingMaxCount {
		return false
	}
	current := declared
	for _, step := range steps {
		if !ValidContextFabricPlanNarrowingStage(step.Stage) {
			return false
		}
		if step.Before != current || step.After >= step.Before || step.After < 0 {
			return false
		}
		if step.Stage == ContextFabricPlanNarrowingCardinality {
			if !contextFabricCohortNarrowingCountValid(step.LimitBefore) || !contextFabricCohortNarrowingCountValid(step.LimitAfter) {
				return false
			}
			// The clamp is what cut the list, so its limit is at most the
			// members that survived it -- and when it LOWERED a requested
			// limit, the new limit is below the old one.
			if step.LimitAfter < step.After || (step.LimitBefore > 0 && step.LimitAfter >= step.LimitBefore) {
				return false
			}
		}
		current = step.After
	}
	return current == served
}

// contextFabricCohortNarrowingPattern is the strict grammar of every sentence
// ContextFabricCohortNarrowingLimitation can write. Built from the same
// constants the composer uses, so the two cannot drift apart in wording.
var contextFabricCohortNarrowingPattern = func() *regexp.Regexp {
	number := `(0|[1-9][0-9]{0,8})`
	q := regexp.QuoteMeta
	stepCause := `(?:` +
		q(contextFabricCohortNarrowingClampFrom) + number + q(contextFabricCohortNarrowingTo) + number + q(contextFabricCohortNarrowingPreRead) + `|` +
		q(contextFabricCohortNarrowingClampSet) + number + q(contextFabricCohortNarrowingPreRead) + `|` +
		q(contextFabricCohortNarrowingSynthInput) + `|` +
		q(contextFabricCohortNarrowingItems) + `|` +
		q(contextFabricCohortNarrowingBytes) + `|` +
		q(contextFabricCohortNarrowingResynth) + `)`
	step := number + q(contextFabricCohortNarrowingTo) + number + stepCause
	return regexp.MustCompile(`^` + q(contextFabricCohortNarrowingPrefix) + number + q(contextFabricCohortNarrowingOf) + number +
		` ([a-z_]+)` + q(contextFabricCohortNarrowingSubjects) +
		`(` + step + `(?:` + q(contextFabricCohortNarrowingStepSep) + step + `)*)` +
		q(contextFabricCohortNarrowingSuffix) + `$`)
}()

// contextFabricCohortNarrowingStepPattern parses one step inside the chain the
// whole-sentence pattern already accepted.
var contextFabricCohortNarrowingStepPattern = func() *regexp.Regexp {
	number := `(0|[1-9][0-9]{0,8})`
	q := regexp.QuoteMeta
	return regexp.MustCompile(`^` + number + q(contextFabricCohortNarrowingTo) + number + `(.*)$`)
}()

// IsContextFabricCohortNarrowingLimitation reports whether a limitation is one
// ContextFabricCohortNarrowingLimitation could have composed.
//
// A PARSE, and then a RE-COMPOSITION: the counts and the kind are read back,
// the chain is re-validated, and the sentence is recomposed and compared for
// equality. Only a string the composer itself would write is recognised --
// so a model caveat that borrows this wording, or a chain whose counts do not
// add up, is not made undisplaceable.
func IsContextFabricCohortNarrowingLimitation(limitation string) bool {
	match := contextFabricCohortNarrowingPattern.FindStringSubmatch(limitation)
	if match == nil {
		return false
	}
	served, errServed := strconv.Atoi(match[1])
	declared, errDeclared := strconv.Atoi(match[2])
	if errServed != nil || errDeclared != nil {
		return false
	}
	kind := ContextFabricSubjectKind(match[3])
	var steps []ContextFabricCohortNarrowingDisclosureStep
	for _, text := range strings.Split(match[4], contextFabricCohortNarrowingStepSep) {
		step, ok := parseContextFabricCohortNarrowingStep(text)
		if !ok {
			return false
		}
		steps = append(steps, step)
	}
	recomposed, ok := ContextFabricCohortNarrowingLimitation(kind, declared, served, steps)
	return ok && recomposed == limitation
}

func parseContextFabricCohortNarrowingStep(text string) (ContextFabricCohortNarrowingDisclosureStep, bool) {
	match := contextFabricCohortNarrowingStepPattern.FindStringSubmatch(text)
	if match == nil {
		return ContextFabricCohortNarrowingDisclosureStep{}, false
	}
	before, errBefore := strconv.Atoi(match[1])
	after, errAfter := strconv.Atoi(match[2])
	if errBefore != nil || errAfter != nil {
		return ContextFabricCohortNarrowingDisclosureStep{}, false
	}
	step := ContextFabricCohortNarrowingDisclosureStep{Before: before, After: after}
	cause := match[3]
	switch {
	case cause == contextFabricCohortNarrowingSynthInput:
		step.Stage = ContextFabricPlanNarrowingSynthesisInput
	case cause == contextFabricCohortNarrowingItems:
		step.Stage = ContextFabricPlanNarrowingAssembledResult
		step.Overrun = ContextFabricBudgetOverrunItems
	case cause == contextFabricCohortNarrowingBytes:
		step.Stage = ContextFabricPlanNarrowingAssembledResult
		step.Overrun = ContextFabricBudgetOverrunBytes
	case cause == contextFabricCohortNarrowingResynth:
		step.Stage = ContextFabricPlanNarrowingAssembledResult
	case strings.HasPrefix(cause, contextFabricCohortNarrowingClampFrom):
		body, ok := strings.CutSuffix(strings.TrimPrefix(cause, contextFabricCohortNarrowingClampFrom), contextFabricCohortNarrowingPreRead)
		if !ok {
			return ContextFabricCohortNarrowingDisclosureStep{}, false
		}
		from, to, ok := strings.Cut(body, contextFabricCohortNarrowingTo)
		if !ok {
			return ContextFabricCohortNarrowingDisclosureStep{}, false
		}
		limitBefore, errFrom := strconv.Atoi(from)
		limitAfter, errTo := strconv.Atoi(to)
		if errFrom != nil || errTo != nil {
			return ContextFabricCohortNarrowingDisclosureStep{}, false
		}
		step.Stage = ContextFabricPlanNarrowingCardinality
		step.LimitBefore, step.LimitAfter = limitBefore, limitAfter
	case strings.HasPrefix(cause, contextFabricCohortNarrowingClampSet):
		body, ok := strings.CutSuffix(strings.TrimPrefix(cause, contextFabricCohortNarrowingClampSet), contextFabricCohortNarrowingPreRead)
		if !ok {
			return ContextFabricCohortNarrowingDisclosureStep{}, false
		}
		limitAfter, err := strconv.Atoi(body)
		if err != nil {
			return ContextFabricCohortNarrowingDisclosureStep{}, false
		}
		step.Stage = ContextFabricPlanNarrowingCardinality
		step.LimitAfter = limitAfter
	default:
		return ContextFabricCohortNarrowingDisclosureStep{}, false
	}
	return step, true
}
