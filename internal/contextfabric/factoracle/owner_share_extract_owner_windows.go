//go:build windows

package factoracle

import "os"

// fileOwnedByRunningUser gives no owner on Windows: the ownership extract
// file is refused there.
func fileOwnedByRunningUser(os.FileInfo) (owned, known bool) { return false, false }
