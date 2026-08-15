package falkorgraph

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file is the CHAOS-3833 (embed-text spec v2 §2) per-kind search-text
// composition -- the ONE derivation both retrieval arms index. The write path
// (subjectMergeAttrs -> propSearchText) and the embedding pass
// (collectEmbedTargets) call the SAME function with the SAME body-gate value,
// so lexical and vector search byte-identical text for every templated kind;
// that identity is what makes their agreement a statement about MECHANISM
// (graphrank.DistinctMechanismCount). The only remaining divergence is the
// embed-side MaxTextRunes tail truncation of UNBOUNDED compositions
// (episodes) -- embedprovider.MinimumMaxTextRunes guarantees no templated
// kind can ever be truncated, because every complete template fits under the
// validation floor.
//
// Rules, applied uniformly (spec §2): deterministic field order; fixed
// template per kind; per-field rune caps INSIDE the composition so the
// truncation point is stable; fields composed in DESCENDING retrieval value
// so tail truncation drops the least valuable text; a field is skipped when
// empty and a line with no fields is dropped; the whole is TrimSpace'd.
// Array-valued fields arrive as producer-canonicalized joined strings
// (deduplicated, sorted, capped -- devhealthsource), so this file never
// re-orders anything.
//
// ANY change to what any kind's text contains -- a template edit, a cap
// move, a new or removed field, a kind added to or removed from the embed
// skip-list -- must bump embedTextTemplateVersion (composition.go), which
// moves the stamped identity and the persisted reuse dimension together.

// Per-field rune caps, spelled as named constants so the template budgets in
// the spec stay auditable against the code. The largest complete template
// (pull_request: 300 + 200 + 120 + 1200 + fixed text ≈ 1,860 runes) is what
// embedprovider.MinimumMaxTextRunes covers.
const (
	capWorkItemTitle    = 500
	capWorkItemType     = 30
	capProjectName      = 120
	capTeamKey          = 60
	capPullRequestTitle = 300
	capRepoSlug         = 200
	capBranch           = 120
	capBodyHead         = 1200
	capEnvironment      = 60
	capReleaseRef       = 80
	capPipelineName     = 200
	capIncidentTitle    = 300
	capDescriptionHead  = 800
	capTeamName         = 120
	capTeamDescription  = 300
	capProjectTitle     = 200
	// capHandles bounds the retrieval-handles line (aliases + previous
	// names). 120 runes is generous against live data (a ticket key, a
	// team id + native key, a project key) while keeping the LARGEST
	// template -- pull_request at title 300 + repo 200 + branch 120 +
	// handles + body 1,200 + fixed text -- under
	// embedprovider.MinimumMaxTextRunes with headroom.
	capHandles = 120
	// capEnum bounds closed-vocabulary fields (state, severity, status):
	// real values are short tokens (APPROVED, CHANGES_REQUESTED,
	// resolved); the cap only bites on corrupt data.
	capEnum = 40
	// capProviderNames bounds the joined provider-name list on the
	// project template.
	capProviderNames = 60
	// capJoinedLabels / capJoinedProjectKeys / capJoinedTags bound the
	// producer-canonicalized joined array fields: each is the producer's
	// own element budget (10 x 40, 10 x 80, 10 x 40) plus separators. The
	// producer discipline makes these caps no-ops on well-formed rows;
	// composing them here anyway is what makes the template budget an
	// arithmetic FACT rather than a cross-package assumption -- the §0 (c)
	// floor guarantee must hold for ANY EntityProjection this adapter is
	// handed, not only ones devhealthsource produced.
	capJoinedLabels      = 440
	capJoinedProjectKeys = 820
	capJoinedTags        = 440
)

// subjectSearchText composes one entity's search text. Kinds without a
// declared template (organization among them) keep the pre-CHAOS-3833
// composition (entitySearchText: Label + Aliases + PreviousNames) -- the
// organization node still carries lexical text; it is the EMBED pass that
// skips it (collectEmbedTargets), not the write path.
//
// NO RETRIEVAL HANDLE IS EVER DROPPED: every template composes the
// entity's aliases AND previous names (retrievalHandles) -- templates
// whose spec line already carries the handles place them there; every
// other templated kind appends them as a trailing line. entitySearchText
// indexed both, and a renamed subject must stay resolvable by its previous
// name after this change exactly as before (pinned by the live
// prior-canonical-metadata invariant test).
func subjectSearchText(entity contextfabric.EntityProjection, includeBodies bool) string {
	switch entity.Subject.Kind {
	case contextfabric.SubjectWorkItem:
		return workItemSearchText(entity)
	case contextfabric.SubjectPullRequest:
		return pullRequestSearchText(entity, includeBodies)
	case contextfabric.SubjectDeployment:
		return deploymentSearchText(entity)
	case contractsv1.ContextFabricSubjectCIRun:
		return ciRunSearchText(entity)
	case contractsv1.ContextFabricSubjectPullRequestReview:
		return pullRequestReviewSearchText(entity)
	case contextfabric.SubjectIncident:
		return incidentSearchText(entity, includeBodies)
	case contextfabric.SubjectTeam:
		return teamSearchText(entity)
	case contextfabric.SubjectProject:
		return projectSearchText(entity)
	case contextfabric.SubjectRepository:
		return repositorySearchText(entity)
	default:
		return entitySearchText(entity)
	}
}

