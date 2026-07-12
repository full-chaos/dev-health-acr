package version

import "strings"

type semanticVersion struct {
	core       [3]string
	prerelease []string
}

// AtLeast reports whether have has SemVer precedence greater than or equal to
// want. Both values must be valid SemVer; malformed values and dev fail
// closed. Build metadata is parsed but does not affect precedence.
func AtLeast(have, want string) bool {
	parsedHave, haveOK := parse(have)
	parsedWant, wantOK := parse(want)
	return haveOK && wantOK && compare(parsedHave, parsedWant) >= 0
}

// IsValid reports whether value is SemVer 2.0.0, accepting one leading v for
// compatibility with existing callers.
func IsValid(value string) bool {
	_, ok := parse(value)
	return ok
}

// IsCanonical reports whether value is SemVer 2.0.0 without a leading v.
func IsCanonical(value string) bool {
	return value == strings.TrimSpace(value) && !strings.HasPrefix(value, "v") && IsValid(value)
}

// Exact reports whether two valid versions identify the same release build,
// including build metadata. A leading v is normalized for legacy callers.
func Exact(left, right string) bool {
	return IsValid(left) && IsValid(right) && normalize(left) == normalize(right)
}

func normalize(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func parse(value string) (semanticVersion, bool) {
	value = normalize(value)
	if value == "" || strings.Count(value, "+") > 1 {
		return semanticVersion{}, false
	}
	coreAndPrerelease, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && !validIdentifiers(build, false) {
		return semanticVersion{}, false
	}
	core, prerelease, hasPrerelease := strings.Cut(coreAndPrerelease, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	parsed := semanticVersion{}
	for index, part := range parts {
		if !validNumber(part) {
			return semanticVersion{}, false
		}
		parsed.core[index] = part
	}
	if hasPrerelease {
		if !validIdentifiers(prerelease, true) {
			return semanticVersion{}, false
		}
		parsed.prerelease = strings.Split(prerelease, ".")
	}
	return parsed, true
}

func compare(left, right semanticVersion) int {
	for index := range left.core {
		if result := compareNumeric(left.core[index], right.core[index]); result != 0 {
			return result
		}
	}
	if len(left.prerelease) == 0 || len(right.prerelease) == 0 {
		switch {
		case len(left.prerelease) == len(right.prerelease):
			return 0
		case len(left.prerelease) == 0:
			return 1
		default:
			return -1
		}
	}
	for index := 0; index < min(len(left.prerelease), len(right.prerelease)); index++ {
		if result := comparePrerelease(left.prerelease[index], right.prerelease[index]); result != 0 {
			return result
		}
	}
	switch {
	case len(left.prerelease) < len(right.prerelease):
		return -1
	case len(left.prerelease) > len(right.prerelease):
		return 1
	default:
		return 0
	}
}

func comparePrerelease(left, right string) int {
	leftNumeric, rightNumeric := digits(left), digits(right)
	switch {
	case leftNumeric && rightNumeric:
		return compareNumeric(left, right)
	case leftNumeric:
		return -1
	case rightNumeric:
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareNumeric(left, right string) int {
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func validNumber(value string) bool {
	return digits(value) && (len(value) == 1 || value[0] != '0')
}

func validIdentifiers(value string, prerelease bool) bool {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" || prerelease && digits(identifier) && len(identifier) > 1 && identifier[0] == '0' {
			return false
		}
		for _, character := range identifier {
			if !(character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '-') {
				return false
			}
		}
	}
	return true
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
