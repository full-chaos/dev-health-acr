package devhealthfacts_test

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-go/readers"
)

// The blocker statements carry their reader settings and the block size in ONE
// trailing SETTINGS clause; a second SETTINGS keyword is a syntax error.
func TestBlockerStatementCarriesOneSettingsClause(t *testing.T) {
	settings := readers.Settings{MaxExecutionTimeSeconds: 5, MaxRowsToRead: 3000000, MaxMemoryUsage: 64 << 20, MaxThreads: 1, MaxResultRows: 201}
	for _, c := range []struct {
		name     string
		rows     int
		settings readers.Settings
		want     string
	}{
		{"block size and settings", 8192, settings, "\nSETTINGS max_block_size = 8192, max_execution_time = 5, "},
		{"block size alone", 8192, readers.Settings{}, "\nSETTINGS max_block_size = 8192"},
		{"settings alone", 0, settings, "\nSETTINGS max_execution_time = 5, "},
		{"neither", 0, readers.Settings{}, ""},
	} {
		restore := devhealthfacts.SetBlockerMaxBlockSizeForTest(c.rows)
		got := devhealthfacts.BlockerStatementForTest("SELECT 1", c.settings)
		restore()
		if !strings.HasPrefix(got, "SELECT 1"+c.want) {
			t.Errorf("%s: %q does not start with SELECT 1%q", c.name, got, c.want)
		}
		if n := strings.Count(got, "SETTINGS"); n > 1 {
			t.Errorf("%s: %d SETTINGS keywords in %q", c.name, n, got)
		}
	}
}
