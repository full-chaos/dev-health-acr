package version

import (
	"regexp"
	"strings"
	"time"
)

var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// localBuildVersion is the semver stamped into the supported `make build`
// output. The leading v keeps it valid for compatibility checks while
// remaining non-canonical, so the binary keeps the non-release override
// path available.
const localBuildVersion = "v0.1.0"

type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

func Current() Info {
	return Info{Version: Version, Commit: Commit, Date: Date}
}

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IsRelease reports whether info has the complete identity injected by a
// release build: canonical SemVer, a full lowercase Git SHA, and UTC RFC3339
// commit time. Local builds intentionally retain the dev identity.
func (i Info) IsRelease() bool {
	if !IsCanonical(i.Version) || !fullCommit.MatchString(i.Commit) {
		return false
	}
	buildTime, err := time.Parse(time.RFC3339, i.Date)
	return err == nil && strings.HasSuffix(i.Date, "Z") && buildTime.Format(time.RFC3339) == i.Date
}

// EffectiveVersion returns the compiled release version whenever the binary
// has a complete release identity. Development binaries may use an explicit
// valid SemVer fixture override; malformed overrides retain dev and fail
// closed at the hosted compatibility boundary.
func EffectiveVersion(compiled Info, fixtureOverride string) string {
	if compiled.IsRelease() {
		return compiled.Version
	}
	if IsValid(fixtureOverride) {
		return normalize(fixtureOverride)
	}
	return compiled.Version
}
