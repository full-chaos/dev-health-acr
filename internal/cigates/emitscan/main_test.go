package emitscan_test

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// maxExportListExecs is the most `go list -export -deps` commands one run of the tests may start, counted at the exec:
// the first real run (no overlay), five overlay runs of the tests that scan other bytes, one after the deliberate
// ResetGoTool, and three against planted stand-in go commands. A scan that reloads what it already has exceeds it.
const maxExportListExecs = 10

func TestMain(m *testing.M) {
	code := m.Run()
	emitscan.ShutdownHelper()
	runs := int64(1)
	if f := flag.Lookup("test.count"); f != nil {
		if n, err := strconv.ParseInt(f.Value.String(), 10, 64); err == nil && n > 1 {
			runs = n
		}
	}
	if got := emitscan.ExportListExecs(); code == 0 && got > maxExportListExecs*runs {
		fmt.Fprintf(os.Stderr, "emitscan tests started %d go list -export -deps commands, want at most %d: a scan reloads the export data it already has\n", got, maxExportListExecs*runs)
		code = 1
	}
	os.Exit(code)
}
