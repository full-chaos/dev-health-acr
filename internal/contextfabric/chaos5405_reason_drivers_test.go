package contextfabric

// CHAOS-5405 -- EXECUTED drivers for every decision_reason member.
//
// WHY THIS EXISTS ALONGSIDE THE AST PIN. The AST walk next door proves a
// reason is ASSIGNABLE somewhere in production source. That is not the same as
// REACHABLE: an assignment behind a condition no request can satisfy passes it
// while remaining as dead as an unassigned constant. It is the same
// list-checked-against-a-list trap this ticket has now hit at several levels.
//
// So this is the pin of record. Every member gets a REQUEST SHAPE driven
// through the real resolver entry point, and the reason is read off the
// EMITTED EVENT -- not off the source, not off a helper called directly. A
// member no shape can reach is not documentation; it is removed, with the
// evidence for why it was unreachable stated at the removal.

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// reasonDriverExpander is shaped per case: it either returns a canned result
// or a canned error, so a driver can reach the outcomes that only an expander
// can produce.
type reasonDriverExpander struct {
	result FactScopeExpansionResult
	err    error
	calls  int
}

func (e *reasonDriverExpander) ExpandFactScope(_ context.Context, _ FactScopeExpansionRequest) (FactScopeExpansionResult, error) {
	e.calls++
	return e.result, e.err
}

func TestChaos5405_EveryDecisionReasonHasAnExecutedDriver(t *testing.T) {
	t.Parallel()

	asOf := time.Unix(1000, 0).UTC()
	workItemCaps := map[FactKind]FactCapability{
		FactStatus: planCapability(FactStatus, "status", SubjectWorkItem),
	}

	type driver struct {
		reason   FactScopeDecisionReason
		why      string
		expander FactScopeExpander
		// nilExpander drives the unwired rung, which needs a nil interface.
		nilExpander bool
		timeContext TimeContext
		cancelled   bool
		fillSlots   bool
		// narrowTable installs a table for a rung the ratified 14 cannot reach.
		narrowTable map[FactKind]map[SubjectKind]factScopePolicyRule
	}

	noneRule := map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactStatus: {SubjectProject: {
			Policy: FactScopePolicyNone, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisDirect, Chain: factScopeChainWorkItem,
		}},
	}
	darkRule := map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactStatus: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemStatus, TargetKind: SubjectWorkItem,
			Basis: FactScopeBasisDirect, Enabled: false, Chain: factScopeChainWorkItem,
		}},
	}
	// A REPOSITORY-target rule: the only way to reach the bad-bounds rung, now
	// that any non-current axis is refused earlier for work-item targets.
	repoRule := map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy: FactScopePolicyProjectWorkItemRepository, TargetKind: SubjectRepository,
			Basis: FactScopeBasisActivityProxy, Enabled: true, Chain: factScopeChainRepository,
		}},
	}

	drivers := []driver{
		{reason: FactScopeDecisionExecuted, why: "a current-axis request that runs",
			expander: &reasonDriverExpander{}},
		{reason: FactScopeDecisionPolicyNone, why: "a pair with no policy",
			expander: &reasonDriverExpander{}, narrowTable: noneRule},
		{reason: FactScopeDecisionPolicyDisabled, why: "a named policy shipped dark",
			expander: &reasonDriverExpander{}, narrowTable: darkRule},
		{reason: FactScopeDecisionAxisUnsupported, why: "a well-formed historical request",
			expander:    &reasonDriverExpander{},
			timeContext: TimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &asOf}},
		{reason: FactScopeDecisionTimeBoundsInvalid, why: "a repository-target policy on valid_time with no AsOf",
			expander:    &reasonDriverExpander{},
			timeContext: TimeContext{Axis: contractsv1.ContextFabricTemporalValidTime},
			narrowTable: repoRule},
		{reason: FactScopeDecisionExpanderUnwired, why: "an enabled policy with nothing to run it",
			nilExpander: true},
		{reason: FactScopeDecisionAuthorizationError, why: "the expander reports the authorization check itself failed",
			expander: &reasonDriverExpander{err: errors.Join(ErrFactScopeAuthorization, errors.New("down"))}},
		{reason: FactScopeDecisionBackendError, why: "an ordinary traversal error",
			expander: &reasonDriverExpander{err: errors.New("clickhouse unavailable")}},
		{reason: FactScopeDecisionTimeout, why: "the traversal exceeds its deadline",
			expander: &reasonDriverExpander{err: context.DeadlineExceeded}},
		{reason: FactScopeDecisionCapacityTimeout, why: "the in-flight gate is full and the caller gives up",
			expander: &reasonDriverExpander{}, cancelled: true, fillSlots: true},
		{reason: FactScopeDecisionOriginUnresolved, why: "every origin failed to decode",
			expander: &reasonDriverExpander{result: FactScopeExpansionResult{
				Counts: FactScopeExpansionCounts{AmbiguousOriginCount: 2},
			}}},
		{reason: FactScopeDecisionAttributionSourceUnrecognized, why: "a source outside the closed vocabulary",
			expander: &reasonDriverExpander{result: FactScopeExpansionResult{
				Counts: FactScopeExpansionCounts{CandidateCount: 1, UnknownAttributionSourceCount: 1},
			}}},
	}

	reached := map[FactScopeDecisionReason]bool{}
	for _, d := range drivers {
		d := d
		t.Run(string(d.reason), func(t *testing.T) {
			var expander FactScopeExpander
			if !d.nilExpander {
				expander = d.expander
			}
			resolver := NewFactReadScopeResolverWithPolicies(expander, d.narrowTable)
			if d.fillSlots {
				for i := 0; i < maxWorkItemScopeInFlight; i++ {
					resolver.workItemSlots <- struct{}{}
				}
			}
			ctx := context.Background()
			if d.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			kind := FactStatus
			caps := workItemCaps
			if d.narrowTable != nil {
				for k := range d.narrowTable {
					kind = k
				}
				caps = map[FactKind]FactCapability{kind: planCapability(kind, "p", SubjectWorkItem, SubjectRepository)}
			}
			tctx := d.timeContext
			if tctx.Axis == "" {
				tctx = TimeContext{Axis: TemporalCurrent}
			}
			scope := resolver.Resolve(ctx, storage.Principal{OrgID: "org_1"},
				newFactScopeResolveInput(scopeTimeRequest(
					[]SubjectRef{scopeProject}, []FactRequirement{{Kind: kind}}, tctx,
				)), caps)

			var got FactScopeDecisionReason
			var found bool
			for _, event := range scope.Events {
				if event.RequirementKind == kind {
					got, found = event.DecisionReason, true
				}
			}
			if !found {
				t.Fatalf("%s: no event emitted -- a reason nothing emits is unreachable, whatever the source says", d.reason)
			}
			if got != d.reason {
				t.Fatalf("%s (%s): emitted decision_reason = %q, want %q", d.reason, d.why, got, d.reason)
			}
			reached[d.reason] = true
		})
	}

	// CLOSURE, in both directions: every declared member must have a driver
	// above, so adding one without a request shape that reaches it fails here
	// rather than shipping as an alertable-but-silent field.
	for _, reason := range factScopeDecisionReasons {
		if !reached[reason] {
			t.Fatalf("decision reason %q has no EXECUTED driver -- assignable in source is not the same as reachable by a request", reason)
		}
	}
}
