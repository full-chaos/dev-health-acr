package v1

import "fmt"

// Validate enforces context_packet.v1.schema.json's "resolved_scope"
// object: repo_id/repo_slug/resolution are required, fallback_reasons must
// be present (a nil slice marshals to JSON null, which the schema's
// "type": "array" rejects) even when empty.
func (s ResolvedScope) Validate() error {
	if !stringLengthBetween(s.RepoID, 1, 128) {
		return fmt.Errorf("repo_id violates v1 bounds")
	}
	if !repositorySlugPattern.MatchString(s.RepoSlug) {
		return fmt.Errorf("repo_slug violates v1 bounds")
	}
	if !stringLengthBetween(s.Branch, 0, 512) || !stringLengthBetween(s.CommitSHA, 0, 64) {
		return fmt.Errorf("resolved_scope string violates v1 bounds")
	}
	if !validScopeResolution(s.Resolution) {
		return fmt.Errorf("resolution violates v1 bounds")
	}
	if s.FallbackReasons == nil {
		return fmt.Errorf("fallback_reasons is required")
	}
	for _, reason := range s.FallbackReasons {
		if !stringLengthBetween(reason, 0, 1000) {
			return fmt.Errorf("fallback_reasons violates v1 bounds")
		}
	}
	return nil
}

func validScopeResolution(resolution ScopeResolution) bool {
	switch resolution {
	case ScopeExactCommit, ScopeBranchFiltered, ScopeRepoFallback, ScopeUnresolved:
		return true
	default:
		return false
	}
}

func validateRequiredChecks(checks []RequiredCheck) error {
	if checks == nil || len(checks) > 100 {
		return fmt.Errorf("required_checks violates v1 bounds")
	}
	for i, check := range checks {
		if !stringLengthBetween(check.CheckID, 1, 256) || !stringLengthBetween(check.Label, 1, 1000) ||
			!stringLengthBetween(check.Reason, 1, 2000) || !stringLengthBetween(check.RuleID, 1, 256) {
			return fmt.Errorf("required_checks[%d] violates v1 bounds", i)
		}
	}
	return nil
}

func validateRecommendedSteps(steps []RecommendedStep) error {
	if steps == nil || len(steps) > 100 {
		return fmt.Errorf("recommended_next_steps violates v1 bounds")
	}
	for i, step := range steps {
		if !stringLengthBetween(step.StepID, 1, 256) || !stringLengthBetween(step.Label, 1, 1000) ||
			!stringLengthBetween(step.Reason, 1, 2000) || !stringLengthBetween(step.RuleID, 1, 256) {
			return fmt.Errorf("recommended_next_steps[%d] violates v1 bounds", i)
		}
	}
	return nil
}

// Validate enforces context_packet.v1.schema.json's "freshness" object:
// as_of is a required timestamp, stale_after_seconds must be
// non-negative, and watermarks must be present (possibly empty) with
// every entry independently valid.
func (f Freshness) Validate() error {
	if f.AsOf.IsZero() {
		return fmt.Errorf("as_of is required")
	}
	if f.StaleAfterSeconds < 0 {
		return fmt.Errorf("stale_after_seconds violates v1 bounds")
	}
	if f.Watermarks == nil {
		return fmt.Errorf("watermarks is required")
	}
	for i, watermark := range f.Watermarks {
		if err := watermark.Validate(); err != nil {
			return fmt.Errorf("watermarks[%d]: %w", i, err)
		}
	}
	return nil
}

func (w SourceWatermark) Validate() error {
	if !stringLengthBetween(w.Source, 1, 100) {
		return fmt.Errorf("source violates v1 bounds")
	}
	switch w.Status {
	case "fresh", "stale", "missing", "unavailable":
	default:
		return fmt.Errorf("status violates v1 bounds")
	}
	return nil
}

// Validate enforces context_packet.v1.schema.json's "coverage" object:
// every list field is required-present (a nil slice marshals to JSON
// null, which the schema's "type": "array" rejects), and the considered/
// available source lists must be internally unique.
func (c Coverage) Validate() error {
	if c.SourcesConsidered == nil || !uniqueStrings(c.SourcesConsidered) {
		return fmt.Errorf("sources_considered violates v1 bounds")
	}
	if c.SourcesAvailable == nil || !uniqueStrings(c.SourcesAvailable) {
		return fmt.Errorf("sources_available violates v1 bounds")
	}
	if c.SourcesUnavailable == nil {
		return fmt.Errorf("sources_unavailable is required")
	}
	for i, source := range c.SourcesUnavailable {
		if !stringLengthBetween(source.Reason, 0, 1000) {
			return fmt.Errorf("sources_unavailable[%d] violates v1 bounds", i)
		}
	}
	if c.DegradedReasons == nil {
		return fmt.Errorf("degraded_reasons is required")
	}
	for _, reason := range c.DegradedReasons {
		if !stringLengthBetween(reason, 0, 1000) {
			return fmt.Errorf("degraded_reasons violates v1 bounds")
		}
	}
	return nil
}

// Validate enforces context_packet.v1.schema.json's "budget" object: the
// three "max_*" fields have a schema minimum of 1, the three "used"/
// "estimated"/"serialized" counters have a schema minimum of 0.
func (b PacketBudget) Validate() error {
	if b.MaxItems < 1 || b.ItemsUsed < 0 || b.MaxOutputTokens < 1 || b.EstimatedTokens < 0 ||
		b.MaxSerializedBytes < 1 || b.SerializedBytes < 0 {
		return fmt.Errorf("budget violates v1 bounds")
	}
	return nil
}

// Validate enforces context_packet.v1.schema.json's "compatibility"
// object: both version strings are required and bounded,
// supported_schema_versions must be present (possibly empty) and unique.
func (c Compatibility) Validate() error {
	if !stringLengthBetween(c.ServiceVersion, 1, 200) || !stringLengthBetween(c.MinimumSidecarVersion, 1, 200) {
		return fmt.Errorf("compatibility version fields violate v1 bounds")
	}
	if c.SupportedSchemaVersions == nil || !uniqueStrings(c.SupportedSchemaVersions) {
		return fmt.Errorf("supported_schema_versions violates v1 bounds")
	}
	return nil
}
