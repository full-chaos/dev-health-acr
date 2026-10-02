package genkitruntime_test

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

// The glossary's per-kind subject lists are DERIVED from the fact capability
// registry here, so the prompt cannot drift from what the providers serve.
func TestInterpretationPromptGlossarySubjectKindsMatchCapabilities(t *testing.T) {
	t.Parallel()
	prompt := genkitruntime.InterpretationSystemPrompt()
	servedBy := map[string][]string{}
	for _, provider := range devhealthfacts.NewProviders(nil) {
		capability := provider.Capability()
		kinds := make([]string, 0, len(capability.SupportedSubjectKinds))
		for _, kind := range capability.SupportedSubjectKinds {
			kinds = append(kinds, string(kind))
		}
		sort.Strings(kinds)
		servedBy[string(capability.Kind)] = kinds
	}
	if len(servedBy) == 0 {
		t.Fatal("no providers registered: nothing to derive the glossary check from")
	}
	listed := regexp.MustCompile(`\(([a-z_]+(?:, [a-z_]+)*)\)`)
	for kind, want := range servedBy {
		var line string
		for _, candidate := range strings.Split(prompt, "\n") {
			if strings.HasPrefix(candidate, "- "+kind+": ") {
				line = candidate
				break
			}
		}
		if line == "" {
			t.Errorf("no glossary line for registered kind %q", kind)
			continue
		}
		groups := listed.FindAllStringSubmatch(line, -1)
		if len(groups) == 0 {
			t.Errorf("glossary line for %q lists no subject kinds: %s", kind, line)
			continue
		}
		got := strings.Split(groups[len(groups)-1][1], ", ")
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("glossary line for %q lists subject kinds %v, the capability registry serves %v", kind, got, want)
		}
	}
}
