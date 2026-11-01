package contractcheck

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var exampleSchemaPairs = map[string]string{
	"context_packet_request.v1.json":                               "context_packet_request.v1.schema.json",
	"context_packet_item.v1.json":                                  "context_packet_item.v1.schema.json",
	"context_packet.v1.json":                                       "context_packet.v1.schema.json",
	"evidence_ref.v1.json":                                         "evidence_ref.v1.schema.json",
	"expanded_evidence.v1.json":                                    "expanded_evidence.v1.schema.json",
	"capabilities.v1.json":                                         "capabilities.v1.schema.json",
	"agent_episode_create.v1.json":                                 "agent_episode_create.v1.schema.json",
	"agent_episode.v1.json":                                        "agent_episode.v1.schema.json",
	"acr_client_credential.v1.json":                                "acr_client_credential.v1.schema.json",
	"error.v1.json":                                                "error.v1.schema.json",
	"error_context_fabric_budget_refusal.v1.json":                  "error.v1.schema.json",
	"error_context_fabric_interpretation_contract.v1.json":         "error.v1.schema.json",
	"error_context_fabric_interpretation_rejected.v1.json":         "error.v1.schema.json",
	"error_context_fabric_synthesis_rejected.v1.json":              "error.v1.schema.json",
	"error_context_fabric_synthesis_contract.v1.json":              "error.v1.schema.json",
	"error_context_fabric_upstream_invalid_output.v1.json":         "error.v1.schema.json",
	"error_context_fabric_service_model_not_entitled.v1.json":      "error.v1.schema.json",
	"error_context_fabric_own_model_gate_closed.v1.json":           "error.v1.schema.json",
	"error_context_fabric_ai_features_turned_off.v1.json":          "error.v1.schema.json",
	"error_context_fabric_byo_no_investigate_config.v1.json":       "error.v1.schema.json",
	"error_context_fabric_own_model_turned_off.v1.json":            "error.v1.schema.json",
	"error_store_unavailable.v1.json":                              "error.v1.schema.json",
	"error_rate_limited_request_budget.v1.json":                    "error.v1.schema.json",
	"error_rate_limited_capabilities_budget.v1.json":               "error.v1.schema.json",
	"error_upstream_unavailable_model_deadline.v1.json":            "error.v1.schema.json",
	"mcp_context_for_task_request.v1.json":                         "mcp_context_for_task_request.v1.schema.json",
	"mcp_context_for_task_request_full.v1.json":                    "mcp_context_for_task_request.v1.schema.json",
	"mcp_context_for_task_response.v1.json":                        "mcp_context_for_task_response.v1.schema.json",
	"mcp_context_for_task_response_mixed.v1.json":                  "mcp_context_for_task_response.v1.schema.json",
	"mcp_source_evidence_request.v1.json":                          "mcp_source_evidence_request.v1.schema.json",
	"mcp_source_evidence_response.v1.json":                         "mcp_source_evidence_response.v1.schema.json",
	"mcp_source_evidence_request_batch.v1.json":                    "mcp_source_evidence_request.v1.schema.json",
	"mcp_source_evidence_response_batch.v1.json":                   "mcp_source_evidence_response.v1.schema.json",
	"mcp_record_episode_request.v1.json":                           "mcp_record_episode_request.v1.schema.json",
	"mcp_record_episode_response.v1.json":                          "mcp_record_episode_response.v1.schema.json",
	"mcp_investigate_question_request.v1.json":                     "mcp_investigate_question_request.v1.schema.json",
	"mcp_investigate_question_request_bare_receipt.v1.json":        "mcp_investigate_question_request.v1.schema.json",
	"mcp_investigate_question_response.v1.json":                    "mcp_investigate_question_response.v1.schema.json",
	"mcp_investigate_with_interpretation_request.v1.json":          "mcp_investigate_with_interpretation_request.v1.schema.json",
	"mcp_investigation_result_request.v1.json":                     "mcp_investigation_result_request.v1.schema.json",
	"mcp_investigation_result_response.v1.json":                    "mcp_investigation_result_response.v1.schema.json",
	"mcp_read_facts_request.v1.json":                               "mcp_read_facts_request.v1.schema.json",
	"mcp_read_facts_response.v1.json":                              "mcp_read_facts_response.v1.schema.json",
	"mcp_read_relationships_request.v1.json":                       "mcp_read_relationships_request.v1.schema.json",
	"mcp_read_relationships_response.v1.json":                      "mcp_read_relationships_response.v1.schema.json",
	"mcp_read_relationships_team_removed_response.v1.json":         "mcp_read_relationships_response.v1.schema.json",
	"mcp_read_relationships_team_unchecked_response.v1.json":       "mcp_read_relationships_response.v1.schema.json",
	"mcp_read_facts_team_unchecked_response.v1.json":               "mcp_read_facts_response.v1.schema.json",
	"evaluation_demo.v1.json":                                      "evaluation_demo.v1.schema.json",
	"mcp_data_catalog_request.v1.json":                             "mcp_data_catalog_request.v1.schema.json",
	"mcp_data_catalog_response.v1.json":                            "mcp_data_catalog_response.v1.schema.json",
	"mcp_find_subjects_request.v1.json":                            "mcp_find_subjects_request.v1.schema.json",
	"mcp_find_subjects_response.v1.json":                           "mcp_find_subjects_response.v1.schema.json",
	"mcp_run_operation_request.v1.json":                            "mcp_run_operation_request.v1.schema.json",
	"mcp_run_operation_response.v1.json":                           "mcp_run_operation_response.v1.schema.json",
	"mcp_graphql_query_request.v1.json":                            "mcp_graphql_query_request.v1.schema.json",
	"mcp_graphql_query_response.v1.json":                           "mcp_graphql_query_response.v1.schema.json",
	"device_authorization_request.v1.json":                         "device_authorization_request.v1.schema.json",
	"device_authorization_response.v1.json":                        "device_authorization_response.v1.schema.json",
	"device_token_request.v1.json":                                 "device_token_request.v1.schema.json",
	"device_token_response.v1.json":                                "device_token_response.v1.schema.json",
	"device_approval_request.v1.json":                              "device_approval_request.v1.schema.json",
	"device_approval_response.v1.json":                             "device_approval_response.v1.schema.json",
	"device_approval_preview_request.v1.json":                      "device_approval_preview_request.v1.schema.json",
	"device_approval_preview_response.v1.json":                     "device_approval_preview_response.v1.schema.json",
	"credential_ack_request.v1.json":                               "credential_ack_request.v1.schema.json",
	"credential_ack_response.v1.json":                              "credential_ack_response.v1.schema.json",
	"credential_revoke_request.v1.json":                            "credential_revoke_request.v1.schema.json",
	"credential_revoke_response.v1.json":                           "credential_revoke_response.v1.schema.json",
	"oauth_device_error.v1.json":                                   "oauth_device_error.v1.schema.json",
	"token_exchange_response.v1.json":                              "token_exchange_response.v1.schema.json",
	"oauth_token_exchange_error.v1.json":                           "oauth_token_exchange_error.v1.schema.json",
	"credential_rotate_request.v1.json":                            "credential_rotate_request.v1.schema.json",
	"credential_rotate_response.v1.json":                           "credential_rotate_response.v1.schema.json",
	"context_fabric_investigation_request.v1.json":                 "context_fabric_investigation_request.v1.schema.json",
	"context_fabric_investigation_result.v1.json":                  "context_fabric_investigation_result.v1.schema.json",
	"context_fabric_investigation_result_origin_state.v1.json":     "context_fabric_investigation_result.v1.schema.json",
	"context_fabric_investigation_result_status_corrected.v1.json": "context_fabric_investigation_result.v1.schema.json",
	// A historical-axis result (CHAOS-3781): carries the temporal label,
	// and shows a fact source that cannot answer for a past time degrading
	// in coverage while the rest of the answer survives (AC-3781-2/5).
	"context_fabric_investigation_result_historical.v1.json":    "context_fabric_investigation_result.v1.schema.json",
	"context_fabric_investigation_result_render_shapes.v1.json": "context_fabric_investigation_result.v1.schema.json",
	// An UNSUPPORTED result (no claimed fact, no citable evidence): the one
	// form whose deterministic_answer may be empty. Its disclosure carries
	// the explanation instead -- see context_fabric_deterministic_answer.go.
	"context_fabric_investigation_result_unsupported.v1.json": "context_fabric_investigation_result.v1.schema.json",
	"context_fabric_answer_projection.v1.json":                "context_fabric_answer_projection.v1.schema.json",
	// The historical-axis projection (CHAOS-3746): the only published
	// example carrying a temporal label, so the only one that validates
	// the label against the projection SCHEMA rather than only against
	// the Go validator.
	"context_fabric_answer_projection_historical.v1.json":    "context_fabric_answer_projection.v1.schema.json",
	"context_fabric_answer_projection_render_shapes.v1.json": "context_fabric_answer_projection.v1.schema.json",
	"context_fabric_projection_batch.v1.json":                "context_fabric_projection_batch.v1.schema.json",
	// CHAOS-4042: the anchor membership-verify semantic major. Wire fields
	// are identical to v1's investigation result; only structure_needs'
	// anchor_options meaning differs (membership, not unique-claimant) --
	// see context_fabric_investigation_result.v2.schema.json's own
	// description.
	"context_fabric_investigation_result.v2.json": "context_fabric_investigation_result.v2.schema.json",
	// CHAOS-12334: both MCP answer tools hand a stored v2 result over
	// unchanged. These two are the real handlers' output for the v2 result
	// above; internal/mcp holds them equal to it.
	"mcp_investigate_question_response_full_result_v2.v1.json": "mcp_investigate_question_response.v1.schema.json",
	"mcp_investigation_result_response_result_v2.v1.json":      "mcp_investigation_result_response.v1.schema.json",
	// CHAOS-11909: the investigation event stream. One golden per event
	// type, one per result body shape and one per error class a stream can
	// end on. Each file is the data line of one frame, encoded by
	// MarshalContextFabricInvestigationEventData from production Go values;
	// internal/contracts/v1's context_fabric_investigation_event_golden_test.go
	// holds every file equal to that encoding. The two whole-stream goldens
	// (*.sse) are not JSON; that test validates them frame by frame.
	"context_fabric_investigation_event_accepted.v1.json":               "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_stage_started.v1.json":          "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_stage_completed.v1.json":        "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_heartbeat.v1.json":              "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_result.v1.json":                 "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_result_v2.v1.json":              "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_result_projection.v1.json":      "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_error_model_deadline.v1.json":   "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_error_request_deadline.v1.json": "context_fabric_investigation_event.v1.schema.json",
	"context_fabric_investigation_event_error_budget_refusal.v1.json":   "context_fabric_investigation_event.v1.schema.json",
	"error_context_fabric_stream_unavailable.v1.json":                   "error.v1.schema.json",
	// CHAOS-11912: the four submission refusals, written by the route
	// itself (internal/api/context_fabric_submission_test.go).
	"error_context_fabric_submission_mismatch.v1.json":    "error.v1.schema.json",
	"error_context_fabric_submission_required.v1.json":    "error.v1.schema.json",
	"error_context_fabric_submission_in_progress.v1.json": "error.v1.schema.json",
	"error_context_fabric_submission_unsaved.v1.json":     "error.v1.schema.json",
	"error_context_fabric_submission_abandoned.v1.json":   "error.v1.schema.json",
	// CHAOS-12092: the per-call access read's refusals, written by the
	// route itself (internal/api/principal_access_test.go).
	"error_context_fabric_access_denied.v1.json":    "error.v1.schema.json",
	"error_context_fabric_no_repository.v1.json":    "error.v1.schema.json",
	"error_context_fabric_budget_exhausted.v1.json": "error.v1.schema.json",
	// CHAOS-11913: the submission status read, written by the route itself
	// (internal/api/context_fabric_submission_status_test.go).
	"context_fabric_submission_status.v1.json": "context_fabric_submission_status.v1.schema.json",
	// CHAOS-11914: the two shared-limit refusals, written by the route itself
	// (internal/api/context_fabric_submission_limits_test.go).
	"error_context_fabric_concurrency_limited.v1.json": "error.v1.schema.json",
	"error_context_fabric_allowance_exhausted.v1.json": "error.v1.schema.json",
	// The Ask Dev conversation contract: the thread read, the thread list
	// and the 409 thread_closed, each encoder output of production Go values
	// (internal/contracts/v1/context_fabric_thread_test.go).
	"context_fabric_thread.v1.json":              "context_fabric_thread.v1.schema.json",
	"context_fabric_thread_list.v1.json":         "context_fabric_thread_list.v1.schema.json",
	"error_context_fabric_thread_closed.v1.json": "error.v1.schema.json",
	// CHAOS-11949: the run lease's codes, written by the routes themselves
	// (internal/api/context_fabric_run_lease_test.go).
	"error_context_fabric_canceled.v1.json":       "error.v1.schema.json",
	"error_context_fabric_run_lost.v1.json":       "error.v1.schema.json",
	"error_context_fabric_cursor_expired.v1.json": "error.v1.schema.json",
	// Ask Dev answer feedback (CHAOS-11950): the read and the request,
	// encoder output of production Go values
	// (internal/contracts/v1/context_fabric_result_feedback_test.go).
	"context_fabric_result_feedback.v1.json":         "context_fabric_result_feedback.v1.schema.json",
	"context_fabric_result_feedback_unset.v1.json":   "context_fabric_result_feedback.v1.schema.json",
	"context_fabric_result_feedback_request.v1.json": "context_fabric_result_feedback_request.v1.schema.json",
	// CHAOS-12230: canonical-id test vectors, the output of the projector's
	// own id builders (internal/contextfabric/devhealthsource/
	// canonical_id_vectors_test.go holds the file equal to that output;
	// ACR_WRITE_CANONICAL_ID_VECTORS=1 rewrites it).
	"context_fabric_canonical_id_vectors.v1.json": "context_fabric_canonical_id_vectors.v1.schema.json",
}

