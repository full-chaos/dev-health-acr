package sidecar

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// ownerRepoTokenPattern restricts owner/repo tokens to a conservative safe
// charset: no path separators, no whitespace, no control characters, and no
// shell/URL metacharacters.
var ownerRepoTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// maxSanitizedRemoteEcho bounds how much of a rejected remote URL is ever
// echoed back in an error, after credential redaction. It is the strict
// total byte bound on redactRemoteURLForError's output, including any
// truncation suffix — never just the pre-suffix prefix.
const maxSanitizedRemoteEcho = 200

// remoteEchoTruncationSuffix is appended when a redacted remote echo is
// truncated, so the caller can tell the echo was cut short.
const remoteEchoTruncationSuffix = "...(truncated)"

// remoteEchoTruncationBudget is the number of prefix bytes kept when
// truncating, leaving exactly enough room for remoteEchoTruncationSuffix
// so prefix+suffix never exceeds maxSanitizedRemoteEcho. The array bound
// below is a compile-time assertion (not a runtime check) that the
// suffix fits within the budget: it fails to compile with a negative
// array length if that invariant is ever violated by a future edit to
// either constant.
const remoteEchoTruncationBudget = maxSanitizedRemoteEcho - len(remoteEchoTruncationSuffix)

var _ [remoteEchoTruncationBudget]struct{}

// parseRemoteURL normalizes a Git remote URL into a RemoteInfo, accepting
// only safe, unambiguous shapes:
//
//   - https://<host>/<owner>/<repo>[.git]      (no userinfo, no query/fragment)
//   - ssh://git@<host>[:port]/<owner>/<repo>[.git]  (user must be exactly
//     "git", no password)
//   - git@<host>:<owner>/<repo>[.git]           (SCP-like shorthand)
//
// Anything else — insecure git:// or plain http://, file:// URLs, embedded
// credentials, extra path segments (subgroups), or a missing owner/repo — is
// rejected as an unsupported remote shape rather than guessed at. Rejected
// URLs are never echoed verbatim into the returned error: any embedded
// userinfo (which may carry a password or access token) is redacted first,
// since a rejected remote is exactly the shape most likely to carry one.
func parseRemoteURL(name, raw string) (RemoteInfo, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return RemoteInfo{}, fmt.Errorf("%w: empty remote URL", ErrUnsupportedRemote)
	}
	if containsControlChar(trimmed) {
		return RemoteInfo{}, fmt.Errorf("%w: remote URL", ErrControlCharacters)
	}

	var host, path string

	switch {
	case strings.HasPrefix(trimmed, "https://"):
		u, err := url.Parse(trimmed)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return RemoteInfo{}, unsupportedRemoteErr(raw)
		}
		host, path = u.Hostname(), u.Path

	case strings.HasPrefix(trimmed, "ssh://"):
		u, err := url.Parse(trimmed)
		if err != nil || u.RawQuery != "" || u.Fragment != "" {
			return RemoteInfo{}, unsupportedRemoteErr(raw)
		}
		if u.User == nil || u.User.Username() != "git" {
			return RemoteInfo{}, unsupportedRemoteErr(raw)
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return RemoteInfo{}, unsupportedRemoteErr(raw)
		}
		host, path = u.Hostname(), u.Path

	case strings.HasPrefix(trimmed, "git@"):
		rest := strings.TrimPrefix(trimmed, "git@")
		idx := strings.Index(rest, ":")
		if idx <= 0 {
			return RemoteInfo{}, unsupportedRemoteErr(raw)
		}
		host = rest[:idx]
		path = rest[idx+1:]

	default:
		// Covers insecure git://, plain http://, file://, and any other
		// unrecognized shape.
		return RemoteInfo{}, unsupportedRemoteErr(raw)
	}

	host = strings.TrimSpace(host)
	if containsControlChar(host) {
		return RemoteInfo{}, fmt.Errorf("%w: remote host", ErrControlCharacters)
	}

	path = strings.Trim(strings.TrimSpace(path), "/")
	path = strings.TrimSuffix(path, ".git")
	segments := strings.Split(path, "/")

	if host == "" || len(segments) != 2 || segments[0] == "" || segments[1] == "" {
		return RemoteInfo{}, unsupportedRemoteErr(raw)
	}

	owner, repo := segments[0], segments[1]
	if !isSafeOwnerRepoToken(owner) || !isSafeOwnerRepoToken(repo) {
		return RemoteInfo{}, unsupportedRemoteErr(raw)
	}

	return RemoteInfo{Name: name, Host: host, Owner: owner, Repo: repo}, nil
}

func isSafeOwnerRepoToken(token string) bool {
	if token == "." || token == ".." {
		return false
	}
	return ownerRepoTokenPattern.MatchString(token)
}

// unsupportedRemoteErr builds an ErrUnsupportedRemote error that includes
// only a credential-redacted, length-bounded echo of the rejected URL.
func unsupportedRemoteErr(raw string) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedRemote, redactRemoteURLForError(raw))
}

// leadingUserinfoPattern matches an optional "scheme://" prefix followed
// by everything up to and including the first '@' that appears before any
// '/' in the string. That is exactly the shape of embedded userinfo in
// every remote URL form this package encounters: a standard URL's
// authority ("scheme://user:pass@host/..."), and the SCP-like shorthand
// ("user[:pass]@host:path", with or without a "git@"-style username, and
// regardless of whether a scheme prefix is present at all). The leading
// userinfo may itself contain further '@' characters (e.g.
// "a@b@host:path"); the greedy match consumes through the *last* '@'
// before the first '/', not just the first, so nothing is left
// half-redacted. It intentionally does not require the matched text to be
// a well-formed URL, since the whole point is to redact credentials from
// remote shapes url.Parse rejects outright (a colon in the first path
// segment of a scheme-less SCP-like remote, e.g. "user@host:owner/repo",
// is a url.Parse error) or silently misparses (treating a bogus "user"
// prefix as a URL scheme when there is no "://", so u.User is never
// populated at all).
var leadingUserinfoPattern = regexp.MustCompile(`^((?:[A-Za-z][A-Za-z0-9+.-]*://)?)[^/]*@`)

// redactRemoteURLForError returns a version of raw safe to include in an
// error message: any embedded userinfo (which may carry a password or an
// access token) is stripped via leadingUserinfoPattern — a single
// string-level pass applied unconditionally, so it covers every remote
// shape this package parses or rejects (including ones url.Parse cannot
// cleanly round-trip) rather than depending on url.Parse succeeding first
// — control characters are removed, and the result is length-bounded via
// truncateUTF8 (shared with sanitizeMessage in api_errors.go — see that
// function's doc comment for why backtracking to the nearest rune-start
// byte is sufficient once control-character stripping has already run),
// so the bound never splits a multi-byte rune. It
// never panics and is used precisely on the rejection path, where the URL
// is by definition not one we trust.
func redactRemoteURLForError(raw string) string {
	s := leadingUserinfoPattern.ReplaceAllString(raw, "${1}[REDACTED]@")

	var b strings.Builder
	for _, r := range s {
		if !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	s = b.String()

	if len(s) > maxSanitizedRemoteEcho {
		s = truncateUTF8(s, remoteEchoTruncationBudget) + remoteEchoTruncationSuffix
	}
	return s
}
