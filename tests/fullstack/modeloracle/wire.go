package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// The OpenAI chat-completions wire format is implemented here in both streaming and
// non-streaming form because the client picks; nothing else in this command depends on it.

const createdEpoch = 1700000000

func base(responseID, model string, object string) map[string]any {
	return map[string]any{
		"id":      responseID,
		"object":  object,
		"created": createdEpoch,
		"model":   model,
	}
}

func (s *server) emitToolCall(w http.ResponseWriter, request chatRequest, responseID, toolName, callID string, arguments map[string]any) {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		http.Error(w, "encode tool arguments", http.StatusInternalServerError)
		return
	}
	toolCall := map[string]any{
		"index": 0,
		"id":    callID,
		"type":  "function",
		"function": map[string]any{
			"name":      toolName,
			"arguments": string(encoded),
		},
	}

	if !request.Stream {
		payload := base(responseID, request.Model, "chat.completion")
		payload["choices"] = []map[string]any{{
			"index":         0,
			"finish_reason": "tool_calls",
			"message": map[string]any{
				"role":       "assistant",
				"content":    nil,
				"tool_calls": []map[string]any{toolCall},
			},
		}}
		payload["usage"] = usage()
		writeJSON(w, http.StatusOK, payload)
		return
	}

	flusher, ok := beginStream(w)
	if !ok {
		return
	}
	first := base(responseID, request.Model, "chat.completion.chunk")
	first["choices"] = []map[string]any{{
		"index": 0,
		"delta": map[string]any{"role": "assistant", "tool_calls": []map[string]any{toolCall}},
	}}
	sendChunk(w, flusher, first)

	final := base(responseID, request.Model, "chat.completion.chunk")
	final["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}
	final["usage"] = usage()
	sendChunk(w, flusher, final)
	endStream(w, flusher)
}

func (s *server) emitText(w http.ResponseWriter, request chatRequest, responseID, text string) {
	if !request.Stream {
		payload := base(responseID, request.Model, "chat.completion")
		payload["choices"] = []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": text},
		}}
		payload["usage"] = usage()
		writeJSON(w, http.StatusOK, payload)
		return
	}

	flusher, ok := beginStream(w)
	if !ok {
		return
	}
	role := base(responseID, request.Model, "chat.completion.chunk")
	role["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}}}
	sendChunk(w, flusher, role)

	content := base(responseID, request.Model, "chat.completion.chunk")
	content["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{"content": text}}}
	sendChunk(w, flusher, content)

	stop := base(responseID, request.Model, "chat.completion.chunk")
	stop["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}
	stop["usage"] = usage()
	sendChunk(w, flusher, stop)
	endStream(w, flusher)
}

// usage is fixed rather than measured: a changing token count would make otherwise identical
// runs differ, and nothing in the gate depends on the value.
func usage() map[string]any {
	return map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
}

func beginStream(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	return flusher, true
}

func sendChunk(w http.ResponseWriter, flusher http.Flusher, payload map[string]any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", encoded)
	flusher.Flush()
}

func endStream(w http.ResponseWriter, flusher http.Flusher) {
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}
