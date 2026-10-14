package devhealthfacts

import (
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-go/readers"
)

// blockerMaxBlockSize is the rows per block the blocker statements read and
// pass between steps. The server's default is 65,536: with the 40-130 byte
// work item ids these statements carry, one block of one string column is
// several MiB, and a statement holds a few of them at once. 8,192 is the
// table's index granularity, the smallest block a granule read produces.
var blockerMaxBlockSize = 8192

// blockerStatement renders statement with its reader settings and the block
// size, in the one trailing SETTINGS clause.
func blockerStatement(statement string, settings readers.Settings) string {
	rendered := settings.Render()
	if blockerMaxBlockSize <= 0 {
		return statement + rendered
	}
	setting := " SETTINGS max_block_size = " + strconv.Itoa(blockerMaxBlockSize)
	if rendered == "" {
		return statement + setting
	}
	return statement + setting + ", " + strings.TrimPrefix(rendered, " SETTINGS ")
}
