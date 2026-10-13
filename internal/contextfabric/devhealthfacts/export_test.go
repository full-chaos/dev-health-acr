package devhealthfacts

import (
	"strings"
	"time"

	"github.com/full-chaos/dev-health-go/readers"
)

func SetClockForTest(now func() time.Time) (restore func()) {
	previous := clock
	clock = now
	return func() { clock = previous }
}

func StatusSinceStatementForTest(settings readers.Settings) string {
	return statusSinceStatement(settings)
}

// RelationCurrentProbeForTest is the relation end rule as one statement over
// every blocker row of orgID: source, target, whether the provider still
// reports the row, and whether its writer could not be derived.
func RelationCurrentProbeForTest(orgID string) string {
	probe := `SELECT s, t, toUInt8(NOT ended), toUInt8(code = 4) FROM ` + relationRowsSQL()
	return strings.ReplaceAll(probe, "{org_id:String}", "'"+orgID+"'")
}
