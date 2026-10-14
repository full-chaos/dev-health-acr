package contextpacket

import (
	"regexp"
	"strconv"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var failureTokens = map[string]bool{"failed": true, "failure": true, "error": true, "errored": true, "timed_out": true}

var openIncidentStatuses = map[string]bool{"open": true, "triggered": true, "acknowledged": true, "investigating": true, "identified": true, "active": true, "ongoing": true}

// citationShowsFailure reads only the persisted status the source row carries.
func citationShowsFailure(citation string) bool {
	return failureTokens[strings.ToLower(strings.TrimSpace(citation))]
}

// incidentIsOpen is true only for a stored status in the open allowlist; an
// empty or unlisted status is unknown, never open.
func incidentIsOpen(citation string) bool {
	rest, found := strings.CutPrefix(citation, "status=")
	if !found {
		return false
	}
	status, _, _ := strings.Cut(rest, ",")
	status = strings.ToLower(strings.TrimSpace(status))
	return openIncidentStatuses[status]
}

var revertedCommitPattern = regexp.MustCompile(`(?im)^this reverts commit ([0-9a-f]{7,64})\b`)

// linkedCauseRefs returns the evidence ids tied to a failure by a persisted
// link inside the same evidence set:
//   - a pull request that a failed or incident-linked deployment carried
//     (deployments.pull_request_number, set by the provider sync from the
//     deployed commit's merged pull request or merge request);
//   - a commit that a later commit's message reverts.
//
// Time order alone never makes a cause. Providers store no deployed commit
// sha on the deployment, so a commit is a cause only through a revert.
func linkedCauseRefs(evidence []contractsv1.EvidenceRef) causeLinks {
	trusted := make([]contractsv1.EvidenceRef, 0, len(evidence))
	for _, ref := range evidence {
		if ref.SourceVersion != aiArtifactSourceVersion {
			trusted = append(trusted, ref)
		}
	}
	evidence = trusted
	openIncidents := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType == "incident" && incidentIsOpen(ref.Citation) {
			openIncidents[ref.Source.EntityID] = true
		}
	}
	// A deployment is incident-linked only through an edge that is not a
	// heuristic one and whose incident is in the set with an open stored status.
	incidentLinked := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "deployment_incident_edge" || ref.Provenance == contractsv1.EvidenceProvenanceHeuristic {
			continue
		}
		deploymentID, _ := ref.Metadata[metadataDeploymentID].(string)
		incidentID, _ := ref.Metadata[metadataIncidentID].(string)
		if deploymentID != "" && incidentID != "" && openIncidents[incidentID] {
			incidentLinked[deploymentID] = true
		}
	}
	// The pull request a failed deployment carried is stored without the tier
	// of the provider's match, so it is a relation, never a cause.
	relatedPullRequests := map[string]string{}
	linkCount := map[string]int{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "deployment" {
			continue
		}
		failed, linked := deploymentFailed(ref), incidentLinked[ref.Source.EntityID]
		if !failed && !linked {
			continue
		}
		number, _ := ref.Metadata[metadataPullRequestNumber].(string)
		if number == "" {
			continue
		}
		what := "a failed deployment"
		if !failed {
			what = "a deployment linked to an open incident"
		}
		linkCount[number]++
		relatedPullRequests[number] = "The provider stored pull request " + number + " for " + what + "; the link's match tier is not stored, so it appears related and is not shown as the cause."
	}
	// A revert message names a hash prefix; it marks a commit only when exactly
	// one commit of the set carries that prefix.
	prefixes := map[string][]string{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "commit" {
			continue
		}
		for _, match := range revertedCommitPattern.FindAllStringSubmatch(ref.Citation, -1) {
			prefix := strings.ToLower(match[1])
			prefixes[prefix] = append(prefixes[prefix], ref.EvidenceRefID)
		}
	}
	links := causeLinks{causes: map[string]linkedCause{}, demoted: map[string]string{}}
	revertedCommit := map[string][]string{}
	for prefix, reverters := range prefixes {
		var matched []string
		for _, ref := range evidence {
			if ref.Source.EntityType == "commit" && strings.HasPrefix(strings.ToLower(ref.Source.EntityID), prefix) {
				matched = append(matched, ref.EvidenceRefID)
			}
		}
		switch {
		case len(matched) == 1:
			revertedCommit[matched[0]] = append(revertedCommit[matched[0]], reverters...)
		case len(matched) > 1:
			links.ambiguousReverts++
		}
	}
	for _, ref := range evidence {
		switch ref.Source.EntityType {
		case "pull_request":
			if why, ok := relatedPullRequests[ref.Source.EntityID]; ok {
				links.demoted[ref.EvidenceRefID] = why
				links.demotedLinks += linkCount[ref.Source.EntityID]
			}
		case "commit":
			if reverters, ok := revertedCommit[ref.EvidenceRefID]; ok {
				links.causes[ref.EvidenceRefID] = linkedCause{claim: contractsv1.ClaimObserved, why: "A later commit's message states that it reverts this commit.", cites: reverters}
			}
		}
	}
	return links
}

