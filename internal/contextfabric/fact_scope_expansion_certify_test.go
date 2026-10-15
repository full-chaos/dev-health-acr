package contextfabric_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const factScopeCertifyRequestID = "req_0123456789abcdef0123456789abcdef"

// factScopeExpansionLegacyKeys is the key sequence the line carried before it
// was declared, written down as a literal so a declaration edit cannot move it.
var factScopeExpansionLegacyKeys = []string{
	"time", "level", "msg",
	"org_id", "requirement_kind", "origin_kind", "target_kind", "policy", "basis", "axis", "outcome",
	"origin_count", "candidate_count", "admitted_count", "authorization_dropped_count", "temporal_dropped_count",
	"unbounded_validity_count", "malformed_touch_count", "duplicate_add_count", "missing_next_hop_count",
	"target_kind_mismatch_count", "truncated", "failure_class", "attribution_source_counts",
	"target_limit", "census_complete", "authorized_count", "repo_less_candidate_count", "repo_less_admitted_count",
	"repo_less_authorization_dropped_count", "organization_grant_authorized_count", "direct_repo_authorized_count",
	"project_ownership_authorized_count", "pr_link_authorized_count", "project_ownership_admitted_count",
	"pr_link_admitted_count", "excluded_explicit_text_link_count", "excluded_heuristic_link_count",
	"authorization_grant_measured", "authorization_grant_organization_wide", "authorization_grant_exact_selectors",
	"authorization_grant_owner_selectors", "orphaned_repository_count", "ambiguous_origin_count",
	"unknown_attribution_source_count", "scope_query_count", "scope_rows_returned", "roots_complete",
	"shared_target_count", "decision_reason", "request_id",
}

func factScopeExpansionBaseEvent() contextfabric.FactScopeExpansionEvent {
	return contextfabric.FactScopeExpansionEvent{
		RequirementKind: contextfabric.FactStatus, OriginKind: contextfabric.SubjectProject, TargetKind: contextfabric.SubjectWorkItem,
		Policy: contextfabric.FactScopePolicyProjectWorkItemStatus, Basis: contextfabric.FactScopeBasis("activity_proxy"),
		Axis: contextfabric.TemporalCurrent, Outcome: contextfabric.FactScopeExpanded,
		OriginCount: 1, CandidateCount: 9, AdmittedCount: 7, AuthorizationDroppedCount: 2, TemporalDroppedCount: 3,
		UnboundedValidityCount: 4, MalformedTouchCount: 5, DuplicateAddCount: 6, MissingNextHopCount: 8, TargetKindMismatchCount: 10,
		Truncated: true, TargetLimit: 200, CensusComplete: true, AuthorizedCount: 11, RepoLessCandidateCount: 12,
		RepoLessAdmittedCount: 13, RepoLessAuthorizationDroppedCount: 14, OrganizationGrantAuthorizedCount: 15,
		DirectRepositoryAuthorizedCount: 16, ProjectOwnershipAuthorizedCount: 17, PullRequestLinkAuthorizedCount: 18,
		ProjectOwnershipAdmittedCount: 19, PullRequestLinkAdmittedCount: 20, ExcludedExplicitTextLinkCount: 21,
		ExcludedHeuristicLinkCount: 22, AuthorizationGrantMeasured: true, AuthorizationGrantOrganizationWide: true,
		AuthorizationGrantExactSelectors: 23, AuthorizationGrantOwnerSelectors: 24, OrphanedRepositoryCount: 25,
		AmbiguousOriginCount: 26, UnknownAttributionSourceCount: 27, ScopeQueryCount: 28, ScopeRowsReturned: 29,
		RootsComplete: true, SharedTargetCount: 30, DecisionReason: contextfabric.FactScopeDecisionExecuted,
		AttributionSourceCounts: map[string]int{"native_team": 3},
	}
}

func recordFactScopeExpansion(event contextfabric.FactScopeExpansionEvent) []byte {
	var buf bytes.Buffer
	ctx := observability.WithRequestID(context.Background(), factScopeCertifyRequestID)
	contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))).
		RecordFactScopeExpansion(ctx, storage.Principal{OrgID: "org_1"}, event)
	return buf.Bytes()
}

func jsonKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		t.Fatalf("token: %v", err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		keys = append(keys, tok.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("value: %v", err)
		}
	}
	return keys
}

