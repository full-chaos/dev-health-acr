package v1

import "encoding/json"

// This file is the ONE definition of "how big is a Context Fabric answer",
// called by BOTH planes (CHAOS-4636 / intent-engine design §6.3 stage 3).
//
// Why it lives here rather than where the enforcement lives. The budget was
// specified three times before this one, and revision 3 failed on an import
// cycle: the measurement was `MarshalContextFabricResponse` in
// internal/api, but internal/api imports internal/contextfabric and NOT the
// reverse, so the engine could never call it. `go list -deps ./internal/api`
// contains .../internal/contextfabric; `go list -deps ./internal/contextfabric`
// does not contain .../internal/api. internal/contracts/v1 is imported by
// both, so it is the only place a single definition can sit.
//
// Revision 3 also tried to TRIM a composed answer to fit. That is abandoned
// on purpose and must not be reintroduced here: by the time the route holds a
// result, render selection, Validate/ValidateAgainst and persistence have all
// run, so dropping a driver can orphan a ContextFabricRenderShape point that
// cites it, break grounding closure, and make the STORED and the SERVED
// answer diverge. This file therefore only ever MEASURES. What to do about an
// over-budget answer is the engine's decision (re-synthesize once with a
// smaller input; then a planned, explained refusal), never a mutation applied
// down here.
//
// The route's own gate becomes an assertion over these same numbers rather
// than a second, drifting measurement -- the "one derivation, used
// everywhere" discipline investigationScopeSubjectSet already documents in
// internal/contextfabric/fact_planner.go.

// ContextFabricResultItemCounts is the per-collection breakdown of what the
// item budget charges against a Context Fabric investigation result.
//
// The set of charged collections is deliberately explicit rather than
// reflective: a new result collection must be a DECISION to charge or not to
// charge it, made in this struct, not something a reflective walk silently
// starts billing on the day the field is added.
type ContextFabricResultItemCounts struct {
	Candidates    int `json:"candidates"`
	Drivers       int `json:"drivers"`
	Paths         int `json:"paths"`
	RemainingWork int `json:"remaining_work"`
	ReadinessGaps int `json:"readiness_gaps"`
	Conflicts     int `json:"conflicts"`
	ClaimedFacts  int `json:"claimed_facts"`
	CohortMembers int `json:"cohort_members"`
}

// Total is every charged item, Paths included. It is what a caller's usage
// accounting reports.
func (c ContextFabricResultItemCounts) Total() int {
	return c.Candidates + c.Drivers + c.Paths + c.RemainingWork + c.ReadinessGaps +
		c.Conflicts + c.ClaimedFacts + c.CohortMembers
}

// Budgeted is Total minus Paths: graph-evidence relationship paths are
// EXCLUDED from the item budget (CHAOS-4523). Keeping the exclusion here,
// beside the count it excludes from, is why the engine and the route cannot
// disagree about it.
func (c ContextFabricResultItemCounts) Budgeted() int { return c.Total() - c.Paths }

// CountContextFabricResultItems charges every collection the item budget
// covers. It is total, not partial: a result whose Cohort is nil charges zero
// cohort members rather than being un-countable.
func CountContextFabricResultItems(result ContextFabricInvestigationResult) ContextFabricResultItemCounts {
	counts := ContextFabricResultItemCounts{
		Candidates:    len(result.SubjectResolution.Candidates),
		Drivers:       len(result.Drivers),
		Paths:         len(result.Paths),
		RemainingWork: len(result.RemainingWork),
		ReadinessGaps: len(result.ReadinessGaps),
		Conflicts:     len(result.Conflicts),
		ClaimedFacts:  len(result.ClaimedFacts),
	}
	if result.Cohort != nil {
		counts.CohortMembers = len(result.Cohort.Members)
	}
	return counts
}

// ContextFabricResponseBudget is the effective ceiling a result is measured
// against. MaxItems bounds Budgeted(); MaxSerializedBytes bounds the marshaled
// response body.
//
// A zero field means "unbounded on that axis" so a caller that knows only one
// of the two (the engine before it has the route's config, a test) is not
// forced to invent the other.
type ContextFabricResponseBudget struct {
	MaxItems           int   `json:"max_items"`
	MaxSerializedBytes int64 `json:"max_serialized_bytes"`
}

// ContextFabricBudgetOverrun is the CLOSED vocabulary for which axis a result
// exceeded. It is closed because it reaches telemetry and a caller-facing
// refusal, and both of those are dashboards and contracts rather than prose.
type ContextFabricBudgetOverrun string

