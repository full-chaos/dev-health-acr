package v1

import (
	"net/url"
	"regexp"
	"unicode/utf8"
)

var repositorySlugPattern = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)
var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
var clientCredentialTokenPrefixPattern = regexp.MustCompile(`^fcacr_[A-Za-z0-9_-]{4,32}$`)

// matchedTermHashPattern is ContextFabricAnchorOption.MatchedTermHash's own
// format: 24 lowercase hex characters, matching HashAliasTerm's own
// hex.EncodeToString output exactly (internal/contextfabric/graphrank's
// HashAliasTerm, the ONE place that mints this value). Codex xhigh review
// (chaos-pivot-p1, first round), finding 5: length alone let a
// non-hexadecimal 24-character string persist as a "valid" hash that could
// only ever fail later, at redemption time, instead of at the point it was
// minted -- enforce the documented digest shape here, the same way
// receipt_id already enforces its own namespace prefix.
var matchedTermHashPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

func stringLengthBetween(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}

// optionalStringBetween validates a field that is either absent (empty
// string, matching its `omitempty` wire encoding) or present and within
// [minimum, maximum] -- the same "empty is fine, otherwise bounded" shape
// optionalURI below already establishes, factored out for plain string
// fields. Codex xhigh review (chaos-pivot-p1, first round), finding 4:
// prior_version_id/prior_entry_id are optional-but-bounded on the JSON
// Schema (minLength 1, maxLength 256 when present) across every structure
// offer/confirmed-entry/offer-snapshot type; Go validation had no bound
// for either field at all.
func optionalStringBetween(value string, minimum, maximum int) bool {
	if value == "" {
		return true
	}
	return stringLengthBetween(value, minimum, maximum)
}

func optionalURI(value string, maximum int) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && stringLengthBetween(value, 0, maximum)
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
