package v1

import "strings"

// ContextFabricNoCoverageCauseReason is the reason a status-absence or
// degraded-fallback sentence gives when no coverage cause was recorded.
const ContextFabricNoCoverageCauseReason = "no coverage cause was recorded"

// ContextFabricCoverageCauseReason is the closed reason phrase for one
// coverage detail code, or empty for a value outside the registry.
func ContextFabricCoverageCauseReason(code ContextFabricCoverageDetailCode) string {
	if !validCoverageDetailCode(code) {
		return ""
	}
	return "the service recorded " + strings.ReplaceAll(string(code), "_", " ")
}

func contextFabricCoverageCauseReasons() []string {
	reasons := []string{ContextFabricNoCoverageCauseReason}
	for _, code := range ContextFabricCoverageDetailCodeVocabulary() {
		reasons = append(reasons, ContextFabricCoverageCauseReason(code))
	}
	return reasons
}

func containsReason(reason string) bool {
	for _, candidate := range contextFabricCoverageCauseReasons() {
		if candidate == reason {
			return true
		}
	}
	return false
}

// ContextFabricDegradedWithoutLimitation is the safety-net sentence served
// when a partial or degraded answer reached the end of assembly with no
// limitation of its own. reason is from the closed reason set.
func ContextFabricDegradedWithoutLimitation(status, reason string) string {
	if (status != "partial" && status != "degraded") || !containsReason(reason) {
		return ""
	}
	return "This answer is " + status + " and the service recorded its cause: " + reason + "."
}

// IsContextFabricDegradedWithoutLimitation reports whether one limitation is
// exactly a sentence ContextFabricDegradedWithoutLimitation generates.
func IsContextFabricDegradedWithoutLimitation(limitation string) bool {
	for _, status := range []string{"partial", "degraded"} {
		for _, reason := range contextFabricCoverageCauseReasons() {
			if limitation == ContextFabricDegradedWithoutLimitation(status, reason) {
				return true
			}
		}
	}
	return false
}
