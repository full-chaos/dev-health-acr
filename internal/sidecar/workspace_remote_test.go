package sidecar

import (
	"errors"
	"strings"
	"testing"
)

func TestParseRemoteURL_SupportedShapes(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		host  string
		owner string
		repo  string
	}{
		{"https with .git suffix", "https://github.com/full-chaos/dev-health-acr.git", "github.com", "full-chaos", "dev-health-acr"},
		{"https without .git suffix", "https://github.com/full-chaos/dev-health-acr", "github.com", "full-chaos", "dev-health-acr"},
		{"https with trailing slash", "https://github.com/full-chaos/dev-health-acr/", "github.com", "full-chaos", "dev-health-acr"},
		{"scp-like shorthand", "git@github.com:full-chaos/dev-health-acr.git", "github.com", "full-chaos", "dev-health-acr"},
		{"ssh URL with git user", "ssh://git@github.com/full-chaos/dev-health-acr.git", "github.com", "full-chaos", "dev-health-acr"},
		{"ssh URL with explicit port", "ssh://git@github.com:2222/full-chaos/dev-health-acr.git", "github.com", "full-chaos", "dev-health-acr"},
		{"non-github host", "https://gitlab.example.com/owner/repo.git", "gitlab.example.com", "owner", "repo"},
		{"owner/repo with dots and dashes", "https://github.com/full-chaos/dev-health.acr-2.git", "github.com", "full-chaos", "dev-health.acr-2"},
		{"ssh URL with percent-encoded user decoding to git", "ssh://gi%74@github.com/full-chaos/dev-health-acr.git", "github.com", "full-chaos", "dev-health-acr"},
		{"ssh URL with IPv6 host and port", "ssh://git@[2001:db8::1]:22/full-chaos/dev-health-acr.git", "2001:db8::1", "full-chaos", "dev-health-acr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseRemoteURL("origin", tc.raw)
			if err != nil {
				t.Fatalf("parseRemoteURL(%q): %v", tc.raw, err)
			}
			if info.Host != tc.host || info.Owner != tc.owner || info.Repo != tc.repo {
				t.Fatalf("parseRemoteURL(%q) = %+v, want host=%s owner=%s repo=%s", tc.raw, info, tc.host, tc.owner, tc.repo)
			}
			if got := info.Slug(); got != tc.owner+"/"+tc.repo {
				t.Fatalf("Slug() = %q, want %q", got, tc.owner+"/"+tc.repo)
			}
		})
	}
}

func TestParseRemoteURL_UnsupportedShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"insecure git protocol", "git://github.com/owner/repo.git"},
		{"plain http", "http://github.com/owner/repo.git"},
		{"local file scheme", "file:///local/repo.git"},
		{"embedded https credentials", "https://user:pass@github.com/owner/repo.git"},
		{"non-git ssh user", "ssh://evil@github.com/owner/repo.git"},
		{"ssh with password on git user", "ssh://git:secret@github.com/owner/repo.git"},
		{"ssh with no user at all", "ssh://github.com/owner/repo.git"},
		{"ssh with multiple '@' before host", "ssh://git@evil@github.com/owner/repo.git"},
		{"ssh percent-encoded userinfo hiding a colon secret", "ssh://git%3Asecret@github.com/owner/repo.git"},
		{"extra path segment (subgroup)", "https://gitlab.example.com/group/owner/repo.git"},
		{"missing repo segment", "https://github.com/owner"},
		{"empty owner", "https://github.com//repo.git"},
		{"local filesystem path", "/absolute/local/path.git"},
		{"relative local path", "../sibling-repo"},
		{"empty URL", ""},
		{"whitespace only", "   "},
		{"owner is a single dot", "https://github.com/./repo.git"},
		{"owner is a double dot", "https://github.com/../repo.git"},
		{"owner contains slash-adjacent scheme junk", "https://github.com/o w/repo.git"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseRemoteURL("origin", tc.raw)
			if !errors.Is(err, ErrUnsupportedRemote) {
				t.Fatalf("parseRemoteURL(%q) error = %v, want ErrUnsupportedRemote", tc.raw, err)
			}
		})
	}
}

func TestParseRemoteURL_ControlCharactersRejected(t *testing.T) {
	_, err := parseRemoteURL("origin", "https://github.com/owner/re\x01po.git")
	if !errors.Is(err, ErrControlCharacters) {
		t.Fatalf("expected ErrControlCharacters, got %v", err)
	}
}

func TestRemoteInfo_SlugEmptyWhenIncomplete(t *testing.T) {
	if got := (RemoteInfo{Owner: "owner"}).Slug(); got != "" {
		t.Fatalf("Slug() = %q, want empty when Repo is unset", got)
	}
	if got := (RemoteInfo{Repo: "repo"}).Slug(); got != "" {
		t.Fatalf("Slug() = %q, want empty when Owner is unset", got)
	}
}

// TestParseRemoteURL_NeverLeaksCredentialsInError proves that a rejected
// remote URL carrying embedded userinfo (a password or access token) never
// has that credential echoed back in the returned error text — the
// rejection path is exactly the path most likely to see a credential, since
// a well-formed, credential-free remote never reaches it.
func TestParseRemoteURL_NeverLeaksCredentialsInError(t *testing.T) {
	const canary = "S3cr3t-Canary-Token-Do-Not-Leak"
	cases := []string{
		"https://" + canary + ":ignored@github.com/owner/repo.git",
		"https://user:" + canary + "@github.com/owner/repo.git",
		"https://" + canary + "@github.com/owner/repo.git",
		// Scheme-less SCP-like shorthand with a non-"git" username: falls
		// through parseRemoteURL's default case (an unrecognized shape),
		// and unlike the https:// cases above, url.Parse itself returns an
		// error on this shape ("first path segment in URL cannot contain
		// colon"), so a redaction path that only fires when url.Parse
		// succeeds would miss it entirely.
		canary + "@github.com:owner/repo.git",
		// Scheme-less shorthand with both a username and a password before
		// the host. url.Parse "succeeds" on this shape but misparses it
		// (treating "user" as a bogus URL scheme and leaving u.User unset),
		// so a redaction path keyed on u.User != nil would also miss it.
		"user:" + canary + "@github.com:owner/repo.git",
		// SSH URL scheme with a non-"git" user carrying the canary as the
		// password.
		"ssh://user:" + canary + "@github.com/owner/repo.git",
		// SSH URL scheme with the correct "git" user but a password carrying
		// the canary — must be rejected for the password alone, and the
		// password must never be echoed back.
		"ssh://git:" + canary + "@github.com/owner/repo.git",
		// Percent-encoded userinfo that decodes to something other than a
		// bare "git" username, carrying the canary as the decoded secret.
		"ssh://git%3A" + canary + "@github.com/owner/repo.git",
		// A control character embedded alongside a credential must still
		// never leak the credential (the control-character rejection path
		// fires first and never echoes raw input at all).
		"https://" + canary + "@github.com/own\x01er/repo.git",
	}
	for _, raw := range cases {
		_, err := parseRemoteURL("origin", raw)
		if err == nil {
			t.Fatalf("expected an error for credential-bearing URL %q", raw)
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("error leaked the credential canary: %v", err)
		}
	}
}
