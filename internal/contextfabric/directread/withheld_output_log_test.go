package directread_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const withheldEdgesWith = `{"data":{"workGraphEdges":{"edges":[{"evidence":"SECRET-EVIDENCE"}]}}}`
const withheldEdgesWithout = `{"data":{"workGraphEdges":{"edges":[{}]}}}`

func runEdges(t *testing.T, answer string) (*opHarness, directread.OperationResponse) {
	t.Helper()
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, answer }, opHarnessOptions{})
	return h, h.run(t, opUnrestricted(opOrgA), "workGraphEdges", map[string]any{})
}

func TestWithheldOutputDeclaredPathLogsInfoAndCounts(t *testing.T) {
	h, resp := runEdges(t, withheldEdgesWith)
	_, clean := runEdges(t, withheldEdgesWithout)
	if resp.Call != directread.CallServed || string(resp.Data) != string(clean.Data) {
		t.Fatalf("served data differs: %s vs %s", resp.Data, clean.Data)
	}
	logs := h.logs.String()
	if strings.Contains(logs, `"level":"ERROR"`) || strings.Contains(logs, directread.OperationPathsRemovedLog) {
		t.Fatalf("declared withheld path logged as error:\n%s", logs)
	}
	if !strings.Contains(logs, `"level":"INFO","msg":"`+directread.OperationPathsWithheldLog+`"`) ||
		!strings.Contains(logs, `"operation":"workGraphEdges"`) ||
		!strings.Contains(logs, `"removed_paths":["workGraphEdges.edges[*].evidence"]`) {
		t.Fatalf("withheld INFO line missing:\n%s", logs)
	}
	if strings.Contains(logs, "SECRET-EVIDENCE") || strings.Contains(string(resp.Data), "SECRET-EVIDENCE") {
		t.Fatal("withheld value escaped")
	}
	if c := h.runner.Counters(); c.PathsWithheld != 1 || c.PathsRemoved != 0 {
		t.Fatalf("counters: %+v", c)
	}
}

func TestWithheldOutputUndeclaredPathStaysError(t *testing.T) {
	h, resp := runEdges(t, `{"data":{"workGraphEdges":{"edges":[{"evidence":"E","author":"SECRET-PERSON"}]}}}`)
	logs := h.logs.String()
	if resp.Call != directread.CallServed || strings.Contains(string(resp.Data), "SECRET-PERSON") {
		t.Fatalf("filter: %s", resp.Data)
	}
	if !strings.Contains(logs, `"level":"ERROR","msg":"`+directread.OperationPathsRemovedLog+`"`) ||
		!strings.Contains(logs, `"removed_paths":["workGraphEdges.edges[*].author"]`) ||
		strings.Contains(logs, `"removed_values":2`) {
		t.Fatalf("undeclared path not ERROR-logged alone:\n%s", logs)
	}
	if c := h.runner.Counters(); c.PathsRemoved != 1 || c.PathsWithheld != 1 {
		t.Fatalf("counters: %+v", c)
	}
}
