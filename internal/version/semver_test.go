package version

import "testing"

func TestAtLeast_usesSemVerPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		have, want string
		wantOK     bool
	}{
		{name: "release equals release", have: "1.0.0", want: "1.0.0", wantOK: true},
		{name: "build metadata does not affect precedence", have: "1.0.0+build.7", want: "1.0.0+build.1", wantOK: true},
		{name: "prerelease ordering", have: "1.0.0-rc.2", want: "1.0.0-rc.1", wantOK: true},
		{name: "prerelease precedes release", have: "1.0.0-rc.1", want: "1.0.0", wantOK: false},
		{name: "release follows prerelease", have: "1.0.0", want: "1.0.0-rc.1", wantOK: true},
		{name: "leading v remains accepted for legacy callers", have: "v2.0.0", want: "1.9.9", wantOK: true},
		{name: "malformed client fails closed", have: "latest", want: "1.0.0", wantOK: false},
		{name: "development sentinel fails closed", have: "dev", want: "1.0.0", wantOK: false},
		{name: "malformed minimum fails closed", have: "1.0.0", want: "1.0.x", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// When
			got := AtLeast(test.have, test.want)

			// Then
			if got != test.wantOK {
				t.Fatalf("AtLeast(%q, %q) = %t, want %t", test.have, test.want, got, test.wantOK)
			}
		})
	}
}

func TestInfoIsRelease_requiresCompleteInjectedIdentity(t *testing.T) {
	tests := []struct {
		name string
		info Info
		want bool
	}{
		{name: "release identity", info: Info{Version: "1.2.3-rc.1+build.7", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-07-12T15:04:05Z"}, want: true},
		{name: "development identity", info: Info{Version: "dev", Commit: "unknown", Date: "unknown"}},
		{name: "short commit", info: Info{Version: "1.2.3", Commit: "0123456", Date: "2026-07-12T15:04:05Z"}},
		{name: "non UTC date", info: Info{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-07-12T15:04:05+01:00"}},
		{name: "leading v version", info: Info{Version: "v1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-07-12T15:04:05Z"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.info.IsRelease(); got != test.want {
				t.Fatalf("Info.IsRelease() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestEffectiveVersion_keepsReleaseIdentityAuthoritative(t *testing.T) {
	release := Info{Version: "1.2.3", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-07-12T15:04:05Z"}
	development := Info{Version: "dev", Commit: "unknown", Date: "unknown"}

	if got := EffectiveVersion(release, "9.9.9"); got != "1.2.3" {
		t.Fatalf("release override = %q, want compiled version", got)
	}
	if got := EffectiveVersion(development, "2.0.0-rc.1"); got != "2.0.0-rc.1" {
		t.Fatalf("development fixture override = %q", got)
	}
	if got := EffectiveVersion(development, "latest"); got != "dev" {
		t.Fatalf("invalid development override = %q, want dev", got)
	}
}
