package contractcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var exampleSchemaPairs = map[string]string{
	"context_packet_request.v1.json": "context_packet_request.v1.schema.json",
	"context_packet_item.v1.json":    "context_packet_item.v1.schema.json",
	"context_packet.v1.json":         "context_packet.v1.schema.json",
	"evidence_ref.v1.json":           "evidence_ref.v1.schema.json",
	"expanded_evidence.v1.json":      "expanded_evidence.v1.schema.json",
	"capabilities.v1.json":           "capabilities.v1.schema.json",
	"agent_episode_create.v1.json":   "agent_episode_create.v1.schema.json",
	"agent_episode.v1.json":          "agent_episode.v1.schema.json",
	"acr_client_credential.v1.json":  "acr_client_credential.v1.schema.json",
	"error.v1.json":                  "error.v1.schema.json",
}

// Options configures repository contract validation.
type Options struct {
	Root  string
	Write bool
	Quiet bool
	Out   io.Writer
}

// Run validates schemas, golden examples, OpenAPI artifacts, and MCP tool
// manifests. It uses only Go and local repository files.
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

func (c *repositoryCheck) validateOpenAPI(write bool) error {
	jsonPath := filepath.Join(c.root, "contracts", "openapi", "acr-v1.json")
	yamlPath := filepath.Join(c.root, "contracts", "openapi", "acr-v1.yaml")
	value, err := decodeJSONFile(jsonPath)
	if err != nil {
		return fmt.Errorf("decode OpenAPI JSON: %w", err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		return errors.New("OpenAPI JSON must be an object")
	}
	version, _ := document["openapi"].(string)
	if !strings.HasPrefix(version, "3.1") {
		return fmt.Errorf("OpenAPI version must be 3.1.x, got %q", version)
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		return errors.New("OpenAPI paths must be a non-empty object")
	}
	allowedKeys := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true,
		"delete": true, "head": true, "options": true, "trace": true,
		"parameters": true, "summary": true, "description": true, "$ref": true,
	}
	operationIDs := map[string]string{}
	for _, pathName := range sortedKeys(paths) {
		if pathName != "/healthz" && pathName != "/readyz" && !strings.HasPrefix(pathName, "/api/v1/agent-context/") {
			return fmt.Errorf("OpenAPI path outside ACR namespace: %s", pathName)
		}
		pathItem, ok := paths[pathName].(map[string]any)
		if !ok {
			return fmt.Errorf("OpenAPI path %s must be an object", pathName)
		}
		for method, raw := range pathItem {
			if !allowedKeys[method] && !strings.HasPrefix(method, "x-") {
				return fmt.Errorf("OpenAPI path %s has unsupported method/key %s", pathName, method)
			}
			if method == "parameters" || method == "summary" || method == "description" || method == "$ref" || strings.HasPrefix(method, "x-") {
				continue
			}
			operation, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("OpenAPI operation %s %s must be an object", strings.ToUpper(method), pathName)
			}
			operationID, _ := operation["operationId"].(string)
			if operationID == "" {
				return fmt.Errorf("OpenAPI operation %s %s is missing operationId", strings.ToUpper(method), pathName)
			}
			if previous, exists := operationIDs[operationID]; exists {
				return fmt.Errorf("duplicate operationId %q at %s and %s %s", operationID, previous, strings.ToUpper(method), pathName)
			}
			operationIDs[operationID] = strings.ToUpper(method) + " " + pathName
		}
	}
	if err := validateDocumentRefs(c.root, jsonPath, value); err != nil {
		return fmt.Errorf("OpenAPI references: %w", err)
	}
	generated, err := marshalCanonicalYAML(value)
	if err != nil {
		return fmt.Errorf("generate OpenAPI YAML: %w", err)
	}
	if write {
		if err := os.WriteFile(yamlPath, generated, 0o644); err != nil {
			return fmt.Errorf("write OpenAPI YAML: %w", err)
		}
		c.ok("generated contracts/openapi/acr-v1.yaml")
	} else {
		current, err := os.ReadFile(yamlPath)
		if err != nil {
			return fmt.Errorf("read OpenAPI YAML: %w", err)
		}
		if !bytes.Equal(current, generated) {
			return errors.New("OpenAPI YAML is stale; run `make contract-write`")
		}
	}
	c.ok("contracts/openapi/acr-v1.json + generated acr-v1.yaml")
	return nil
}

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
	expected := map[string]bool{"context_for_task": false, "source_evidence": false, "record_episode": false}
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
			if reference, ok := tool[field].(string); ok {
				resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(reference)))
				if !pathWithin(c.root, resolved) {
					return fmt.Errorf("MCP tool %s %s escapes repository: %s", name, field, reference)
				}
				if _, err := os.Stat(resolved); err != nil {
					return fmt.Errorf("MCP tool %s %s is missing: %s", name, field, reference)
				}
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

func validateDocumentRefs(root, documentPath string, value any) error {
	var walk func(any) error
	walk = func(current any) error {
		switch node := current.(type) {
		case map[string]any:
			for key, child := range node {
				if key == "$ref" {
					reference, ok := child.(string)
					if !ok {
						return errors.New("$ref must be a string")
					}
					if strings.HasPrefix(reference, "#") || strings.Contains(reference, "://") {
						continue
					}
					file := strings.SplitN(reference, "#", 2)[0]
					resolved := filepath.Clean(filepath.Join(filepath.Dir(documentPath), filepath.FromSlash(file)))
					if !pathWithin(root, resolved) {
						return fmt.Errorf("$ref escapes repository: %s", reference)
					}
					if _, err := os.Stat(resolved); err != nil {
						return fmt.Errorf("missing $ref %s", reference)
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range node {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func decodeJSONFile(path string) (any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing JSON content: %w", err)
	}
	return value, nil
}

func findRoot(start string) (string, error) {
	if strings.TrimSpace(start) == "" {
		start = "."
	}
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(current, "contracts")); err == nil {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("could not find repository root from %s", start)
		}
		current = parent
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
