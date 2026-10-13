package contextpacket

import (
	"regexp"
	"strings"
	"unicode"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var failureTokens = map[string]bool{"failed": true, "failure": true, "error": true, "errored": true, "timed_out": true}

var closedIncidentTokens = map[string]bool{"resolved": true, "closed": true, "done": true, "cancelled": true, "canceled": true, "mitigated": true, "completed": true}

func citationTokens(citation string) []string {
	return strings.FieldsFunc(strings.ToLower(citation), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
}

// citationShowsFailure reads only the persisted status the source row carries.
func citationShowsFailure(citation string) bool {
	for _, token := range citationTokens(citation) {
		if failureTokens[token] {
			return true
		}
	}
	return false
}

// incidentIsOpen is true only for a stored, non-empty status that is not a
// closed one; an empty status is unknown, not open.
func incidentIsOpen(citation string) bool {
	rest, found := strings.CutPrefix(citation, "status=")
	if !found {
		return false
	}
	status, _, _ := strings.Cut(rest, ",")
	status = strings.ToLower(strings.TrimSpace(status))
	return status != "" && !closedIncidentTokens[status] && status != "unknown"
}

var revertedCommitPattern = regexp.MustCompile(`(?i)\breverts? commit ([0-9a-f]{7,64})\b`)

func releaseRefOf(citation string) string {
	index := strings.Index(citation, "release_ref=")
	if index < 0 {
		return ""
	}
	value := citation[index+len("release_ref="):]
	value, _, _ = strings.Cut(value, ",")
	return strings.TrimSpace(value)
}

// linkedCauseRefs returns the evidence ids of commits tied to a failure by a
// persisted link inside the same evidence set: a failed or incident-linked
// deployment whose release_ref is the commit, or a later commit whose message
// reverts it. Time order alone never makes a cause.
func linkedCauseRefs(evidence []contractsv1.EvidenceRef) map[string]bool {
	incidentLinked := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "deployment_incident_edge" {
			continue
		}
		if deploymentID, _, found := strings.Cut(ref.Source.DisplayLabel, " linked to "); found && deploymentID != "" {
			incidentLinked[deploymentID] = true
		}
	}
	carried := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "deployment" || ref.Confidence < minimumEvidenceConfidence {
			continue
		}
		if !citationShowsFailure(ref.Citation) && !incidentLinked[ref.Source.EntityID] {
			continue
		}
		if sha := strings.ToLower(releaseRefOf(ref.Citation)); sha != "" {
			carried[sha] = true
		}
	}
	reverted := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "commit" {
			continue
		}
		for _, match := range revertedCommitPattern.FindAllStringSubmatch(ref.Citation, -1) {
			reverted[strings.ToLower(match[1])] = true
		}
	}
	causes := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "commit" {
			continue
		}
		sha := strings.ToLower(ref.Source.EntityID)
		if carried[sha] {
			causes[ref.EvidenceRefID] = true
			continue
		}
		for prefix := range reverted {
			if strings.HasPrefix(sha, prefix) {
				causes[ref.EvidenceRefID] = true
				break
			}
		}
	}
	return causes
}

type roleResolver map[string]rule

func newRoleResolver(evidence []contractsv1.EvidenceRef) roleResolver {
	resolver := roleResolver{}
	for id := range linkedCauseRefs(evidence) {
		resolver[id] = causeRule()
	}
	for id := range activeBlockerRefs(evidence) {
		resolver[id] = pressureRule()
	}
	return resolver
}

var closedWorkItemStatuses = map[string]bool{"done": true, "canceled": true, "cancelled": true, "closed": true, "resolved": true}

// activeBlockerRefs returns the dependency rows that state a blocker whose
// own stored work item is in the same evidence set with a non-closed status.
// An unknown blocker (not in the set, empty status) is never pressure.
func activeBlockerRefs(evidence []contractsv1.EvidenceRef) map[string]bool {
	status := map[string]string{}
	for _, ref := range evidence {
		if ref.Source.EntityType == "work_item" {
			status[ref.Source.EntityID] = strings.ToLower(strings.TrimSpace(ref.Citation))
		}
	}
	active := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "work_item_dependency" {
			continue
		}
		parts := strings.Fields(ref.Source.DisplayLabel)
		if len(parts) != 3 {
			continue
		}
		var blocker string
		switch strings.ToLower(parts[1]) {
		case "blocks":
			blocker = parts[0]
		case "blocked_by", "is_blocked_by":
			blocker = parts[2]
		default:
			continue
		}
		if current, known := status[blocker]; known && current != "" && !closedWorkItemStatuses[current] {
			active[ref.EvidenceRefID] = true
		}
	}
	return active
}

func (r roleResolver) rule(ref contractsv1.EvidenceRef) rule {
	base := evidenceRule(ref)
	if linked, ok := r[ref.EvidenceRefID]; ok {
		linked.id, linked.claim = base.id, base.claim
		if _, explicit := explicitRuleOutcome(ref.Metadata); !explicit {
			linked.id = r[ref.EvidenceRefID].id
		}
		return linked
	}
	return base
}
