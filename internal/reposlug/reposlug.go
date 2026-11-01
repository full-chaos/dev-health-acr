// Package reposlug is the single definition of a valid owner or repository
// name part in an owner/repo slug. It is a dependency-free leaf so that
// internal/auth and every storage adapter (which internal/auth imports, so
// they cannot import it) agree on one grammar.
package reposlug

import "regexp"

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
