package synthesisprompt_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

// CHAOS-12887: the model saw a claimed detail table by name only, so on reach
// run part 2's turn 2 (CHAOS-4415, result_5c93495a…) it wrote "the investment
// split across repositories cannot be reported" above a drawn sankey of that
// split. The input now gives each listed detail table's row count, never its
// rows, and the prompt asks the model not to call a counted breakdown missing.

// turnTwoInvestmentFact is the team investment fact of that turn, in shape: the
// detail table repository_theme_breakdown with 41 rows over the five themes
// (quality 10, maintenance 9, feature_delivery 8, operational 7, risk 7), its
// declared key, measures, observation and word columns, beside the theme mix
// and the theme scalars. Repository ids, labels and values are made up: no
// row of the askdev-1 org is in the repository.
func turnTwoInvestmentFact(t *testing.T) contextfabric.CanonicalFact {
	t.Helper()
	perTheme := []struct {
		theme string
		repos int
	}{{"quality", 10}, {"maintenance", 9}, {"feature_delivery", 8}, {"operational", 7}, {"risk", 7}}
	var rows []contextfabric.FactValueRow
	for _, each := range perTheme {
		theme := each.theme
		for i := range each.repos {
			repo := fmt.Sprintf("repository:00000000-0000-4000-8000-%012d", i+1)
			label := fmt.Sprintf("example-org/repo-%02d", i+1)
			effort := float64(i + 1)
			themeEffort := 100.0
			rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
				"repository": {String: &repo}, "repository_label": {String: &label}, "theme": {String: &theme},
				"weighted_effort": {Number: &effort}, "theme_weighted_effort": {Number: &themeEffort},
			}})
		}
	}
	if len(rows) != 41 {
		t.Fatalf("the turn-2 table has %d rows, want 41", len(rows))
	}
	theme := "quality"
	share := 0.4
	themeRows := []contextfabric.FactValueRow{{Fields: map[string]contextfabric.FactValue{"theme": {String: &theme}, "share": {Number: &share}}}}
	return contextfabric.CanonicalFact{
		ID: "fact_turn_two", Kind: contextfabric.FactInvestment,
		Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:linear:CHAOS", Label: "Fullchaos"},
		Fields: map[string]contextfabric.FactValue{
			"theme_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{Shape: contextfabric.FactTableBreakdown, Key: []string{"theme"}, Measures: []string{"share"}, Rows: themeRows}),
			"repository_theme_breakdown": contextfabric.TableFactValue(contextfabric.FactTable{
				Shape: contextfabric.FactTableBreakdown, Key: []string{"repository", "theme"},
				Measures: []string{"weighted_effort", "theme_weighted_effort"}, Observations: []string{"repository_label"},
				Detail: true, Rows: rows,
			}),
			"theme_quality": {Number: &share},
		},
	}
}

// modelFacing is the JSON object of one fact as the model receives it.
func modelFacing(t *testing.T, fact contextfabric.CanonicalFact) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(synthesisprompt.ModelFacingFacts([]contextfabric.CanonicalFact{fact})[0])
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheInputGivesADetailTableItsRowCountNotItsRows(t *testing.T) {
	t.Parallel()
	got := modelFacing(t, turnTwoInvestmentFact(t))
	if names, _ := got["detail_tables"].([]any); len(names) != 1 || names[0] != "repository_theme_breakdown" {
		t.Fatalf("detail_tables = %v, want [repository_theme_breakdown]", got["detail_tables"])
	}
	counts, _ := got["detail_table_rows"].(map[string]any)
	if n, _ := counts["repository_theme_breakdown"].(float64); n != 41 || len(counts) != 1 {
		t.Fatalf("detail_table_rows = %v, want {repository_theme_breakdown: 41}: the model is told the breakdown exists but not that it holds rows", got["detail_table_rows"])
	}
	encoded, _ := json.Marshal(got)
	for _, leak := range []string{"example-org/repo-01", "weighted_effort", "theme_weighted_effort"} {
		if strings.Contains(string(encoded), leak) {
			t.Fatalf("the model-facing fact holds %q: a detail table's rows or columns reached the model", leak)
		}
	}
}

// A table the reader withholds (past its row bound, or with a non-canonical
// key) is not in the fact at all: no name, no count, so nothing tells the
// model that a breakdown exists.
func TestAWithheldDetailTableGetsNoNameAndNoCount(t *testing.T) {
	t.Parallel()
	fact := turnTwoInvestmentFact(t)
	delete(fact.Fields, "repository_theme_breakdown")
	got := modelFacing(t, fact)
	if _, ok := got["detail_tables"]; ok {
		t.Fatalf("detail_tables = %v on a fact without the table", got["detail_tables"])
	}
	if _, ok := got["detail_table_rows"]; ok {
		t.Fatalf("detail_table_rows = %v on a fact without the table", got["detail_table_rows"])
	}
}

// The prompt tells the model what a count means and never promises that the
// breakdown is shown: a serve-time cut (CHAOS-12582) can still withhold it
// after synthesis, and then the server's own sentence says so.
func TestThePromptReadsADetailTableCountWithoutPromisingItIsShown(t *testing.T) {
	t.Parallel()
	system := synthesisprompt.System()
	line := ""
	for _, each := range strings.Split(system, "\n") {
		if strings.Contains(each, "detail_tables") {
			line = each
		}
	}
	if !strings.Contains(line, "detail_table_rows") {
		t.Fatalf("the detail table line does not explain detail_table_rows: %q", line)
	}
	if !strings.Contains(line, "do not write that the breakdown is unavailable") {
		t.Fatalf("the detail table line does not forbid calling a counted breakdown unavailable: %q", line)
	}
	for _, promise := range []string{"served", "shown", "displayed", "drawn"} {
		if strings.Contains(strings.ToLower(line), promise) {
			t.Fatalf("the detail table line promises the breakdown is %s: %q", promise, line)
		}
	}
}

// The prompt before this change (v30) and its digest: the version and the
// served contract move with the new line.
const (
	preRowsPromptVersion = "context-fabric-synthesis.v30"
	preRowsSystemSHA256  = "3f98398bd1bd585722cf670c892b3b632a3f19661c0e5e8243ee0334242d5fbf"
)

func TestTheContractMovedWithTheDetailTableRowCount(t *testing.T) {
	t.Parallel()
	if synthesisprompt.PromptVersion == preRowsPromptVersion {
		t.Fatalf("the prompt version is still %q", preRowsPromptVersion)
	}
	system := sha256.Sum256([]byte(synthesisprompt.System()))
	digest := hex.EncodeToString(system[:])
	if digest == preRowsSystemSHA256 {
		t.Fatal("the system prompt digest did not move")
	}
	if assembly := synthesisprompt.ClientAssembly(); assembly.PromptVersion != synthesisprompt.PromptVersion || assembly.SystemSHA256 != digest {
		t.Fatalf("the served contract reads %q %q, want %q %q", assembly.PromptVersion, assembly.SystemSHA256, synthesisprompt.PromptVersion, digest)
	}
}
