package v1

import (
	"regexp"
	"strings"
)

var contextFabricDegradedWithoutLimitationPattern = regexp.MustCompile(`^This answer is (?:partial|degraded) and the service recorded its cause: .{1,200}\.$`)

// ContextFabricDegradedWithoutLimitation is the safety-net sentence served
// when a partial or degraded answer reached the end of assembly with no
// limitation of its own. reason is a fixed coverage label.
func ContextFabricDegradedWithoutLimitation(status, reason string) string {
	reason = strings.TrimRight(strings.TrimSpace(reason), ".")
	if (status != "partial" && status != "degraded") || reason == "" || len(reason) > 200 {
		return ""
	}
	return "This answer is " + status + " and the service recorded its cause: " + reason + "."
}

// IsContextFabricDegradedWithoutLimitation reports whether one limitation is a
// sentence ContextFabricDegradedWithoutLimitation generates.
func IsContextFabricDegradedWithoutLimitation(limitation string) bool {
	return contextFabricDegradedWithoutLimitationPattern.MatchString(limitation)
}
