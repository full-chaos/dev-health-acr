package v1

import (
	"net/url"
	"regexp"
	"unicode/utf8"
)

var repositorySlugPattern = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)
var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
var clientCredentialTokenPrefixPattern = regexp.MustCompile(`^fcacr_[A-Za-z0-9_-]{4,32}$`)

func stringLengthBetween(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
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
