package v1

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// The commit decision digest is validated in Go and described by a published
// schema in five copies. Each digest below gets one verdict from Go and one
// from every copy; they must agree.
func TestCommitDecisionDigestSchemaAgreesWithTheGoValidator(t *testing.T) {
	t.Parallel()
	subject := ContextFabricSubjectRef{Kind: ContextFabricSubjectRepository, CanonicalID: "repository:acme/widgets", Label: "acme/widgets"}
	digest := func(mutate func(*ContextFabricCommitDecisionDigest)) ContextFabricCommitDecisionDigest {
		d := ContextFabricCommitDecisionDigest{Subject: subject, CommitGate: "exact_index"}
		mutate(&d)
		return d
	}
	cases := map[string]ContextFabricCommitDecisionDigest{
		"unrecorded lookup":          digest(func(*ContextFabricCommitDecisionDigest) {}),
		"legacy flag only":           digest(func(d *ContextFabricCommitDecisionDigest) { d.AliasLookupComplete = true }),
		"complete with the flag":     digest(func(d *ContextFabricCommitDecisionDigest) { d.IdentityLookup, d.AliasLookupComplete = "complete", true }),
		"complete without the flag":  digest(func(d *ContextFabricCommitDecisionDigest) { d.IdentityLookup = "complete" }),
		"graph_lag without the flag": digest(func(d *ContextFabricCommitDecisionDigest) { d.IdentityLookup = "graph_lag" }),
		"graph_lag with the flag": digest(func(d *ContextFabricCommitDecisionDigest) {
			d.IdentityLookup, d.AliasLookupComplete = "graph_lag", true
		}),
		"incomplete without the flag": digest(func(d *ContextFabricCommitDecisionDigest) { d.IdentityLookup = "incomplete" }),
		"not_run_time_axis with the flag": digest(func(d *ContextFabricCommitDecisionDigest) {
			d.IdentityLookup, d.AliasLookupComplete = "not_run_time_axis", true
		}),
		"not_wired without the flag":     digest(func(d *ContextFabricCommitDecisionDigest) { d.IdentityLookup = "not_wired" }),
		"unknown token":                  digest(func(d *ContextFabricCommitDecisionDigest) { d.IdentityLookup = "lagging" }),
		"lookup recorded without a gate": digest(func(d *ContextFabricCommitDecisionDigest) { d.CommitGate, d.IdentityLookup = "", "graph_lag" }),
	}
	copies := []struct{ file, defs string }{
		{"contracts/jsonschema/v1/context_fabric_common.v1.schema.json", ""},
		{"contracts/jsonschema/v1/mcp_investigate_question_response.v1.schema.json", ""},
		{"contracts/jsonschema/v1/mcp_investigation_result_response.v1.schema.json", ""},
		{"internal/mcp/schemas/mcp_investigate_question_response.v1.schema.json", ""},
		{"internal/mcp/schemas/mcp_investigation_result_response.v1.schema.json", ""},
	}
	if err := cases["graph_lag without the flag"].Validate(); err != nil {
		t.Fatalf("control: the graph_lag digest is refused by Go: %v", err)
	}
	if err := cases["complete without the flag"].Validate(); err == nil {
		t.Fatal("control: the contradictory digest is accepted by Go")
	}
	for _, copyOf := range copies {
		raw, err := os.ReadFile(repopath.Path(t, splitPath(copyOf.file)...))
		if err != nil {
			t.Fatal(err)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		pointer := defPointer(document, "CommitDecisionDigest", "#")
		if pointer == "" {
			t.Fatalf("%s: no $defs holds CommitDecisionDigest", copyOf.file)
		}
		wrapper := map[string]any{}
		for key, value := range document.(map[string]any) {
			wrapper[key] = value
		}
		delete(wrapper, "$id")
		wrapper["$ref"] = pointer
		schema, err := gojsonschema.NewSchema(gojsonschema.NewGoLoader(wrapper))
		if err != nil {
			t.Fatalf("%s: %v", copyOf.file, err)
		}
		for name, d := range cases {
			if name == "graph_lag without the flag" {
				encoded, _ := json.Marshal(d)
				if result, err := schema.Validate(gojsonschema.NewBytesLoader(encoded)); err != nil || !result.Valid() {
					t.Fatalf("%s: control: the graph_lag digest is refused by the schema: %v %v", copyOf.file, err, result.Errors())
				}
			}
			encoded, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			result, err := schema.Validate(gojsonschema.NewBytesLoader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			goErr := d.Validate()
			if name == "unknown token" {
				if !result.Valid() || goErr == nil {
					t.Errorf("%s / %s: an additive vocabulary value must pass the schema (valid=%t) and fail Go Validate (%v)", copyOf.file, name, result.Valid(), goErr)
				}
				continue
			}
			if result.Valid() != (goErr == nil) {
				t.Errorf("%s / %s: schema valid=%t (%v), Go Validate = %v", copyOf.file, name, result.Valid(), result.Errors(), goErr)
			}
		}
	}
}

func splitPath(path string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	return parts
}

// defPointer returns the JSON pointer of the first "$defs" member called name.
func defPointer(node any, name, at string) string {
	switch value := node.(type) {
	case map[string]any:
		if defs, ok := value["$defs"].(map[string]any); ok {
			if _, has := defs[name]; has {
				return at + "/$defs/" + name
			}
		}
		for key, child := range value {
			if found := defPointer(child, name, at+"/"+key); found != "" {
				return found
			}
		}
	case []any:
		for index, child := range value {
			if found := defPointer(child, name, at+"/"+strconv.Itoa(index)); found != "" {
				return found
			}
		}
	}
	return ""
}
