// Command gen regenerates eventspec's zz_generated.go and schema.json from
// spec.go. Run via `go generate ./internal/contextfabric/eventspec/...`;
// regen_test.go asserts the checked-in files already match this output.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
)

func main() {
	// go:generate always runs with cwd == the directory of the file holding
	// the directive: internal/contextfabric/eventspec itself.
	outDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "eventspec/gen:", err)
		os.Exit(1)
	}
	files, err := eventspec.Generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "eventspec/gen: generate:", err)
		os.Exit(1)
	}
	for name, content := range files {
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "eventspec/gen: write", path, err)
			os.Exit(1)
		}
	}
}
