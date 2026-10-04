package guidegen

import (
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const workItemMemberHeading = "Work items of a project or a repository: status and period"

// workItemMemberSection renders the work-item member questions of the
// question-shape guide from the registries the engine reads: the closed
// status set and the time-role verb forms. Nothing in it is a second list.
func workItemMemberSection(in Inputs) (string, error) {
	if len(in.WorkItemStatuses) == 0 || len(in.MemberTimeRoles) == 0 {
		return "", fmt.Errorf("guidegen: the work-item status set or the time-role registry is empty")
	}
	var b strings.Builder
	b.WriteString("## " + workItemMemberHeading + "\n\n")
	b.WriteString("Name one project or one repository and ask for its work items (\"which issues of project X are in progress\", \"which issues are open in repository owner/name\"). `investigate_question` lists the members and states the filter it used in the answer's limitations.\n\n")
	b.WriteString("- The work items of a repository are the issues linked to a pull request of that repository, whatever the source of the issue. Each member's reason names the tier of its link: \"" + contractsv1.ContextFabricWorkItemRepositoryMembershipReason(contractsv1.ContextFabricWorkItemRepositoryTierNative) + "\", \"" + contractsv1.ContextFabricWorkItemRepositoryMembershipReason(contractsv1.ContextFabricWorkItemRepositoryTierExplicitText) + "\" or \"" + contractsv1.ContextFabricWorkItemRepositoryMembershipReason(contractsv1.ContextFabricWorkItemRepositoryTierHeuristic) + "\". A member linked only by a heuristic match is never presented as natively linked, and the answer says how many are. An issue whose own repository is the named one but that no pull request links is not a member. The answer always states: \"" + contractsv1.ContextFabricWorkItemRepositoryFreshnessLimitation + "\"\n")
	b.WriteString("- A repository answers a status and a period over completion (\"completed in the last 30 days\"). A period over when the work items were created or last updated is refused for a repository, because no canonical fact carries those times for the members: \"" + contractsv1.ContextFabricWorkItemRepositoryPeriodRoleRefusalLimitation + "\"\n")
	b.WriteString("- A repository that has no pull request, or whose pull requests link no issue, lists no members and says which; that is a count of links, not a statement about the repository's health.\n")
	b.WriteString("- Status: the closed set is " + strings.Join(quoteAll(in.WorkItemStatuses), ", ") + ". The filter reads the current status only, as of now and over no period. Status is not completion and not readiness. A status outside the set is refused, not answered as unqualified membership. On GitHub and GitLab the status is mapped from issue labels and open or closed state: a mapping, not a provider fact.\n")
	b.WriteString("- Period: a period needs one time-role verb in the same clause. The verb picks the field:\n")
	for _, row := range in.MemberTimeRoles {
		fmt.Fprintf(&b, "  - `%s` (field `%s`): %s\n", row.Role, row.Column, strings.Join(quoteAll(row.Forms), ", "))
	}
	b.WriteString("- A period with no such verb is not guessed: the answer lists no members and offers the readings " + strings.Join(quoteAll(roleNames(in)), ", ") + ". Ask again with one verb.\n")
	b.WriteString("- Two different roles for the one period (\"created and closed in the last 30 days\") list no members either: the answer says the question named more than one and offers the readings. A form that describes the item itself (\"closed issues created in the last 30 days\", \"issues that are closed were created in the last 30 days\") does not count as a role when another verb in the clause names the period. A past period over a status (\"were in progress last March\") is refused, because status history is not stored.\n")
	b.WriteString("- A period sent with the request, with no verb and no status in the question, gives the current members and this limitation: \"" + contractsv1.ContextFabricWorkItemWindowNotAppliedLimitation + "\" With a status in the question, the members are filtered by that status as of now, and the status limitation says it is read over no period.\n")
	b.WriteString("- A period filters the members by that one time field. Their status and every other fact stay as of now, not as of the period.\n")
	b.WriteString("- Each work-item `status` fact carries `status_basis`, `status_in_vocabulary` (null when the status is missing, false when it is outside the set) and `status_provenance`. Read `status_provenance` before you quote a status.\n\n")
	return b.String(), nil
}

func roleNames(in Inputs) []string {
	names := make([]string, len(in.MemberTimeRoles))
	for i, row := range in.MemberTimeRoles {
		names[i] = string(row.Role)
	}
	return names
}