func certifyFactScopeLine(t *testing.T, raw []byte, event eventspec.Event, extra map[string]any) certify.Line {
	t.Helper()
	log, err := certify.Parse(raw)
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	want := map[string]any{
		"request_id": factScopeCertifyRequestID, "requirement_kind": "status", "origin_kind": "project",
		"policy": string(contextfabric.FactScopePolicyProjectWorkItemStatus),
	}
	for k, v := range extra {
		want[k] = v
	}
	result, err := certify.Certify(log, certify.Assertion{Event: event, Want: want})
	if err != nil {
		t.Fatalf("%s failed certification: %v\n%s", event.ID, err, raw)
	}
	return result.Line
}

// factScopeExpansionBaseWant is the value every key of the base event must
// carry on the line; every count is distinct so a swapped pair of fields fails.
func factScopeExpansionBaseWant(outcome string) map[string]any {
	return map[string]any{
		"org_id": "org_1", "target_kind": "work_item", "basis": "activity_proxy", "axis": "current", "outcome": outcome,
		"origin_count": 1, "candidate_count": 9, "admitted_count": 7, "authorization_dropped_count": 2, "temporal_dropped_count": 3,
		"unbounded_validity_count": 4, "malformed_touch_count": 5, "duplicate_add_count": 6, "missing_next_hop_count": 8,
		"target_kind_mismatch_count": 10, "truncated": true, "failure_class": "", "target_limit": 200, "census_complete": true,
		"authorized_count": 11, "repo_less_candidate_count": 12, "repo_less_admitted_count": 13,
		"repo_less_authorization_dropped_count": 14, "organization_grant_authorized_count": 15, "direct_repo_authorized_count": 16,
		"project_ownership_authorized_count": 17, "pr_link_authorized_count": 18, "project_ownership_admitted_count": 19,
		"pr_link_admitted_count": 20, "excluded_explicit_text_link_count": 21, "excluded_heuristic_link_count": 22,
		"authorization_grant_measured": true, "authorization_grant_organization_wide": true,
		"authorization_grant_exact_selectors": 23, "authorization_grant_owner_selectors": 24, "orphaned_repository_count": 25,
		"ambiguous_origin_count": 26, "unknown_attribution_source_count": 27, "scope_query_count": 28, "scope_rows_returned": 29,
		"roots_complete": true, "shared_target_count": 30, "decision_reason": "executed",
		"attribution_source_counts": map[string]any{"native_team": 3},
	}
}

func TestFactScopeExpansionLinesPinTheirLegacyShapeAndCertifyEveryOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		outcome contextfabric.FactScopeExpansionOutcome
		event   eventspec.Event
	}{
		{contextfabric.FactScopeNotNeeded, eventspec.FactScopeExpansionDecision},
		{contextfabric.FactScopeExpanded, eventspec.FactScopeExpansionDecision},
		{contextfabric.FactScopeAttemptedEmpty, eventspec.FactScopeExpansionDecision},
		{contextfabric.FactScopePolicyUnavailable, eventspec.FactScopeExpansionGap},
		{contextfabric.FactScopeExpandedPartial, eventspec.FactScopeExpansionGap},
		{contextfabric.FactScopeFailed, eventspec.FactScopeExpansionGap},
		{contextfabric.FactScopeTargetKindMismatch, eventspec.FactScopeExpansionGap},
		{contextfabric.FactScopeMatchedUnauthorized, eventspec.FactScopeExpansionGap},
	} {
		tc := tc
		t.Run(string(tc.outcome), func(t *testing.T) {
			t.Parallel()
			event := factScopeExpansionBaseEvent()
			event.Outcome = tc.outcome
			raw := recordFactScopeExpansion(event)
			if got := jsonKeys(t, raw); !reflect.DeepEqual(got, factScopeExpansionLegacyKeys) {
				t.Fatalf("key sequence = %v, want %v", got, factScopeExpansionLegacyKeys)
			}
			line := certifyFactScopeLine(t, raw, tc.event, factScopeExpansionBaseWant(string(tc.outcome)))
			if line["msg"] != tc.event.Msg {
				t.Fatalf("msg = %v, want %q", line["msg"], tc.event.Msg)
			}
		})
	}
}

