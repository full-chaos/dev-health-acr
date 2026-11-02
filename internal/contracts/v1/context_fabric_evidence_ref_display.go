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
func evidenceIDDisplay(entityType ContextFabricEvidenceEntityType, id string, segments []string, name ContextFabricEvidenceSubjectName) string {
	named := func(kind ContextFabricSubjectKind, rawID string) string {
		if name == nil || strings.TrimSpace(rawID) == "" {
			return ""
		}
		if value, ok := name(kind, rawID); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	segment := func(i int) string {
		if i < len(segments) {
			return segments[i]
		}
		return ""
	}
	// A number is scoped by its repository (the leading "<uuid>:"): with the
	// repository's name in hand it reads "owner/repo#n", so two #869 of two
	// repositories in one answer differ; without it, "#n" (codex r3).
	repositoryNumber := func(repositoryID, number string) string {
		label := evidenceNumber(number)
		if label == "" {
			return label
		}
		if repository := named(ContextFabricSubjectRepository, repositoryID); repository != "" {
			return repository + label
		}
		return label
	}
	switch entityType {
	case ContextFabricEvidenceEntityPullRequest:
		head, rest, _ := strings.Cut(id, ":")
		if rest == "" {
			return evidenceNumber(id)
		}
		return repositoryNumber(head, withoutLeadingUUID(id))
	case ContextFabricEvidenceEntityCI, ContextFabricEvidenceEntityDeployment:
		return evidenceNotUUID(withoutLeadingUUID(id))
	case ContextFabricEvidenceEntityWorkItem:
		return evidenceKey(withoutLeadingUUID(id))
	case ContextFabricEvidenceEntityTeam:
		return evidenceKey(id)
	case ContextFabricEvidenceEntityProject:
		// A project's source id is no name a reader knows, UUID or not: without
		// a name the answer holds, the noun alone.
		return named(ContextFabricSubjectProject, id)
	case ContextFabricEvidenceEntityRepository:
		return named(ContextFabricSubjectRepository, id)
	case ContextFabricEvidenceEntityOrganization:
		return named(ContextFabricSubjectOrganization, id)
	case ContextFabricEvidenceEntityCommit, ContextFabricEvidenceEntityCommitFile,
		ContextFabricEvidenceEntityHotspot, ContextFabricEvidenceEntityComplexity,
		ContextFabricEvidenceEntityIncident:
		return evidenceNotUUID(id)
	case ContextFabricEvidenceEntityGraph, ContextFabricEvidenceEntityAIRun,
		ContextFabricEvidenceEntityAIArtifact, ContextFabricEvidenceEntityReviewOutcome,
		ContextFabricEvidenceEntityEpisode:
		// A row id, a relationship id, an internal run id or an episode id
		// minted inside ACR: nothing a reader names.
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
		return repositoryNumber(segment(0), segment(1))
	}
	// The retired un-encoded joins (a stored row from before their ".v2"
	// successors) cannot be split back into their parts: the noun alone.
	return ""
}

// ContextFabricEvidenceRefStoredLabel is a stored evidence label for a reader. A
// result saved before CHAOS-12620 keeps the "<noun>: <ref id>" label it was made
// with, and a by-id read serves it with no rebuild; a label that is exactly that
// old form reads as ContextFabricEvidenceRefLabelNamed. Any other stored label
// stands, so a label made after the change, with its name, is served as made.
func ContextFabricEvidenceRefStoredLabel(refID, stored string, name ContextFabricEvidenceSubjectName) string {
	if before, ok := evidenceRefLabelBefore12620(refID); ok && stored == before {
		if label, _ := ContextFabricEvidenceRefLabelNamed(refID, name); label != "" {
			return label
		}
	}
	return stored
}

// evidenceRefLabelBefore12620 is the label a known entity type's ref got before
// CHAOS-12620: its noun and the whole ref id, an encoded (".v2") id with its
// segments unescaped, bounded like every label. ok is false for an unknown type,
// whose generic fallback did not change.
func evidenceRefLabelBefore12620(refID string) (string, bool) {
	parts := strings.Split(refID, ":")
	if len(parts) < 4 || parts[0] != "acr" || parts[1] != "v1" {
		return "", false
	}
	entityType := ContextFabricEvidenceEntityType(parts[2])
	noun, known := contextFabricEvidenceEntityLabels[entityType]
	id := strings.Join(parts[3:], ":")
	if !known || id == "" {
		return "", false
	}
	if EncodedEvidenceEntityType(entityType) {
		id = decodedEvidenceID(id)
	}
	return clampLabel(noun + ": " + id), true
}
