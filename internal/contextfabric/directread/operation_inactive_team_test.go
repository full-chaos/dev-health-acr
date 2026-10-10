package directread_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const opTeamInactive = "team:old"

func opTeamVariablePath(t *testing.T, op *directread.OperationPolicy) (string, bool) {
	t.Helper()
	for _, con := range op.Constraints {
		if con.Kind != directread.ConstraintRequired {
			continue
		}
		if rule, _ := op.Variable(con.Path); rule.Subject != nil && rule.Subject.Kind == directread.SubjectKindTeam {
			return con.Path, true
		}
	}
	return "", false
}

func TestRunOperationAnswersTeamInactiveForANamedInactiveTeam(t *testing.T) {
	h := newOpHarness(t, func(opRecorded) (int, string) {
		t.Error("an inactive team reached the query service")
		return 500, ""
	}, opHarnessOptions{})
	h.graph.inactiveTeams = map[string]map[string]opInactiveTeam{opOrgA: {
		opTeamInactive: {attributes: map[string]interface{}{"authorization_repositories": []string{"acme/a", "acme/b"}}, twin: opTeamT},
	}}
	var found int
	for _, op := range h.cat.Operations(directread.CallerUnrestricted) {
		path, ok := opTeamVariablePath(t, op)
		if !ok {
			continue
		}
		found++
		for _, tc := range []struct {
			name, id string
			want     directread.RefusalCode
			reason   string
		}{
			{"inactive team", opTeamInactive, directread.RefusalTeamInactive, "a named team is inactive; its active replacement is " + opTeamT},
			{"unknown team", "team:guess", directread.RefusalDeniedOrNotFound, "a named subject is denied or not found"},
		} {
			t.Run(op.Name+"/"+tc.name, func(t *testing.T) {
				vars := opMinimalVariables(t, op)
				opMerge(vars, path, tc.id)
				resp := h.run(t, opUnrestricted(opOrgA), op.Name, vars)
				if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != tc.want || resp.Refusal.Reason != tc.reason {
					t.Fatalf("got call %s refusal %+v, want %s %q", resp.Call, resp.Refusal, tc.want, tc.reason)
				}
			})
		}
	}
	if found == 0 {
		t.Fatal("no operation takes a required team id: the row measured nothing")
	}
}
