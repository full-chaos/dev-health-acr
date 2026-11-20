package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// wantsRespondAsync reports whether the request carries the RFC 7240
// respond-async preference in any Prefer value. Preference names are
// case-insensitive; parameters after ';' are ignored.
func wantsRespondAsync(r *http.Request) bool {
	for _, value := range r.Header.Values(contractsv1.ContextFabricPreferHeader) {
		for _, preference := range strings.Split(value, ",") {
			name, _, _ := strings.Cut(preference, ";")
			name, _, _ = strings.Cut(name, "=")
			if strings.EqualFold(strings.TrimSpace(name), contractsv1.ContextFabricRespondAsyncPreference) {
				return true
			}
		}
	}
	return false
}

// writeRespondAsyncUnavailable is the refusal of an accept-first create on a
// deployment that runs no leased runs. Its code is the stream's flag-off
// code: an accept-first run is a leased run, and a leased run needs the
// stream.
func (a *App) writeRespondAsyncUnavailable(w http.ResponseWriter, r *http.Request) {
	a.logger.InfoContext(r.Context(), "context fabric investigation event stream refused",
		"request_id", contextfabric.SanitizeLogAttr(RequestID(r.Context())),
		"reason", "respond_async_without_run_lease",
	)
	writeError(w, r, contractsv1.ContextFabricStreamUnavailableHTTPStatus, contractsv1.ErrorCodeStreamUnavailable, contractsv1.ContextFabricRespondAsyncUnavailableMessage, false, nil)
}

// submissionStatusOf is the status body of a record, without the run detail.
func submissionStatusOf(record contextfabric.SubmissionRecord) contractsv1.ContextFabricSubmissionStatus {
	return contractsv1.ContextFabricSubmissionStatus{
		SchemaVersion: contractsv1.ContextFabricSubmissionStatusSchema, SubmissionID: record.Key.SubmissionID,
		State: contractsv1.ContextFabricSubmissionState(record.State), ReservedResultID: record.ReservedResultID,
		ResultID: record.ResultID, ThreadID: record.ThreadID, UpdatedAt: record.UpdatedAt.UTC(),
	}
}

// serveInvestigationAsync answers an admitted accept-first create 202 with
// its submission status, then runs it as a leased run with no viewer: the
// run's events go to its log only, and the submission status, attach and
// cancel routes serve it from any replica. The 202 carries Content-Length
// and is flushed before the run starts, so the client has the whole answer
// while this handler goes on running.
func (a *App) serveInvestigationAsync(w http.ResponseWriter, r *http.Request, investigator contextfabric.Investigator, principal storage.Principal, request contextfabric.InvestigationRequest, submission *admittedInvestigationSubmission, submissionID, threadID, reserved string, investigateCtx context.Context, collectedSynthesisInput func() *contractsv1.ContextFabricSynthesisInput) string {
	if submission == nil || !submission.record.Leased() {
		a.abandonInvestigationSubmission(r, principal, submission)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation could not be started", false, nil)
		return "error"
	}
	status := submissionStatusOf(submission.record)
	encoded, err := json.Marshal(status)
	if err != nil || status.Validate() != nil {
		a.abandonInvestigationSubmission(r, principal, submission)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation could not be started", false, nil)
		return "error"
	}
	encoded = append(encoded, '\n')
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Length", strconv.Itoa(len(encoded)))
	header.Set(contractsv1.ContextFabricPreferenceAppliedHeader, contractsv1.ContextFabricRespondAsyncPreference)
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(encoded)
	_ = http.NewResponseController(w).Flush()
	return a.serveInvestigationEventStream(noViewerWriter{header: http.Header{}}, r, investigator, principal, request, submission, submissionID, threadID, reserved, investigateCtx, collectedSynthesisInput, true)
}

// noViewerWriter is the writer of a run whose response is already written:
// it keeps nothing.
type noViewerWriter struct{ header http.Header }

func (w noViewerWriter) Header() http.Header         { return w.header }
func (w noViewerWriter) WriteHeader(int)             {}
func (w noViewerWriter) Write(p []byte) (int, error) { return len(p), nil }
