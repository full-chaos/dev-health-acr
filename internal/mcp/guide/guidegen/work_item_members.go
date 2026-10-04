package guidegen

import (
	"fmt"
	"strings"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

const workItemMemberHeading = "Work items of a project: status and period"

// workItemMemberSection renders the work-item member questions of the
// question-shape guide from the registries the engine reads: the closed
// status set and the time-role verb forms. Nothing in it is a second list.
func workItemMemberSection(in Inputs) (string, error) {
	if len(in.WorkItemStatuses) == 0 || len(in.MemberTimeRoles) == 0 {
		return "", fmt.Errorf("guidegen: the work-item status set or the time-role registry is empty")
	}
	var b strings.Builder
	b.WriteString("## " + workItemMemberHeading + "\n\n")
	b.WriteString("Name one project and ask for its work items (\"which issues of project X are in progress\"). `investigate_question` lists the members and states the filter it used in the answer's limitations.\n\n")
	b.WriteString("- Status: the closed set is " + strings.Join(quoteAll(in.WorkItemStatuses), ", ") + ". The filter reads the current status only, as of now and over no period. Status is not completion and not readiness. A status outside the set is refused, not answered as unqualified membership. On GitHub and GitLab the status is mapped from issue labels and open or closed state: a mapping, not a provider fact.\n")
	b.WriteString("- Period: a period needs one time-role verb in the same clause. The verb picks the field:\n")
	for _, row := range in.MemberTimeRoles {
		fmt.Fprintf(&b, "  - `%s` (field `%s`): %s\n", row.Role, row.Column, strings.Join(quoteAll(row.Forms), ", "))
	}
	b.WriteString("- A period with no such verb is not guessed: the answer lists no members and offers the readings " + strings.Join(quoteAll(roleNames(in)), ", ") + ". Ask again with one verb.\n")
	b.WriteString("- Two different roles in one clause get the same answer as no verb. A past period over a status (\"were in progress last March\") is refused, because status history is not stored.\n")
	b.WriteString("- A period sent with the request, with no verb in the question, gives the current members and this limitation: \"" + contractsv1.ContextFabricWorkItemWindowNotAppliedLimitation + "\"\n")
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
