package orgpurge_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/stretchr/testify/require"
)

// seedPostgres puts at least one row for org in every purged table, plus one
// audit event. $1 is the org; $2 is a short unique suffix for primary keys.
func seedPostgres(t *testing.T, db *sql.DB, org string) {
	t.Helper()
	suffix := strings.ReplaceAll(org[:8], "-", "")
	statements := []string{
		`INSERT INTO acr.workload_bindings (binding_id, org_id, trust_domain, namespace, service_account_name, service_account_uid, role, created_at, created_by)
		 VALUES ('wb-' || $2, $1::uuid, 'td-' || $2, 'ns', 'sa', 'uid-' || $2, 'read', now(), 'seed')`,
		`INSERT INTO acr.client_credentials (credential_id, org_id, name, token_prefix, token_hash, created_at, workload_binding_id)
		 VALUES ('cred-' || $2, $1::uuid, 'seed', 'fcacr_seed', 'hash-' || $2, now(), 'wb-' || $2)`,
		`INSERT INTO acr.device_authorizations (device_code_hash, user_code_hash, state, authorized_org_id, created_at, expires_at, poll_interval_seconds, issuance_provenance)
		 VALUES (repeat(md5('d1' || $2), 2), repeat(md5('u1' || $2), 2), 'pending', $1::uuid, now(), now() + interval '10 minutes', 5, 'device_authorization')`,
		`INSERT INTO acr.device_authorizations (device_code_hash, user_code_hash, state, authorized_org_id, created_at, expires_at, poll_interval_seconds, issuance_provenance, approved_at, redeemed_at, redeemed_credential_id)
		 VALUES (repeat(md5('d2' || $2), 2), repeat(md5('u2' || $2), 2), 'redeemed', $1::uuid, now(), now() + interval '10 minutes', 5, 'device_authorization', now(), now(), 'cred-' || $2)`,
		`INSERT INTO acr.device_authorizations (device_code_hash, user_code_hash, state, organization_id_hint, created_at, expires_at, poll_interval_seconds, issuance_provenance)
		 VALUES (repeat(md5('d3' || $2), 2), repeat(md5('u3' || $2), 2), 'pending', $1, now(), now() + interval '10 minutes', 5, 'device_authorization')`,
		`INSERT INTO acr.oauth_authorization_requests (handle_hash, device_code_hash, client_id, client_kind, redirect_uri, code_challenge, resource, created_at, expires_at, bound_org_id, bound_subject)
		 VALUES (repeat(md5('h' || $2), 2), repeat(md5('d3' || $2), 2), 'client', 'dynamic', 'http://localhost/cb', repeat('a', 43), 'res', now(), now() + interval '10 minutes', $1, 'subject')`,
		`INSERT INTO acr.context_packet_snapshots (context_packet_id, org_id, repo_id, repo_slug, request_id, schema_version, query_version, ranking_version, scope_resolution, status, payload, generated_at, expires_at, created_at)
		 VALUES ('pkt-' || $2, $1::uuid, $1::uuid, 'a/b', 'req', 'context_packet.v1', 'q', 'r', 'exact_commit', 'complete', '{}', now(), now() + interval '1 day', now())`,
		`INSERT INTO acr.agent_episodes (episode_id, org_id, repo_id, repo_slug, context_packet_id, client_episode_id, idempotency_key, schema_version, outcome, retention_class, payload, started_at, ended_at, created_at)
		 VALUES ('ep1-' || $2, $1::uuid, $1::uuid, 'a/b', 'pkt', 'c1', 'i1', 'agent_episode.v1', 'succeeded', 'default_90d', '{}', now(), now(), now())`,
		`INSERT INTO acr.agent_episodes (episode_id, org_id, repo_id, repo_slug, context_packet_id, client_episode_id, idempotency_key, schema_version, outcome, retention_class, payload, started_at, ended_at, created_at)
		 VALUES ('ep2-' || $2, $1::uuid, $1::uuid, 'a/b', 'pkt', 'c2', 'i2', 'agent_episode.v1', 'succeeded', 'legal_hold', '{}', now(), now(), now())`,
		`INSERT INTO acr.audit_events (audit_event_id, org_id, actor_type, actor_id, action, resource_type, resource_id, status, created_at)
		 VALUES (gen_random_uuid(), $1::uuid, 'operator', 'seed', 'seed', 'organization', 'x', 'success', now())`,
		`INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, updated_at) VALUES ($1, 'src', now())`,
		`INSERT INTO acr.context_fabric_projection_rebuild_markers (org_id, started_at) VALUES ($1, now())`,
		`INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at) VALUES ('result-' || $2, $1, '{}', now())`,
		`INSERT INTO acr.context_fabric_model_execution_receipts (receipt_id, org_id, operation, provider, outcome, fallback_used, payload, started_at, completed_at)
		 VALUES ('receipt-' || $2, $1, 'op', 'p', 'ok', false, '{}', now(), now())`,
		`INSERT INTO acr.context_fabric_org_model_config (org_id, provider, model, credential_ciphertext, credential_kid) VALUES ($1, 'p', 'm', '\x00', 'kid')`,
		`INSERT INTO acr.context_fabric_reuse_invalidations (org_id, invalidated_at) VALUES ($1, now())`,
		`INSERT INTO acr.context_fabric_clarification_selections (selection_id, org_id, captured_at, question_hash, prior_result_id, selected_receipt_id, selected_subject_kind, selected_subject_canonical_id, selection_provenance, offered_candidates, pipeline_context)
		 VALUES ('clar-' || $2, $1, now(), repeat('a', 64), 'prior', 'rcpt', 'team', 'id', 'web_assertion', '[]', '{}')`,
		`INSERT INTO acr.context_fabric_structure_selections (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context)
		 VALUES ('struct-' || $2, $1, now(), repeat('a', 64), 'prior', 'expected_kind', 'rcpt', 'val', true, 'human_panel', 'web_assertion', '[]', '{}')`,
		`INSERT INTO acr.context_fabric_structure_supersession_claims (org_id, prior_result_id, member, claimed_by_result_id) VALUES ($1, 'prior', 'window', 'claimer')`,
		`INSERT INTO acr.context_fabric_structure_prior_pointer_history (org_id, from_version, to_version, ratified_by) VALUES ($1, 1, 2, 'seed')`,
		`INSERT INTO acr.context_fabric_structure_prior_pointer (org_id, active_version) VALUES ($1, 1)`,
		`INSERT INTO acr.context_fabric_structure_prior_revocations (org_id, entry_id, revoked_by) VALUES ($1, 'entry', 'seed')`,
		`INSERT INTO acr.context_fabric_structure_priors (org_id, version, entries, created_from_watermark, curation_rule_version) VALUES ($1, 1, '[]', 'wm', 'v1')`,
		`INSERT INTO acr.context_fabric_graph_build_source_progress (org_id, epoch, source, updated_at) VALUES ($1, 2, 'src', now())`,
		`INSERT INTO acr.context_fabric_graph_epoch_retirements (org_id, epoch, reason, drain_start, created_at, updated_at) VALUES ($1, 1, 'grace_expired', now(), now(), now())`,
		`INSERT INTO acr.context_fabric_graph_lifecycle (org_id, active_epoch, last_allocated_epoch, updated_at) VALUES ($1, 2, 2, now())`,
	}
	for _, statement := range statements {
		args := []any{org}
		if strings.Contains(statement, "$2") {
			args = append(args, suffix)
		}
		_, err := db.ExecContext(context.Background(), statement, args...)
		require.NoError(t, err, statement)
	}
}

func seedGraph(t *testing.T, adapter *falkorgraph.Adapter, org string) {
	t.Helper()
	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	authz := contextfabric.AuthorizationScope{RepositorySlugs: []string{"example-org/widget-service"}}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work-item-1", Label: "WIDGET-1"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_purge_00000001", OrgID: org, Source: graphSource,
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities:      []contextfabric.EntityProjection{{Subject: subject, Authorization: authz, EvidenceRefIDs: []string{"evidence_1"}, ObservedAt: observed, SourceVersion: "v1"}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	_, err := adapter.ApplyProjectionBatch(context.Background(), batch)
	require.NoError(t, err)
}
