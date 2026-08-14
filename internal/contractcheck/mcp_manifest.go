package contractcheck

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (c *repositoryCheck) validateMCP() error {
	path := filepath.Join(c.root, "contracts", "mcp", "tools.v1.json")
	value, err := decodeJSONFile(path)
	if err != nil {
		return fmt.Errorf("decode MCP manifest: %w", err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		return errors.New("MCP manifest must be an object")
	}
	if document["schema_version"] != "mcp_tools.v1" {
		return errors.New("MCP schema_version must be mcp_tools.v1")
	}
	tools, ok := document["tools"].([]any)
	if !ok {
		return errors.New("MCP tools must be an array")
	}
	// The manifest is a CLOSED set, not a minimum: every tool the sidecar
	// may expose is named here, so adding one is a deliberate contract
	// change rather than something that can appear by accident.
	// investigate_question and investigation_result are the CHAOS-3746
	// answer surface.
	expected := map[string]bool{
		"context_for_task":     false,
		"source_evidence":      false,
		"investigate_question": false,
		"investigation_result": false,
		"record_episode":       false,
	}
	for index, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("MCP tool %d must be an object", index)
		}
		name, _ := tool["name"].(string)
		if _, exists := expected[name]; !exists {
			return fmt.Errorf("unexpected MCP tool %q", name)
		}
		if expected[name] {
			return fmt.Errorf("duplicate MCP tool %q", name)
		}
		expected[name] = true
		if description, _ := tool["description"].(string); strings.TrimSpace(description) == "" {
			return fmt.Errorf("MCP tool %s requires a description", name)
		}
		for _, field := range []string{"input_schema_ref", "output_schema_ref"} {
			if err := c.resolveToolSchemaRef(path, tool, name, field); err != nil {
				return err
			}
		}
		if inline, ok := tool["input_schema"].(map[string]any); ok {
			if err := c.registry.checkSchema(inline, "$mcp."+name+".input_schema"); err != nil {
				return err
			}
		}
		readOnly, ok := tool["read_only"].(bool)
		if !ok {
			return fmt.Errorf("MCP tool %s requires read_only", name)
		}
		if name == "record_episode" {
			if readOnly {
				return errors.New("record_episode must not be read_only")
			}
			if disabled, _ := tool["disabled_by_default"].(bool); !disabled {
				return errors.New("record_episode must be disabled_by_default")
			}
		} else if !readOnly {
			return fmt.Errorf("MCP read tool %s must be read_only", name)
		}
	}
	for name, found := range expected {
		if !found {
			return fmt.Errorf("missing MCP tool %s", name)
		}
	}
	c.ok("contracts/mcp/tools.v1.json")
	return nil
}

// resolveToolSchemaRef requires field (input_schema_ref or
// output_schema_ref) to be present on tool and a correctly typed,
// repository-relative, resolvable string reference. Fields that are
// absent, non-string, empty, or that resolve outside the repository or to
// a missing file are rejected: a contract-bearing MCP tool must always
// declare a resolvable schema reference, never fail open on a malformed
// or missing one.
func (c *repositoryCheck) resolveToolSchemaRef(manifestPath string, tool map[string]any, name, field string) error {
	raw, exists := tool[field]
	if !exists {
		return fmt.Errorf("MCP tool %s requires %s", name, field)
	}
	reference, ok := raw.(string)
	if !ok || strings.TrimSpace(reference) == "" {
		return fmt.Errorf("MCP tool %s %s must be a non-empty string", name, field)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(reference)))
	if !pathWithin(c.root, resolved) {
		return fmt.Errorf("MCP tool %s %s escapes repository: %s", name, field, reference)
	}
	if _, err := os.Stat(resolved); err != nil {
		return fmt.Errorf("MCP tool %s %s is missing: %s", name, field, reference)
	}
	return nil
}
