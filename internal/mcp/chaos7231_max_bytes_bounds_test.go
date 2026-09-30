package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7231 (the class of CHAOS-4867): the published max_bytes bounds of the
// data tools must equal what the Go validators accept. run_operation once
// published `minimum: 1` while its validator (and the OpenAPI) accepted 0, where
// 0 means the 32768 default; a schema-validating client that sent 0 was refused
// by the schema alone.
//
// This test compares the VALIDATORS with the schema, by behaviour: the Go side
// is measured by calling it, not by re-reading a constant. It is separate from
// TestEveryPublishedIntegerFieldIsHandledAsItsSchemaSays, which drives the whole
// tools/call path (where the integer middleware makes the server follow the
// schema whatever the schema says) and so cannot see a schema that is stricter
// than the handler's own validator.
func TestPublishedMaxBytesBoundsMatchTheGoValidators(t *testing.T) {
	// The cells straddle every bound any of the three tools states: 0 (the
	// default), 1 and 4095/4096 (read_facts' floor), the run_operation default,
	// and the 262144 ceiling with its neighbours.
	cells := []int{-1, 0, 1, 4095, 4096, 4097, 32768, 65536, 262143, 262144, 262145}
	tools := []struct {
		name      string
		accepts   func(maxBytes int) bool
		embedded  string
		canonical string
		// wantAccepted pins the intended contract, so a change of BOTH sides to
		// the same wrong value also fails.
		wantAccepted map[int]bool
		// openapi names the components.schemas entry that carries the same
		// bound ("" = the tool has no hosted OpenAPI request schema).
		openapi string
	}{
		{
			name: "run_operation",
			accepts: func(n int) bool {
				return contractsv1.MCPRunOperationRequest{Operation: "hotspots", MaxBytes: n}.Validate() == nil
			},
			embedded:     runOperationRequestSchemaFile,
			canonical:    "contracts/jsonschema/v1/mcp_run_operation_request.v1.schema.json",
			wantAccepted: acceptedSet(cells, func(n int) bool { return n >= 0 && n <= directread.MaxOperationMaxBytes }),
			openapi:      "DataOperationRequest",
		},
		{
			name: "graphql_query",
			accepts: func(n int) bool {
				return contractsv1.MCPGraphQLQueryRequest{Query: "{ x }", MaxBytes: n}.Validate() == nil
			},
			embedded:     graphqlQueryRequestSchemaFile,
			canonical:    "contracts/jsonschema/v1/mcp_graphql_query_request.v1.schema.json",
			wantAccepted: acceptedSet(cells, func(n int) bool { return n >= 0 && n <= directread.MaxOperationMaxBytes }),
			openapi:      "DataGraphQLRequest",
		},
		{
			// 0 means the default (65536); otherwise 4096 to 262144.
			name: "read_facts",
			accepts: func(n int) bool {
				in := readFactsInput{
					Kinds:    []string{"investment"},
					Subjects: []readFactsSubjectInput{{Kind: "team", CanonicalID: "team:1"}},
					MaxBytes: n,
				}
				return in.validate() == nil
			},
			embedded:     readFactsRequestSchemaFile,
			canonical:    "contracts/jsonschema/v1/mcp_read_facts_request.v1.schema.json",
			wantAccepted: acceptedSet(cells, func(n int) bool { return n == 0 || (n >= directread.MinMaxBytes && n <= directread.MaxMaxBytes) }),
		},
	}
	root := findRepoRoot(t)
	for _, tl := range tools {
		t.Run(tl.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, tl.canonical))
			if err != nil {
				t.Fatal(err)
			}
			embedded, err := schemaFiles.ReadFile(tl.embedded)
			if err != nil {
				t.Fatal(err)
			}
			documents := map[string][]byte{"canonical " + tl.canonical: data, "embedded " + tl.embedded: embedded}
			for where, document := range documents {
				resolved, base := resolveWithExample(t, document)
				for _, n := range cells {
					instance := cloneJSON(base).(map[string]any)
					instance["max_bytes"] = n
					bySchema := resolved.Validate(instance) == nil
					byGo := tl.accepts(n)
					if bySchema != byGo {
						t.Errorf("%s: max_bytes=%d: the schema %s it, the Go validator %s it", where, n, verdict(bySchema), verdict(byGo))
					}
					if want := tl.wantAccepted[n]; want != byGo {
						t.Errorf("%s: max_bytes=%d: the Go validator %s it, the contract is that it %s it", where, n, verdict(byGo), verdict(want))
					}
				}
			}
			if tl.openapi == "" {
				return
			}
			raw, err := os.ReadFile(filepath.Join(root, "contracts/openapi/acr-v1.json"))
			if err != nil {
				t.Fatal(err)
			}
			var spec struct {
				Components struct {
					Schemas map[string]struct {
						Properties map[string]map[string]any `json:"properties"`
					} `json:"schemas"`
				} `json:"components"`
			}
			if err := json.Unmarshal(raw, &spec); err != nil {
				t.Fatal(err)
			}
			prop := spec.Components.Schemas[tl.openapi].Properties["max_bytes"]
			lo, okLo := prop["minimum"].(float64)
			hi, okHi := prop["maximum"].(float64)
			if !okLo || !okHi {
				t.Fatalf("openapi %s: max_bytes must declare both minimum and maximum, got %v", tl.openapi, prop)
			}
			for _, n := range cells {
				byOpenAPI := float64(n) >= lo && float64(n) <= hi
				if byGo := tl.accepts(n); byOpenAPI != byGo {
					t.Errorf("openapi %s: max_bytes=%d: the OpenAPI [%v, %v] %s it, the Go validator %s it", tl.openapi, n, lo, hi, verdict(byOpenAPI), verdict(byGo))
				}
			}
		})
	}
}

// resolveWithExample compiles a request schema and returns its first example as
// an object to vary.
func resolveWithExample(t *testing.T, document []byte) (*jsonschema.Resolved, map[string]any) {
	t.Helper()
	var schema jsonschema.Schema
	if err := json.Unmarshal(document, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Examples []map[string]any `json:"examples"`
	}
	if err := json.Unmarshal(document, &raw); err != nil || len(raw.Examples) == 0 {
		t.Fatalf("the request schema has no example to vary: %v", err)
	}
	return resolved, raw.Examples[0]
}

func acceptedSet(cells []int, accepts func(int) bool) map[int]bool {
	out := map[int]bool{}
	for _, n := range cells {
		out[n] = accepts(n)
	}
	return out
}
