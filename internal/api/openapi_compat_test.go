package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHostedReadRoutesMatchOpenAPI(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string                     `json:"operationId"`
			Responses   map[string]json.RawMessage `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path, method, operation string
		statuses                []string
	}{
		{path: "/api/v1/agent-context/capabilities", method: "get", operation: "getCapabilities", statuses: []string{"200", "401", "403", "426", "429", "500", "503"}},
		{path: "/api/v1/agent-context/context-packets", method: "post", operation: "createContextPacket", statuses: []string{"200", "400", "401", "403", "413", "429", "500", "503", "504"}},
		{path: "/api/v1/agent-context/evidence/{evidence_ref_id}", method: "get", operation: "getEvidence", statuses: []string{"200", "401", "403", "404", "413", "429", "500", "503", "504"}},
		{path: "/api/v1/agent-context/episodes", method: "post", operation: "createEpisode", statuses: []string{"200", "201", "204", "400", "401", "403", "404", "409", "413", "429", "500", "503", "504"}},
	}
	for _, test := range tests {
		operation, ok := document.Paths[test.path][test.method]
		if !ok || operation.OperationID != test.operation {
			t.Fatalf("%s %s operation = %#v", test.method, test.path, operation)
		}
		for _, status := range test.statuses {
			if _, ok := operation.Responses[status]; !ok {
				t.Fatalf("%s %s is missing response %s", test.method, test.path, status)
			}
		}
	}
}
