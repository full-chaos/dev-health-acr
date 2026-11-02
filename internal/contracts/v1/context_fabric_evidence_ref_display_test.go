package v1

import (
	"regexp"
	"strings"
	"testing"
)

const (
	displayRepoUUID = "920f9442-07df-4217-4dc4-c5833c0b8268"
	displayZeroUUID = "00000000-0000-0000-0000-000000000000"
)

// rawInLabel is what a reader must never see in an evidence label (CHAOS-12620):
// a UUID, or a provider token before a raw source id.
var rawInLabel = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|(^|[\s,:])(linear|jira|gh|ghpr|gitlab|gl|extkey):|%3A|relates_to|:fwd|:rev|ep_[0-9a-f]{32}`)

// Every evidence entity type, from the id forms its producers mint (graph
// producers scope an id by its repository's UUID; context packet reads mint the
// bare id). The label names the subject as a reader does, or is the noun alone.
func TestAnEvidenceLabelNeverShowsARawID(t *testing.T) {
	t.Parallel()
	cases := map[ContextFabricEvidenceEntityType][]struct{ id, want string }{
		ContextFabricEvidenceEntityPullRequest: {{displayRepoUUID + ":869", "Pull request: #869"}, {"869", "Pull request: #869"}},
		ContextFabricEvidenceEntityCI:          {{displayRepoUUID + ":29103196850", "CI run: 29103196850"}, {"29103196850", "CI run: 29103196850"}},
		ContextFabricEvidenceEntityDeployment:  {{displayRepoUUID + ":3530993037", "Deployment: 3530993037"}},
		ContextFabricEvidenceEntityRepository:  {{displayRepoUUID, "Repository"}},
		ContextFabricEvidenceEntityWorkItem: {
			{displayZeroUUID + ":linear:CHAOS-2311", "Work item: CHAOS-2311"},
			{displayRepoUUID + ":gh:full-chaos/dev-health-ops#5035", "Work item: full-chaos/dev-health-ops#5035"},
			{"jira:OPS-11", "Work item: OPS-11"},
			{displayRepoUUID + ":gitlab:group/project!12", "Work item: group/project!12"},
		},
		ContextFabricEvidenceEntityTeam: {{"linear:CHAOS", "Team: CHAOS"}, {"gl:full.chaos", "Team: full.chaos"}},
		// A nameless project reads as its source id with the provider dropped;
		// only a UUID-shaped id reads as the noun (lead ruling, 2026-11-02).
		ContextFabricEvidenceEntityProject: {
			{"linear:c67b1602-31db-4422-8dec-a4a02bbcc513", "Project"},
			{"gitlab:acme/live-e2e", "Project: acme/live-e2e"},
			{"github:ci-metrics-executed-proof/repo", "Project: ci-metrics-executed-proof/repo"},
			{"jira:OPS", "Project: OPS"},
		},
		ContextFabricEvidenceEntityOrganization: {{"70d529e0-3c06-4597-8480-794fd02328b6", "Organization"}},
		ContextFabricEvidenceEntityCommit:       {{"3f9c2ab", "Commit: 3f9c2ab"}},
		ContextFabricEvidenceEntityCommitFile:   {{"3f9c2ab:cmd/main.go", "Commit file: 3f9c2ab:cmd/main.go"}},
		ContextFabricEvidenceEntityHotspot:      {{"cmd/main.go", "File hotspot: cmd/main.go"}},
		ContextFabricEvidenceEntityComplexity:   {{"cmd/main.go", "File complexity: cmd/main.go"}},
		ContextFabricEvidenceEntityIncident:     {{"INC-42", "Incident: INC-42"}},
		// An episode id is minted inside ACR (ep_ and 32 hex): no reader names it.
		ContextFabricEvidenceEntityEpisode:       {{"episode-7", "Episode"}, {"ep_0501bca3e78a45b86930192bc8c50444", "Episode"}},
		ContextFabricEvidenceEntityGraph:         {{"relationship.v2:work_item_team:8dce29ed83b79174cd7aaf8a149a035a", "Relationship"}},
		ContextFabricEvidenceEntityAIRun:         {{"6f1d2c3b-1111-4222-8333-944455556666", "AI workflow run"}},
		ContextFabricEvidenceEntityAIArtifact:    {{"edge-1", "AI artifact"}},
		ContextFabricEvidenceEntityReviewOutcome: {{"edge-2", "Review outcome"}},
		ContextFabricEvidenceEntityWorkItemDependencyV2: {
			{"ghpr%3Afull-chaos/ask-dev#90:linear%3ACHAOS-9616:relates_to%3Afwd", "Work item dependency: full-chaos/ask-dev#90, CHAOS-9616"},
		},
		ContextFabricEvidenceEntityWorkItemHierarchyV2: {{displayZeroUUID + ":linear%3ACHAOS-2:linear%3ACHAOS-1", "Work item hierarchy: CHAOS-2, CHAOS-1"}},
		ContextFabricEvidenceEntityWorkItemTeamV2:      {{displayZeroUUID + ":linear%3ACHAOS-2:linear%3ACHAOS:native", "Work item/team link: CHAOS-2, CHAOS"}},
		ContextFabricEvidenceEntityDeploymentIncidentV2: {
			{displayRepoUUID + ":3530993037:INC-42:native", "Deployment/incident link: 3530993037, INC-42"},
		},
		ContextFabricEvidenceEntityProjectTeamV2: {{"linear:c67b1602-31db-4422-8dec-a4a02bbcc513:linear%3ACHAOS:native", "Project/team link: CHAOS"}},
		ContextFabricEvidenceEntityReviewV2:      {{displayRepoUUID + ":1042:approval-42", "Review: #1042"}},
		// The retired un-encoded joins cannot be split back: the noun alone.
		ContextFabricEvidenceEntityWorkItemDependency: {{"jira:A:jira:B:blocks", "Work item dependency"}},
		ContextFabricEvidenceEntityWorkItemHierarchy:  {{displayRepoUUID + ":linear:CHAOS-2:linear:CHAOS-1", "Work item hierarchy"}},
		ContextFabricEvidenceEntityWorkItemTeam:       {{displayRepoUUID + ":linear:CHAOS-2:linear:CHAOS", "Work item/team link"}},
		ContextFabricEvidenceEntityDeploymentIncident: {{displayRepoUUID + ":1:2", "Deployment/incident link"}},
		ContextFabricEvidenceEntityProjectTeam:        {{"linear:p:linear:t", "Project/team link"}},
		ContextFabricEvidenceEntityReview:             {{displayRepoUUID + ":1042:approval-42", "Review"}},
	}
	for _, entityType := range ContextFabricEvidenceEntityTypeVocabulary() {
		rows, ok := cases[entityType]
		if !ok {
			t.Errorf("%s: no case; every entity type needs one", entityType)
			continue
		}
		for _, row := range rows {
			ref := "acr:v1:" + string(entityType) + ":" + row.id
			label, known := ContextFabricEvidenceRefLabel(ref)
			if !known || label != row.want {
				t.Errorf("%s: label = %q (known %v), want %q", ref, label, known, row.want)
			}
			if rawInLabel.MatchString(label) {
				t.Errorf("%s: label %q shows a raw id", ref, label)
			}
			// A result saved before CHAOS-12620 holds the old label; a
			// read restates it to the same label (codex r1).
			if got := ContextFabricEvidenceRefStoredLabel(ref, labelOnMainBefore12620(ref), nil); got != row.want {
				t.Errorf("%s: stored old label %q reads %q, want %q", ref, labelOnMainBefore12620(ref), got, row.want)
			}
		}
	}
	if got := ContextFabricEvidenceRefStoredLabel("acr:v1:repository:"+displayRepoUUID, "Repository: full-chaos/dev-health-ops", nil); got != "Repository: full-chaos/dev-health-ops" {
		t.Errorf("a label made after the change must stand: %q", got)
	}
	if label, known := ContextFabricEvidenceRefLabel("acr:v1:mystery:x"); known || label != "Evidence: x" {
		t.Errorf("unknown type: label = %q (known %v), want the id-carrying generic fallback", label, known)
	}
}

// A repository, project or organization ref reads as the name the answer holds,
// and as the noun alone when it holds none (CHAOS-12620).
func TestAnEvidenceLabelTakesTheNameTheAnswerHolds(t *testing.T) {
	t.Parallel()
	names := func(kind ContextFabricSubjectKind, rawID string) (string, bool) {
		switch {
		case kind == ContextFabricSubjectRepository && rawID == displayRepoUUID:
			return "full-chaos/dev-health-ops", true
		case kind == ContextFabricSubjectProject && rawID == "linear:c67b1602-31db-4422-8dec-a4a02bbcc513":
			return "Investment View Documentation", true
		case kind == ContextFabricSubjectOrganization && rawID == "70d529e0-3c06-4597-8480-794fd02328b6":
			return "Full Chaos", true
		}
		return "", false
	}
	for ref, want := range map[string]string{
		"acr:v1:repository:" + displayRepoUUID:                       "Repository: full-chaos/dev-health-ops",
		"acr:v1:project:linear:c67b1602-31db-4422-8dec-a4a02bbcc513": "Project: Investment View Documentation",
		"acr:v1:organization:70d529e0-3c06-4597-8480-794fd02328b6":   "Organization: Full Chaos",
		"acr:v1:repository:7b9583ee-4d24-2be7-4d09-34f815bebdd7":     "Repository",
		"acr:v1:project:linear:aaaaaaaa-31db-4422-8dec-a4a02bbcc513": "Project",
		// A number is scoped by its repository: with the repository's name in
		// hand, the label names it, so two #869 in two repositories differ (codex r3).
		"acr:v1:pull-request:" + displayRepoUUID + ":869":              "Pull request: full-chaos/dev-health-ops#869",
		"acr:v1:pull-request:7b9583ee-4d24-2be7-4d09-34f815bebdd7:869": "Pull request: #869",
		"acr:v1:pull-request:869":                                      "Pull request: #869",
		"acr:v1:review.v2:" + displayRepoUUID + ":1042:approval-42":    "Review: full-chaos/dev-health-ops#1042",
	} {
		if got, known := ContextFabricEvidenceRefLabelNamed(ref, names); !known || got != want {
			t.Errorf("%s: label = %q (known %v), want %q", ref, got, known, want)
		}
	}
	if got, _ := ContextFabricEvidenceRefLabel("acr:v1:repository:" + displayRepoUUID); got != "Repository" {
		t.Errorf("with no names: %q, want the noun", got)
	}
}

// labelOnMainBefore12620 is a copy of ContextFabricEvidenceRefLabel as main had
// it before CHAOS-12620 (the label a stored result holds), kept here as the
// test's own oracle rather than read from the code under test.
func labelOnMainBefore12620(refID string) string {
	parts := strings.Split(refID, ":")
	label := contextFabricEvidenceEntityLabels[ContextFabricEvidenceEntityType(parts[2])]
	id := strings.Join(parts[3:], ":")
	if EncodedEvidenceEntityType(ContextFabricEvidenceEntityType(parts[2])) {
		segments := strings.Split(id, ":")
		for index, segment := range segments {
			segment = strings.ReplaceAll(segment, "%3A", ":")
			segments[index] = strings.ReplaceAll(segment, "%25", "%")
		}
		id = strings.Join(segments, ":")
	}
	return clampLabel(label + ": " + id)
}
