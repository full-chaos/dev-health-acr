// Command gen writes the MCP guide resources from the ACR registries into
// ../content. Run it with go generate ./internal/mcp/guide.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/full-chaos/dev-health-acr/internal/mcp/guide/guidegen"
)

func main() {
	files, err := guidegen.Build(guidegen.FromRegistries())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// go:generate runs with cwd == the directory holding the directive.
	dir := "content"
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
