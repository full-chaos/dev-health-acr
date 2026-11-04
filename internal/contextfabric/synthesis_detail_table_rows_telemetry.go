package contextfabric

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// SynthesisDetailTableRowsEvent is what one synthesis input told the model
// about detail tables (CHAOS-12887): "<field>=<rows>" per listed table, sorted
// and unique, at most MaxLoggedDetailTables, and how many the cap left out.
// Names and counts only, never a row.
type SynthesisDetailTableRowsEvent struct {
	Tables  []string
	Omitted int
}

type synthesisDetailTableRowsRecorder interface {
	RecordSynthesisDetailTableRows(ctx context.Context, principal storage.Principal, event SynthesisDetailTableRowsEvent)
}

// synthesisDetailTableRows reads the row count each declared detail table of
// the input's facts gives the model (the same count ModelFacingFacts lists).
func synthesisDetailTableRows(facts []CanonicalFact) SynthesisDetailTableRowsEvent {
	seen := map[string]struct{}{}
	for _, fact := range facts {
		for field, value := range fact.Fields {
			if detailTableValue(value) {
				seen[fmt.Sprintf("%s=%d", field, len(value.Rows))] = struct{}{}
			}
		}
	}
	var event SynthesisDetailTableRowsEvent
	names := slices.Sorted(maps.Keys(seen))
	if len(names) > MaxLoggedDetailTables {
		event.Omitted = len(names) - MaxLoggedDetailTables
		names = names[:MaxLoggedDetailTables]
	}
	if len(names) > 0 {
		event.Tables = names
	}
	return event
}

// RecordSynthesisDetailTableRows writes the Debug line. Content-safe: an org
// id, "<declared field>=<count>" pairs and a count.
func (t SlogEngineTelemetry) RecordSynthesisDetailTableRows(ctx context.Context, principal storage.Principal, event SynthesisDetailTableRowsEvent) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "detail_table_rows", SanitizeLogStrings(nonNilStrings(event.Tables)), "detail_tables_omitted", event.Omitted}, requestIDLogAttrs(ctx)...)
	t.logger.DebugContext(ctx, "context fabric synthesis detail table rows", args...)
}