func TestFactScopeExpansionLineCertifiesEveryFailureClassReasonAndMapShape(t *testing.T) {
	t.Parallel()
	for _, class := range contextfabric.FactScopeFailureClassLineVocabulary() {
		event := factScopeExpansionBaseEvent()
		event.Outcome, event.FailureClass = contextfabric.FactScopeFailed, contextfabric.FactScopeFailureClass(class)
		certifyFactScopeLine(t, recordFactScopeExpansion(event), eventspec.FactScopeExpansionGap, map[string]any{"failure_class": class})
	}
	for _, reason := range contextfabric.FactScopeDecisionReasonLineVocabulary() {
		event := factScopeExpansionBaseEvent()
		event.DecisionReason = contextfabric.FactScopeDecisionReason(reason)
		want := reason
		if reason == "unrecognized" {
			event.DecisionReason = contextfabric.FactScopeDecisionReason("not_a_ruled_reason")
		}
		certifyFactScopeLine(t, recordFactScopeExpansion(event), eventspec.FactScopeExpansionDecision, map[string]any{"decision_reason": want})
	}

	nilMap := factScopeExpansionBaseEvent()
	nilMap.AttributionSourceCounts = nil
	line := certifyFactScopeLine(t, recordFactScopeExpansion(nilMap), eventspec.FactScopeExpansionDecision, nil)
	if value, present := line["attribution_source_counts"]; !present || value != nil {
		t.Fatalf("a nil map must be written as an explicit null, got present=%t value=%v", present, value)
	}
	empty := factScopeExpansionBaseEvent()
	empty.AttributionSourceCounts = map[string]int{}
	certifyFactScopeLine(t, recordFactScopeExpansion(empty), eventspec.FactScopeExpansionDecision, nil)
}

// TestFactScopeExpansionOpenCountMapKeysAreNamesNeverIdsOrText pins the key
// rule of an open count map: the real stored attribution-source and link-tier
// names pass, and an id-shaped or free-text key fails the declaration.
func TestFactScopeExpansionOpenCountMapKeysAreNamesNeverIdsOrText(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"native_team", "issue_project", "project_ownership", "repo_ownership", "assignee_membership",
		"linked_issue", "manual_fallback", "team_repo_ownership", "native", "heuristic",
	} {
		event := factScopeExpansionBaseEvent()
		event.AttributionSourceCounts = map[string]int{name: 1}
		certifyFactScopeLine(t, recordFactScopeExpansion(event), eventspec.FactScopeExpansionDecision, nil)
	}
	for _, bad := range []string{
		"3f2b8c1e-9d4a-4c7b-8e21-5a6f0d9c1b73",         // uuid
		"abcdef12-3456-7890-abcd-ef1234567890",         // uuid that starts with a letter
		"alex.morgan@example.com",                      // email
		"Native_Team",                                  // not lowercase
		"native team",                                  // free text
		"team_attribution_source_that_is_far_too_long", // past the bound
		"repo-42", // carries a digit
		"",        // empty
	} {
		event := factScopeExpansionBaseEvent()
		event.AttributionSourceCounts = map[string]int{bad: 1}
		raw := recordFactScopeExpansion(event)
		log, err := certify.Parse(raw)
		if err != nil {
			t.Fatalf("certify.Parse: %v", err)
		}
		_, err = certify.Certify(log, certify.Assertion{Event: eventspec.FactScopeExpansionDecision, Want: map[string]any{
			"request_id": factScopeCertifyRequestID, "requirement_kind": "status", "origin_kind": "project",
			"policy": string(contextfabric.FactScopePolicyProjectWorkItemStatus),
		}})
		if err == nil {
			t.Errorf("key %q was certified, want it refused as not a name token", bad)
		}
	}
	for _, bad := range []string{"3.5", "-1", "9223372036854775808.1", "9223372036854775808", "9007199254740993"} {
		raw := bytes.Replace(recordFactScopeExpansion(factScopeExpansionBaseEvent()), []byte(`"native_team":3`), []byte(`"native_team":`+bad), 1)
		log, err := certify.Parse(raw)
		if err != nil {
			t.Fatalf("certify.Parse: %v", err)
		}
		if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.FactScopeExpansionDecision, Want: map[string]any{
			"request_id": factScopeCertifyRequestID, "requirement_kind": "status", "origin_kind": "project",
			"policy": string(contextfabric.FactScopePolicyProjectWorkItemStatus),
		}}); err == nil {
			t.Errorf("count %s was certified, want it refused", bad)
		}
	}
	// The producer's own sink writes a negative count it is handed, so the
	// certifier is the only guard: drive it through the real sink too.
	negative := factScopeExpansionBaseEvent()
	negative.AttributionSourceCounts = map[string]int{"native_team": -1}
	log, err := certify.Parse(recordFactScopeExpansion(negative))
	if err != nil {
		t.Fatalf("certify.Parse: %v", err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.FactScopeExpansionDecision, Want: map[string]any{
		"request_id": factScopeCertifyRequestID, "requirement_kind": "status", "origin_kind": "project",
		"policy": string(contextfabric.FactScopePolicyProjectWorkItemStatus),
	}}); err == nil {
		t.Error("a negative count written by the real sink was certified, want it refused")
	}
}
