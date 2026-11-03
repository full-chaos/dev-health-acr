package synthesisprompt_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

// CHAOS-12554 (lead ruling B): a fact lists its detail tables in the synthesis
// input, names only, and the prompt asks for one, as a claim with value null,
// only when the question asks for that breakdown. Rich views are not the
// default (North Star check 10).

// detailTableRule is the one prompt line, pinned word for word.
const detailTableRule = "A canonical_fact may list detail_tables, breakdowns of that fact that the input leaves out. Claim a listed detail table, with its name as the field and value null, only when the question asks for that breakdown; never claim one by default."

// The prompt before this rule.
const (
	preDetailPromptVersion = "context-fabric-synthesis.v29"
	preDetailSystemSHA256  = "c033e0f7ae9a6248d4bd669eb331f7443ceaf034d2d919dc864cd0c3c9a14b45"
)

func TestTheSynthesisPromptAsksForADetailTableOnlyByIntent(t *testing.T) {
	t.Parallel()
	if got := strings.Count(synthesisprompt.System(), detailTableRule); got != 1 {
		t.Fatalf("the synthesis prompt carries the detail table rule %d times, want once, word for word", got)
	}
}

func TestTheContractMovedWithTheDetailTableRule(t *testing.T) {
	t.Parallel()
	if synthesisprompt.PromptVersion == preDetailPromptVersion {
		t.Fatalf("the prompt version is still %q", preDetailPromptVersion)
	}
	system := sha256.Sum256([]byte(synthesisprompt.System()))
	digest := hex.EncodeToString(system[:])
	if digest == preDetailSystemSHA256 {
		t.Fatal("the system prompt digest did not move")
	}
	if assembly := synthesisprompt.ClientAssembly(); assembly.PromptVersion != synthesisprompt.PromptVersion || assembly.SystemSHA256 != digest {
		t.Fatalf("the served contract reads %q %q, want %q %q", assembly.PromptVersion, assembly.SystemSHA256, synthesisprompt.PromptVersion, digest)
	}
}

func detailInputFact() contextfabric.CanonicalFact {
	share := 0.5
	label := "quality.bugfix"
	rows := []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{"subcategory": {String: &label}, "share": {Number: &share}}}}
	detail := func(name string) contextfabric.FactValue {
		return contextfabric.TableFactValue(contextfabric.FactTable{Shape: contextfabric.FactTableBreakdown, Key: []string{"subcategory"}, Measures: []string{"share"}, Detail: true, Rows: rows})
	}
	return contextfabric.CanonicalFact{
		ID: "fact_detail", Kind: contextfabric.FactInvestment,
		Fields: map[string]contextfabric.FactValue{
			"theme_breakdown":       contextfabric.TableFactValue(contextfabric.FactTable{Shape: contextfabric.FactTableBreakdown, Key: []string{"subcategory"}, Measures: []string{"share"}, Rows: rows}),
			"subcategory_breakdown": detail("subcategory_breakdown"),
			"area_breakdown":        detail("area_breakdown"),
			"theme_quality":         {Number: &share},
		},
	}
}

// The input lists each detail table by name, sorted, and carries no table rows.
func TestTheInputListsDetailTablesByNameOnly(t *testing.T) {
	t.Parallel()
	source := detailInputFact()
	facing := synthesisprompt.ModelFacingFacts([]contextfabric.CanonicalFact{source})[0]
	want := []string{"area_breakdown", "subcategory_breakdown"}
	// Sorted on every call, whatever order the field map gives.
	for range 32 {
		if got := synthesisprompt.ModelFacingFacts([]contextfabric.CanonicalFact{source})[0].DetailTables; !slices.Equal(got, want) {
			t.Fatalf("detail_tables = %v, want %v", got, want)
		}
	}
	// A list the source fact already carried is never passed on.
	stale := source
	stale.DetailTables = []string{"stale_breakdown"}
	if got := synthesisprompt.ModelFacingFacts([]contextfabric.CanonicalFact{stale})[0].DetailTables; !slices.Equal(got, want) {
		t.Fatalf("with a stale list: detail_tables = %v, want %v", got, want)
	}
	for name, value := range facing.Fields {
		if len(value.Rows) > 0 {
			t.Fatalf("field %q keeps its rows", name)
		}
	}
	if _, kept := facing.Fields["theme_quality"]; !kept || len(facing.Fields) != 1 {
		t.Fatalf("fields = %v, want only the scalar", facing.Fields)
	}
	if source.DetailTables != nil || len(source.Fields) != 4 {
		t.Fatal("the source fact was changed")
	}
	encoded, err := json.Marshal(facing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"detail_tables":["area_breakdown","subcategory_breakdown"]`) {
		t.Fatalf("encoded = %s, want detail_tables", encoded)
	}
	// A fact with no detail table carries no key at all, so every other input
	// is unchanged.
	plain := source
	plain.Fields = map[string]contextfabric.FactValue{"theme_quality": source.Fields["theme_quality"], "theme_breakdown": source.Fields["theme_breakdown"]}
	encoded, err = json.Marshal(synthesisprompt.ModelFacingFacts([]contextfabric.CanonicalFact{plain})[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "detail_tables") {
		t.Fatalf("encoded = %s, want no detail_tables key", encoded)
	}
}
