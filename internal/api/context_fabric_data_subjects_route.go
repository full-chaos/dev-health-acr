package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// dataSubjectsRequest is find_subjects' wire request (design C.4; S1a modes
// list and name only). Unknown fields are refused by the strict decoder.
type dataSubjectsRequest struct {
	Kind    string   `json:"kind,omitempty"`
	Query   string   `json:"query,omitempty"`
	Kinds   []string `json:"kinds,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Cursor  string   `json:"cursor,omitempty"`
	OwnedBy string   `json:"owned_by,omitempty"`
	Handle  string   `json:"handle,omitempty"`
}

// dataSubjectsEcho repeats the effective request.
type dataSubjectsEcho struct {
	Mode    string   `json:"mode"`
	Kind    string   `json:"kind,omitempty"`
	Query   string   `json:"query,omitempty"`
	Kinds   []string `json:"kinds,omitempty"`
	Limit   int      `json:"limit"`
	Cursor  string   `json:"cursor,omitempty"`
	OwnedBy string   `json:"owned_by,omitempty"`
	Handle  string   `json:"handle,omitempty"`
}

type dataSubjectsResponse struct {
	directread.FindResponse
	Request          dataSubjectsEcho                `json:"request"`
	UntrustedContent contractsv1.MCPUntrustedContent `json:"untrusted_content"`
}

// dataSubjectsUntrustedFields are the members that carry graph or client
// text: a subject label is a provider name, the query is the client's.
var dataSubjectsUntrustedFields = []string{"subjects[].label", "request.query", "request.handle"}

// contextFabricDataSubjectsHandler serves find_subjects (CHAOS-7072, S1a;
// design C.4). Every returned subject passed the S0 subject gate for this
// principal in this call (the lookup does it; this handler holds no
// proof). 400 invalid_request for a malformed request, 503 when the graph
// or the gate is unavailable (or not composed), 500 for a broken proof.
func (a *App) contextFabricDataSubjectsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lookup := a.dataSubjects()
		if lookup == nil {
			a.writeContextFabricDataUnavailable(w, r, "subjects", "graph_not_composed")
			return
		}
		var request dataSubjectsRequest
		if err := decodeJSONBody(w, r, contextFabricDataRequestBytes, &request); err != nil {
			writeContextFabricDataDecodeError(w, r, err)
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeError(w, r, http.StatusUnauthorized, "invalid_token", "Missing or invalid ACR credential", false, nil)
			return
		}
		response, err := lookup.Find(r.Context(), principal, directread.FindRequest{
			Kind: request.Kind, Query: request.Query, Kinds: request.Kinds, Limit: request.Limit, Cursor: request.Cursor,
			OwnedBy: request.OwnedBy, Handle: request.Handle,
		})
		if err != nil {
			switch {
			case errors.Is(err, directread.ErrFindInvalidRequest):
				writeError(w, r, http.StatusBadRequest, "invalid_request", "find_subjects request is invalid", false, map[string]any{"reason": "invalid_find_request"})
			case isDirectReadContractError(err):
				a.writeContextFabricDataInternal(w, r, "subjects")
			default:
				a.writeContextFabricDataUnavailable(w, r, "subjects", "graph_unavailable")
			}
			return
		}
		echo := dataSubjectsEcho{Mode: directread.FindModeName, Kind: request.Kind, Query: request.Query, Kinds: request.Kinds, Limit: effectiveFindLimit(request.Limit), Cursor: request.Cursor, OwnedBy: request.OwnedBy, Handle: request.Handle}
		switch {
		case strings.TrimSpace(request.OwnedBy) != "":
			echo.Mode = directread.FindModeOwnedBy
		case strings.TrimSpace(request.Handle) != "":
			echo.Mode = directread.FindModeHandle
		case request.Query == "":
			echo.Mode = directread.FindModeList
		}
		encoded, err := encodeBounded(dataSubjectsResponse{
			FindResponse: response, Request: echo,
			UntrustedContent: contractsv1.MCPUntrustedContent{Untrusted: true, Notice: contractsv1.MCPUntrustedContentNotice, Fields: dataSubjectsUntrustedFields},
		}, contextFabricDataResponseBytes)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "Direct data response exceeded service limits", false, nil)
			return
		}
		a.recordReadAudit(r.Context(), principal, "direct_subjects_read", "context_fabric_data_subjects", echo.Mode, "success", map[string]any{"status": string(response.Status), "returned": response.Page.Returned})
		writeEncodedJSON(w, http.StatusOK, encoded)
	}
}

func effectiveFindLimit(limit int) int {
	switch {
	case limit <= 0:
		return directread.DefaultFindLimit
	case limit > directread.MaxFindLimit:
		return directread.MaxFindLimit
	default:
		return limit
	}
}
