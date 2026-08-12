package v1

import (
	"fmt"
	"math"
	"strings"
)

func (v ContextFabricScalarValue) Validate() error {
	set := 0
	if v.String != nil {
		set++
		if !stringLengthBetween(*v.String, 0, 4000) {
			return fmt.Errorf("scalar string violates v1 bounds")
		}
	}
	if v.Integer != nil {
		set++
	}
	if v.Number != nil {
		set++
		if math.IsNaN(*v.Number) || math.IsInf(*v.Number, 0) {
			return fmt.Errorf("scalar number must be finite")
		}
	}
	if v.Boolean != nil {
		set++
	}
	if v.Null {
		set++
	}
	if set != 1 {
		return fmt.Errorf("scalar value must contain exactly one value")
	}
	return nil
}

func (b ContextFabricProjectionBatch) Validate() error {
	if b.SchemaVersion != ContextFabricProjectionBatchSchema || !stringLengthBetween(b.BatchID, 8, 256) || !stringLengthBetween(b.OrgID, 1, 256) || !stringLengthBetween(b.Source, 1, 128) || !stringLengthBetween(b.SourceVersion, 1, 256) || !stringLengthBetween(b.Cursor, 0, 512) || !stringLengthBetween(b.NextCursor, 0, 512) || b.GeneratedAt.IsZero() || b.Entities == nil || b.Relationships == nil || b.Contents == nil || b.Episodes == nil || b.Tombstones == nil {
		return fmt.Errorf("projection batch identity or arrays violate v1 bounds")
	}
	if b.FullSnapshot && !b.CompleteEnumeration {
		return fmt.Errorf("full snapshot requires a complete enumeration proof")
	}
	if len(b.Entities)+len(b.Relationships)+len(b.Contents)+len(b.Episodes)+len(b.Tombstones) == 0 {
		return fmt.Errorf("projection batch is empty")
	}
	if len(b.Entities) > 1000 || len(b.Relationships) > 5000 || len(b.Contents) > 1000 || len(b.Episodes) > 1000 || len(b.Tombstones) > 5000 {
		return fmt.Errorf("projection batch exceeds v1 item bounds")
	}
	for _, entity := range b.Entities {
		if err := entity.Validate(); err != nil {
			return fmt.Errorf("entities: %w", err)
		}
	}
	for _, relationship := range b.Relationships {
		if err := relationship.Validate(); err != nil {
			return fmt.Errorf("relationships: %w", err)
		}
	}
	for _, content := range b.Contents {
		if err := content.Validate(); err != nil {
			return fmt.Errorf("contents: %w", err)
		}
	}
	for _, episode := range b.Episodes {
		if err := episode.Validate(); err != nil {
			return fmt.Errorf("episodes: %w", err)
		}
	}
	for _, tombstone := range b.Tombstones {
		if err := tombstone.Validate(); err != nil {
			return fmt.Errorf("tombstones: %w", err)
		}
	}
	return nil
}

func (s ContextFabricAuthorizationScope) Validate() error {
	if len(s.RepositorySlugs) > 200 || len(s.ProjectIDs) > 200 || len(s.TeamIDs) > 200 || !uniqueTrimmedStrings(s.RepositorySlugs, 512) || !uniqueTrimmedStrings(s.ProjectIDs, 256) || !uniqueTrimmedStrings(s.TeamIDs, 256) {
		return fmt.Errorf("authorization scope violates v1 bounds")
	}
	if len(s.RepositorySlugs)+len(s.ProjectIDs)+len(s.TeamIDs) == 0 {
		return fmt.Errorf("authorization scope must not be empty")
	}
	// Backends that persist a scope as a delimited string (e.g. the zepgraph
	// adapter's "|a|b|" encoding) use '|' as their internal separator. A
	// scope value containing '|' would corrupt that encoding into multiple
	// unintended scope entries, so it must fail here, at the port, before
	// any backend ever sees it.
	if containsSeparatorCharacter(s.RepositorySlugs) || containsSeparatorCharacter(s.ProjectIDs) || containsSeparatorCharacter(s.TeamIDs) {
		return fmt.Errorf("authorization scope value must not contain '|'")
	}
	return nil
}

func (e ContextFabricEntityProjection) Validate() error {
	if err := e.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if len(e.Aliases) > 100 || len(e.PreviousNames) > 100 || !uniqueTrimmedStrings(e.Aliases, 512) || !uniqueTrimmedStrings(e.PreviousNames, 512) || len(e.ProviderIDs) > 50 || len(e.Properties) > 100 || !boundedEvidenceRefs(e.EvidenceRefIDs, 500, false) || e.ObservedAt.IsZero() || !validVersion(e.SourceVersion) {
		return fmt.Errorf("entity projection violates v1 bounds")
	}
	if err := e.Authorization.Validate(); err != nil {
		return fmt.Errorf("authorization: %w", err)
	}
	for provider, id := range e.ProviderIDs {
		if !stringLengthBetween(provider, 1, 128) || !stringLengthBetween(id, 1, 512) || strings.TrimSpace(provider) != provider || strings.TrimSpace(id) != id {
			return fmt.Errorf("provider identity violates v1 bounds")
		}
	}
	if err := validateScalarMap(e.Properties); err != nil {
		return fmt.Errorf("properties: %w", err)
	}
	return validateTimeRange(&e.ObservedAt, e.ValidFrom, e.ValidTo)
}