const (
	// ContextFabricBudgetFits is the zero value: the result is inside both
	// axes. It is spelled rather than left as "" so a switch over this
	// vocabulary has no unnamed arm.
	ContextFabricBudgetFits ContextFabricBudgetOverrun = "fits"
	// ContextFabricBudgetOverrunItems means Budgeted() exceeded MaxItems.
	ContextFabricBudgetOverrunItems ContextFabricBudgetOverrun = "items"
	// ContextFabricBudgetOverrunBytes means the marshaled body exceeded
	// MaxSerializedBytes.
	ContextFabricBudgetOverrunBytes ContextFabricBudgetOverrun = "bytes"
)

var contextFabricBudgetOverruns = [3]ContextFabricBudgetOverrun{
	ContextFabricBudgetFits,
	ContextFabricBudgetOverrunItems,
	ContextFabricBudgetOverrunBytes,
}

// ContextFabricBudgetOverrunCount is the size of the closed overrun
// vocabulary, as a compile-time constant.
const ContextFabricBudgetOverrunCount = len(contextFabricBudgetOverruns)

// ContextFabricBudgetOverrunVocabulary returns the closed overrun vocabulary
// in published order. The return type is an ARRAY, so the value is copied to
// the caller -- see ContextFabricFactKindVocabulary for why that matters.
func ContextFabricBudgetOverrunVocabulary() [ContextFabricBudgetOverrunCount]ContextFabricBudgetOverrun {
	return contextFabricBudgetOverruns
}

