package main

import (
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
)

func main() {
	providers := devhealthfacts.NewProviders(nil)
	capabilities := make([]contextfabric.FactCapability, 0, len(providers))
	for _, p := range providers {
		capabilities = append(capabilities, p.Capability())
	}
	rows := contextfabric.GenerateDimensionFactKindRankingFamilyTable(capabilities)
	fmt.Print(contextfabric.RenderDimensionFactKindRankingFamilyMarkdown(rows))
}
