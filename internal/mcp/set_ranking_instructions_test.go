package mcp

import (
	"strings"
	"testing"
)

// A client model asked "which projects have the most open pull requests" is
// told the data-tool route: list, read the kind per set, order it itself.
func TestServerInstructionsNameTheSetRankingRouteOnReadFacts(t *testing.T) {
	h := newDTHosted(t)
	with := serverInstructions(bootHandlerHalvesConfig(func() *Bootstrap {
		b := h.boot(t, dtAllTools...)
		b.Capabilities.EnabledTools = append(b.Capabilities.EnabledTools, toolReadFacts)
		return b
	}()))
	line := ""
	for _, candidate := range strings.Split(with, "\n") {
		if strings.HasPrefix(candidate, "- read_facts:") {
			line = candidate
		}
	}
	for _, want := range []string{"To rank a set", "projects by open pull requests or blockers", "find_subjects", "up to 25 subjects per call", "order the counts yourself"} {
		if !strings.Contains(line, want) {
			t.Errorf("read_facts instruction lacks %q: %q", want, line)
		}
	}
	without := serverInstructions(bootHandlerHalvesConfig(h.boot(t, toolDataCatalog, toolFindSubjects)))
	if strings.Contains(without, "To rank a set") {
		t.Errorf("read_facts not advertised, yet its ranking route is described:\n%s", without)
	}
}
