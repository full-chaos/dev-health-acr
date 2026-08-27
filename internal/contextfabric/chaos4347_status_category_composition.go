package contextfabric

import (
	"context"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4347: the driver-category -> fact-kind mapping the model's own
// interpretation output rides on (contractsv1.ContextFabricFactRequirement,
// authored directly by Interpret() from the closed FactKind vocabulary) is
// 1:1 for every category. For "status" specifically, that 1:1 mapping
// (contracts/v1's contextFabricDriverCategoryFactKind: status->FactStatus)
// assumes every subject has a single "status" column -- true for work_item
// (StatusProvider reads work_items.status directly, devhealthfacts/
// workitems.go), but there is no repository or team analog: no such column
// exists, so a bare FactStatus requirement for a repository/team subject is
// pruned subject_kind_unsupported by construction, regardless of how much
// OTHER canonical data exists about that subject.
//
// CHAOS-4344 case 23 is exactly this: a real, human-curated oracle case
// (question_class=subject_status, kind=repository, authority=annotation --
// not synthetic) that the pre-existing 1:1 mapping could never answer, even
// though devhealthfacts.NewProviders registers 19 producers and four of
// them (metrics, health, identity, membership) already cover repository.
//
// statusCategoryFactKindComposition is the closed subject-kind -> fact-kind
// SET this ticket adds for status alone (team-lead ruling, 2026-08-26,
// derived from the corpus review above): the fact kinds that are actually
// registered and answer the closest thing to "what is the current state of
// this subject" for a kind that has no discrete status field of its own.
// work_item is deliberately ABSENT -- FactStatus already answers it
// correctly, and this table only ever ADDS coverage, never removes the
// existing 1:1 behavior for the kind that already had it.
var statusCategoryFactKindComposition = map[SubjectKind][]FactKind{
	SubjectRepository: {FactMetrics, FactHealth, FactIdentity},
	SubjectTeam:       {FactHealth, FactWorkload, FactReadiness},
}

// CategoryFactCompositionEvent (CHAOS-4347) reports ONE status-category
// composition decision: a bare FactStatus requirement was expanded into the
// closed fact-kind set for one subject kind this investigation resolved.
// Content-safe by construction: three closed enums (or a closed enum plus a
// slice of closed enums) and nothing else -- no subject identifier, no
// question text, no canonical fact value.
type CategoryFactCompositionEvent struct {
	// RequirementKind is always FactStatus today (the one category this
	// ticket composes) -- carried explicitly rather than assumed, so a
	// future second composed category is visible in the event shape the
	// moment it is added, not inferred from "this event fired at all".
	RequirementKind FactKind
	// SubjectKind is the resolved subject kind that triggered the
	// composition (SubjectRepository or SubjectTeam -- the only two keys
	// statusCategoryFactKindComposition carries).
	SubjectKind SubjectKind
	// ComposedKinds is the fact-kind set substituted in, in the SAME
	// deterministic order the substituted requirements were emitted --
	// never re-derived by a downstream reader from the table, which could
	// drift from what this run actually composed if the table changes
	// after the event was recorded.
	ComposedKinds []FactKind
}

// composeStatusCategoryRequirements (CHAOS-4347) expands every bare
// FactStatus requirement in requirements into statusCategoryFactKindComposition's
// own set, for whichever of subjects' kinds have an entry in that table.
// Requirements for any OTHER category pass through completely unchanged.
//
// A requirement whose own Subjects field is already set scopes the
// expansion to exactly those subjects (mirroring factQuerySubjects' own
// precedence: a requirement's explicit Subjects always wins over the
// investigation-wide set); an empty Subjects field falls back to the
// investigation-wide subjects the SAME way planFactReads' own no-receipts
// branch already does downstream, so this composition and the planner it
// feeds can never disagree about which subjects a bare requirement means.
//
// MIXED SUBJECT KINDS ARE NEVER LOSSY: a subject kind with no composition
// entry (work_item, or any future kind this table has not caught up to)
// keeps its own FactStatus requirement alongside whatever OTHER subject
// kind's composed requirements this call also emits -- a cohort spanning
// both a repository and a work_item must read status facts for the
// work_item exactly as before AND the composed set for the repository, not
// one or the other.
func (e *Engine) composeStatusCategoryRequirements(ctx context.Context, principal storage.Principal, requirements []FactRequirement, subjects []SubjectRef) []FactRequirement {
	if len(requirements) == 0 {
		return requirements
	}
	composed := make([]FactRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		if requirement.Kind != FactStatus {
			composed = append(composed, requirement)
			continue
		}
		effectiveSubjects := requirement.Subjects
		if len(effectiveSubjects) == 0 {
			effectiveSubjects = subjects
		}
		presentKinds := make(map[SubjectKind]bool, len(effectiveSubjects))
		for _, subject := range effectiveSubjects {
			presentKinds[subject.Kind] = true
		}
		var composedSubjectKinds []SubjectKind
		expandedFactKinds := make(map[FactKind]bool)
		uncomposedKindPresent := false
		for subjectKind := range presentKinds {
			expansion, ok := statusCategoryFactKindComposition[subjectKind]
			if !ok {
				uncomposedKindPresent = true
				continue
			}
			composedSubjectKinds = append(composedSubjectKinds, subjectKind)
			for _, factKind := range expansion {
				expandedFactKinds[factKind] = true
			}
		}
		if len(composedSubjectKinds) == 0 {
			// No subject kind this ticket composes for is present (a bare
			// work_item requirement, or an empty/unresolved subject set
			// buildFactQuery already handles downstream) -- unchanged.
			composed = append(composed, requirement)
			continue
		}
		sort.Slice(composedSubjectKinds, func(i, j int) bool { return composedSubjectKinds[i] < composedSubjectKinds[j] })
		if uncomposedKindPresent {
			composed = append(composed, FactRequirement{Kind: FactStatus, Subjects: requirement.Subjects, Parameters: requirement.Parameters})
		}
		orderedFactKinds := make([]FactKind, 0, len(expandedFactKinds))
		for factKind := range expandedFactKinds {
			orderedFactKinds = append(orderedFactKinds, factKind)
		}
		sort.Slice(orderedFactKinds, func(i, j int) bool { return orderedFactKinds[i] < orderedFactKinds[j] })
		for _, factKind := range orderedFactKinds {
			composed = append(composed, FactRequirement{Kind: factKind, Subjects: requirement.Subjects, Parameters: requirement.Parameters})
		}
		for _, subjectKind := range composedSubjectKinds {
			e.recordCategoryFactComposition(ctx, principal, CategoryFactCompositionEvent{
				RequirementKind: FactStatus,
				SubjectKind:     subjectKind,
				ComposedKinds:   statusCategoryFactKindComposition[subjectKind],
			})
		}
	}
	return composed
}

// recordCategoryFactComposition emits one CategoryFactCompositionEvent.
// Needs no type assertion: RecordCategoryFactComposition is a method on
// EngineTelemetry itself, so a sink that drops it fails to compile -- the
// SAME discipline RecordFactScopeExpansion's own doc comment states and
// CHAOS-4085 lost a whole signal to by shipping it optional.
func (e *Engine) recordCategoryFactComposition(ctx context.Context, principal storage.Principal, event CategoryFactCompositionEvent) {
	if e.telemetry == nil {
		return
	}
	e.telemetry.RecordCategoryFactComposition(ctx, principal, event)
}
