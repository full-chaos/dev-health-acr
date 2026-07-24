package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// findArtifact resolves an artifact's actual path on disk given the run's --artifacts dir,
// the current task ID and a bare base name (e.g. "context-packet" for
// "context-packet.json"). scripts/e2e/fullstack-opencode.sh evolved, while this tool was
// being written, from a single-task-per-run layout to a per-task-suffixed layout for the
// artifacts that vary per task within one `full` scenario run (opencode-events-<task>.jsonl,
// agent-result-<task>.json); other artifacts (capabilities.json, context-packet.json,
// mcp-tools.json, expanded-evidence/) were not yet wired to a naming convention as of this
// writing. To stay correct under either convention without another round-trip, this tries,
// in order:
//
//  1. <dir>/<base>-<task_id>.<ext>          (flat, per-task suffix -- opencode-events, agent-result)
//  2. <dir>/<task_id>/<base>.<ext>          (per-task subdirectory)
//  3. <dir>/<base>.<ext>                    (flat, single-task-per-run)
//
// It returns the first path that exists, or ("", false) if none do. Call sites report a
// clear failing check (not a panic) when an artifact is missing.
func findArtifact(dir, taskID, base, ext string) (string, bool) {
	candidates := []string{
		filepath.Join(dir, fmt.Sprintf("%s-%s.%s", base, taskID, ext)),
		filepath.Join(dir, taskID, fmt.Sprintf("%s.%s", base, ext)),
		filepath.Join(dir, fmt.Sprintf("%s.%s", base, ext)),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// findExpandedEvidenceDir resolves the expanded-evidence directory for a task, trying a
// per-task subdirectory before falling back to the flat shared directory documented in
// docs/fullstack-acceptance.md section 9 (expanded-evidence/*.json).
func findExpandedEvidenceDir(dir, taskID string) (string, bool) {
	perTask := filepath.Join(dir, "expanded-evidence", taskID)
	if info, err := os.Stat(perTask); err == nil && info.IsDir() {
		return perTask, true
	}
	flat := filepath.Join(dir, "expanded-evidence")
	if info, err := os.Stat(flat); err == nil && info.IsDir() {
		return flat, true
	}
	return "", false
}

// listExpandedEvidenceFiles returns the sorted list of *.json files directly inside dir. It
// does not recurse, so a stray per-task subdirectory under the flat layout is not double
// counted.
func listExpandedEvidenceFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read expanded evidence dir %s: %w", dir, err)
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(out)
	return out, nil
}

func readJSONFile(path string, out any) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return data, nil
}

// mcpToolNames tolerates the two shapes a captured mcp-tools.json artifact plausibly takes:
// a bare JSON array of tool name strings, or an object carrying a raw MCP tools/list result
// shape ({"tools":[{"name":"..."}, ...]}) like the one scripts/e2e/svs.sh already asserts
// against (jq: '[.result.tools[].name]'). Either an array at the top level or under "tools"
// is accepted; a nested {"result":{"tools":[...]}} JSONRPC envelope is accepted too.
func mcpToolNames(data []byte) ([]string, error) {
	var asArray []string
	if err := json.Unmarshal(data, &asArray); err == nil {
		return asArray, nil
	}
	var withNames struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &withNames); err != nil {
		return nil, fmt.Errorf("decode mcp-tools.json: %w", err)
	}
	names := make([]string, 0, len(withNames.Tools)+len(withNames.Result.Tools))
	for _, t := range withNames.Tools {
		names = append(names, t.Name)
	}
	for _, t := range withNames.Result.Tools {
		names = append(names, t.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("mcp-tools.json did not contain a recognizable tool list")
	}
	return names, nil
}
