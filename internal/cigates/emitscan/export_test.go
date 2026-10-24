package emitscan

import (
	"context"
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
