package directread

import (
	"context"
	"log/slog"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread/gatevocab"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// RelationshipsReadLogMessage is the one Info line every read_relationships
// request writes.
const RelationshipsReadLogMessage = gatevocab.RelationshipsReadLogMessage

// RelationshipsReadLogArgs renders one read for the trace: counts and closed
// vocabulary values only. Never a subject id, a label, a relationship id or
// an error text. The caller appends request_id.
func RelationshipsReadLogArgs(principal storage.Principal, record RelationshipsReadRecord) []any {
	withheld := make([]string, 0, len(record.EdgesWithheld))
	for _, reason := range gatevocab.EdgeWithheldReasonVocabulary() {
		if record.EdgesWithheld[reason] > 0 {
			withheld = append(withheld, string(reason))
		}
	}
	sort.Strings(withheld)
	total := 0
	for _, n := range record.EdgesWithheld {
		total += n
	}
	args := []any{
		"tool", RelationshipsTool,
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"principal_class", string(ClassifyPrincipal(principal)),
		"status", contextfabric.SanitizeLogAttr(string(record.Status)),
		"subject_kind", contextfabric.SanitizeLogAttr(record.SubjectKind),
		"depth", record.Depth,
		"hop", record.Hop,
		"type_count", record.TypeCount,
		"direction", contextfabric.SanitizeLogAttr(record.Direction),
		"window_mode", contextfabric.SanitizeLogAttr(record.WindowMode),
		"edges_examined", record.EdgesExamined,
		"edges_returned", record.EdgesReturned,
		"edges_not_visible", total,
		"edges_withheld_reasons", contextfabric.SanitizeLogStrings(withheld),
		"edges_withheld_attributes", record.EdgesWithheld[gatevocab.EdgeWithheldAttributes],
		"edges_withheld_source", record.EdgesWithheld[gatevocab.EdgeWithheldSource],
		"edges_withheld_target", record.EdgesWithheld[gatevocab.EdgeWithheldTarget],
		"evidence_refs_withheld", record.EvidenceRefs,
		"end_nodes_gated", record.EndNodesGated,
		"end_nodes_refused", record.EndNodesRefused,
		"latency_ms", record.Latency.Milliseconds(),
	}
	if record.TruncatedBy != "" {
		args = append(args, "truncated_by", contextfabric.SanitizeLogAttr(record.TruncatedBy))
	}
	if record.CursorIn != "" {
		args = append(args, "cursor_in", contextfabric.SanitizeLogAttr(string(record.CursorIn)))
	}
	if record.CursorOut != "" {
		args = append(args, "cursor_out", contextfabric.SanitizeLogAttr(string(record.CursorOut)))
	}
	if record.FailureClass != "" {
		args = append(args, "failure_class", contextfabric.SanitizeLogAttr(string(record.FailureClass)))
	}
	return args
}

// SlogRelationshipsRecorder is the production RelationshipsRecorder. The
// route writes its own Warn line for an unavailable read.
type SlogRelationshipsRecorder struct {
	logger *slog.Logger
}

// NewSlogRelationshipsRecorder returns a recorder over logger; a nil logger
// uses the default logger, so a read is never silently unrecorded.
func NewSlogRelationshipsRecorder(logger *slog.Logger) *SlogRelationshipsRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogRelationshipsRecorder{logger: logger}
}

// RecordDirectRelationshipsRead writes the read line.
func (r *SlogRelationshipsRecorder) RecordDirectRelationshipsRead(ctx context.Context, principal storage.Principal, record RelationshipsReadRecord) {
	if r == nil || r.logger == nil {
		return
	}
	args := RelationshipsReadLogArgs(principal, record)
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		args = append(args, "request_id", contextfabric.SanitizeLogAttr(string(requestID)))
	}
	r.logger.InfoContext(ctx, RelationshipsReadLogMessage, args...)
}
