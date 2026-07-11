package api

import "strings"

type semanticVersion struct {
	core       [3]string
	prerelease []string
}

func clientVersionCompatible(value, minimum string) bool {
	client, clientOK := parseSemanticVersion(value)
	required, requiredOK := parseSemanticVersion(minimum)
	return clientOK && requiredOK && compareSemanticVersions(client, required) >= 0
}

func parseSemanticVersion(value string) (semanticVersion, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" || strings.Count(value, "+") > 1 {
		return semanticVersion{}, false
	}
	coreAndPrerelease, build, hasBuild := strings.Cut(value, "+")
	if hasBuild && !validSemanticIdentifiers(build, false) {
		return semanticVersion{}, false
	}
	core, prerelease, hasPrerelease := strings.Cut(coreAndPrerelease, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	parsed := semanticVersion{}
	for index, part := range parts {
		if !validSemanticNumber(part) {
			return semanticVersion{}, false
		}
		parsed.core[index] = part
	}
	if hasPrerelease {
		if !validSemanticIdentifiers(prerelease, true) {
			return semanticVersion{}, false
		}
		parsed.prerelease = strings.Split(prerelease, ".")
	}
	return parsed, true
}

func compareSemanticVersions(left, right semanticVersion) int {
	for index := range left.core {
		if comparison := compareNumericIdentifier(left.core[index], right.core[index]); comparison != 0 {
			return comparison
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
		if comparison := comparePrereleaseIdentifier(left.prerelease[index], right.prerelease[index]); comparison != 0 {
			return comparison
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

func comparePrereleaseIdentifier(left, right string) int {
	leftNumeric, rightNumeric := semanticDigits(left), semanticDigits(right)
	switch {
	case leftNumeric && rightNumeric:
		return compareNumericIdentifier(left, right)
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

func compareNumericIdentifier(left, right string) int {
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

func validSemanticNumber(value string) bool {
	return semanticDigits(value) && (len(value) == 1 || value[0] != '0')
}

func validSemanticIdentifiers(value string, prerelease bool) bool {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" || prerelease && semanticDigits(identifier) && len(identifier) > 1 && identifier[0] == '0' {
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

func semanticDigits(value string) bool {
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
