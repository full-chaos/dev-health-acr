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

// linkedCauseRefs returns the evidence ids tied to a failure by a persisted
// link inside the same evidence set:
//   - a pull request that a failed or incident-linked deployment carried
//     (deployments.pull_request_number, set by the provider sync from the
//     deployed commit's merged pull request or merge request);
//   - a commit that a later commit's message reverts.
//
// Time order alone never makes a cause. Providers store no deployed commit
// sha on the deployment, so a commit is a cause only through a revert.
func linkedCauseRefs(evidence []contractsv1.EvidenceRef) map[string]linkedCause {
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
	carriedPullRequests := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "deployment" {
			continue
		}
		if !deploymentFailed(ref) && !incidentLinked[ref.Source.EntityID] {
			continue
		}
		if number, _ := ref.Metadata[metadataPullRequestNumber].(string); number != "" {
			carriedPullRequests[number] = true
		}
	}
	// A revert message names a hash prefix; it marks a commit only when exactly
	// one commit of the set carries that prefix.
	prefixes := map[string]bool{}
	for _, ref := range evidence {
		if ref.Source.EntityType != "commit" {
			continue
		}
		for _, match := range revertedCommitPattern.FindAllStringSubmatch(ref.Citation, -1) {
			prefixes[strings.ToLower(match[1])] = true
		}
	}
	revertedCommit := map[string]string{}
	for prefix := range prefixes {
		var matched []string
		for _, ref := range evidence {
			if ref.Source.EntityType == "commit" && strings.HasPrefix(strings.ToLower(ref.Source.EntityID), prefix) {
				matched = append(matched, ref.EvidenceRefID)
			}
		}
		if len(matched) == 1 {
			revertedCommit[matched[0]] = prefix
		}
	}
	causes := map[string]linkedCause{}
	for _, ref := range evidence {
		switch ref.Source.EntityType {
		case "pull_request":
			if carriedPullRequests[ref.Source.EntityID] {
				causes[ref.EvidenceRefID] = linkedCause{
					claim: contractsv1.ClaimInferred,
					why:   "The provider stored pull request " + ref.Source.EntityID + " for the deployed commit of a failed or incident-linked deployment; the provider's match tier is not stored, so this appears to be the change that deployment carried.",
				}
			}
		case "commit":
			if _, ok := revertedCommit[ref.EvidenceRefID]; ok {
				causes[ref.EvidenceRefID] = linkedCause{claim: contractsv1.ClaimObserved, why: "A later commit's message states that it reverts this commit."}
			}
		}
	}
	return causes
}

// linkedCause is one cause assignment: the claim kind the stored link
// supports and the text that says only what is stored.
type linkedCause struct {
	claim contractsv1.ClaimKind
	why   string
}

// deploymentFailed reads the stored status the deployments statement carries as
// a typed column.
func deploymentFailed(ref contractsv1.EvidenceRef) bool {
	status, _ := ref.Metadata[metadataDeploymentStatus].(string)
	return failureTokens[strings.ToLower(strings.TrimSpace(status))]
}

type roleResolver map[string]rule

const (
	metadataDeploymentStatus  = "deployment_status"
	metadataPullRequestNumber = "pull_request_number"
	metadataDeploymentID      = "deployment_id"
	metadataIncidentID        = "incident_id"
)

func newRoleResolver(evidence []contractsv1.EvidenceRef) roleResolver {
	resolver := roleResolver{}
	for id, link := range linkedCauseRefs(evidence) {
		cause := causeRule()
		cause.why, cause.claim = link.why, link.claim
		resolver[id] = cause
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