// ValidContextFabricBudgetOverrun reports whether value is a member of the
// closed overrun vocabulary.
func ValidContextFabricBudgetOverrun(value ContextFabricBudgetOverrun) bool {
	for _, member := range contextFabricBudgetOverruns {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricResponseMeasurement is one measurement of one assembled
// result: the item breakdown, and the size of the body that would be served
// for it.
type ContextFabricResponseMeasurement struct {
	Items ContextFabricResultItemCounts `json:"items"`
	Bytes int64                         `json:"bytes"`
	// Attribution is the per-bucket split of Items.Budgeted() for the SAME
	// document, computed at the SAME moment by MeasureContextFabricResponse.
	//
	// It lives on the measurement rather than being derived at each reader
	// for one reason: a split taken from a different document from the
	// count beside it is worse than no split at all, and every caller that
	// reports both would otherwise have to be trusted to pass the same
	// result to two functions. Here it cannot pass two.
	Attribution ContextFabricItemAttribution `json:"attribution"`
}

// MeasureContextFabricResponse counts and sizes result with the SAME encoder
// the investigation route serves with, so a fit decided here is a fit there.
//
// The error is the marshal error and nothing else. A result that cannot be
// marshaled is not "too large" -- it is a server defect, and conflating the
// two would let a serialization bug present as a budget refusal.
func MeasureContextFabricResponse(result ContextFabricInvestigationResult) (ContextFabricResponseMeasurement, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return ContextFabricResponseMeasurement{}, err
	}
	return ContextFabricResponseMeasurement{
		Items: CountContextFabricResultItems(result),
		Bytes: int64(len(encoded)),
		// Same result, same call. Attribution.Total() therefore equals
		// Items.Budgeted() by construction here, and
		// TestAMeasurementSplitsExactlyTheQuantityItCharges asserts it.
		Attribution: AttributeContextFabricResultItems(result),
	}, nil
}

// Overrun reports which axis, if any, this measurement exceeds under budget.
//
// Items are checked before bytes, deliberately and stably: the two can be
// exceeded together, a single closed value must be reported for telemetry and
// for the refusal, and the item budget is the one a caller can act on by
// asking a narrower question. A zero on either budget axis disables that axis.
func (m ContextFabricResponseMeasurement) Overrun(budget ContextFabricResponseBudget) ContextFabricBudgetOverrun {
	if budget.MaxItems > 0 && m.Items.Budgeted() > budget.MaxItems {
		return ContextFabricBudgetOverrunItems
	}
	if budget.MaxSerializedBytes > 0 && m.Bytes > budget.MaxSerializedBytes {
		return ContextFabricBudgetOverrunBytes
	}
	return ContextFabricBudgetFits
}

// Fits is Overrun's boolean form, for the common call site that does not need
// to know which axis failed.
func (m ContextFabricResponseMeasurement) Fits(budget ContextFabricResponseBudget) bool {
	return m.Overrun(budget) == ContextFabricBudgetFits
}

// ContextFabricItemBucket is the CLOSED vocabulary naming WHAT a charged item
// is about, when a cohort has a GROUP axis (CHAOS-4636 / S5, observing half).
//
// WHY THE SPLIT HAS TO EXIST AT ALL. ContextFabricResultItemCounts above is a
// per-COLLECTION breakdown -- drivers, claims, members -- and every one of
// those totals is flat across the whole answer. "This answer spent most of its
// budget on one group" is not a statement any of those numbers can support or
// refute, because none of them knows what an item is ABOUT. An operator
// reading a 413 today can see that thirty-four items were charged and cannot
// see where they went.
//
// Closed, and spelled rather than left to a bare string, for the same reason
// every other vocabulary in this file is: it reaches telemetry, and a
// dashboard filtering on a typo is worse than one filtering on nothing.
type ContextFabricItemBucket string

const (
	// ContextFabricItemBucketGlobal is an item that belongs to the answer
	// rather than to any group: resolution candidates, and findings that
	// name no member and no group.
	ContextFabricItemBucketGlobal ContextFabricItemBucket = "global"
	// ContextFabricItemBucketMember is an item attributable to exactly one
	// cohort MEMBER -- the member row itself, a driver whose affected
	// subjects are members, a claim whose subject is a member.
	ContextFabricItemBucketMember ContextFabricItemBucket = "member"
	// ContextFabricItemBucketGroup is an item naming exactly one GROUP
	// subject. These became possible only when the group entity became
	// citable (lever 3): before that, a driver about a team was rejected,
	// so this bucket would have been empty by construction.
	ContextFabricItemBucketGroup ContextFabricItemBucket = "group"
	// ContextFabricItemBucketMultiGroup is an item naming SEVERAL groups.
	// It is a member of this vocabulary rather than an error because the
	// relational citation rule deliberately permits it: ownership is a
	// relation, not a partition, so one driver may legitimately be about
	// two teams. A split that could not express this would have to either
	// reject a valid answer or silently pick one group for it.
	ContextFabricItemBucketMultiGroup ContextFabricItemBucket = "multi_group"
)

var contextFabricItemBuckets = [4]ContextFabricItemBucket{
	ContextFabricItemBucketGlobal,
	ContextFabricItemBucketMember,
	ContextFabricItemBucketGroup,
	ContextFabricItemBucketMultiGroup,
}

// ContextFabricItemBucketCount is the closed vocabulary's size.
const ContextFabricItemBucketCount = len(contextFabricItemBuckets)

// ContextFabricItemBucketVocabulary returns the closed bucket vocabulary in
// published order, as an array so the value is copied to the caller.
func ContextFabricItemBucketVocabulary() [ContextFabricItemBucketCount]ContextFabricItemBucket {
	return contextFabricItemBuckets
}

// ValidContextFabricItemBucket reports membership of the closed vocabulary.
// The empty value is not a member.
func ValidContextFabricItemBucket(value ContextFabricItemBucket) bool {
	for _, member := range contextFabricItemBuckets {
		if member == value {
			return true
		}
	}
	return false
}

// ContextFabricItemAttribution is the per-bucket split of the SAME quantity
// ContextFabricResultItemCounts.Budgeted() reports.
//
// THE INVARIANT THAT MAKES IT WORTH HAVING: Total() here equals Budgeted()
// there, for every result. If the two can disagree, the split describes a
// different answer from the one the budget enforces, and every per-bucket
// number is decoration. Paths are excluded from both, for the one reason
// stated on Budgeted().
//
// It is a MEASUREMENT and nothing else. It apportions nothing, reserves
// nothing and bounds nothing -- see the decision record in
// docs/design/context-fabric-architecture-diagrams.md section 10c for why the
// apportioning half was deliberately left out of the change that added this.
type ContextFabricItemAttribution struct {
	Global     int `json:"global"`
	Member     int `json:"member"`
	Group      int `json:"group"`
	MultiGroup int `json:"multi_group"`
}

// Total is every attributed item. It is defined to equal
// ContextFabricResultItemCounts.Budgeted() for the same result.
func (a ContextFabricItemAttribution) Total() int {
	return a.Global + a.Member + a.Group + a.MultiGroup
}

// AttributeContextFabricResultItems splits the SAME quantity
// CountContextFabricResultItems(...).Budgeted() reports into the four buckets.
//
// THE INVARIANT, and it is what makes this function worth having rather than a
// second opinion about the same result: for every result,
//
//	AttributeContextFabricResultItems(r).Total() == CountContextFabricResultItems(r).Budgeted()
//
// TestEveryChargedItemIsAttributedToExactlyOneBucket pins it, and
// MeasureContextFabricResponse computes both from one document at one moment
// so a caller cannot pair a split of one answer with a count of another.
//
// EXACTLY ONE BUCKET PER ITEM. An item naming several groups is charged to
// MultiGroup once -- not once per group. What an item IS and who would PAY for
// it under some apportioning rule are different questions; answering the
// second here would break the totals-sum invariant the moment the rule
// changed, and that invariant is the one property this function exists to
// hold.
//
// Paths are excluded, for the single reason stated on Budgeted().
func AttributeContextFabricResultItems(result ContextFabricInvestigationResult) ContextFabricItemAttribution {
	members := map[string]struct{}{}
	groups := map[string]struct{}{}
	if result.Cohort != nil {
		for _, member := range result.Cohort.Members {
			members[contextFabricSubjectBucketKey(member.Subject)] = struct{}{}
		}
		for _, group := range result.Cohort.Groups {
			groups[contextFabricSubjectBucketKey(group.Subject)] = struct{}{}
		}
	}

	attribution := ContextFabricItemAttribution{}
	// Cohort member rows: one item each, member-attributed by definition.
	if result.Cohort != nil {
		attribution.Member += len(result.Cohort.Members)
	}
	// Resolution candidates belong to the ANSWER, never to a group: they
	// are alternatives the investigation did not commit to, so charging one
	// to a group would attribute to a group a subject it does not own.
	attribution.Global += len(result.SubjectResolution.Candidates)

	for _, driver := range result.Drivers {
		attribution.charge(contextFabricSubjectsBucket(driver.AffectedSubjects, members, groups))
	}
	for _, findings := range [][]ContextFabricFinding{result.RemainingWork, result.ReadinessGaps, result.Conflicts} {
		for _, finding := range findings {
			attribution.charge(contextFabricSubjectsBucket(finding.Subjects, members, groups))
		}
	}
	for _, claim := range result.ClaimedFacts {
		attribution.charge(contextFabricSubjectsBucket([]ContextFabricSubjectRef{claim.Subject}, members, groups))
	}
	return attribution
}

// charge adds one item to the named bucket.
func (a *ContextFabricItemAttribution) charge(bucket ContextFabricItemBucket) {
	switch bucket {
	case ContextFabricItemBucketMember:
		a.Member++
	case ContextFabricItemBucketGroup:
		a.Group++
	case ContextFabricItemBucketMultiGroup:
		a.MultiGroup++
	default:
		a.Global++
	}
}

// contextFabricSubjectsBucket decides ONE bucket for an item from the subjects
// it names.
//
// Precedence, and each step is a decision rather than an ordering convenience:
//   - two or more DISTINCT groups named  -> multi_group. Counted by distinct
//     group, so a driver naming the same group twice is not promoted out of
//     the group bucket by a duplicate.
//   - exactly one group named            -> group, EVEN IF members are also
//     named. A driver about a team that cites its projects is an item about
//     the team; charging it to a member would leave the group's own line
//     unable to see the item that is most characteristically its own.
//   - otherwise a member named           -> member.
//   - nothing recognised                 -> global. Fail-safe direction: an
//     unrecognised subject charges the shared pool rather than silently
//     inflating some group's reading.
func contextFabricSubjectsBucket(subjects []ContextFabricSubjectRef, members, groups map[string]struct{}) ContextFabricItemBucket {
	namedGroups := map[string]struct{}{}
	sawMember := false
	for _, subject := range subjects {
		key := contextFabricSubjectBucketKey(subject)
		if _, isGroup := groups[key]; isGroup {
			namedGroups[key] = struct{}{}
			continue
		}
		if _, isMember := members[key]; isMember {
			sawMember = true
		}
	}
	switch {
	case len(namedGroups) > 1:
		return ContextFabricItemBucketMultiGroup
	case len(namedGroups) == 1:
		return ContextFabricItemBucketGroup
	case sawMember:
		return ContextFabricItemBucketMember
	default:
		return ContextFabricItemBucketGlobal
	}
}

// contextFabricSubjectBucketKey keys a subject by kind AND canonical id, the
// same composite the synthesis allow-set uses, so a member and a group that
// happen to share an id are never confused for one another.
func contextFabricSubjectBucketKey(subject ContextFabricSubjectRef) string {
	return string(subject.Kind) + "\x00" + subject.CanonicalID
}