// Options configures repository contract validation.
type Options struct {
	Root  string
	Write bool
	Quiet bool
	Out   io.Writer
}

// Run validates schemas, golden examples, OpenAPI artifacts, and MCP tool
// manifests. It uses only Go and local repository files. The heavier
// checks live in dedicated files: openapi.go (validateOpenAPI,
// validateDocumentRefs), mcp_manifest.go (validateMCP), and util.go
// (shared file/path helpers) to keep each file within the repository's
// module-size ceiling.
func Run(options Options) error {
	root, err := findRoot(options.Root)
	if err != nil {
		return err
	}
	if options.Out == nil {
		options.Out = io.Discard
	}
	check := &repositoryCheck{root: root, out: options.Out, quiet: options.Quiet}
	if err := check.loadSchemas(); err != nil {
		return err
	}
	if err := check.validateExamples(); err != nil {
		return err
	}
	if err := check.validateOpenAPI(options.Write); err != nil {
		return err
	}
	if err := check.validateMCP(); err != nil {
		return err
	}
	if err := check.validateMCPSchemaDefsSync(); err != nil {
		return err
	}
	return nil
}

type repositoryCheck struct {
	root     string
	out      io.Writer
	quiet    bool
	registry *schemaRegistry
}

func (c *repositoryCheck) ok(format string, args ...any) {
	if !c.quiet {
		fmt.Fprintf(c.out, "OK   "+format+"\n", args...)
	}
}

