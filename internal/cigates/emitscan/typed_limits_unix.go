//go:build unix

package emitscan

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// residentBytes is the resident set and its peak of a process, 0 where /proc is not there.
func residentBytes(pid int) (rss, peak int64) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || (name != "VmRSS" && name != "VmHWM") {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if name == "VmRSS" {
			rss = kb << 10
		} else {
			peak = kb << 10
		}
	}
	return rss, peak
}

// recordUsage notes the CPU time of a finished helper (its peak resident set is read while it runs: ru_maxrss of an
// exec'd child can carry the parent's size).
func recordUsage(state *os.ProcessState) {
	if state == nil {
		return
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return
	}
	childStats.count.Add(1)
	noteChildStat(&childStats.cpuNanos, usage.Utime.Nano()+usage.Stime.Nano())
}

// checkHelperFile refuses to run anything but a regular file (not a symlink) owned by this user, in a directory that no
// one else can write.
func checkHelperFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %v", errHelperUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is not a regular file of this user", errHelperUnavailable, filepath.Base(path))
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: the helper directory is writable by others", errHelperUnavailable)
	}
	return nil
}

// usedCPU is the CPU time a finished process used (user plus system), 0 where it is not known.
func usedCPU(state *os.ProcessState) int64 {
	if state == nil {
		return 0
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok {
		return 0
	}
	return usage.Utime.Nano() + usage.Stime.Nano()
}

// processStart is the start time (field 22 of /proc/<pid>/stat, in clock ticks since boot) of a process, which tells a
// process from a later one that reused its id. procUnknown where /proc is not there or the read fails for any reason
// other than the process being gone.
func processStart(pid int) (string, procStatus) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		return "", procUnknown
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, fs.ErrNotExist) {
		return "", procGone
	}
	if err != nil {
		return "", procUnknown
	}
	text := string(data)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return "", procUnknown
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 20 {
		return "", procUnknown
	}
	return fields[19], procKnown
}

// ownedByThisUser is true for a file of the user running the scan.
func ownedByThisUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}
