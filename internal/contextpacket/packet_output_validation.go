package contextpacket

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func validatePacketOutputBounds(packet contractsv1.ContextPacket) error {
	if !packetString(packet.ContextPacketID, 8, 256) || !packetString(packet.RequestID, 8, 256) || !packetString(packet.Goal, 1, 4000) || !packetString(packet.QueryVersion, 1, 200) || !packetString(packet.RankingVersion, 1, 200) || !packetString(packet.Summary, 0, 4000) {
		return fmt.Errorf("context packet string violates v1 bounds")
	}
	if !validPacketArrays(packet) || len(packet.Items) > 50 || len(packet.RequiredChecks) > 100 || len(packet.RecommendedNextSteps) > 100 {
		return fmt.Errorf("context packet collection violates v1 bounds")
	}
	if !validPacketStatus(packet.Status) || !validRepository(packet.Repository) || !validRequestedScope(packet.RequestedScope) || !validResolvedScope(packet.ResolvedScope) || !validActions(packet.RequiredChecks, packet.RecommendedNextSteps) || !validCoverage(packet.Coverage) || !validFreshness(packet.Freshness) || !validBudget(packet.Budget) || !validCompatibility(packet.Compatibility) {
		return fmt.Errorf("context packet metadata violates v1 bounds")
	}
	for _, warning := range packet.Warnings {
		if !packetString(warning, 0, 2000) {
			return fmt.Errorf("context packet warning violates v1 bounds")
		}
	}
	return nil
}

func validPacketArrays(packet contractsv1.ContextPacket) bool {
	return packet.Items != nil && packet.RequiredChecks != nil && packet.RecommendedNextSteps != nil && packet.Freshness.Watermarks != nil && packet.Coverage.SourcesConsidered != nil && packet.Coverage.SourcesAvailable != nil && packet.Coverage.SourcesUnavailable != nil && packet.Coverage.DegradedReasons != nil && packet.Warnings != nil && packet.ResolvedScope.FallbackReasons != nil && packet.Compatibility.SupportedSchemaVersions != nil
}

func validPacketStatus(status contractsv1.PacketStatus) bool {
	switch status {
	case contractsv1.PacketComplete, contractsv1.PacketPartial, contractsv1.PacketDegraded, contractsv1.PacketEmpty:
		return true
	default:
		return false
	}
}

func validRepository(repository contractsv1.RepositoryRef) bool {
	return validSlug(repository.Slug) && packetString(repository.RepoID, 0, 128) && optionalPacketURI(repository.RemoteURL, 2048)
}

func validRequestedScope(scope contractsv1.RequestedScope) bool {
	if !packetString(scope.Branch, 0, 512) || !packetString(scope.TaskRef, 0, 1024) || !packetString(scope.CommitSHA, 0, 64) {
		return false
	}
	if scope.CommitSHA != "" && !hexString(scope.CommitSHA, 7, 64) {
		return false
	}
	if len(scope.Files) > 200 || !uniquePacketStrings(scope.Files) {
		return false
	}
	for _, file := range scope.Files {
		if !packetString(file, 1, 2048) {
			return false
		}
	}
	return scope.TimeWindowDays == 0 || (scope.TimeWindowDays >= 1 && scope.TimeWindowDays <= 365)
}

func validResolvedScope(scope contractsv1.ResolvedScope) bool {
	if !packetString(scope.RepoID, 1, 128) || !validSlug(scope.RepoSlug) || !packetString(scope.Branch, 0, 512) || !packetString(scope.CommitSHA, 0, 64) || scope.FallbackReasons == nil {
		return false
	}
	for _, reason := range scope.FallbackReasons {
		if !packetString(reason, 0, 1000) {
			return false
		}
	}
	switch scope.Resolution {
	case contractsv1.ScopeExactCommit, contractsv1.ScopeBranchFiltered, contractsv1.ScopeRepoFallback, contractsv1.ScopeUnresolved:
		return true
	default:
		return false
	}
}

func validActions(checks []contractsv1.RequiredCheck, steps []contractsv1.RecommendedStep) bool {
	for _, check := range checks {
		if !packetString(check.CheckID, 1, 256) || !packetString(check.Label, 1, 1000) || !packetString(check.Reason, 1, 2000) || !packetString(check.RuleID, 1, 256) {
			return false
		}
	}
	for _, step := range steps {
		if !packetString(step.StepID, 1, 256) || !packetString(step.Label, 1, 1000) || !packetString(step.Reason, 1, 2000) || !packetString(step.RuleID, 1, 256) {
			return false
		}
	}
	return true
}

func validCoverage(coverage contractsv1.Coverage) bool {
	if !uniquePacketStrings(coverage.SourcesConsidered) || !uniquePacketStrings(coverage.SourcesAvailable) {
		return false
	}
	for _, unavailable := range coverage.SourcesUnavailable {
		if !packetString(unavailable.Source, 0, 0) || !packetString(unavailable.Reason, 0, 1000) {
			return false
		}
	}
	for _, reason := range coverage.DegradedReasons {
		if !packetString(reason, 0, 1000) {
			return false
		}
	}
	return true
}

func validFreshness(freshness contractsv1.Freshness) bool {
	for _, watermark := range freshness.Watermarks {
		if !packetString(watermark.Source, 1, 100) {
			return false
		}
		switch watermark.Status {
		case "fresh", "stale", "missing", "unavailable":
		default:
			return false
		}
	}
	return true
}

func validCompatibility(compatibility contractsv1.Compatibility) bool {
	return packetString(compatibility.ServiceVersion, 1, 200) && packetString(compatibility.MinimumSidecarVersion, 1, 200) && uniquePacketStrings(compatibility.SupportedSchemaVersions)
}

func validBudget(budget contractsv1.PacketBudget) bool {
	return budget.MaxItems >= 1 && budget.ItemsUsed >= 0 && budget.MaxOutputTokens >= 1 && budget.EstimatedTokens >= 0 && budget.MaxSerializedBytes >= 1 && budget.SerializedBytes >= 0
}

func packetString(value string, minimum, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return length >= minimum && (maximum == 0 || length <= maximum)
}

func optionalPacketURI(value string, maximum int) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && packetString(value, 0, maximum)
}

func validSlug(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(value, " \t\n\r") && packetString(value, 0, 512)
}

func hexString(value string, minimum, maximum int) bool {
	if !packetString(value, minimum, maximum) {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func uniquePacketStrings(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
