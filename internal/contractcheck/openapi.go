package contractcheck

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
