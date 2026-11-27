package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const methodSubscriptionsListen = "subscriptions/listen"

type bodyReadCloser struct {
	io.Reader
	io.Closer
}

// refuseTaskSubscriptionWithoutExtension answers -32021 to a
// subscriptions/listen that asks for task notifications (notifications.taskIds)
// but does not declare the tasks extension. The SDK's typed listen params drop
// taskIds, so the request body is read here, bounded by limit, and handed on
// unchanged. It returns true when it wrote the refusal.
func refuseTaskSubscriptionWithoutExtension(w http.ResponseWriter, r *http.Request, cfg *ProcessConfig, limit int64) bool {
	if !cfg.tasksEnabled || r.Method != http.MethodPost || r.Header.Get("Mcp-Method") != methodSubscriptionsListen || r.Body == nil {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	r.Body = bodyReadCloser{Reader: io.MultiReader(bytes.NewReader(raw), r.Body), Closer: r.Body}
	if err != nil || int64(len(raw)) > limit {
		return false
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Method != methodSubscriptionsListen {
		return false
	}
	var params mcpsdk.SubscriptionsListenParams
	var asked struct {
		Notifications struct {
			TaskIDs []string `json:"taskIds"`
		} `json:"notifications"`
	}
	if json.Unmarshal(envelope.Params, &params) != nil || json.Unmarshal(envelope.Params, &asked) != nil {
		return false
	}
	if len(asked.Notifications.TaskIDs) == 0 || clientDeclaresTasks(&params) {
		return false
	}
	body, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id,omitempty"`
		Error   any             `json:"error"`
	}{"2.0", envelope.ID, errTaskCapabilityMissing})
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write(body)
	return true
}
