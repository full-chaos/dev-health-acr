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
// Worst case over every test order is 14: 7 execs that do not depend on order (ExportDataIsBuiltFromTheParsedBytes 2,
// ExportCacheIsKeyedByTheParsedBytes 2, and one each in OverlayDirectoryIsRemoved, ExportDataIsListedOnce and
// GoListRunsWithTheResolvedGo), plus one load of the module's fixture key per stretch between stand-in go tests (5 of
// them empty the cache, so 6 stretches), plus 1 for the shared real-tree scan. File order gives 10; shuffle seeds gave
// 13-14. An unshared load starts 36. Adding a test that starts go list raises the bound: recount, do not just raise.
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
