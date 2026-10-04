package v1

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

const (
	hostedRequestSchemaFile = "context_fabric_investigation_request.v1.schema.json"
	mcpRequestSchemaFile    = "mcp_investigate_with_interpretation_request.v1.schema.json"
)

func writeBackHash() string { return strings.Repeat("a", 64) }

func hostedWriteBackBase() map[string]any {
	return map[string]any{
		"schema_version": "context_fabric_investigation_request.v1",
		"request_id":     "req_write_back_1",
		"question":       "Is the ledger ready?",
		"time_context":   map[string]any{"axis": "current"},
		"options": map[string]any{
			"max_subject_candidates": 5, "max_cohort_members": 25, "max_relationship_paths": 10, "max_drivers": 5,
			"max_evidence_refs": 50, "max_serialized_bytes": 262144, "allow_clarification": true, "include_debug": false,
		},
		"consumer": map[string]any{"name": "test", "version": "1", "surface": "test"},
	}
}

func hostedWriteBack() map[string]any {
	doc := hostedWriteBackBase()
	doc["synthesis_mode"] = "client"
	doc["supplied_interpretation"] = map[string]any{
		"output": map[string]any{}, "model_output_version": "v", "prompt_version": "p", "system_sha256": writeBackHash(),
	}
	doc["supplied_synthesis"] = map[string]any{
		"output": map[string]any{}, "model_output_version": "v", "prompt_version": "p",
		"system_sha256": writeBackHash(), "input_sha256": writeBackHash(),
	}
	return doc
}

func mcpWriteBackBase() map[string]any {
	return map[string]any{
		"question": "Is the ledger ready?", "interpretation": map[string]any{},
		"contract": map[string]any{"model_output_version": "v", "prompt_version": "p", "system_sha256": writeBackHash()},
	}
}

func mcpWriteBack() map[string]any {
	doc := mcpWriteBackBase()
	doc["synthesis"] = "client"
	doc["synthesis_output"] = map[string]any{}
	doc["synthesis_contract"] = map[string]any{
		"model_output_version": "v", "prompt_version": "p", "system_sha256": writeBackHash(), "input_sha256": writeBackHash(),
	}
	return doc
}

func without(doc map[string]any, keys ...string) map[string]any {
	copied := map[string]any{}
	for key, value := range doc {
		copied[key] = value
	}
	for _, key := range keys {
		delete(copied, key)
	}
	return copied
}

func with(doc map[string]any, key string, value any) map[string]any {
	copied := without(doc)
	copied[key] = value
	return copied
}

func validatePublished(t *testing.T, schemaFile string, doc map[string]any) []string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []string
	verdicts = append(verdicts, "contractcheck="+okOrRefused(contractcheck.ValidateSerialized("", schemaFile, raw)))
	if schemaFile == mcpRequestSchemaFile {
		schema, err := gojsonschema.NewSchema(gojsonschema.NewReferenceLoader("file://" + repopath.Path(t, "contracts", "jsonschema", "v1", schemaFile)))
		if err != nil {
			t.Fatalf("load %s: %v", schemaFile, err)
		}
		result, err := schema.Validate(gojsonschema.NewBytesLoader(raw))
		if err != nil {
			t.Fatal(err)
		}
		verdicts = append(verdicts, "gojsonschema="+okOrRefused(boolError(result.Valid())))
	}
	return verdicts
}

func okOrRefused(err error) string {
	if err != nil {
		return "refused"
	}
	return "accepted"
}

func boolError(valid bool) error {
	if valid {
		return nil
	}
	return errors.New("schema refused the document")
}

func requireVerdict(t *testing.T, schemaFile, name string, doc map[string]any, want string) {
	t.Helper()
	for _, verdict := range validatePublished(t, schemaFile, doc) {
		if !strings.HasSuffix(verdict, "="+want) {
			t.Fatalf("%s: %s, want %s", name, verdict, want)
		}
	}
}

func TestHostedRequestSchemaEncodesTheWriteBackConditions(t *testing.T) {
	t.Parallel()
	full := hostedWriteBack()
	accepted := map[string]map[string]any{
		"no write-back":                     hostedWriteBackBase(),
		"client mode without a draft":       with(hostedWriteBackBase(), "synthesis_mode", "client"),
		"a full write-back":                 full,
		"an interpretation without a draft": without(full, "supplied_synthesis"),
	}
	for name, doc := range accepted {
		requireVerdict(t, hostedRequestSchemaFile, name, doc, "accepted")
	}
	refused := map[string]map[string]any{
		"a draft with the mode absent":              without(full, "synthesis_mode"),
		"a draft with the mode server":              with(full, "synthesis_mode", "server"),
		"a draft without a supplied interpretation": without(full, "supplied_interpretation"),
	}
	for name, doc := range refused {
		requireVerdict(t, hostedRequestSchemaFile, name, doc, "refused")
	}
}

func TestMCPRequestSchemaEncodesTheWriteBackConditions(t *testing.T) {
	t.Parallel()
	full := mcpWriteBack()
	accepted := map[string]map[string]any{
		"no write-back":               mcpWriteBackBase(),
		"client mode without a draft": with(mcpWriteBackBase(), "synthesis", "client"),
		"a full write-back":           full,
	}
	for name, doc := range accepted {
		requireVerdict(t, mcpRequestSchemaFile, name, doc, "accepted")
	}
	refused := map[string]map[string]any{
		"a lone synthesis_output":            without(full, "synthesis_contract"),
		"a lone synthesis_contract":          without(full, "synthesis_output"),
		"the pair with synthesis not client": with(full, "synthesis", "server"),
		"the pair without synthesis":         without(full, "synthesis"),
	}
	for name, doc := range refused {
		requireVerdict(t, mcpRequestSchemaFile, name, doc, "refused")
	}
}
