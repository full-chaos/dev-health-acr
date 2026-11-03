package contextfabric_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12716 (lead ruling, 2026-11-03): one function reads a raw source id for a
// reader, for the graph end (ReadableSourceID) and the evidence label alike, so the
// two never disagree on the same id. "" is the kind word.
func TestTheGraphEndAndTheEvidenceLabelReadASourceIDAlike(t *testing.T) {
	t.Parallel()
	const org = "70d529e0-3c06-4597-8480-794fd02328b6"
	for raw, want := range map[string]string{
		org + ":gitlab:71133891":                               "71133891",
		"c67b1602-31db-4422-8dec-a4a02bbcc513:gitlab:71133891": "71133891",
		"gitlab:" + org + ":gitlab:71133891":                   "71133891",
		org + ":c67b1602-31db-4422-8dec-a4a02bbcc513":          "",
		"c67b1602-31db-4422-8dec-a4a02bbcc513":                 "",
		"linear:CHAOS-2320":                                    "CHAOS-2320",
		org + ":linear:CHAOS-2320":                             "CHAOS-2320",
		"gitlab:acme/live-e2e":                                 "acme/live-e2e",
		"gh:full-chaos/dev-health-ops#5035":                    "full-chaos/dev-health-ops#5035",
		"jira:OPS":                                             "OPS",
		"OPS":                                                  "OPS",
		"":                                                     "",
	} {
		if got := contractsv1.ContextFabricSourceKey(raw); got != want {
			t.Errorf("source key %q = %q, want %q", raw, got, want)
		}
		for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectProject, contextfabric.SubjectTeam, contextfabric.SubjectWorkItem, contextfabric.SubjectWorkItemRef} {
			graph := contextfabric.ReadableSourceID(kind, raw)
			if want == "" {
				if word, _ := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularySubjectKind, string(kind)); graph != word {
					t.Errorf("%s %q: graph end %q, want the kind word %q", kind, raw, graph, word)
				}
				continue
			}
			if graph != want {
				t.Errorf("%s %q: graph end %q, evidence key %q", kind, raw, graph, want)
			}
		}
	}
}