// causeLinks is what the persisted links of one evidence set support: the
// causes, the relations demoted from cause because the link tier is not
// stored, and the revert messages whose prefix named more than one commit.
type causeLinks struct {
	causes           map[string]linkedCause
	demoted          map[string]string
	ambiguousReverts int
	demotedLinks     int
}

// linkedCause is one cause assignment: the claim kind the stored link
// supports and the text that says only what is stored.
type linkedCause struct {
	claim contractsv1.ClaimKind
	why   string
	cites []string
}

// roleWarnings discloses the links this pass withheld from the cause role, so
// a demoted or ambiguous link is counted rather than dropped silently.
func roleWarnings(evidence []contractsv1.EvidenceRef) []string {
	links := linkedCauseRefs(evidence)
	var out []string
	if links.demotedLinks > 0 {
		out = append(out, "cause_links_demoted:"+strconv.Itoa(links.demotedLinks)+":link_tier_not_stored")
	}
	if links.ambiguousReverts > 0 {
		out = append(out, "revert_links_ambiguous:"+strconv.Itoa(links.ambiguousReverts))
	}
	return out
}

// deploymentFailed reads the stored status the deployments statement carries as
// a typed column.
func deploymentFailed(ref contractsv1.EvidenceRef) bool {
	status, _ := ref.Metadata[metadataDeploymentStatus].(string)
	return failureTokens[strings.ToLower(strings.TrimSpace(status))]
}

var closedWorkItemStatuses = map[string]bool{"done": true, "canceled": true, "cancelled": true, "closed": true, "resolved": true}

// activeBlockerRefs returns the dependency rows that state a BLOCKS relation
// whose blocking work item has a stored, non-closed status. The decision rests
// on the typed columns the source statement carries (relation key, blocker id,
// blocker status); an unknown blocker (no stored status) is never pressure.
func activeBlockerRefs(evidence []contractsv1.EvidenceRef) map[string]bool {
	active := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "work_item_dependency" {
			continue
		}
		key, _ := ref.Metadata[metadataRelationKey].(string)
		blocker, _ := ref.Metadata[metadataBlockerID].(string)
		status, _ := ref.Metadata[metadataBlockerStatus].(string)
		status = strings.ToLower(strings.TrimSpace(status))
		if (key != "blocks:fwd" && key != "blocks:inv") || blocker == "" || status == "" || closedWorkItemStatuses[status] {
			continue
		}
		active[ref.EvidenceRefID] = true
	}
	return active
}

type roleResolver map[string]rule

const (
	metadataDeploymentStatus  = "deployment_status"
	metadataPullRequestNumber = "pull_request_number"
	metadataDeploymentID      = "deployment_id"
	metadataIncidentID        = "incident_id"
	metadataRelationKey       = "relation_key"
	metadataBlockerID         = "blocker_work_item_id"
	metadataBlockerStatus     = "blocker_status"
)

func newRoleResolver(evidence []contractsv1.EvidenceRef) roleResolver {
	resolver := roleResolver{}
	links := linkedCauseRefs(evidence)
	for id, link := range links.causes {
		cause := causeRule()
		cause.why, cause.claim, cause.cites = link.why, link.claim, link.cites
		resolver[id] = cause
	}
	for id := range activeBlockerRefs(evidence) {
		resolver[id] = pressureRule()
	}
	for id, why := range links.demoted {
		relation := rule{"evidence.observed.reference.v1", contractsv1.CategoryEvidence, contractsv1.ClaimObserved, contractsv1.SeverityInfo, 3, why, nil}
		resolver[id] = relation
	}
	return resolver
}

func (r roleResolver) rule(ref contractsv1.EvidenceRef) rule {
	base := evidenceRule(ref)
	if linked, ok := r[ref.EvidenceRefID]; ok {
		if _, explicit := explicitRuleOutcome(ref.Metadata); explicit {
			linked.id, linked.claim = base.id, base.claim
		}
		return linked
	}
	return base
}

// ActionItemsUnavailableWarning discloses that no persisted recommendation
// source is read by context_for_task, so an empty action category means
// "not served", never "nothing to do".
const ActionItemsUnavailableWarning = "action_items_unavailable:no_stored_recommendation_source"

const (
	// ActionReasonNoStoredSource is the coverage reason when no stored
	// recommendation source is read.
	ActionReasonNoStoredSource = "no_stored_recommendation_source"
	actionCoverageSource       = "action_findings.v1"
)
