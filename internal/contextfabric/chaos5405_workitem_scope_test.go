package contextfabric

// CHAOS-5405 -- bounded work-item fact scope with an authorized census.
//
// WHAT THESE ARE ORACLES FOR. Seven work-item-target requirement kinds carry
// FactScopePolicyNone for BOTH origin kinds (fact_scope.go:669-718), so every
// project- and team-scoped answer that needs one reaches the fail-closed
// ladder's first rung (fact_scope.go:1288-1290) and serves hollow. The ruling
// of record (design-of-record vol. 2, "2026-09-07 -- Bounded work-item fact
// scope with an authorized census", ratified 2026-09-09) replaces those seven
// rows with FOURTEEN per-origin `_v1` policies, a `direct` basis, a pushed-down
// authorized census, and a bounded in-flight gate.
//
// THE AMBIENT TABLE ONLY. D-f acceptance gate 5 requires that this class fail
// at the parent because the COMPILED policies remain unavailable and pass at
// the tip "without injecting an enabled replacement table". Nothing in this
// file installs a narrow factScopePolicies -- unlike the CHAOS-4099 stage
// tests next door, which deliberately pin their own tables. Deleting any ONE
// of the fourteen activation mappings must therefore kill exactly one subtest,
// which is why every pair is its own t.Run.
//
// POLICY NAMES ARE STRINGS HERE, not the constants the tip will declare, so
// this file COMPILES at the parent and fails BEHAVIOURALLY rather than at
// build time. A build failure is not a red pin.
//
// RED-FIRST at 0945a53dfdad0e84ba8244c59e8e9d85a7d195f2.

