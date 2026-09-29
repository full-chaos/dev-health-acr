package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

func chaos7126RepoFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(append([]string{filepath.Dir(thisFile), "..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func propertyNames(t *testing.T, schema map[string]any) []string {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// r1 P1 (permanent): the published OpenAPI request component of the subjects
// route, the MCP request schema and the Go request type the route decodes
// name the same members. A mode the route serves but OpenAPI omits is
// rejected by any strict validator (additionalProperties:false).
func TestChaos7126_R1_SubjectsRequestContractsAgree(t *testing.T) {
	var openapi map[string]any
	if err := json.Unmarshal(chaos7126RepoFile(t, "contracts", "openapi", "acr-v1.json"), &openapi); err != nil {
		t.Fatal(err)
	}
	component := openapi["components"].(map[string]any)["schemas"].(map[string]any)["DataSubjectsRequest"].(map[string]any)
	var mcp map[string]any
	if err := json.Unmarshal(chaos7126RepoFile(t, "contracts", "jsonschema", "v1", "mcp_find_subjects_request.v1.schema.json"), &mcp); err != nil {
		t.Fatal(err)
	}
	var goNames []string
	typ := reflect.TypeOf(dataSubjectsRequest{})
	for i := 0; i < typ.NumField(); i++ {
		goNames = append(goNames, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(goNames)
	openapiNames, mcpNames := propertyNames(t, component), propertyNames(t, mcp)
	if !reflect.DeepEqual(openapiNames, mcpNames) || !reflect.DeepEqual(openapiNames, goNames) {
		t.Fatalf("request members differ:\n openapi %v\n mcp     %v\n go      %v", openapiNames, mcpNames, goNames)
	}
}

// r1 P2 (permanent): every caller-supplied string the route echoes under
// request is declared untrusted.
func TestChaos7126_R1_EveryEchoedCallerStringIsUntrusted(t *testing.T) {
	h := chaos7126Harness(t)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	response := h.postSubjects(token, `{"owned_by":"team:t"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	declared := map[string]bool{}
	for _, field := range dataSubjectsUntrustedFields {
		declared[field] = true
	}
	typ := reflect.TypeOf(dataSubjectsEcho{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		kind := typ.Field(i).Type.Kind()
		if name == "mode" || kind == reflect.Int {
			continue // server-chosen values
		}
		if !declared["request."+name] {
			t.Errorf("request.%s is echoed but not declared untrusted", name)
		}
	}
}
