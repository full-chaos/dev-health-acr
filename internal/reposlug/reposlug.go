// Package reposlug is the single definition of a valid owner or repository
// name part in an owner/repo slug. It is a dependency-free leaf so that
// internal/auth and every storage adapter (which internal/auth imports, so
// they cannot import it) agree on one grammar.
package reposlug

import (
	"regexp"
	"strings"
)

// NamePattern is the grammar of a repository name as one RE2 group, with no
// anchor, so a ClickHouse statement can apply the rule ValidName applies. It
// holds no backslash, quote or '?', so it reads the same as a SQL string
// literal. Its first branch is one alphanumeric character, which is how a lone
// '.' stays out; its second is 2-100 characters.
const NamePattern = `([a-z0-9]|[a-z0-9.][a-z0-9._-]{0,98}[a-z0-9])`

var (
	// ownerPattern: an alphanumeric first and last character, 1-100 characters.
	ownerPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,98}[a-z0-9])?$`)
	// namePattern: as ownerPattern, but a leading '.' is allowed. GitHub accepts
	// it for repository names (full-chaos/.github is the standard organization
	// configuration repository).
	namePattern = regexp.MustCompile(`^` + NamePattern + `$`)
)

// ValidOwner reports whether a lower-cased owner part is valid.
func ValidOwner(part string) bool { return ownerPattern.MatchString(part) }

// ValidName reports whether a lower-cased repository name part is valid. "."
// and ".." are refused by the pattern: its last character must be alphanumeric.
func ValidName(part string) bool { return namePattern.MatchString(part) }

// Fault names why a lower-cased owner/repo slug part is invalid, or "" when it
// is valid. owner selects the owner grammar.
func Fault(part string, owner bool) string {
	switch {
	case part == "":
		return "empty"
	case !owner && (part == "." || part == ".."):
		return "reserved path name"
	case len(part) > 100:
		return "longer than 100 characters"
	case owner && ValidOwner(part), !owner && ValidName(part):
		return ""
	default:
		return "must start with a letter or digit (a leading '.' is allowed for a repository name), end with a letter or digit, and contain only letters, digits, '.', '_' and '-'"
	}
}

// MaxSlugSegments and MaxSlugBytes bound a whole repository key: a GitLab path
// nests 20 groups deep and holds 255 characters.
const (
	MaxSlugSegments = 20
	MaxSlugBytes    = 255
)

// groupPattern is ownerPattern's grammar as one RE2 group, with no anchor and
// no 100-character bound: RE2 refuses a bounded repeat nested in a repeat past
// 1000 copies, so a statement tests each segment's length beside SlugPattern.
const groupPattern = `([a-z0-9]|[a-z0-9][a-z0-9._-]*[a-z0-9])`

// SlugPattern is the grammar of a whole repository key as one RE2 group, with
// no anchor and without the 255-byte cap and the 100-character segment bound,
// which a statement tests as lengths: 1-19 group segments, then a repository
// name. It reads the same as a SQL string literal.
const SlugPattern = groupPattern + `(/` + groupPattern + `){0,18}/` + NamePattern

// ValidSlug reports whether a lower-cased repository key is valid: 2-20
// segments in at most 255 bytes, every segment but the last a group name (no
// leading '.') and the last a repository name.
func ValidSlug(slug string) bool {
	if len(slug) > MaxSlugBytes {
		return false
	}
	parts := strings.Split(slug, "/")
	if len(parts) < 2 || len(parts) > MaxSlugSegments {
		return false
	}
	for _, group := range parts[:len(parts)-1] {
		if !ValidOwner(group) {
			return false
		}
	}
	return ValidName(parts[len(parts)-1])
}

// ValidScope reports whether a lower-cased repository scope is valid: "*",
// "<owner>/*" (one owner segment; the wildcard reaches every depth below it),
// or a repository key.
func ValidScope(scope string) bool {
	if scope == "*" {
		return true
	}
	if owner, ok := strings.CutSuffix(scope, "/*"); ok {
		return ValidOwner(owner)
	}
	return ValidSlug(scope)
}

// Lower lower-cases ASCII letters only, so a repository key folds the way the
// schema pattern reads it.
func Lower(s string) string { return strings.ToLower(s) }

// ValidSlugFolded reports whether a repository key is valid once ASCII case is
// folded: the form a request carries before it is normalised.
func ValidSlugFolded(slug string) bool { return ValidSlug(slug) }

// SchemaSlugPattern is the pattern every published schema holds for a
// repository key, and maxLength is MaxSlugBytes.
const SchemaSlugPattern = `^[^/\s]+(/[^/\s]+){1,19}$`
