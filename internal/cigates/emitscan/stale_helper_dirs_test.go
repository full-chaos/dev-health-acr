//go:build unix

package emitscan_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

func helperDirWithMarker(t *testing.T, tmp, name, marker string) string {
	t.Helper()
	dir := filepath.Join(tmp, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if marker != "" {
		if err := os.WriteFile(filepath.Join(dir, emitscan.CreatorMarker), []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// The sweep removes the helper directories of creators that are gone and nothing else: no age rule, since every lane on a
// host is the same user.
func TestSweepRemovesOnlyDirectoriesOfGoneCreators(t *testing.T) {
	tmp := t.TempDir()
	prefix := emitscan.HelperDirPrefix

	creator := exec.Command("sleep", "60")
	if err := creator.Start(); err != nil {
		t.Fatal(err)
	}
	deadStart, ok := emitscan.ProcessStartOf(creator.Process.Pid)
	if !ok {
		t.Skip("no /proc process start time on this host")
	}
	_ = creator.Process.Kill()
	_ = creator.Wait()

	self := os.Getpid()
	selfStart, _ := emitscan.ProcessStartOf(self)

	dead := helperDirWithMarker(t, tmp, prefix+"dead", strconv.Itoa(creator.Process.Pid)+" "+deadStart+"\n")
	live := helperDirWithMarker(t, tmp, prefix+"live", "")
	emitscan.WriteCreatorMarker(live)
	noMarker := helperDirWithMarker(t, tmp, prefix+"nomarker", "")
	reused := helperDirWithMarker(t, tmp, prefix+"reused", strconv.Itoa(self)+" "+selfStart+"0\n")
	garbage := helperDirWithMarker(t, tmp, prefix+"garbage", "not a marker\n")
	elsewhere := helperDirWithMarker(t, tmp, "other-"+"dead", strconv.Itoa(creator.Process.Pid)+" "+deadStart+"\n")
	linked := helperDirWithMarker(t, tmp, prefix+"linked", "")
	target := filepath.Join(tmp, "marker-target")
	if err := os.WriteFile(target, []byte(strconv.Itoa(creator.Process.Pid)+" "+deadStart+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(linked, emitscan.CreatorMarker)); err != nil {
		t.Fatal(err)
	}

	emitscan.SweepStaleHelperDirs(tmp)

	for name, want := range map[string]struct {
		dir  string
		kept bool
	}{
		"a dead creator":                        {dead, false},
		"a creator that is this live process":   {live, true},
		"no marker":                             {noMarker, true},
		"a reused pid with another start time":  {reused, false},
		"an unparsable marker":                  {garbage, true},
		"a directory without the helper prefix": {elsewhere, true},
		"a marker that is a symlink":            {linked, true},
	} {
		if exists(want.dir) != want.kept {
			t.Errorf("%s: directory kept = %v, want %v", name, exists(want.dir), want.kept)
		}
	}
}

// The marker names this process and its start time.
func TestCreatorMarkerNamesTheRunningProcess(t *testing.T) {
	dir := t.TempDir()
	emitscan.WriteCreatorMarker(dir)
	data, err := os.ReadFile(filepath.Join(dir, emitscan.CreatorMarker))
	if err != nil {
		t.Skipf("no marker written on this host: %v", err)
	}
	start, _ := emitscan.ProcessStartOf(os.Getpid())
	if want := strconv.Itoa(os.Getpid()) + " " + start; strings.TrimSpace(string(data)) != want {
		t.Fatalf("marker = %q, want %q", data, want)
	}
}
