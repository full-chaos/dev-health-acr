package hosted

import (
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// composeFindModes gives find_subjects its owned_by and handle modes
// (CHAOS-7126): the SAME graph the engine reads serves the bounded edge page
// and the node read, and the engine's own census (devhealthsource
// NewCensusFunc over the same ClickHouse client) serves the handle lookup. A
// part that cannot be composed leaves its mode unavailable, loudly.
func composeFindModes(subjects *directread.SubjectLookup, investigator contextfabric.Investigator, queryClient contextpacket.ClickHouseQueryClient, logger *slog.Logger) {
	if subjects == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	var edges directread.EdgeGraph
	var nodes directread.SubjectNodeReader
	if engine, ok := investigator.(directReadSourcer); ok && !storage.IsNil(investigator) {
		graph, _ := engine.DirectReadSources()
		if value, ok := graph.(directread.EdgeGraph); ok && !storage.IsNil(graph) {
			edges = value
		}
		if value, ok := graph.(directread.SubjectNodeReader); ok && !storage.IsNil(graph) {
			nodes = value
		}
	}
	var census graphrank.CensusFunc
	if !storage.IsNil(queryClient) {
		census = devhealthsource.NewCensusFunc(queryClient)
	}
	if edges == nil {
		logger.Error("context fabric find_subjects mode not composed", "mode", directread.FindModeOwnedBy, "reason", "graph_edges_unsupported")
	}
	if census == nil || nodes == nil {
		logger.Error("context fabric find_subjects mode not composed", "mode", directread.FindModeHandle, "reason", "census_or_node_read_absent")
	}
	subjects.WithOwnershipAndHandles(edges, census, nodes)
}
