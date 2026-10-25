package emitscan

import (
	"context"
	"go/ast"
	"go/token"
	"strings"
	"time"
)

// SetWorkBudget lets a test shrink the work budget; the returned function restores it.
func SetWorkBudget(base, perNode int) func() {
	oldBase, oldPerNode := workBase, workPerNode
	workBase, workPerNode = base, perNode
	return func() { workBase, workPerNode = oldBase, oldPerNode }
}

// ChildStats are the largest CPU time (ns) and resident set (KB) a type-check helper used, and how many ran.
func ChildStats() (cpuNanos, rssKB, count int64) {
	return childStats.cpuNanos.Load(), childStats.rssKB.Load(), childStats.count.Load()
}

// ChildMaxNodes is the most syntax nodes one package had that a helper checked inside its limits.
func ChildMaxNodes() int64 { return childStats.nodes.Load() }

// SetChildLimits lets a test change the helper's limits; the returned function restores them and clears the verdict cache.
func SetChildLimits(cpuSeconds int, rssBytes int64, wall time.Duration) func() {
	oldCPU, oldRSS, oldWall := childCPUSeconds, childRSSLimit, childWall
	childCPUSeconds, childRSSLimit, childWall = cpuSeconds, rssBytes, wall
	clearProbeCache()
	return func() {
		childCPUSeconds, childRSSLimit, childWall = oldCPU, oldRSS, oldWall
		clearProbeCache()
	}
}

func clearProbeCache() {
	probeCache.Lock()
	probeCache.verdict = map[string]error{}
	probeCache.Unlock()
}

// HelperEnvNames runs the helper in its env mode and returns the names of the variables it received.
func HelperEnvNames(root string) (string, error) {
	return runChildMode(context.Background(), root, "env")
}

// SetHelperBinary plants a helper in place of the built one; the returned function restores the real one.
func SetHelperBinary(path string) func() {
	old := helperOverride
	helperOverride = path
	clearProbeCache()
	return func() {
		helperOverride = old
		clearProbeCache()
	}
}

// ChildLimits are the helper's current CPU seconds, resident-set bytes and wall-clock limit.
func ChildLimits() (cpuSeconds int, rssBytes int64, wall time.Duration) {
	return childCPUSeconds, childRSSLimit, childWall
}

// SetHelperBuilder makes the helper's build go through f (to make it fail); the returned function restores the real build.
func SetHelperBuilder(f func() (string, error)) func() {
	old := helperBuilder
	helperBuilder = f
	ShutdownHelper()
	clearProbeCache()
	return func() {
		helperBuilder = old
		ShutdownHelper()
		clearProbeCache()
	}
}

// HelperPath builds (or finds) the helper and returns its path.
func HelperPath(root string) (string, error) {
	return childBinaryPath(context.Background(), root)
}

// SetCostEstimate switches the syntax estimate that refuses oversized packages before the helper runs.
func SetCostEstimate(on bool) func() {
	old := costEstimateDisabled
	costEstimateDisabled = !on
	return func() { costEstimateDisabled = old }
}

// BuildEnvironment is the environment of the helper's `go build`.
func BuildEnvironment() []string { return buildEnvironment() }

// SetScanHelperCPU sets the CPU all helpers of one scan may use together; the returned function restores it.
func SetScanHelperCPU(d time.Duration) func() {
	old := scanHelperCPU
	scanHelperCPU = d
	return func() { scanHelperCPU = old }
}

// MemoryExceeded is the helper's memory test: over the limit now, or over it at its peak.
func MemoryExceeded(rss, peak, limit int64) bool { return memoryExceeded(rss, peak, limit) }

// LiteralHuge reports whether the syntax estimate refuses a numeric literal.
func LiteralHuge(src string) bool {
	return literalHuge(&ast.BasicLit{Kind: literalKind(src), Value: src})
}

func literalKind(src string) token.Token {
	switch {
	case strings.HasPrefix(src, "'"):
		return token.CHAR
	case strings.HasSuffix(src, "i"):
		return token.IMAG
	case strings.ContainsAny(src, ".pP") || (!strings.HasPrefix(src, "0x") && strings.ContainsAny(src, "eE")):
		return token.FLOAT
	}
	return token.INT
}

// ResetChildStats clears the largest-helper statistics.
func ResetChildStats() {
	childStats.cpuNanos.Store(0)
	childStats.rssKB.Store(0)
	childStats.count.Store(0)
	childStats.nodes.Store(0)
}

// ProbeCacheLen is the number of helper verdicts cached in this process.
func ProbeCacheLen() int {
	probeCache.Lock()
	defer probeCache.Unlock()
	return len(probeCache.verdict)
}

// ResetGoTool forgets the resolved go command and the export data read with it.
func ResetGoTool() {
	goTool.Lock()
	goTool.path, goTool.err, goTool.done = "", nil, false
	goTool.Unlock()
	exportCache.Lock()
	exportCache.byDir = map[string]*exportEntry{}
	exportCache.Unlock()
}

// GoBinary is the resolved go command or the reason there is none.
func GoBinary() (string, error) { return goBinary() }

// ExportFiles reads the export data of the module at dir with the resolved go command.
func ExportFiles(dir string) error {
	_, err := exportFiles(context.Background(), dir)
	return err
}

// WriteCreatorMarker and SweepStaleHelperDirs expose the leftover-directory sweep.
func WriteCreatorMarker(dir string)   { writeCreatorMarker(dir) }
func SweepStaleHelperDirs(tmp string) { sweepStaleHelperDirs(tmp) }
func ProcessStartOf(pid int) (string, bool) {
	start, status := processStart(pid)
	return start, status == procKnown
}

const HelperDirPrefix = helperDirPrefix
const CreatorMarker = creatorMarker

// PIDNamespace is the pid namespace of this process.
func PIDNamespace() (string, bool) { return pidNamespace() }
