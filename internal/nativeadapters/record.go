package nativeadapters

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type Recording struct {
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
	Dir    string            `json:"dir"`
	Config json.RawMessage   `json:"config"`
}

func Record(client Client, args []string, environ []string, dir string, writer io.Writer) error {
	values := map[string]string{}
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	config, err := Config(client)
	if err != nil {
		return err
	}
	recording := Recording{Args: args, Dir: dir, Env: map[string]string{}, Config: config}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "CODEX_SQLITE_HOME", "PATH"} {
		recording.Env[key] = values[key]
	}
	encoded, err := json.Marshal(recording)
	if err != nil {
		return fmt.Errorf("record native adapter: %w", err)
	}
	path := values["ACR_NATIVE_RECORDS"]
	if path != "" {
		if err := os.WriteFile(path+"/"+string(client)+".json", append(encoded, '\n'), 0o600); err != nil {
			return fmt.Errorf("record native adapter: %w", err)
		}
	}
	for _, line := range recordingEvents(client) {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			return err
		}
	}
	return nil
}

func recordingEvents(client Client) []string {
	switch client {
	case OpenCode:
		return []string{`{"type":"mcp.connected","server":"acr"}`, `{"type":"mcp.tool","name":"context_for_task"}`, `{"type":"result","status":"ok"}`}
	case Claude:
		return []string{`{"type":"system","subtype":"init"}`, `{"type":"assistant","event":"tool_use","tool":"context_for_task"}`, `{"type":"result","subtype":"success"}`}
	case Codex:
		return []string{`{"type":"mcp_tool_call","tool":"context_for_task"}`, `{"type":"completion","status":"completed"}`}
	case Cursor:
		return []string{`{"type":"mcp","server":"acr"}`, `{"type":"result","status":"ok"}`}
	default:
		return nil
	}
}