import (
	"context"
	"log/slog"
	"reflect"
	"sort"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos5405Pair is one ratified (requirement kind, origin kind) activation
// mapping, with the policy name and basis D-b assigns it.
type chaos5405Pair struct {
	kind   FactKind
	origin SubjectKind
	policy FactScopePolicy
}

// chaos5405Pairs is the fourteen-pair matrix of D-b, spelled out rather than
// generated: a generated list would silently follow the table it is supposed
// to be checking.
func chaos5405Pairs() []chaos5405Pair {
	return []chaos5405Pair{
		{FactStatus, SubjectProject, "project_work_item_status_v1"},
		{FactWork, SubjectProject, "project_work_item_work_v1"},
		{FactActualCompletion, SubjectProject, "project_work_item_actual_completion_v1"},
		{FactBlockers, SubjectProject, "project_work_item_blockers_v1"},
		{FactRequiredChildren, SubjectProject, "project_work_item_required_children_v1"},
		{FactIdentity, SubjectProject, "project_work_item_identity_v1"},
		{FactMembership, SubjectProject, "project_work_item_membership_v1"},

		{FactStatus, SubjectTeam, "team_primary_attribution_work_item_status_v1"},
		{FactWork, SubjectTeam, "team_primary_attribution_work_item_work_v1"},
		{FactActualCompletion, SubjectTeam, "team_primary_attribution_work_item_actual_completion_v1"},
		{FactBlockers, SubjectTeam, "team_primary_attribution_work_item_blockers_v1"},
		{FactRequiredChildren, SubjectTeam, "team_primary_attribution_work_item_required_children_v1"},
		{FactIdentity, SubjectTeam, "team_primary_attribution_work_item_identity_v1"},
		{FactMembership, SubjectTeam, "team_primary_attribution_work_item_membership_v1"},
	}
}

// TestChaos5405_EveryWorkItemPairCarriesItsOwnRuledPolicy is the activation
// mapping itself, one subtest per pair (D-f gate 5's deletion mutants).
func TestChaos5405_EveryWorkItemPairCarriesItsOwnRuledPolicy(t *testing.T) {
	t.Parallel()
	for _, pair := range chaos5405Pairs() {
		pair := pair
		t.Run(string(pair.kind)+"/"+string(pair.origin), func(t *testing.T) {
			t.Parallel()
			rule, eligible := lookupFactScopePolicy(pair.kind, pair.origin)
			if !eligible {
				t.Fatalf("(%s, %s) is not expansion-eligible at all", pair.kind, pair.origin)
			}
			if rule.Policy != pair.policy {
				t.Fatalf("(%s, %s) policy = %q, want %q", pair.kind, pair.origin, rule.Policy, pair.policy)
			}
			if !rule.Enabled {
				t.Fatalf("(%s, %s) policy %q ships disabled -- the ruling activates all fourteen", pair.kind, pair.origin, rule.Policy)
			}
			if rule.TargetKind != SubjectWorkItem {
				t.Fatalf("(%s, %s) target kind = %q, want %q", pair.kind, pair.origin, rule.TargetKind, SubjectWorkItem)
			}
			if rule.Basis != FactScopeBasisDirect {
				t.Fatalf("(%s, %s) basis = %q, want %q -- an item's own asserted membership is not an activity proxy (D-b)", pair.kind, pair.origin, rule.Basis, FactScopeBasisDirect)
			}
			if rule.Chain != factScopeChainWorkItem {
				t.Fatalf("(%s, %s) chain = %q, want the one-hop work-item chain", pair.kind, pair.origin, rule.Chain)
			}
			if rule.Limit != 0 && rule.Limit != maxFactScopeTargets {
				t.Fatalf("(%s, %s) limit override = %d, want the default %d (D-a keeps maxFactScopeTargets with no work-item override)", pair.kind, pair.origin, rule.Limit, maxFactScopeTargets)
			}
		})
	}
}

// TestChaos5405_APolicyNameIsNeverSharedAcrossPairs pins D-b's "fourteen
// versioned policies ... separate product identities". One shared name across
// two requirements would make a per-requirement product commitment
// unauditable, and would make the deletion mutants above non-discriminating.
func TestChaos5405_APolicyNameIsNeverSharedAcrossPairs(t *testing.T) {
	t.Parallel()
	owner := map[FactScopePolicy]string{}
	for _, pair := range chaos5405Pairs() {
		rule, eligible := lookupFactScopePolicy(pair.kind, pair.origin)
		if !eligible {
			t.Fatalf("(%s, %s) is not expansion-eligible at all", pair.kind, pair.origin)
		}
		key := string(pair.kind) + "/" + string(pair.origin)
		if previous, taken := owner[rule.Policy]; taken {
			t.Fatalf("policy %q is claimed by both %s and %s -- fourteen pairs, fourteen identities", rule.Policy, previous, key)
		}
		owner[rule.Policy] = key
	}
	if len(owner) != len(chaos5405Pairs()) {
		t.Fatalf("distinct policies = %d, want %d", len(owner), len(chaos5405Pairs()))
	}
}

// TestChaos5405_TheRuledActivationSetIsExactlyTwenty is the widened successor
// of TestChaos4099_OnlyTheSixRuledPoliciesAreEverActivatable: the ruled set
// grows from six to twenty, and NOTHING beyond it may ever ship enabled.
// Cardinality is checked in both directions -- a one-way subset check
// certifies nothing about a present-and-unexpected member.
func TestChaos5405_TheRuledActivationSetIsExactlyTwenty(t *testing.T) {
	t.Parallel()
	ruled := map[FactScopePolicy]SubjectKind{
		FactScopePolicyProjectWorkItemRepository:               SubjectProject,
		FactScopePolicyProjectWorkItemPullRequest:              SubjectProject,
		FactScopePolicyProjectWorkItemPullRequestReview:        SubjectProject,
		FactScopePolicyTeamPrimaryAttributionRepository:        SubjectTeam,
		FactScopePolicyTeamPrimaryAttributionPullRequest:       SubjectTeam,
		FactScopePolicyTeamPrimaryAttributionPullRequestReview: SubjectTeam,
	}
	for _, pair := range chaos5405Pairs() {
		ruled[pair.policy] = pair.origin
	}
	if len(ruled) != 20 {
		t.Fatalf("ruled activation set = %d policies, want 20 (6 CHAOS-4099/4101 + 14 CHAOS-5405)", len(ruled))
	}
	seen := map[FactScopePolicy]bool{}
	for _, row := range factScopeEligibility {
		if row.Rule.Policy == FactScopePolicyNone {
			if row.Rule.Enabled {
				t.Fatalf("%s is enabled with no policy to name it", row.Requirement)
			}
			continue
		}
		origin, ok := ruled[row.Rule.Policy]
		if !ok {
			t.Fatalf("%s carries unratified policy %q", row.Requirement, row.Rule.Policy)
		}
		if !row.Rule.Enabled {
			t.Fatalf("%s ships disabled -- every ruled policy is activated", row.Requirement)
		}
		for _, rowOrigin := range row.Origins {
			if rowOrigin != origin {
				t.Fatalf("policy %q declared for origin %q, ruled for %q only", row.Rule.Policy, rowOrigin, origin)
			}
		}
		seen[row.Rule.Policy] = true
	}
	for policy := range ruled {
		if !seen[policy] {
			t.Fatalf("ruled policy %q never appears as an enabled row -- activation is incomplete", policy)
		}
	}
}

// TestChaos5405_TheProjectHealthRepositoryRowStaysUnruled is the ruling's own
// scope BOUNDARY, pinned so widening the seven does not quietly widen the
// eighth. The design says so in terms: the project-origin FactHealth
// repository-target row "remains outside this ruling".
func TestChaos5405_TheProjectHealthRepositoryRowStaysUnruled(t *testing.T) {
	t.Parallel()
	rule, eligible := lookupFactScopePolicy(FactHealth, SubjectProject)
	if !eligible {
		t.Fatalf("(health, project) is not expansion-eligible at all -- the disclosure row must survive")
	}
	if rule.Policy != FactScopePolicyNone {
		t.Fatalf("(health, project) policy = %q, want %q -- out of scope for CHAOS-5405", rule.Policy, FactScopePolicyNone)
	}
	if rule.TargetKind != SubjectRepository {
		t.Fatalf("(health, project) target kind = %q, want %q", rule.TargetKind, SubjectRepository)
	}
}

// TestChaos5405_EveryRuledPolicyNameIsOnTheWireVocabulary pins D-b's contract
// half. A policy the domain can emit but the wire vocabulary rejects fails
// ContextFabricCoverageDetail.Validate at serve time, turning a fixed hollow
// answer into a 500.
func TestChaos5405_EveryRuledPolicyNameIsOnTheWireVocabulary(t *testing.T) {
	t.Parallel()
	wire := contractsv1.ContextFabricFactScopePolicyVocabulary()
	known := make(map[string]bool, len(wire))
	for _, name := range wire {
		known[name] = true
	}
	for _, pair := range chaos5405Pairs() {
		if !known[string(pair.policy)] {
			t.Fatalf("policy %q is not on the wire vocabulary (%d members) -- a coverage detail naming it would fail validation", pair.policy, len(wire))
		}
	}
}

// ---------------------------------------------------------------------------
// D-e: the decision record
// ---------------------------------------------------------------------------

// chaos5405EventFields is D-e's added field set, Go name -> log key.
func chaos5405EventFields() map[string]string {
	return map[string]string{
		"TargetLimit":                       "target_limit",
		"CensusComplete":                    "census_complete",
		"AuthorizedCount":                   "authorized_count",
		"RepoLessCandidateCount":            "repo_less_candidate_count",
		"RepoLessAdmittedCount":             "repo_less_admitted_count",
		"RepoLessAuthorizationDroppedCount": "repo_less_authorization_dropped_count",
		"OrphanedRepositoryCount":           "orphaned_repository_count",
		"AmbiguousOriginCount":              "ambiguous_origin_count",
		"UnknownAttributionSourceCount":     "unknown_attribution_source_count",
		"ScopeQueryCount":                   "scope_query_count",
		"ScopeRowsReturned":                 "scope_rows_returned",
		"DecisionReason":                    "decision_reason",
	}
}

// TestChaos5405_TheTwelveNewDecisionFieldsExistAndReachTheSink is D-e in one
// pin: the fields exist on the event, and every one is written by the sink at
// an Info-configured logger -- including its zero, false and empty value, so
// "the filter dropped nothing" stays distinguishable from "nobody counted".
func TestChaos5405_TheTwelveNewDecisionFieldsExistAndReachTheSink(t *testing.T) {
	t.Parallel()
	eventType := reflect.TypeOf(FactScopeExpansionEvent{})
	for field, key := range chaos5405EventFields() {
		field, key := field, key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			if _, ok := eventType.FieldByName(field); !ok {
				t.Fatalf("FactScopeExpansionEvent has no field %s -- D-e's %q has nowhere to come from", field, key)
			}
			records := captureSlogJSON(t, func(logger *slog.Logger) {
				NewSlogEngineTelemetry(logger).RecordFactScopeExpansion(
					context.Background(), storage.Principal{OrgID: "org_1"},
					FactScopeExpansionEvent{Outcome: FactScopeExpanded},
				)
			})
			if len(records) != 1 {
				t.Fatalf("want one record, got %d", len(records))
			}
			if _, present := records[0][key]; !present {
				t.Fatalf("the sink never wrote %q (zero values are emitted unconditionally)", key)
			}
		})
	}
}

