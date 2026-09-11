package contextfabric

// CHAOS-5405 -- the temporal-axis gate, at BOTH boundaries.
//
// The fourteen work-item policies are `_v1` and serve the CURRENT axis only.
// Before this, only an observed_time request or one with MISSING bounds was
// refused at the resolver. A WELL-FORMED valid_time request -- named policy,
// enabled, bounds present, expander wired -- passed every rung, reached the
// expander, and was refused by its own boundary check. That refusal came back
// as an untyped error and classified as backend_unavailable.
//
// So an axis refusal reported itself as a GRAPH-BACKEND FAULT. An operator
// alerting on backend errors would page because a caller asked a historical
// question, and `axis_unsupported` -- which exists for precisely this -- never
// fired on the one path where it mattered. Found by writing out the input
// shape space rather than by a test failing.
//
// Two gates now, and the second one cannot lie: the resolver refuses any
// non-current axis (policy_unavailable / axis_unsupported, "we did not look"),
// and the expander's own boundary wraps a typed sentinel so that if the first
// gate ever stops holding, the failure still reads axis_unsupported instead of
// blaming infrastructure.

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// axisGateExpander stands in for the real one: it refuses a non-current axis
// exactly as devhealthfacts does, WRAPPING the shared sentinel.
type axisGateExpander struct{ calls int }

func (e *axisGateExpander) ExpandFactScope(_ context.Context, request FactScopeExpansionRequest) (FactScopeExpansionResult, error) {
	e.calls++
	switch request.TimeContext.Axis {
	case "", contractsv1.ContextFabricTemporalCurrent:
		return FactScopeExpansionResult{}, nil
	default:
		return FactScopeExpansionResult{}, errors.Join(ErrFactScopeAxisUnsupported, errors.New("axis refused"))
	}
}

// TestChaos5405_TheResolverRefusesEveryNonCurrentAxisAtTheGate is the primary
// fix: the refusal happens BEFORE the expander is called at all.
func TestChaos5405_TheResolverRefusesEveryNonCurrentAxisAtTheGate(t *testing.T) {
	t.Parallel()
	asOf := time.Unix(1000, 0).UTC()
	start, end := time.Unix(500, 0).UTC(), time.Unix(1500, 0).UTC()
	for _, tc := range []struct {
		name string
		tctx TimeContext
	}{
		{"observed_time", TimeContext{Axis: contractsv1.ContextFabricTemporalObservedTime}},
		{"valid_time_without_bounds", TimeContext{Axis: contractsv1.ContextFabricTemporalValidTime}},
		{"valid_time_WITH_bounds", TimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &asOf}},
		{"range_without_bounds", TimeContext{Axis: contractsv1.ContextFabricTemporalRange}},
		{"range_WITH_bounds", TimeContext{Axis: contractsv1.ContextFabricTemporalRange, Start: &start, End: &end}},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			expander := &axisGateExpander{}
			scope := NewFactReadScopeResolver(expander).Resolve(
				context.Background(), storage.Principal{OrgID: "org_1"},
				newFactScopeResolveInput(scopeTimeRequest(
					[]SubjectRef{scopeProject},
					[]FactRequirement{{Kind: FactStatus}},
					tc.tctx,
				)),
				map[FactKind]FactCapability{FactStatus: planCapability(FactStatus, "status", SubjectWorkItem)},
			)
			if expander.calls != 0 {
				t.Fatalf("%s: the expander ran %d time(s) -- a refused axis must never reach it", tc.name, expander.calls)
			}
			var seen bool
			for _, event := range scope.Events {
				if event.RequirementKind != FactStatus {
					continue
				}
				seen = true
				if event.Outcome != FactScopePolicyUnavailable {
					t.Fatalf("%s: outcome = %q, want %q -- a gate refusal is 'we did not look', not an execution failure", tc.name, event.Outcome, FactScopePolicyUnavailable)
				}
				if event.DecisionReason != FactScopeDecisionAxisUnsupported {
					t.Fatalf("%s: decision_reason = %q, want %q", tc.name, event.DecisionReason, FactScopeDecisionAxisUnsupported)
				}
			}
			if !seen {
				t.Fatalf("%s: no event was recorded for the requirement", tc.name)
			}
		})
	}
}

// TestChaos5405_TheExpandersOwnAxisRefusalNeverReadsAsABackendFault is the
// SECOND gate. It is reachable only if the first stops holding, which is
// exactly when a misleading classification would do the most damage.
func TestChaos5405_TheExpandersOwnAxisRefusalNeverReadsAsABackendFault(t *testing.T) {
	t.Parallel()
	err := errors.Join(ErrFactScopeAxisUnsupported, errors.New("wrapped by the expander"))
	if got := classifyFactScopeFailure(err); got != FactScopeFailureAxisUnsupported {
		t.Fatalf("failure class = %q, want %q -- an axis refusal must never be reported as a graph-backend fault", got, FactScopeFailureAxisUnsupported)
	}
	if got := factScopeDecisionReasonForFailure(FactScopeFailureAxisUnsupported); got != FactScopeDecisionAxisUnsupported {
		t.Fatalf("decision reason = %q, want %q", got, FactScopeDecisionAxisUnsupported)
	}
	// The control that makes the assertion above mean something: an ordinary
	// error still classifies as a backend fault, so the branch above is
	// discriminating rather than swallowing everything.
	if got := classifyFactScopeFailure(errors.New("some other failure")); got != FactScopeFailureBackendUnavailable {
		t.Fatalf("an unrelated error classified as %q, want %q", got, FactScopeFailureBackendUnavailable)
	}
}

