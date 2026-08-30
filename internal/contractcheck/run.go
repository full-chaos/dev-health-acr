package contractcheck

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var exampleSchemaPairs = map[string]string{
	"context_packet_request.v1.json":                       "context_packet_request.v1.schema.json",
	"context_packet_item.v1.json":                          "context_packet_item.v1.schema.json",
	"context_packet.v1.json":                               "context_packet.v1.schema.json",
	"evidence_ref.v1.json":                                 "evidence_ref.v1.schema.json",
	"expanded_evidence.v1.json":                            "expanded_evidence.v1.schema.json",
	"capabilities.v1.json":                                 "capabilities.v1.schema.json",
	"agent_episode_create.v1.json":                         "agent_episode_create.v1.schema.json",
	"agent_episode.v1.json":                                "agent_episode.v1.schema.json",
	"acr_client_credential.v1.json":                        "acr_client_credential.v1.schema.json",
	"error.v1.json":                                        "error.v1.schema.json",
	"error_context_fabric_interpretation_rejected.v1.json": "error.v1.schema.json",
	"error_context_fabric_synthesis_rejected.v1.json":      "error.v1.schema.json",
	"error_context_fabric_upstream_invalid_output.v1.json": "error.v1.schema.json",
	"mcp_context_for_task_request.v1.json":                 "mcp_context_for_task_request.v1.schema.json",
	"mcp_context_for_task_request_full.v1.json":            "mcp_context_for_task_request.v1.schema.json",
	"mcp_context_for_task_response.v1.json":                "mcp_context_for_task_response.v1.schema.json",
	"mcp_context_for_task_response_mixed.v1.json":          "mcp_context_for_task_response.v1.schema.json",
	"mcp_source_evidence_request.v1.json":                  "mcp_source_evidence_request.v1.schema.json",
	"mcp_source_evidence_response.v1.json":                 "mcp_source_evidence_response.v1.schema.json",
	"mcp_record_episode_request.v1.json":                   "mcp_record_episode_request.v1.schema.json",
	"mcp_record_episode_response.v1.json":                  "mcp_record_episode_response.v1.schema.json",
	"mcp_investigate_question_request.v1.json":             "mcp_investigate_question_request.v1.schema.json",
	"mcp_investigate_question_response.v1.json":            "mcp_investigate_question_response.v1.schema.json",
	"mcp_investigation_result_request.v1.json":             "mcp_investigation_result_request.v1.schema.json",
	"mcp_investigation_result_response.v1.json":            "mcp_investigation_result_response.v1.schema.json",
	"evaluation_demo.v1.json":                              "evaluation_demo.v1.schema.json",
	"device_authorization_request.v1.json":                 "device_authorization_request.v1.schema.json",
	"device_authorization_response.v1.json":                "device_authorization_response.v1.schema.json",
	"device_token_request.v1.json":                         "device_token_request.v1.schema.json",
	"device_token_response.v1.json":                        "device_token_response.v1.schema.json",
	"device_approval_request.v1.json":                      "device_approval_request.v1.schema.json",
	"device_approval_response.v1.json":                     "device_approval_response.v1.schema.json",
	"device_approval_preview_request.v1.json":              "device_approval_preview_request.v1.schema.json",
	"device_approval_preview_response.v1.json":             "device_approval_preview_response.v1.schema.json",
	"credential_revoke_request.v1.json":                    "credential_revoke_request.v1.schema.json",
	"credential_revoke_response.v1.json":                   "credential_revoke_response.v1.schema.json",
	"oauth_device_error.v1.json":                           "oauth_device_error.v1.schema.json",
	"token_exchange_response.v1.json":                      "token_exchange_response.v1.schema.json",
	"oauth_token_exchange_error.v1.json":                   "oauth_token_exchange_error.v1.schema.json",
	"credential_rotate_request.v1.json":                    "credential_rotate_request.v1.schema.json",
	"credential_rotate_response.v1.json":                   "credential_rotate_response.v1.schema.json",
	"context_fabric_investigation_request.v1.json":         "context_fabric_investigation_request.v1.schema.json",
	"context_fabric_investigation_result.v1.json":          "context_fabric_investigation_result.v1.schema.json",
	// A historical-axis result (CHAOS-3781): carries the temporal label,
	// and shows a fact source that cannot answer for a past time degrading
	// in coverage while the rest of the answer survives (AC-3781-2/5).
	"context_fabric_investigation_result_historical.v1.json":    "context_fabric_investigation_result.v1.schema.json",
	"context_fabric_investigation_result_render_shapes.v1.json": "context_fabric_investigation_result.v1.schema.json",
	"context_fabric_answer_projection.v1.json":                  "context_fabric_answer_projection.v1.schema.json",
	// The historical-axis projection (CHAOS-3746): the only published
	// example carrying a temporal label, so the only one that validates
	// the label against the projection SCHEMA rather than only against
	// the Go validator.
	"context_fabric_answer_projection_historical.v1.json":    "context_fabric_answer_projection.v1.schema.json",
	"context_fabric_answer_projection_render_shapes.v1.json": "context_fabric_answer_projection.v1.schema.json",
	"context_fabric_projection_batch.v1.json":                "context_fabric_projection_batch.v1.schema.json",
	"context_fabric_org_model_config.v1.json":                "context_fabric_org_model_config.v1.schema.json",
	"context_fabric_org_model_config_write_request.v1.json":  "context_fabric_org_model_config_write_request.v1.schema.json",
	// CHAOS-4042: the anchor membership-verify semantic major. Wire fields
	// are identical to v1's investigation result; only structure_needs'
	// anchor_options meaning differs (membership, not unique-claimant) --
	// see context_fabric_investigation_result.v2.schema.json's own
	// description.
	"context_fabric_investigation_result.v2.json": "context_fabric_investigation_result.v2.schema.json",
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
		c.ok("contracts/examples/v1/%s", name)
	}
	return nil
}