// TestChaos5405_DecisionReasonCarriesTheRuledVocabularyAndRejectsTheRest pins
// D-e's validated allow-list. Written entirely through reflection so it
// COMPILES at the parent, where the field does not yet exist: an unvalidated
// free-text reason is a field no operator can alert on and a regression can
// silently rename.
func TestChaos5405_DecisionReasonCarriesTheRuledVocabularyAndRejectsTheRest(t *testing.T) {
	t.Parallel()
	ruled := []string{
		"attribution_source_unrecognized", "authorization_error", "axis_unsupported",
		"backend_error", "capacity_timeout", "executed", "expander_unwired",
		"origin_unresolved", "policy_disabled", "policy_none", "time_bounds_invalid",
		"timeout",
	}
	sort.Strings(ruled)

	emit := func(t *testing.T, reason string) map[string]any {
		t.Helper()
		event := FactScopeExpansionEvent{Outcome: FactScopeExpanded}
		value := reflect.ValueOf(&event).Elem()
		field := value.FieldByName("DecisionReason")
		if !field.IsValid() {
			t.Fatalf("FactScopeExpansionEvent has no DecisionReason field")
		}
		if field.Kind() != reflect.String {
			t.Fatalf("DecisionReason kind = %s, want a string-kinded closed type", field.Kind())
		}
		field.SetString(reason)
		records := captureSlogJSON(t, func(logger *slog.Logger) {
			NewSlogEngineTelemetry(logger).RecordFactScopeExpansion(
				context.Background(), storage.Principal{OrgID: "org_1"}, event,
			)
		})
		if len(records) != 1 {
			t.Fatalf("want one record, got %d", len(records))
		}
		return records[0]
	}

	for _, reason := range ruled {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			if got := emit(t, reason); got["decision_reason"] != reason {
				t.Fatalf("decision_reason = %v, want %q -- a ruled reason must survive validation", got["decision_reason"], reason)
			}
		})
	}
	t.Run("unknown_is_rejected", func(t *testing.T) {
		t.Parallel()
		if got := emit(t, "definitely_not_a_reason"); got["decision_reason"] == "definitely_not_a_reason" {
			t.Fatalf("decision_reason = %v -- an unvalidated reason reached the sink verbatim; the allow-list is not closed", got["decision_reason"])
		}
	})
}

