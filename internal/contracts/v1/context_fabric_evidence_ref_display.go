package v1

import (
	"regexp"
	"strings"
)

// CHAOS-12620: an evidence ref's label used to be "<noun>: <ref id>", so the raw
// parts of the id reached a reader: a repository or organization UUID, a work item's
// provider id (linear:CHAOS-2311), a team's raw key (linear:CHAOS), a relation key
// and its direction. The label now carries the id only in the form a reader knows,
// and the ref id itself stays in evidence_ref_ids for the collapsed Details
// (CHAOS-4690). Display only: a label is never parsed or compared.

var evidenceUUIDForm = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// evidenceProviderToken is the provider part of a raw source id, any lower-case
// token before the first ':'. No provider is named.
var evidenceProviderToken = regexp.MustCompile(`^[a-z][a-z0-9._-]*:`)

// evidenceKey is a raw source id as a reader names it: the provider token dropped
// (linear:CHAOS-2311 reads CHAOS-2311, gh:owner/repo#7 reads owner/repo#7). A UUID
// names nothing a reader knows and reads as "".
func evidenceKey(raw string) string {
	key := evidenceProviderToken.ReplaceAllString(strings.TrimSpace(raw), "")
	if evidenceUUIDForm.MatchString(key) {
		return ""
	}
	return key
}

// withoutLeadingUUID drops a "<uuid>:" scope a producer put before the id (the
// row's repository, or the zero repository of a repository-less work item).
func withoutLeadingUUID(id string) string {
	if head, rest, found := strings.Cut(id, ":"); found && evidenceUUIDForm.MatchString(head) {
		return rest
	}
	return id
}

func evidenceNumber(value string) string {
	if value = strings.TrimSpace(value); value == "" {
		return ""
	}
	return "#" + value
}

func evidenceJoin(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, ", ")
}

func evidenceNotUUID(value string) string {
	if evidenceUUIDForm.MatchString(strings.TrimSpace(value)) {
		return ""
	}
	return value
}

// evidenceIDDisplay is the part of an evidence label after the noun, or "" for the
// noun alone. segments are an encoded (".v2") id's decoded segments, nil for any
// other type.
func evidenceIDDisplay(entityType ContextFabricEvidenceEntityType, id string, segments []string) string {
	segment := func(i int) string {
		if i < len(segments) {
			return segments[i]
		}
		return ""
	}
	switch entityType {
	case ContextFabricEvidenceEntityPullRequest:
		return evidenceNumber(withoutLeadingUUID(id))
	case ContextFabricEvidenceEntityCI, ContextFabricEvidenceEntityDeployment:
		return evidenceNotUUID(withoutLeadingUUID(id))
	case ContextFabricEvidenceEntityWorkItem:
		return evidenceKey(withoutLeadingUUID(id))
	case ContextFabricEvidenceEntityTeam, ContextFabricEvidenceEntityProject:
		return evidenceKey(id)
	case ContextFabricEvidenceEntityCommit, ContextFabricEvidenceEntityCommitFile,
		ContextFabricEvidenceEntityHotspot, ContextFabricEvidenceEntityComplexity,
		ContextFabricEvidenceEntityIncident, ContextFabricEvidenceEntityEpisode:
		return evidenceNotUUID(id)
	case ContextFabricEvidenceEntityRepository, ContextFabricEvidenceEntityOrganization,
		ContextFabricEvidenceEntityGraph, ContextFabricEvidenceEntityAIRun,
		ContextFabricEvidenceEntityAIArtifact, ContextFabricEvidenceEntityReviewOutcome:
		// A row id, a relationship id or an internal run id: nothing a reader names.
		return ""
	case ContextFabricEvidenceEntityWorkItemDependencyV2:
		return evidenceJoin(evidenceKey(segment(0)), evidenceKey(segment(1)))
	case ContextFabricEvidenceEntityWorkItemHierarchyV2:
		return evidenceJoin(evidenceKey(segment(1)), evidenceKey(segment(2)))
	case ContextFabricEvidenceEntityWorkItemTeamV2:
		return evidenceJoin(evidenceKey(segment(1)), evidenceKey(segment(2)))
	case ContextFabricEvidenceEntityDeploymentIncidentV2:
		return evidenceJoin(evidenceNotUUID(segment(1)), evidenceNotUUID(segment(2)))
	case ContextFabricEvidenceEntityProjectTeamV2:
		return evidenceJoin(evidenceKey(segment(1)), evidenceKey(segment(2)))
	case ContextFabricEvidenceEntityReviewV2:
		return evidenceNumber(segment(1))
	}
	// The retired un-encoded joins (a stored row from before their ".v2"
	// successors) cannot be split back into their parts: the noun alone.
	return ""
}
