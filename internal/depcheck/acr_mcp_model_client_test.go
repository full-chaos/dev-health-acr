package depcheck

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// acr-mcp serves the prompt text from the leaf prompt packages and must not
// link the model client.
func TestAcrMCPLinksNoModelClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "list", "-deps", "github.com/full-chaos/dev-health-acr/cmd/acr-mcp").Output()
	if err != nil {
		t.Fatalf("go list -deps ./cmd/acr-mcp: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) < 50 {
		t.Fatalf("go list -deps returned %d packages; the measurement did not run", len(deps))
	}
	prompts := 0
	for _, dep := range deps {
		if strings.Contains(dep, "genkit") {
			t.Errorf("acr-mcp links %s", dep)
		}
		if strings.HasSuffix(dep, "/internal/contextfabric/interpretprompt") || strings.HasSuffix(dep, "/internal/contextfabric/synthesisprompt") {
			prompts++
		}
	}
	if prompts != 2 {
		t.Errorf("acr-mcp links %d of the 2 leaf prompt packages", prompts)
	}
}
