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
			twin     string
		}{
			{"inactive team", opTeamInactive, directread.RefusalTeamInactive, "a named team is inactive", opTeamT},
			{"unknown team", "team:guess", directread.RefusalDeniedOrNotFound, "a named subject is denied or not found", ""},
		} {
			t.Run(op.Name+"/"+tc.name, func(t *testing.T) {
				vars := opMinimalVariables(t, op)
				opMerge(vars, path, tc.id)
				resp := h.run(t, opUnrestricted(opOrgA), op.Name, vars)
				if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != tc.want || resp.Refusal.Reason != tc.reason || resp.Refusal.ActiveCanonicalID != tc.twin {
					t.Fatalf("got call %s refusal %+v, want %s %q twin %q", resp.Call, resp.Refusal, tc.want, tc.reason, tc.twin)
				}
			})
		}
	}
	if found == 0 {
		t.Fatal("no operation takes a required team id: the row measured nothing")
	}
}

func TestGraphQLQueryAnswersTeamInactiveForANamedInactiveTeam(t *testing.T) {
	h := newGQLHarness(t, gqlHarnessOptions{})
	h.graph.inactiveTeams = map[string]map[string]opInactiveTeam{opOrgA: {
		opTeamInactive: {attributes: map[string]interface{}{"authorization_repositories": []string{"acme/a", "acme/b"}}, twin: opTeamT},
	}}
	var found int
	for _, op := range h.policy.Catalogue().Operations(directread.CallerUnrestricted) {
		path, ok := opTeamVariablePath(t, op)
		if !ok {
			continue
		}
		probe := rootVars(t, op)
		opMerge(probe, path, opTeamT)
		pq := gqlQueryFor(t, op, probe, nil, "")
		if resp := h.run(t, opUnrestricted(opOrgA), pq.text, pq.vars); resp.Refusal != nil && resp.Refusal.Code == directread.RefusalRootFieldNotAllowed {
			continue
		}
		found++
		for _, tc := range []struct {
			name, id string
			want     directread.RefusalCode
			twin     string
		}{
			{"inactive team", opTeamInactive, directread.RefusalTeamInactive, opTeamT},
			{"unknown team", "team:guess", directread.RefusalDeniedOrNotFound, ""},
		} {
			t.Run(op.Name+"/"+tc.name, func(t *testing.T) {
				vars := rootVars(t, op)
				opMerge(vars, path, tc.id)
				h.listener.reset()
				q := gqlQueryFor(t, op, vars, nil, "")
				resp := h.run(t, opUnrestricted(opOrgA), q.text, q.vars)
				h.wantRefused(t, resp, tc.want)
				if resp.Refusal.ActiveCanonicalID != tc.twin {
					t.Fatalf("active_canonical_id %q, want %q", resp.Refusal.ActiveCanonicalID, tc.twin)
				}
			})
		}
	}
	if found == 0 {
		t.Fatal("no operation takes a required team id: the row measured nothing")
	}
}
