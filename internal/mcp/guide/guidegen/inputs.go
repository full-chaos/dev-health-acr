// Package guidegen builds the static MCP guide resources from the ACR
// registries. It links the engine's registries, so it lives outside the
// package the acr-mcp binary links: the generator (../gen) writes the
// output into ../zz_generated.go.
package guidegen

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// FamilyRow is one question family as the registry declares it.
type FamilyRow struct {
	ID             string
	SubjectAxis    string
	Dimension      string
	RequireDrivers bool
	RequireRanking bool
	RenderKinds    []string
	Unreachable    bool
}

// GrammarRow is one handle-grammar registry entry.
type GrammarRow struct {
	PatternID string
	Kind      string
}

// Inputs is every registry value the guide quotes. Build reads nothing else,
// so a test can remove one entry and show the parity check fail.
type Inputs struct {
	Families            []FamilyRow
	SubjectKinds        []string
	ServableCohortKinds []string
	Grammars            []GrammarRow
	Windows             []string
	Statuses            []string
	// UnproducedRenderKinds are render kinds a family may name that no
	// producer builds today.
	UnproducedRenderKinds []string
	// DataOperations and DataNotServed are the direct data operations
	// catalogue (CHAOS-7072): the run_operation names with their purpose and
	// which caller classes they are served to, and the registered documents
	// that are not served with their refusal code.
	DataOperations []DataOperationRow
	DataNotServed  []DataNotServedRow
}

// FromRegistries reads the live registries.
func FromRegistries() Inputs {
	unreachable := map[contextfabric.QuestionFamily]bool{}
	for _, family := range contextfabric.UnreachableQuestionFamilies() {
		unreachable[family] = true
	}
	var in Inputs
	for _, def := range contextfabric.QuestionFamilyDefinitions() {
		row := FamilyRow{
			ID:             string(def.Family),
			SubjectAxis:    string(def.SubjectAxis),
			Dimension:      string(def.Dimension),
			RequireDrivers: def.RequireDrivers,
			RequireRanking: def.RequireRanking,
			Unreachable:    unreachable[def.Family],
		}
		for _, kind := range def.RenderKinds {
			row.RenderKinds = append(row.RenderKinds, string(kind))
		}
		in.Families = append(in.Families, row)
	}
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		in.SubjectKinds = append(in.SubjectKinds, string(kind))
	}
	for _, kind := range contextfabric.ServableCohortKindsForAudit() {
		in.ServableCohortKinds = append(in.ServableCohortKinds, string(kind))
	}
	for _, pattern := range graphrank.HandleGrammarPatterns() {
		in.Grammars = append(in.Grammars, GrammarRow{PatternID: pattern.ID, Kind: string(pattern.Kind)})
	}
	for _, window := range contractsv1.ContextFabricRelativeWindowIDVocabulary() {
		in.Windows = append(in.Windows, string(window))
	}
	for _, kind := range contextfabric.DeclaredUnproducedRenderKinds() {
		in.UnproducedRenderKinds = append(in.UnproducedRenderKinds, string(kind))
	}
	in.Statuses = []string{
		string(contractsv1.ContextFabricInvestigationComplete),
		string(contractsv1.ContextFabricInvestigationPartial),
		string(contractsv1.ContextFabricInvestigationDegraded),
		string(contractsv1.ContextFabricInvestigationClarificationRequired),
		string(contractsv1.ContextFabricInvestigationNoMatch),
	}
	in.DataOperations, in.DataNotServed = dataRegistryRows()
	return in
}