// retrievalHandles is the union of an entity's aliases and previous names,
// deduplicated and sorted -- the lexical handles entitySearchText always
// indexed. Every template composes it exactly once.
func retrievalHandles(entity contextfabric.EntityProjection) string {
	handles := make([]string, 0, len(entity.Aliases)+len(entity.PreviousNames))
	handles = append(handles, entity.Aliases...)
	handles = append(handles, entity.PreviousNames...)
	return capRunes(strings.Join(graphrank.UniqueSorted(handles), " "), capHandles)
}

// workItemSearchText (spec §2):
//
//	CHAOS-1725 <title[≤500]>
//	type: <type[≤30]> labels: <labels, producer-canonicalized>
//	project: <project_name[≤120]> team: <native_team_key[≤60]>
//
// status is deliberately ABSENT: it flips constantly (re-embed churn on
// every transition) and adds ~zero paraphrase signal; it stays a structured
// property.
func workItemSearchText(entity contextfabric.EntityProjection) string {
	return composeLines(
		joinFields(" ",
			retrievalHandles(entity),
			capRunes(entity.Subject.Label, capWorkItemTitle)),
		joinFields(" ",
			labeledField("type: ", capRunes(propText(entity, "type"), capWorkItemType)),
			labeledField("labels: ", capRunes(propText(entity, "labels"), capJoinedLabels))),
		joinFields(" ",
			labeledField("project: ", capRunes(propText(entity, "project_name"), capProjectName)),
			labeledField("team: ", capRunes(propText(entity, "native_team_key"), capTeamKey))),
	)
}

// pullRequestSearchText (spec §2):
//
//	PR #52 <title[≤300]>
//	repo: <repo_slug[≤200]> branch: <head_branch[≤120]>
//	<body first 1200 runes>            (only when the §3 body gate is on)
//
// The 1,200-rune body head is what BOTH arms get -- there is no separate
// full-body lexical copy (spec §0 decision (b)).
func pullRequestSearchText(entity contextfabric.EntityProjection, includeBodies bool) string {
	title := capRunes(entity.Subject.Label, capPullRequestTitle)
	head := title
	if number, ok := propInteger(entity, "number"); ok {
		prefix := fmt.Sprintf("PR #%d", number)
		if title == "" || title == prefix {
			// The producer labels a title-less pull request "PR #N"
			// already; repeating it would embed the same token twice.
			head = prefix
		} else {
			head = prefix + " " + title
		}
	}
	body := ""
	if includeBodies {
		body = capRunes(propText(entity, "body"), capBodyHead)
	}
	return composeLines(
		head,
		joinFields(" ",
			labeledField("repo: ", capRunes(propText(entity, "repo"), capRepoSlug)),
			labeledField("branch: ", capRunes(propText(entity, "branch"), capBranch))),
		retrievalHandles(entity),
		body,
	)
}

// deploymentSearchText (spec §2):
//
//	<environment[≤60]> deployment <release_ref[≤80]>
//	repo: <repo_slug[≤200]>
func deploymentSearchText(entity contextfabric.EntityProjection) string {
	head := entity.Subject.Label
	if environment := propText(entity, "environment"); environment != "" {
		head = capRunes(environment, capEnvironment) + " deployment"
	}
	return composeLines(
		joinFields(" ", head, capRunes(propText(entity, "release_ref"), capReleaseRef)),
		labeledField("repo: ", capRunes(propText(entity, "repo"), capRepoSlug)),
		retrievalHandles(entity),
	)
}

// ciRunSearchText (spec §2), one line:
//
//	CI run <pipeline_name[≤200]> branch: <branch[≤120]> repo: <repo_slug[≤200]>
//
// Deliberately WITHOUT the run id: `CI <digits>` is the pure-ID noise that
// makes up 78% of the live corpus (the id still resolves via ExactHint and
// stays the node label). A row with neither name nor branch degenerates to
// "CI run repo: X" -- the L5/T5 skip-list is the follow-up that stops
// embedding those, not this template's concern.
func ciRunSearchText(entity contextfabric.EntityProjection) string {
	return composeLines(
		joinFields(" ",
			"CI run",
			capRunes(propText(entity, "pipeline_name"), capPipelineName),
			labeledField("branch: ", capRunes(propText(entity, "branch"), capBranch)),
			labeledField("repo: ", capRunes(propText(entity, "repo"), capRepoSlug))),
		retrievalHandles(entity),
	)
}