// TestChaos5405_TheRepositoryPoliciesObservedTimeRefusalStillNamesItsReason is
// the THIRD boundary, and the one this ticket nearly broke on its way past the
// other two.
//
// The rung above ("any non-current axis") applies to work-item TARGETS only,
// so the SIX older repository-target policies still fall through to the
// observed-time rung that CHAOS-4109 wrote. Adding the work-item rung directly
// above it left that older rung with an EMPTY case body: the switch matched,
// nothing was assigned, and the event went out with outcome "" and
// decision_reason "". No refusal, no gap recorded, no disclosure -- an
// observed-time question against a repository policy became silently
// unanswerable-without-saying-so.
//
// That is precisely the shape D-e exists to forbid: a non-execution that is
// indistinguishable, in the log, from an evaluated zero. An empty outcome is
// worse than a wrong one, because no alert rule matches on it.
//
// The assertion is taken from the EMITTED LINE through the real JSON handler
// AT THE PRODUCTION LEVEL, not from the struct: a refusal demoted below Info,
// or a key that vanishes when its value is empty, would pass a struct read and
// disappear for the operator this field is for.
func TestChaos5405_TheRepositoryPoliciesObservedTimeRefusalStillNamesItsReason(t *testing.T) {
	// A REPOSITORY-target rule, which is what makes this distinct from the
	// gate test above: it does NOT match the work-item axis rung and must be
	// refused by the observed-time rung on its own.
	repoRule := map[FactKind]map[SubjectKind]factScopePolicyRule{
		FactMetrics: {SubjectProject: {
			Policy:     FactScopePolicyProjectWorkItemRepository,
			TargetKind: SubjectRepository,
			Basis:      FactScopeBasisActivityProxy,
			Enabled:    true,
			Chain:      factScopeChainRepository,
		}},
	}

	resolve := func(t *testing.T, tctx TimeContext) (FactScopeExpansionEvent, *axisGateExpander) {
		t.Helper()

		expander := &axisGateExpander{}
		scope := NewFactReadScopeResolverWithPolicies(expander, repoRule).Resolve(
			context.Background(), storage.Principal{OrgID: "org_1"},
			newFactScopeResolveInput(scopeTimeRequest(
				[]SubjectRef{scopeProject},
				[]FactRequirement{{Kind: FactMetrics}},
				tctx,
			)),
			map[FactKind]FactCapability{
				FactMetrics: planCapability(FactMetrics, "metrics", SubjectRepository),
			},
		)
		var found bool
		var event FactScopeExpansionEvent
		for _, candidate := range scope.Events {
			if candidate.RequirementKind == FactMetrics {
				event, found = candidate, true
			}
		}
		if !found {
			t.Fatalf("no event recorded for the requirement -- a rung that emits nothing cannot be alerted on at all")
		}
		return event, expander
	}

	t.Run("observed_time_is_refused_with_a_named_reason", func(t *testing.T) {
		event, expander := resolve(t, TimeContext{Axis: contractsv1.ContextFabricTemporalObservedTime})
		if expander.calls != 0 {
			t.Fatalf("the expander ran %d time(s) -- observed_time must be refused at the gate", expander.calls)
		}

		records := captureSlogJSONAtProductionLevel(t, func(logger *slog.Logger) {
			NewSlogEngineTelemetry(logger).RecordFactScopeExpansion(
				canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, event,
			)
		})
		if len(records) != 1 {
			t.Fatalf("emitted %d records at the production level, want 1 -- a refusal demoted below Info is invisible to an operator", len(records))
		}
		record := records[0]
		if got := record["outcome"]; got != string(FactScopePolicyUnavailable) {
			t.Fatalf("emitted outcome = %v, want %q -- an empty outcome is not a refusal, it is a rung that decided nothing", got, FactScopePolicyUnavailable)
		}
		if got := record["decision_reason"]; got != string(FactScopeDecisionAxisUnsupported) {
			t.Fatalf("emitted decision_reason = %v, want %q -- the refusal must name WHY, or it reads as an unexplained gap", got, FactScopeDecisionAxisUnsupported)
		}
		if got := record["axis"]; got != string(contractsv1.ContextFabricTemporalObservedTime) {
			t.Fatalf("emitted axis = %v, want %q -- the refused axis must be on the line that refused it", got, contractsv1.ContextFabricTemporalObservedTime)
		}
	})

	// CONTROL: the SAME table on the current axis reaches the expander and
	// executes, so the refusal above is attributable to the axis and to
	// nothing else about this rule.
	t.Run("control_the_current_axis_still_executes", func(t *testing.T) {
		event, expander := resolve(t, TimeContext{Axis: TemporalCurrent})
		if expander.calls != 1 {
			t.Fatalf("the expander ran %d time(s) on the current axis, want 1 -- without this the refusal above proves nothing about the axis", expander.calls)
		}
		if event.DecisionReason != FactScopeDecisionExecuted {
			t.Fatalf("decision_reason = %q on the current axis, want %q", event.DecisionReason, FactScopeDecisionExecuted)
		}
	})
}
