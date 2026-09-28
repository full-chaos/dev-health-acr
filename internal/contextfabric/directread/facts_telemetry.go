package directread

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// FactsReadLogMessage is the one Info line every read_facts call writes.
const FactsReadLogMessage = "context fabric direct read"

// FactsReadLogArgs renders one read for the trace: counts and closed
// vocabulary values only. Never a subject id, a label, a fact value or an
// error text. The caller appends request_id.
func FactsReadLogArgs(principal storage.Principal, record FactsReadRecord) []any {
	return []any{
		"tool", "read_facts",
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"status", contextfabric.SanitizeLogAttr(record.Status),
		"kinds", contextfabric.SanitizeLogStrings(append([]string{}, record.Kinds...)),
		"subject_kinds", contextfabric.SanitizeLogStrings(append([]string{}, record.SubjectKinds...)),
		"subject_count", record.SubjectCount,
		"admitted_count", record.AdmittedCount,
		"window_mode", contextfabric.SanitizeLogAttr(record.WindowMode),
		"facts_returned", record.FactsReturned,
		"rows_returned", record.RowsReturned,
		"rows_withheld", record.RowsWithheld,
		"fields_withheld", record.FieldsWithheld,
		"fields_undeclared", record.FieldsUndeclared,
		"evidence_withheld", record.EvidenceWithheld,
		"references_refused", record.ReferencesRefused,
		"truncated_by", contextfabric.SanitizeLogAttr(record.TruncatedBy),
		"bytes", record.Bytes,
		"latency_ms", record.Latency.Milliseconds(),
	}
}

// SlogFactsRecorder is the production FactsRecorder.
type SlogFactsRecorder struct {
	logger *slog.Logger
}

// NewSlogFactsRecorder returns a recorder over logger; a nil logger uses the
// default logger, so a read is never silently unrecorded.
func NewSlogFactsRecorder(logger *slog.Logger) *SlogFactsRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogFactsRecorder{logger: logger}
}

// RecordDirectFactsRead writes the read line.
func (r *SlogFactsRecorder) RecordDirectFactsRead(ctx context.Context, principal storage.Principal, record FactsReadRecord) {
	if r == nil || r.logger == nil {
		return
	}
	args := FactsReadLogArgs(principal, record)
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		args = append(args, "request_id", contextfabric.SanitizeLogAttr(string(requestID)))
	}
	r.logger.InfoContext(ctx, FactsReadLogMessage, args...)
}
