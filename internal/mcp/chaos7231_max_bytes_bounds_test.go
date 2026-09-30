package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7231 (the class of CHAOS-4867): the published max_bytes bounds of the
// data tools must equal the bounds the Go validators enforce. run_operation
// once published `minimum: 1` while its validator (and the OpenAPI) accepted
// 0, where 0 means the 32768 default; a schema-validating client that sent 0
// was refused by the schema alone.
//
// The Go side is measured by PROBING the validator, not by re-reading a
// constant: a published minimum m is right only if Validate accepts m and
// refuses m-1; a published maximum M only if Validate accepts M and refuses
// M+1. A schema bound that is looser or stricter than Go by one fails here.
func TestPublishedMaxBytesBoundsMatchTheGoValidators(t *testing.T) {
	type tool struct {
		name    string
		accepts func(maxBytes int) bool
		// wantMin/wantMax pin the intended contract, so a change of BOTH the
		// schema and the validator to the same wrong value is also caught.
		wantMin, wantMax int
		embedded         string
		canonical        string
		// openapi names the components.schemas entry that carries the same
		// bound ("" = the tool has no hosted OpenAPI request schema).
		openapi string
	}
	tools := []tool{
		{
			name: "run_operation",
			accepts: func(n int) bool {
				return contractsv1.MCPRunOperationRequest{Operation: "hotspots", MaxBytes: n}.Validate() == nil
			},
			wantMin: 0, wantMax: directread.MaxOperationMaxBytes,
			embedded:  runOperationRequestSchemaFile,
			canonical: "contracts/jsonschema/v1/mcp_run_operation_request.v1.schema.json",
			openapi:   "DataOperationRequest",
		},
		{
			name: "graphql_query",
			accepts: func(n int) bool {
				return contractsv1.MCPGraphQLQueryRequest{Query: "{ x }", MaxBytes: n}.Validate() == nil
			},
			wantMin: 0, wantMax: directread.MaxOperationMaxBytes,
			embedded:  graphqlQueryRequestSchemaFile,
			canonical: "contracts/jsonschema/v1/mcp_graphql_query_request.v1.schema.json",
			openapi:   "DataGraphQLRequest",
		},
		{
			name: "read_facts",
			accepts: func(n int) bool {
				in := readFactsInput{
					Kinds:    []string{"investment"},
					Subjects: []readFactsSubjectInput{{Kind: "team", CanonicalID: "team:1"}},
					MaxBytes: n,
				}
				return in.validate() == nil
			},
			// 0 means "absent" for read_facts (the field is an int, not a
			// pointer), so the probe cannot see the schema's explicit-0
			// refusal; the minimum it can see is 4096.
			wantMin: readFactsMinMaxBytes, wantMax: readFactsMaxMaxBytes,
			embedded:  readFactsRequestSchemaFile,
			canonical: "contracts/jsonschema/v1/mcp_read_facts_request.v1.schema.json",
		},
	}

	root := findRepoRoot(t)
	bound := func(t *testing.T, where string, prop map[string]any) (int, int) {
		t.Helper()
		lo, okLo := prop["minimum"].(float64)
		hi, okHi := prop["maximum"].(float64)
		if !okLo || !okHi {
			t.Fatalf("%s: max_bytes must declare both minimum and maximum, got %v", where, prop)
		}
		return int(lo), int(hi)
	}
	for _, tl := range tools {
		t.Run(tl.name, func(t *testing.T) {
			check := func(where string, lo, hi int) {
				t.Helper()
				if lo != tl.wantMin || hi != tl.wantMax {
					t.Errorf("%s: max_bytes bounds are [%d, %d], want [%d, %d]", where, lo, hi, tl.wantMin, tl.wantMax)
				}
				if !tl.accepts(lo) || tl.accepts(lo-1) {
					t.Errorf("%s: published minimum %d is not the Go validator's lowest accepted value (accepts(min)=%v, accepts(min-1)=%v)", where, lo, tl.accepts(lo), tl.accepts(lo-1))
				}
				if !tl.accepts(hi) || tl.accepts(hi+1) {
					t.Errorf("%s: published maximum %d is not the Go validator's highest accepted value (accepts(max)=%v, accepts(max+1)=%v)", where, hi, tl.accepts(hi), tl.accepts(hi+1))
				}
			}

			embedded := readEmbeddedJSON(t, tl.embedded)["properties"].(map[string]any)["max_bytes"].(map[string]any)
			lo, hi := bound(t, tl.embedded, embedded)
			check("embedded "+tl.embedded, lo, hi)

			data, err := os.ReadFile(filepath.Join(root, tl.canonical))
			if err != nil {
				t.Fatal(err)
			}
			var canonical struct {
				Properties map[string]map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(data, &canonical); err != nil {
				t.Fatal(err)
			}
			lo, hi = bound(t, tl.canonical, canonical.Properties["max_bytes"])
			check("canonical "+tl.canonical, lo, hi)

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
			lo, hi = bound(t, "openapi "+tl.openapi, spec.Components.Schemas[tl.openapi].Properties["max_bytes"])
			check("openapi "+tl.openapi, lo, hi)
		})
	}
}
