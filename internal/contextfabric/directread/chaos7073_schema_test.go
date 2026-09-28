package directread

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// CHAOS-7073: the published MCP schemas for read_facts must accept exactly
// what this package marshals. Two guards: a real, rich response validates
// against the response schema, and every Go wire key of every response type
// is a published property (and the reverse), so a field added to a struct
// without a schema property fails here rather than at a consumer.

func readFactsSchemaPath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", "jsonschema", "v1", name)
}

func loadSchema(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(readFactsSchemaPath(t, name))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func validateAgainst(t *testing.T, schemaName string, document []byte) {
	t.Helper()
	result, err := gojsonschema.Validate(
		gojsonschema.NewReferenceLoader("file://"+readFactsSchemaPath(t, schemaName)),
		gojsonschema.NewBytesLoader(document))
	if err != nil {
		t.Fatalf("validate %s: %v", schemaName, err)
	}
	if !result.Valid() {
		var problems []string
		for _, problem := range result.Errors() {
			problems = append(problems, problem.String())
		}
		t.Fatalf("document does not satisfy %s:\n%s\n%s", schemaName, strings.Join(problems, "\n"), document)
	}
}

func jsonKeys(rt reflect.Type) []string {
	var keys []string
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			keys = append(keys, tag)
		}
	}
	sort.Strings(keys)
	return keys
}

func schemaKeys(node map[string]any) []string {
	props, _ := node["properties"].(map[string]any)
	var keys []string
	for key := range props {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestChaos7073ResponseSchemaAcceptsWhatTheReaderMarshals(t *testing.T) {
	provider := &stubProvider{capability: healthLikeCapability(), read: func(query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
		facts := []contextfabric.CanonicalFact{}
		for _, subject := range query.Subjects {
			facts = append(facts, projectQHealth(subject))
		}
		return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable}, nil
	}}
	reader := newTestFactsReader(t, graphOfOrgA(), provider)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	for name, request := range map[string]FactsRequest{
		"current window, served with withheld rows and labels": {Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}},
		"range window, a refused subject":                      {Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}, {Kind: "repository", CanonicalID: "repository:guessed"}}, Window: &RequestWindow{Mode: WindowRange, Start: &start, End: &end}},
		"tables omitted":                                       {Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}, Tables: TablesOmit},
		"truncated by max_bytes":                               {Kinds: []string{"health"}, Subjects: []RequestSubject{{Kind: "project", CanonicalID: projectQ.CanonicalID}}, MaxBytes: MinMaxBytes},
	} {
		t.Run(name, func(t *testing.T) {
			response, err := reader.Read(requestContext(), restrictedToA(), request)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			validateAgainst(t, "mcp_read_facts_response.v1.schema.json", encoded)
			requestEncoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			validateAgainst(t, "mcp_read_facts_request.v1.schema.json", requestEncoded)
		})
	}
}

func TestChaos7073SchemaPropertiesMatchGoWireKeys(t *testing.T) {
	response := loadSchema(t, "mcp_read_facts_response.v1.schema.json")
	defs := response["$defs"].(map[string]any)
	node := func(name string) map[string]any { return defs[name].(map[string]any) }
	props := func(m map[string]any, key string) map[string]any {
		return m["properties"].(map[string]any)[key].(map[string]any)
	}
	items := func(m map[string]any) map[string]any { return m["items"].(map[string]any) }
	request := props(response, "request")
	cases := []struct {
		name   string
		schema map[string]any
		goType reflect.Type
	}{
		{"response", response, reflect.TypeFor[FactsResponse]()},
		{"request echo", request, reflect.TypeFor[EffectiveRead]()},
		{"kinds_refused", items(props(request, "kinds_refused")), reflect.TypeFor[RefusedKind]()},
		{"subjects_refused", items(props(request, "subjects_refused")), reflect.TypeFor[RefusedSubject]()},
		{"window", props(request, "window"), reflect.TypeFor[EffectiveWindow]()},
		{"fact", node("ServedFact"), reflect.TypeFor[ServedFact]()},
		{"subject", node("ServedSubject"), reflect.TypeFor[ServedSubject]()},
		{"table", node("ServedTable"), reflect.TypeFor[ServedTable]()},
		{"withheld", node("WithheldItem"), reflect.TypeFor[WithheldItem]()},
		{"provenance", node("Provenance"), reflect.TypeFor[Provenance]()},
		{"natural key", items(props(node("Provenance"), "natural_keys")), reflect.TypeFor[NaturalKey]()},
		{"coverage row", items(props(response, "coverage")), reflect.TypeFor[CoverageRow]()},
		{"subject ref", node("ReadFactsSubjectRef"), reflect.TypeFor[RequestSubject]()},
		{"truncation", props(response, "truncation"), reflect.TypeFor[Truncation]()},
		{"versions", props(response, "versions"), reflect.TypeFor[ResponseVersion]()},
		{"untrusted", props(response, "untrusted_content"), reflect.TypeFor[UntrustedLabel]()},
	}
	for _, tc := range cases {
		if got, want := schemaKeys(tc.schema), jsonKeys(tc.goType); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: schema properties %v, Go wire keys %v", tc.name, got, want)
		}
	}
	requestSchema := loadSchema(t, "mcp_read_facts_request.v1.schema.json")
	if got, want := schemaKeys(requestSchema), jsonKeys(reflect.TypeFor[FactsRequest]()); !reflect.DeepEqual(got, want) {
		t.Errorf("request: schema properties %v, Go wire keys %v", got, want)
	}
	if got, want := schemaKeys(props(requestSchema, "window")), jsonKeys(reflect.TypeFor[RequestWindow]()); !reflect.DeepEqual(got, want) {
		t.Errorf("request window: schema properties %v, Go wire keys %v", got, want)
	}
}
