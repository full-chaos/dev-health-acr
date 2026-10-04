package guidegen

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func workItemMemberGuide(t *testing.T) string {
	t.Helper()
	text := embeddedFiles(t)[FileQuestions]
	start := strings.Index(text, "## "+workItemMemberHeading)
	if start < 0 {
		t.Fatal("questions guide has no work-item member section")
	}
	rest := text[start+3:]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

func TestWorkItemMemberGuideNamesEveryStatusAndVerb(t *testing.T) {
	section := workItemMemberGuide(t)
	statuses := contextfabric.WorkItemStatusVocabulary()
	if len(statuses) == 0 {
		t.Fatal("empty status vocabulary")
	}
	for _, status := range statuses {
		if !strings.Contains(section, "`"+status+"`") {
			t.Errorf("guide section does not name status %q", status)
		}
	}
	rows := contextfabric.MemberTimeRoleFormRows()
	if len(rows) == 0 {
		t.Fatal("empty time-role registry")
	}
	for _, row := range rows {
		if !strings.Contains(section, "`"+string(row.Role)+"` (field `"+row.Column+"`)") {
			t.Errorf("guide section does not name role %q with field %q", row.Role, row.Column)
		}
		for _, form := range row.Forms {
			if !strings.Contains(section, "`"+form+"`") {
				t.Errorf("guide section does not name verb %q of role %q", form, row.Role)
			}
		}
	}
}

func TestWorkItemMemberGuideListsNoVerbTheBinderIgnores(t *testing.T) {
	section := workItemMemberGuide(t)
	listed := 0
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "  - `") {
			continue
		}
		_, forms, ok := strings.Cut(line, "): ")
		if !ok {
			t.Fatalf("unreadable role line %q", line)
		}
		for _, form := range strings.Split(forms, ", ") {
			word := strings.Trim(form, "`")
			span := contextfabric.BoundWindowSpan{SpanStart: len("issues "), SpanEnd: len("issues " + word + " in the last 30 days")}
			outcome := contextfabric.BindMemberTimeRole("issues "+word+" in the last 30 days", span)
			if outcome.Reason != contextfabric.MemberTimeRoleBound {
				t.Errorf("guide lists verb %q but the binder does not bind it (%s)", word, outcome.Reason)
			}
			listed++
		}
	}
	if listed == 0 {
		t.Fatal("guide lists no verb")
	}
}

func TestWorkItemMemberSectionFailsWhenStatusSetOrRolesAreEmpty(t *testing.T) {
	in := FromRegistries()
	in.WorkItemStatuses = nil
	if _, err := workItemMemberSection(in); err == nil {
		t.Error("empty status set did not fail")
	}
	in = FromRegistries()
	in.MemberTimeRoles = nil
	if _, err := workItemMemberSection(in); err == nil {
		t.Error("empty role registry did not fail")
	}
}

func TestWorkItemMemberGuideDropsAVerbWhenTheRegistryDropsIt(t *testing.T) {
	in := FromRegistries()
	rows := append([]contextfabric.MemberTimeRoleFormRow(nil), in.MemberTimeRoles...)
	dropped := rows[0].Forms[1]
	rows[0].Forms = append([]string{rows[0].Forms[0]}, rows[0].Forms[2:]...)
	in.MemberTimeRoles = rows
	section, err := workItemMemberSection(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(section, "`"+dropped+"`") {
		t.Error("section still names a verb the input dropped")
	}
}