// pullRequestReviewSearchText (spec §2):
//
//	<state> review of PR #52: <pr_title[≤300]>
//	repo: <repo_slug[≤200]>
func pullRequestReviewSearchText(entity contextfabric.EntityProjection) string {
	head := entity.Subject.Label
	if number, ok := propInteger(entity, "number"); ok {
		head = joinFields(" ",
			capRunes(propText(entity, "state"), capEnum),
			fmt.Sprintf("review of PR #%d:", number),
			capRunes(propText(entity, "pr_title"), capPullRequestTitle))
	}
	return composeLines(
		head,
		labeledField("repo: ", capRunes(propText(entity, "repo"), capRepoSlug)),
		retrievalHandles(entity),
	)
}

// incidentSearchText (spec §2):
//
//	<title[≤300]>
//	severity: <normalized_severity> status: <normalized_status>
//	<description first 800 runes>      (only when the §3 body gate is on)
//
// Status churn is ACCEPTED here, unlike work items: incidents are few and
// status IS the semantic payload.
func incidentSearchText(entity contextfabric.EntityProjection, includeBodies bool) string {
	description := ""
	if includeBodies {
		description = capRunes(propText(entity, "description"), capDescriptionHead)
	}
	return composeLines(
		capRunes(entity.Subject.Label, capIncidentTitle),
		joinFields(" ",
			labeledField("severity: ", capRunes(propText(entity, "severity"), capEnum)),
			labeledField("status: ", capRunes(propText(entity, "status"), capEnum))),
		retrievalHandles(entity),
		description,
	)
}

// teamSearchText (spec §2):
//
//	<name[≤120]>
//	<id> <native_team_key>            (the retrieval handles: aliases + previous names)
//	<description[≤300]>
//	projects: <project_keys, producer-canonicalized>
func teamSearchText(entity contextfabric.EntityProjection) string {
	return composeLines(
		capRunes(entity.Subject.Label, capTeamName),
		retrievalHandles(entity),
		capRunes(propText(entity, "description"), capTeamDescription),
		labeledField("projects: ", capRunes(propText(entity, "project_keys"), capJoinedProjectKeys)),
	)
}

// projectSearchText (spec §2):
//
//	<name[≤200]>
//	<project_key[≤80]>                 (the retrieval handles: aliases + previous names)
//	provider: <provider> state: <state>
//
// url is excluded (semantically empty); lead_name/email are excluded (PII)
// and never projected in the first place.
func projectSearchText(entity contextfabric.EntityProjection) string {
	providers := make([]string, 0, len(entity.ProviderIDs))
	for provider := range entity.ProviderIDs {
		providers = append(providers, provider)
	}
	return composeLines(
		capRunes(entity.Subject.Label, capProjectTitle),
		retrievalHandles(entity),
		joinFields(" ",
			labeledField("provider: ", capRunes(strings.Join(graphrank.UniqueSorted(providers), " "), capProviderNames)),
			labeledField("state: ", capRunes(propText(entity, "state"), capEnum))),
	)
}

// repositorySearchText (spec §2):
//
//	<repo slug[≤200]>
//	tags: <parsed tags, producer-canonicalized>
func repositorySearchText(entity contextfabric.EntityProjection) string {
	return composeLines(
		capRunes(entity.Subject.Label, capRepoSlug),
		retrievalHandles(entity),
		labeledField("tags: ", capRunes(propText(entity, "tags"), capJoinedTags)),
	)
}

// propText reads a string property, empty when absent or non-string.
func propText(entity contextfabric.EntityProjection, name string) string {
	value, ok := entity.Properties[name]
	if !ok || value.String == nil {
		return ""
	}
	return strings.TrimSpace(*value.String)
}

// propInteger reads an integer property.
func propInteger(entity contextfabric.EntityProjection, name string) (int64, bool) {
	value, ok := entity.Properties[name]
	if !ok || value.Integer == nil {
		return 0, false
	}
	return *value.Integer, true
}

// labeledField renders "label value" or nothing -- a label must never
// appear over an absent value, or every empty field would embed its label
// as a shared token across the whole corpus.
func labeledField(label, value string) string {
	if value == "" {
		return ""
	}
	return label + value
}

// joinFields joins the non-empty fields of one line.
func joinFields(separator string, fields ...string) string {
	kept := fields[:0:0]
	for _, field := range fields {
		if field != "" {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, separator)
}

// composeLines joins the non-empty lines and trims the whole -- "skip a
// line when its field is empty" made structural.
func composeLines(lines ...string) string {
	kept := lines[:0:0]
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// capRunes truncates one field to its template cap -- the authoritative cap
// per spec §0 (b): applied INSIDE the shared composition, so both arms see
// the identical truncation point.
func capRunes(text string, limit int) string {
	return embedprovider.TruncateRunes(strings.TrimSpace(text), limit)
}
