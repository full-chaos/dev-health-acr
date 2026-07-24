package main

import "regexp"

// Redaction rules mirror redact_log in scripts/e2e/compose.sh so artifacts written by this
// tool never leak an ACR (fcacr_*) or Ops (svc_acr_*) credential, a Postgres/ClickHouse DSN,
// or an Authorization header value. Everything this tool writes to disk (assertion-report.json,
// junit.xml, fixture-verification.json, and any echoed probe SQL or event text) must pass
// through redact before it is serialized.
var (
	credentialPattern = regexp.MustCompile(`(?:fcacr|svc_acr)_[A-Za-z0-9_-]+`)
	dsnPattern        = regexp.MustCompile(`(?i)(?:postgres(?:ql)?|clickhouse)://[^\s"']+`)
	// Matches an Authorization header/field in either "Authorization: <value>" transport
	// form or a quoted JSON field "authorization":"<value>", capturing everything up to the
	// next whitespace, quote, or comma as the value to redact.
	authHeaderPattern = regexp.MustCompile(`(?i)("?authorization"?\s*[:=]\s*"?)([^\s"',}]+(?:\s+[^\s"',}]+)?)`)
)

// redact strips known-sensitive substrings from s and returns a sanitized copy. It is safe
// to call on arbitrary text, including multi-line log output and JSON-encoded strings.
func redact(s string) string {
	s = credentialPattern.ReplaceAllString(s, "REDACTED")
	s = dsnPattern.ReplaceAllString(s, "REDACTED_DSN")
	s = authHeaderPattern.ReplaceAllString(s, "${1}REDACTED")
	return s
}

// redactBytes is the []byte convenience form of redact.
func redactBytes(b []byte) []byte {
	return []byte(redact(string(b)))
}
