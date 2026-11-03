package v1

import "testing"

// CHAOS-12716 (codex r1, executed P2): a result saved after CHAOS-12620 and before
// the org-scope rule holds the label that formula made, with the org UUID in it, and
// a by-id read serves stored labels. Such a label is restated, and only such a label:
// the oracle is the literal text acr main a29f4932 served on askdev-1, not the code
// under test.
func TestAStoredLabelMadeBeforeTheOrgScopeRuleReadsTheNativeID(t *testing.T) {
	t.Parallel()
	const org = "70d529e0-3c06-4597-8480-794fd02328b6"
	for _, row := range []struct{ ref, stored, want string }{
		{"acr:v1:project-team.v2:gitlab:" + org + "%3Agitlab%3A71133891:gl%3Afull.chaos:provider_access",
			"Project/team link: " + org + ":gitlab:71133891, full.chaos", "Project/team link: 71133891, full.chaos"},
		{"acr:v1:project:gitlab:" + org + ":gitlab:71133891", "Project: " + org + ":gitlab:71133891", "Project: 71133891"},
		{"acr:v1:project:gitlab:" + org + ":c67b1602-31db-4422-8dec-a4a02bbcc513", "Project: " + org + ":c67b1602-31db-4422-8dec-a4a02bbcc513", "Project"},
		// Unchanged by the rule: the label stands as stored.
		{"acr:v1:work-item:" + displayZeroUUID + ":linear:CHAOS-2311", "Work item: CHAOS-2311", "Work item: CHAOS-2311"},
		// A label made with a name stands.
		{"acr:v1:project:gitlab:" + org + ":gitlab:71133891", "Project: Payments", "Project: Payments"},
		// Text that is not what the old formula made for this ref stands.
		{"acr:v1:project-team.v2:gitlab:" + org + "%3Agitlab%3A71133891:gl%3Afull.chaos:provider_access",
			"Project/team link: " + org + ":gitlab:71133892, full.chaos", "Project/team link: " + org + ":gitlab:71133892, full.chaos"},
	} {
		if got := ContextFabricEvidenceRefStoredLabel(row.ref, row.stored, nil); got != row.want {
			t.Errorf("%s: stored %q reads %q, want %q", row.ref, row.stored, got, row.want)
		}
	}
}
