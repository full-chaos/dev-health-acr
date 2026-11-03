package contextfabric

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// RecordDetailTablesAttached implements EngineTelemetry (CHAOS-12587).
// Content-safe: an org id, two counts and declared field names.
func (t SlogEngineTelemetry) RecordDetailTablesAttached(ctx context.Context, principal storage.Principal, event DetailTablesAttachedEvent) {
	args := append([]any{"org_id", SanitizeLogAttr(principal.OrgID), "detail_table_claims", event.Claims, "detail_tables", SanitizeLogStrings(nonNilStrings(event.Tables)), "detail_tables_omitted", event.Omitted}, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, "context fabric detail tables attached", args...)
}
