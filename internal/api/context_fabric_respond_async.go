package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// wantsRespondAsync reports whether the request carries the RFC 7240
// respond-async preference as a top-level preference name in any Prefer
// value. Names compare case-insensitively; a parameter or value after it is
// ignored, and text inside a quoted-string (escapes included) is never a
// name.
func wantsRespondAsync(r *http.Request) bool {
	for _, value := range r.Header.Values(contractsv1.ContextFabricPreferHeader) {
		for _, name := range preferenceNames(value) {
			if strings.EqualFold(name, contractsv1.ContextFabricRespondAsyncPreference) {
				return true
			}
		}
	}
	return false
}

// preferenceNames lists the top-level preference names of one Prefer field
// value: each comma-separated element's token before its first '=' or ';',
// trimmed, with commas, '=' and ';' inside a quoted-string not splitting
// anything. An unterminated quoted-string ends the value.
func preferenceNames(value string) []string {
	var names []string
	var name strings.Builder
	inName, quoted, escaped := true, false, false
	for _, c := range value {
		switch {
		case quoted && escaped:
			escaped = false
		case quoted && c == '\\':
			escaped = true
		case quoted:
			quoted = c != '"'
		case c == '"':
			quoted = true
		case c == ',':
			names = append(names, strings.TrimSpace(name.String()))
			name.Reset()
			inName = true
		case c == '=' || c == ';':
			inName = false
		case inName:
			name.WriteRune(c)
		}
	}
	if !quoted {
		names = append(names, strings.TrimSpace(name.String()))
	} else if inName {
		names = append(names, "")
	}
	return names
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

// acceptInvestigationAsync answers an admitted accept-first create 202 with
// its submission status. The caller then hands the run to a detached
// goroutine and returns, so the response ends with the handler and the
// connection is free for the client's next request (HTTP/1.1 keep-alive). A record admitted without its lease is abandoned and refused
// 500: a connection-owned run would end with the connection.
func (a *App) acceptInvestigationAsync(w http.ResponseWriter, r *http.Request, principal storage.Principal, submission *admittedInvestigationSubmission) bool {
	if submission == nil || !submission.record.Leased() {
		a.abandonInvestigationSubmission(r, principal, submission)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation could not be started", false, nil)
		return false
	}
	status := submissionStatusOf(submission.record)
	if status.Validate() != nil {
		a.abandonInvestigationSubmission(r, principal, submission)
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Context Fabric investigation could not be started", false, nil)
		return false
	}
	w.Header().Set(contractsv1.ContextFabricPreferenceAppliedHeader, contractsv1.ContextFabricRespondAsyncPreference)
	writeJSON(w, http.StatusAccepted, status)
	return true
}

// DrainDetachedRuns waits for the accept-first runs this replica still runs,
// until ctx ends (the server's shutdown bound). A run still going then ends
// through its lease: another replica's read or sweep takes it over as
// run_lost.
func (a *App) DrainDetachedRuns(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		a.detachedRuns.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// noViewerWriter is the writer of a run whose response is already written:
// it keeps nothing.
type noViewerWriter struct{ header http.Header }

func (w noViewerWriter) Header() http.Header         { return w.header }
func (w noViewerWriter) WriteHeader(int)             {}
func (w noViewerWriter) Write(p []byte) (int, error) { return len(p), nil }