// ---------------------------------------------------------------------------
// Negative controls
// ---------------------------------------------------------------------------

// TestChaos5405_ControlTheSixEarlierPoliciesStayRuledAndEnabled is the
// NEGATIVE CONTROL for this whole file: it asserts the pre-existing CHAOS-4099
// / CHAOS-4101 activation, so it passes at the parent as well. A file whose
// every test is red proves only that the harness is broken.
func TestChaos5405_ControlTheSixEarlierPoliciesStayRuledAndEnabled(t *testing.T) {
	t.Parallel()
	for policy, pair := range map[FactScopePolicy]struct {
		kind   FactKind
		origin SubjectKind
	}{
		FactScopePolicyProjectWorkItemRepository:               {FactMetrics, SubjectProject},
		FactScopePolicyProjectWorkItemPullRequest:              {FactPullRequests, SubjectProject},
		FactScopePolicyProjectWorkItemPullRequestReview:        {FactReviews, SubjectProject},
		FactScopePolicyTeamPrimaryAttributionRepository:        {FactMetrics, SubjectTeam},
		FactScopePolicyTeamPrimaryAttributionPullRequest:       {FactPullRequests, SubjectTeam},
		FactScopePolicyTeamPrimaryAttributionPullRequestReview: {FactReviews, SubjectTeam},
	} {
		rule, eligible := lookupFactScopePolicy(pair.kind, pair.origin)
		if !eligible || rule.Policy != policy || !rule.Enabled {
			t.Fatalf("(%s, %s) = %+v (eligible=%v), want enabled policy %q", pair.kind, pair.origin, rule, eligible, policy)
		}
	}
}

// TestChaos5405_ControlTheOutcomeVocabularyGainsNoMember is D-c's "add no
// outcome or failure-class member", and is GREEN at the parent -- the ruling
// reuses the closed vocabulary rather than widening it, so this pin exists to
// catch the fix widening it by accident.
func TestChaos5405_ControlTheOutcomeVocabularyGainsNoMember(t *testing.T) {
	t.Parallel()
	outcomes := contractsv1.ContextFabricFactScopeOutcomeVocabulary()
	if len(outcomes) != 8 {
		t.Fatalf("expansion outcome vocabulary = %d members, want the closed 8", len(outcomes))
	}
	bases := contractsv1.ContextFabricFactScopeBasisVocabulary()
	if len(bases) != 3 {
		t.Fatalf("basis vocabulary = %d members, want the closed 3 -- CHAOS-5405 adds no basis token", len(bases))
	}
}