func (r ContextFabricRelationshipProjection) Validate() error {
	if !stringLengthBetween(r.RelationshipID, 8, 256) || !stringLengthBetween(strings.TrimSpace(r.Type), 1, 128) || len(r.Properties) > 100 || !validDerivationMethod(r.Derivation) || !validEpistemicStatus(r.EpistemicStatus) || !boundedEvidenceRefs(r.EvidenceRefIDs, 500, false) || r.ObservedAt.IsZero() || !validVersion(r.SourceVersion) {
		return fmt.Errorf("relationship projection violates v1 bounds")
	}
	if err := r.From.Validate(); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	if err := r.To.Validate(); err != nil {
		return fmt.Errorf("to: %w", err)
	}
	if r.From == r.To {
		return fmt.Errorf("relationship cannot be self-referential")
	}
	if err := r.Authorization.Validate(); err != nil {
		return fmt.Errorf("authorization: %w", err)
	}
	if err := validateScalarMap(r.Properties); err != nil {
		return fmt.Errorf("properties: %w", err)
	}
	return validateTimeRange(&r.ObservedAt, r.ValidFrom, r.ValidTo)
}

func (c ContextFabricContentProjection) Validate() error {
	if !stringLengthBetween(c.ContentID, 8, 256) || c.Subject.Validate() != nil || !stringLengthBetween(strings.TrimSpace(c.Title), 1, 1024) || !stringLengthBetween(c.Body, 0, 100000) || !stringLengthBetween(c.ContentDigest, 8, 256) || !boundedEvidenceRefs(c.EvidenceRefIDs, 500, false) || c.ObservedAt.IsZero() || !validVersion(c.SourceVersion) || !c.Untrusted {
		return fmt.Errorf("content projection violates v1 bounds or untrusted-content requirement")
	}
	return c.Authorization.Validate()
}

func (e ContextFabricEpisodeProjection) Validate() error {
	if !stringLengthBetween(e.EpisodeID, 8, 256) || e.Subject.Validate() != nil || !stringLengthBetween(strings.TrimSpace(e.Goal), 1, 4000) || !stringLengthBetween(strings.TrimSpace(e.Outcome), 1, 128) || !stringLengthBetween(strings.TrimSpace(e.Summary), 1, 8000) || !boundedEvidenceRefs(e.EvidenceRefIDs, 500, false) || e.StartedAt.IsZero() || e.EndedAt.IsZero() || e.EndedAt.Before(e.StartedAt) || !validVersion(e.SourceVersion) {
		return fmt.Errorf("episode projection violates v1 bounds")
	}
	return e.Authorization.Validate()
}

func (t ContextFabricProjectionTombstone) Validate() error {
	if !stringLengthBetween(strings.TrimSpace(t.Kind), 1, 64) || !stringLengthBetween(t.CanonicalID, 1, 256) || !stringLengthBetween(strings.TrimSpace(t.Reason), 1, 2000) || t.EffectiveAt.IsZero() || !validVersion(t.SourceVersion) {
		return fmt.Errorf("projection tombstone violates v1 bounds")
	}
	return nil
}

func (c ContextFabricCapabilities) Validate() error {
	if !c.Enabled {
		if c.SupportsOpenQuestions || c.SupportsPriorSubjectReceipts || len(c.SupportedRequestSchemaVersions) != 0 || len(c.SupportedResultSchemaVersions) != 0 {
			return fmt.Errorf("disabled context fabric capabilities must not advertise active features")
		}
		return nil
	}
	if !c.SupportsOpenQuestions || c.SupportedRequestSchemaVersions == nil || !uniqueStrings(c.SupportedRequestSchemaVersions) || c.SupportedResultSchemaVersions == nil || !uniqueStrings(c.SupportedResultSchemaVersions) {
		return fmt.Errorf("context fabric capabilities violate v1 bounds")
	}
	if err := c.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	return nil
}

func (l ContextFabricCapabilityLimits) Validate() error {
	return (ContextFabricInvestigationOptions{
		MaxSubjectCandidates: l.MaxSubjectCandidates,
		MaxCohortMembers:     l.MaxCohortMembers,
		MaxRelationshipPaths: l.MaxRelationshipPaths,
		MaxDrivers:           l.MaxDrivers,
		MaxEvidenceRefs:      l.MaxEvidenceRefs,
		MaxSerializedBytes:   l.MaxSerializedBytes,
	}).Validate()
}
