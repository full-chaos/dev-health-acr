package mcp

import (
	"reflect"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func init() {
	registerOutputVocabularies(
		outputVocabulary{
			name:  "sections.items",
			kind:  vocabFrozen,
			why:   "request-echo",
			sites: []string{"data_catalog:sections.items"},
			// schema: "operations", "facts", "subjects", "relationships", "limits", "schema"
			goSet:      fromList(directread.CatalogSectionVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "caller.grant_class",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"data_catalog:caller.grant_class", "data_catalog:operations.caller_class", "data_catalog:schema.caller_class"},
			// schema: "unrestricted", "restricted"
			goSet:      fromList(directread.CallerClassVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.scope_class",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"data_catalog:operations.operations.items.scope_class"},
			// schema: "org_wide", "forced_grant"
			goSet:      strs(directread.ScopeOrgWide, directread.ScopeForcedGrant),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "schema.reason",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"data_catalog:schema.reason"},
			// schema: "scope_missing_data_read", "data_graphql_not_configured", "subject_gate_unavailable"
			goSet:      strs(directread.CatalogUnavailableScopeMissing, directread.CatalogUnavailableGraphQLNotConfigured, directread.CatalogUnavailableGateMissing),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.cost_class",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"data_catalog:schema.roots.items.cost_class"},
			// schema: "compute", "analytics_batch", "list", "series", "catalog"
			goSet:      fromList(directread.CostClassVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.scope_class#2",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"data_catalog:schema.roots.items.scope_class"},
			// schema: "org_wide", "client_scope", "forced_grant", "not_reached"
			goSet:      fromList(directread.ScopeClassVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "caller.effective_access",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"data_catalog:caller.effective_access"},
			// schema: "data_read", "no_data_read", "not_servable"
			goSet:      strs(directread.EffectiveAccessDataRead, directread.EffectiveAccessNoDataRead, directread.EffectiveAccessNotServable),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "caller.tool_access",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"data_catalog:caller.tool_access.additionalProperties"},
			// schema: "available", "no_data_read", "not_servable"
			goSet:      strs(string(directread.ToolAccessAvailable), string(directread.ToolAccessNoDataRead), string(directread.ToolAccessNotServable)),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "status",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"find_subjects:status"},
			// schema: "complete", "partial", "empty", "ambiguous"
			goSet:      fromList(directread.FindStatusVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.match",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"find_subjects:subjects.items.match"},
			// schema: "", "exact", "alias", "provider_key"
			goSet: strs(directread.MatchExact, directread.MatchAlias, directread.MatchProviderKey),
		},
		outputVocabulary{
			name:  "request.mode",
			kind:  vocabFrozen,
			why:   "request-echo",
			sites: []string{"find_subjects:request.mode"},
			// schema: "list", "name", "owned_by", "handle"
			goSet:      fromList(directread.FindModeVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "call",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"graphql_query:call", "run_operation:call"},
			// schema: "served", "refused", "operation_unavailable", "upstream_error", "upstream_timeout"
			goSet:      fromList(directread.CallStatusVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "completeness",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"graphql_query:completeness", "graphql_query:root_fields.items.completeness", "run_operation:completeness"},
			// schema: "declared_complete", "declared_partial", "unknown"
			goSet:      fromList(directread.CompletenessVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "completeness_reason",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"graphql_query:completeness_reason", "graphql_query:root_fields.items.completeness_reason", "run_operation:completeness_reason"},
			// schema: "page_cut", "bounded", "coverage_below_one", "insufficient_history", "degraded", "disclosure_absent", "limit_not_sent"
			goSet:      fromList(directread.CompletenessReasonVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "result",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"graphql_query:result", "run_operation:result"},
			// schema: "data", "empty_unverified", "empty_declared"
			goSet:      fromList(directread.ResultStateVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "refusal.code",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"graphql_query:refusal.code"},
			// schema: "variable_not_allowed", "variable_out_of_range", "person_scope_not_served", "basis_dependent_shape", "no_granted_scope", "operation_not_served_for_caller", "response_budget", "invalid_request", "scope_required", "row_outside_grant", "policy_stale", "denied_or_not_found", "query_invalid", "operation_type_not_allowed", "root_field_not_allowed", "field_not_allowed", "fragment_not_allowed", "directive_not_allowed", "query_limit_exceeded", "read_budget_exceeded", "team_inactive"
			goSet:      fromList(directread.GraphQLRefusalCodes()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "refusal.read_budget",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"graphql_query:refusal.read_budget"},
			// schema: "bytes_ceiling", "rows_ceiling", "time_ceiling", "unknown"
			goSet:      fromList(directread.ReadBudgetReasonVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.class",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"graphql_query:errors.items.class"},
			// schema: "graphql_errors", "decode", "http_status", "transport", "canceled", "acr_deadline", "carrier_refused", "listener_refused", "not_found", "concurrency_wait"
			goSet:      fromList(directread.GraphQLUpstreamErrorClasses()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.graphql_code",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"graphql_query:errors.items.graphql_code", "run_operation:errors.items.graphql_code"},
			// schema: "graphql_validation_failed", "graphql_parse_failed", "mcp_refused", "mcp_read_budget_exceeded", "other"
			goSet:      fromList(directread.UpstreamGraphQLCodeVocabulary()),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "status#2",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"read_facts:status"},
			// schema: "complete", "partial", "denied", "unavailable"
			goSet:      strs(directread.StatusComplete, directread.StatusPartial, directread.StatusDenied, directread.StatusUnavailable),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.outcome",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"read_facts:coverage.items.outcome"},
			// schema: "fact_served", "read_no_fact", "measured_zero", "not_applicable", "unavailable", "stale", "truncated", "withheld_response_budget"
			goSet:      strs(directread.OutcomeFactServed, directread.OutcomeReadNoFact, directread.OutcomeMeasuredZero, directread.OutcomeNotApplicable, directread.OutcomeUnavailable, directread.OutcomeStale, directread.OutcomeTruncated, directread.OutcomeWithheldBudget),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "expansion.outcome",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"read_facts:coverage.items.expansion.outcome"},
			// schema: "not_needed", "policy_unavailable", "attempted_empty", "target_kind_mismatch", "expanded", "expanded_partial", "failed", "matched_unauthorized"
			goSet:      strs(contextfabric.FactScopeNotNeeded, contextfabric.FactScopePolicyUnavailable, contextfabric.FactScopeAttemptedEmpty, contextfabric.FactScopeTargetKindMismatch, contextfabric.FactScopeExpanded, contextfabric.FactScopeExpandedPartial, contextfabric.FactScopeFailed, contextfabric.FactScopeMatchedUnauthorized),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "status#3",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"read_relationships:status"},
			// schema: "complete", "partial", "denied", "unavailable", "invalid"
			goSet:      strs(directread.RelationshipsComplete, directread.RelationshipsPartial, directread.RelationshipsDenied, directread.RelationshipsUnavailable, directread.RelationshipsInvalid),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.type",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"read_relationships:edges.items.type", "read_relationships:effective.types.items"},
			// schema: "BELONGS_TO_PROJECT", "BELONGS_TO_PULL_REQUEST", "BELONGS_TO_REPOSITORY", "BLOCKS", "CORRELATED_WITH_INCIDENT", "DOCUMENTED_BY", "DUPLICATES", "HAS_EPISODE", "LINKS_PULL_REQUEST", "OWNED_BY_TEAM", "PART_OF", "RELATED_TO", "RELATES_TO"
			goSet:      directread.RelationshipTypeVocabulary(),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "effective.direction",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"read_relationships:effective.direction"},
			// schema: "out", "in", "both"
			goSet:      strs(directread.EdgeDirectionOut, directread.EdgeDirectionIn, directread.EdgeDirectionBoth),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "effective.axis",
			kind:  vocabFrozen,
			why:   "protocol",
			sites: []string{"read_relationships:effective.axis"},
			// schema: "current", "as_of"
			schemaOnly: "the current/as_of axis is served from string literals in directread/read_relationships.go with no constant",
		},
		outputVocabulary{
			name:           "from.status",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"read_relationships:edges.items.from.status", "read_relationships:edges.items.to.status"},
			// schema: "backlog", "todo", "in_progress", "in_review", "blocked", "done", "canceled", "unknown"
			goSet:      contextfabric.WorkItemStatusVocabulary(),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "provenance.link_tier",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"read_relationships:edges.items.provenance.link_tier"},
			// schema: "native", "explicit_text", "heuristic"
			schemaOnly: "directread.linkTierVocabulary is unexported",
		},
		outputVocabulary{
			name:           "truncated_by",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"read_relationships:truncated_by"},
			// schema: "frontier_cap", "frontier_scan_cap"
			goSet:      strs(directread.RelationshipsTruncatedFrontierCap, directread.RelationshipsTruncatedScanCap),
			exhaustive: true,
		},
		outputVocabulary{
			name:  "status#4",
			kind:  vocabFrozen,
			why:   "state-machine",
			sites: []string{"record_episode:status"},
			// schema: "recorded", "no_persist"
			schemaOnly: "recorded/no_persist are string literals in mcp/record_episode.go",
		},
		outputVocabulary{
			name:           "redaction_state",
			kind:           vocabAdditive,
			clientValidate: "contractsv1.MCPRecordEpisodeResponse.ValidateRead",
			sites:          []string{"record_episode:redaction_state"},
			// schema: "active", "redacted", "purged_tombstone"
			schemaOnly: "active/redacted/purged_tombstone are string literals in contracts/v1/validate.go",
		},
		outputVocabulary{
			name:           "transcript_disposition",
			kind:           vocabAdditive,
			clientValidate: "none: minted by the local sidecar from the request and the hosted episode; it never carries a server-added value",
			sites:          []string{"record_episode:transcript_disposition"},
			// schema: "not_submitted", "accepted", "redacted"
			schemaOnly: "not_submitted/accepted/redacted are string literals in contracts/v1/mcp_validate.go",
		},
		outputVocabulary{
			name:           "refusal.code#2",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"run_operation:refusal.code"},
			// schema: "unknown_operation", "variable_not_allowed", "variable_out_of_range", "person_scope_not_served", "basis_dependent_shape", "no_granted_scope", "operation_not_served_for_caller", "response_budget", "invalid_request", "scope_required", "row_outside_grant", "policy_stale", "denied_or_not_found", "team_inactive"
			goSet:      fromList(directread.OperationRefusalCodes()),
			exhaustive: true,
		},
		outputVocabulary{
			name:           "items.class#2",
			kind:           vocabAdditive,
			clientValidate: "none: the sidecar passes data tool responses through as raw JSON and validates no field of them",
			sites:          []string{"run_operation:errors.items.class"},
			// schema: "graphql_errors", "decode", "http_status", "transport", "canceled", "timeout", "not_found", "concurrency_wait"
			goSet:      fromList(directread.UpstreamErrorClassVocabulary()),
			exhaustive: true,
		},
	)
}

func strs[T ~string](v ...T) []string {
	out := make([]string, len(v))
	for i, x := range v {
		out[i] = string(x)
	}
	return out
}

func fromList(list any) []string {
	v := reflect.ValueOf(list)
	out := make([]string, v.Len())
	for i := range out {
		out[i] = v.Index(i).String()
	}
	return out
}