func (c *repositoryCheck) loadSchemas() error {
	directory := filepath.Join(c.root, "contracts", "jsonschema", "v1")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read schema directory: %w", err)
	}
	registry := newSchemaRegistry()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		value, err := decodeJSONFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return fmt.Errorf("decode schema %s: %w", entry.Name(), err)
		}
		schema, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("schema %s must be a JSON object", entry.Name())
		}
		if err := registry.add(entry.Name(), schema); err != nil {
			return fmt.Errorf("schema %s: %w", entry.Name(), err)
		}
	}
	if len(registry.byName) == 0 {
		return errors.New("no JSON Schemas found")
	}
	if err := registry.checkReferences(); err != nil {
		return err
	}
	c.registry = registry
	c.ok("%d JSON Schemas compile with the supported Draft 2020-12 profile", len(registry.byName))
	return nil
}

func (c *repositoryCheck) validateExamples() error {
	for _, name := range sortedKeys(exampleSchemaPairs) {
		value, err := decodeJSONFile(filepath.Join(c.root, "contracts", "examples", "v1", name))
		if err != nil {
			return fmt.Errorf("decode example %s: %w", name, err)
		}
		if err := c.registry.validate(exampleSchemaPairs[name], value); err != nil {
			return fmt.Errorf("example %s: %w", name, err)
		}
		if !isMCPResponseSchema(exampleSchemaPairs[name]) {
			c.ok("contracts/examples/v1/%s", name)
			continue
		}
		if err := c.registry.rejectUndeclared(exampleSchemaPairs[name], value); err != nil {
			return fmt.Errorf("example %s: %w", name, err)
		}
		c.ok("contracts/examples/v1/%s", name)
	}
	return nil
}
