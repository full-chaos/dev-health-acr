package emitscan_test

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// maxExportListExecs is the most `go list -export -deps` commands one run of the tests may start, counted at the exec.
// Measured 10 in file order and 13-14 over eight shuffle seeds (a deliberate ResetGoTool empties the cache, so the
// order moves the count); an unshared load starts 36. Above the cap, a scan reloads export data it already has.
const maxExportListExecs = 16

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
