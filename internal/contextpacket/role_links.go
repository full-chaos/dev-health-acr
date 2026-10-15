package contextpacket

import (
	"regexp"
	"strconv"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var failureTokens = map[string]bool{"failed": true, "failure": true, "error": true, "errored": true, "timed_out": true}

// failureGoalWords are the goal words that name a failure. A goal containing
// one is about failure rows and their causes (see effectiveGoalScores). Each
// stored failure status above has a word here (the census test pins it).
var failureGoalWords = map[string]bool{
	"fail": true, "fails": true, "failed": true, "failing": true, "failure": true, "failures": true,
	"error": true, "errors": true, "errored": true, "timed": true, "timeout": true, "timeouts": true,
	"broken": true, "flaky": true, "incident": true, "incidents": true, "outage": true, "outages": true,
	"regression": true, "regressions": true, "rollback": true, "rollbacks": true, "revert": true, "reverts": true, "reverted": true,
}

func goalNamesFailure(goal string) bool {
	for _, word := range goalWords(goal) {
		if failureGoalWords[word] {
			return true
		}
	}
	return false
}

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
	// The pull request a deployment carried is stored with the tier of the
	// provider's match when the deployments table has the tier column. A link
	// whose tier is exact_merge_sha, merged_pr or pushed on a FAILED deployment
	// is a cause candidate, ordered in that strength; every other link
	// (first_listed, an empty tier, a table without the column, an unrecognized
	// value, a deployment that did not fail) is a relation, counted per tier.
	type prLink struct {
		bestTier string
		relation string
	}
	prLinks := map[string]*prLink{}
	demotedBuckets := map[string]map[string]int{}
	promotedBuckets := map[string]map[string]int{}
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
		tier, bucket := linkTier(ref)
		entry := prLinks[number]
		if entry == nil {
			entry = &prLink{}
			prLinks[number] = entry
		}
		if failed && tierRank[tier] > 0 {
			if tierRank[tier] > tierRank[entry.bestTier] {
				entry.bestTier = tier
			}
			addBucket(promotedBuckets, number, tier)
			continue
		}
		what := "a failed deployment"
		if !failed {
			what = "a deployment linked to an open incident"
		}
		if entry.relation == "" {
			entry.relation = "The provider stored pull request " + number + " for " + what + "; the link's match tier is " + tierPhrase(bucket) + ", so it appears related and is not shown as the cause."
		}
		addBucket(demotedBuckets, number, bucket)
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
	links := causeLinks{causes: map[string]linkedCause{}, demoted: map[string]string{}, demotedBuckets: map[string]int{}, promotedBuckets: map[string]int{}}
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
	// Every deployment link is counted, whether or not its pull request row is
	// in the evidence set.
	for _, buckets := range demotedBuckets {
		for bucket, n := range buckets {
			links.demotedBuckets[bucket] += n
		}
	}
	for _, tiers := range promotedBuckets {
		for tier, n := range tiers {
			links.promotedBuckets[tier] += n
		}
	}
	for _, ref := range evidence {
		switch ref.Source.EntityType {
		case "pull_request":
			entry, ok := prLinks[ref.Source.EntityID]
			if !ok {
				continue
			}
			if entry.bestTier != "" {
				links.causes[ref.EvidenceRefID] = linkedCause{claim: tierClaim[entry.bestTier], why: tierCauseText(ref.Source.EntityID, entry.bestTier), subRank: tierRank[entry.bestTier]}
			} else if entry.relation != "" {
				links.demoted[ref.EvidenceRefID] = entry.relation
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
	demotedBuckets   map[string]int
	promotedBuckets  map[string]int
}

// linkedCause is one cause assignment: the claim kind the stored link
// supports and the text that says only what is stored.
type linkedCause struct {
	claim   contractsv1.ClaimKind
	why     string
	cites   []string
	subRank int
}

// roleWarnings discloses the links this pass withheld from the cause role, so
// a demoted or ambiguous link is counted rather than dropped silently.
func roleWarnings(evidence []contractsv1.EvidenceRef) []string {
	links := linkedCauseRefs(evidence)
	var out []string
	for _, ref := range evidence {
		if value, _ := ref.Metadata[metadataPullRequestTier].(string); ref.Source.EntityType == "deployment" && value == deploymentTierColumnAbsent {
			out = append(out, "deployments_pull_request_tier_column_absent")
			break
		}
	}
	if n := unknownStatusCount(evidence, "incident"); n > 0 {
		out = append(out, "status_unknown:incident:"+strconv.Itoa(n))
	}
	if n := unknownStatusCount(evidence, "deployment"); n > 0 {
		out = append(out, "status_unknown:deployment:"+strconv.Itoa(n))
	}
	for bucket, n := range links.demotedBuckets {
		out = append(out, "cause_links_demoted:"+strconv.Itoa(n)+":"+bucket)
	}
	for tier, n := range links.promotedBuckets {
		out = append(out, "cause_links_promoted:"+strconv.Itoa(n)+":"+tier)
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
		if (key != "blocks:fwd" && key != "blocks:inv") || blocker == "" || status == "" || status == "unknown" || closedWorkItemStatuses[status] {
			continue
		}
		active[ref.EvidenceRefID] = true
	}
	return active
}

var knownDeploymentStatuses = map[string]bool{
	"success": true, "inactive": true, "in_progress": true, "queued": true, "pending": true, "created": true, "running": true,
	"canceled": true, "cancelled": true, "skipped": true, "blocked": true, "waiting": true,
}

var knownClosedIncidentStatuses = map[string]bool{"resolved": true, "closed": true, "done": true, "cancelled": true, "canceled": true, "mitigated": true, "completed": true}

// unknownStatusCount counts the trusted rows of one entity type whose stored
// status is present but neither a failure/open value nor a known resolved or
// healthy one: such a row is "status unknown", never pressure and never
// resolved or success.
func unknownStatusCount(evidence []contractsv1.EvidenceRef, entityType string) int {
	count := 0
	for _, ref := range evidence {
		if ref.Source.EntityType != entityType || ref.SourceVersion == aiArtifactSourceVersion {
			continue
		}
		switch entityType {
		case "incident":
			rest, found := strings.CutPrefix(ref.Citation, "status=")
			if !found {
				continue
			}
			status, _, _ := strings.Cut(rest, ",")
			status = strings.ToLower(strings.TrimSpace(status))
			if status != "" && !openIncidentStatuses[status] && !knownClosedIncidentStatuses[status] {
				count++
			}
		case "deployment":
			status, _ := ref.Metadata[metadataDeploymentStatus].(string)
			status = strings.ToLower(strings.TrimSpace(status))
			if status != "" && !failureTokens[status] && !knownDeploymentStatuses[status] {
				count++
			}
		}
	}
	return count
}

type roleResolver map[string]rule

const (
	metadataDeploymentStatus  = "deployment_status"
	metadataPullRequestNumber = "pull_request_number"
	metadataSubjectKey        = "subject_key"
	metadataFocusKey          = "focus_key"
	metadataDeploymentID      = "deployment_id"
	metadataIncidentID        = "incident_id"
	metadataPullRequestTier   = "pull_request_tier"
	metadataRelationKey       = "relation_key"
	metadataBlockerID         = "blocker_work_item_id"
	metadataBlockerStatus     = "blocker_status"
)

func newRoleResolver(evidence []contractsv1.EvidenceRef) roleResolver {
	resolver := roleResolver{}
	links := linkedCauseRefs(evidence)
	for id, link := range links.causes {
		cause := causeRule()
		cause.why, cause.claim, cause.cites, cause.subRank = link.why, link.claim, link.cites, link.subRank
		resolver[id] = cause
	}
	for id := range activeBlockerRefs(evidence) {
		resolver[id] = pressureRule()
	}
	for id, why := range links.demoted {
		relation := rule{"evidence.observed.reference.v1", contractsv1.CategoryEvidence, contractsv1.ClaimObserved, contractsv1.SeverityInfo, 3, why, nil, 0}
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

const (
	tierExactMergeSHA = "exact_merge_sha"
	tierMergedPR      = "merged_pr"
	tierPushed        = "pushed"
	tierFirstListed   = "first_listed"
	// bucketTierNotStored names a link whose tier is empty or whose table has
	// no tier column; bucketTierUnrecognized names a stored value outside the
	// closed vocabulary.
	bucketTierNotStored    = "link_tier_not_stored"
	bucketTierUnrecognized = "link_tier_unrecognized"
)

// tierRank orders the tiers that may reach the cause role: exact_merge_sha is
// strongest; a tier absent from the map never does.
var tierRank = map[string]int{tierExactMergeSHA: 3, tierMergedPR: 2, tierPushed: 1}

var tierClaim = map[string]contractsv1.ClaimKind{
	tierExactMergeSHA: contractsv1.ClaimObserved,
	tierMergedPR:      contractsv1.ClaimInferred,
	tierPushed:        contractsv1.ClaimInferred,
}

// linkTier reads the stored match tier of a deployment's pull request link. A
// row without the typed key comes from a table without the column.
func linkTier(ref contractsv1.EvidenceRef) (tier, bucket string) {
	raw, present := ref.Metadata[metadataPullRequestTier]
	if !present {
		return "", bucketTierNotStored
	}
	value, _ := raw.(string)
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "":
		return "", bucketTierNotStored
	case tierExactMergeSHA, tierMergedPR, tierPushed:
		return value, value
	case deploymentTierColumnAbsent:
		return "", bucketTierNotStored
	case tierFirstListed:
		return "", tierFirstListed
	default:
		return "", bucketTierUnrecognized
	}
}

func tierPhrase(bucket string) string {
	if tierRank[bucket] > 0 {
		return bucket + ", but this deployment did not fail, so the link is not a cause candidate"
	}
	switch bucket {
	case tierFirstListed:
		return "first_listed (the first pull request the provider listed for that commit)"
	case bucketTierUnrecognized:
		return "an unrecognized stored value"
	default:
		return "not stored"
	}
}

func tierCauseText(number, tier string) string {
	switch tier {
	case tierExactMergeSHA:
		return "The provider matched pull request " + number + " to the deployed commit of a failed deployment by its exact merge commit sha (match tier exact_merge_sha)."
	case tierMergedPR:
		return "The provider matched pull request " + number + " to the deployed commit of a failed deployment as a merged pull request of that commit (match tier merged_pr), not by an exact merge commit sha; it appears to be the change that deployment carried."
	default:
		return "The ingest caller asserted pull request " + number + " for the deployed commit of a failed deployment (match tier pushed); it appears to be the change that deployment carried."
	}
}

func addBucket(buckets map[string]map[string]int, number, key string) {
	if buckets[number] == nil {
		buckets[number] = map[string]int{}
	}
	buckets[number][key]++
}
