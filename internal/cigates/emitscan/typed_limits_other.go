//go:build !unix

package emitscan

import "os"

func residentBytes(pid int) (rss, peak int64) { return 0, 0 }

func recordUsage(state *os.ProcessState) {}

func checkHelperFile(path string) error { return nil }

func usedCPU(state *os.ProcessState) int64 { return 0 }

func processStart(pid int) (string, procStatus) { return "", procUnknown }

func ownedByThisUser(info os.FileInfo) bool { return false }

func pidNamespace() (string, bool) { return "", false }
