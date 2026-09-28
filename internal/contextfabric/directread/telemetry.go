package directread

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// AuthorizationLogArgs renders a decision for the trace: counts, kinds and
// closed-vocabulary values only. Never a subject id, a label, a repository
// name or an error text. The caller appends request_id.
func AuthorizationLogArgs(principal storage.Principal, decision Authorization) []any {
	args := []any{
		"org_id", contextfabric.SanitizeLogAttr(principal.OrgID),
		"principal_class", string(decision.PrincipalClass),
		"repository_scope_count", decision.RepositoryScopeCount,
		"decision", string(decision.Decision),
		"reason", string(decision.Reason),
		"subject_count", decision.SubjectCount,
		"admitted_count", decision.AdmittedCount,
		"denied_count", decision.DeniedCount,
		"absent_count", decision.AbsentCount,
		"ownership_unproven_count", decision.OwnershipUnprovenCount,
		"organization_mismatch_count", decision.OrganizationMismatchCount,
		"invalid_count", decision.InvalidCount,
		"refused_kinds", contextfabric.SanitizeLogStrings(append([]string{}, decision.RefusedKinds...)),
	}
	if class := decision.ErrorClass(); class != "" {
		args = append(args, "error_class", contextfabric.SanitizeLogAttr(class))
	}
	return args
}

// SlogRecorder is the production Recorder.
type SlogRecorder struct {
	logger *slog.Logger
}

// NewSlogRecorder returns a recorder over logger; a nil logger uses the
// default logger, so a decision is never silently unrecorded.
func NewSlogRecorder(logger *slog.Logger) *SlogRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	return &SlogRecorder{logger: logger}
}

// RecordDirectReadAuthorization writes the decision line.
func (r *SlogRecorder) RecordDirectReadAuthorization(ctx context.Context, principal storage.Principal, decision Authorization) {
	if r == nil || r.logger == nil {
		return
	}
	args := AuthorizationLogArgs(principal, decision)
	if requestID, ok := observability.RequestIDFromContext(ctx); ok {
		args = append(args, "request_id", contextfabric.SanitizeLogAttr(string(requestID)))
	}
	r.logger.InfoContext(ctx, AuthorizationLogMessage, args...)
}
