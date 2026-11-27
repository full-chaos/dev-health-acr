package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// codeHeaderMismatch is the Streamable HTTP HeaderMismatch error.
const codeHeaderMismatch = -32020

type nameHeaderBody struct {
	io.Reader
	io.Closer
}

// taskNameHeaderHandler gives tasks/get, tasks/update and tasks/cancel the
// Mcp-Name check the SDK gives tools/call: a header, when sent, must equal
// params.taskId, else 400 with -32020 (HeaderMismatch). A request with no
// header is served. The body is read here, bounded by limit, and handed on
// unchanged.
func taskNameHeaderHandler(cfg *ProcessConfig, limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Header.Get("Mcp-Method")
		header := r.Header.Get("Mcp-Name")
		if !cfg.tasksEnabled || r.Method != http.MethodPost || header == "" || r.Body == nil ||
			(method != methodTasksGet && method != methodTasksUpdate && method != methodTasksCancel) {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		r.Body = nameHeaderBody{Reader: io.MultiReader(bytes.NewReader(raw), r.Body), Closer: r.Body}
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				TaskID *string `json:"taskId"`
			} `json:"params"`
		}
		if err != nil || int64(len(raw)) > limit || json.Unmarshal(raw, &envelope) != nil ||
			envelope.Method != method || envelope.Params.TaskID == nil || *envelope.Params.TaskID == header {
			next.ServeHTTP(w, r)
			return
		}
		body, err := json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Error   any             `json:"error"`
		}{"2.0", envelope.ID, struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}{codeHeaderMismatch, "header mismatch: Mcp-Name header value '" + header + "' does not match body value '" + *envelope.Params.TaskID + "'"}})
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	})
}
