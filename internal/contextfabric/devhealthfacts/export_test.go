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
// every work_item_dependencies row of orgID: source, target, whether the
// provider still reports the row, and whether its writer could not be derived.
func RelationCurrentProbeForTest(orgID string) string {
	probe := `SELECT d.source_work_item_id, d.target_work_item_id, toUInt8(NOT (` + relationEndedSQL("d") + `)), toUInt8(` + relationWriterCodeSQL("d") + ` = ` + "4" + `)
FROM work_item_dependencies AS d FINAL` + relationCurrentJoinsSQL("d") + `
WHERE d.org_id = {org_id:String}`
	return strings.ReplaceAll(probe, "{org_id:String}", "'"+orgID+"'")
}
