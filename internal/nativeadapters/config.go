package nativeadapters

import "fmt"

func Config(client Client) ([]byte, error) {
	switch client {
	case OpenCode:
		return []byte(`{"mcp":{"acr":{"type":"local","command":["acr-mcp","serve"]}}}`), nil
	case Claude:
		return []byte(`{"mcpServers":{"acr":{"command":"acr-mcp","args":["serve"]}}}`), nil
	case Codex:
		return []byte(`{"mcpServers":{"acr":{"command":"acr-mcp","args":["serve"],"enabled":true,"required":false,"default_tools_approval_mode":"prompt","enabled_tools":["context_for_task","source_evidence"]}}}`), nil
	case Cursor:
		return []byte(`{"mcpServers":{"acr":{"type":"stdio","command":"acr-mcp","args":["serve"]}}}`), nil
	default:
		return nil, fmt.Errorf("native adapter: unsupported client %q", client)
	}
}
