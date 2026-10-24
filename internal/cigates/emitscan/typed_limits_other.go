//go:build !unix

package emitscan

import "os"

func residentBytes(pid int) (rss, peak int64) { return 0, 0 }

func recordUsage(state *os.ProcessState) {}

func checkHelperFile(path string) error { return nil }

func usedCPU(state *os.ProcessState) int64 { return 0 }
