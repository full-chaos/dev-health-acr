package directread_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// CHAOS-7202: recommendations, workItemTeamAttributions and home are served
// to unrestricted callers only. Restricted callers are refused with the typed
// class reason and NO upstream call is made.

var chaos7202Answers = map[string]string{
	"recommendations":          `{"data":{"recommendations":[{"ruleId":"r1","teamId":"t1","orgId":"o","severity":"HIGH","title":"t","rationale":"why","evidence":[{"teamId":"t1","metricTable":"m","field":"f","value":1}]}]}}`,
	"workItemTeamAttributions": `{"data":{"workItemTeamAttributions":[{"workItemId":"w1","provider":"github","teamId":"t1","teamName":"Team","source":"LINKED_ISSUE","confidence":"HIGH","isPrimary":true,"evidence":"linked_issue:X-1"}]}}`,
	"home":                     `{"data":{"home":{"freshness":null,"deltas":[],"summary":[],"tiles":[],"events":[],"signals":[],"limitingFactor":{"claim":"c","whyItMatters":"w","recommendedAction":"a","confidence":"low","evidenceRef":"e"}}}}`,
}

func chaos7202Variables(op string) map[string]any {
	switch op {
	case "recommendations":
		return map[string]any{"team": opTeamT, "window": map[string]any{"value": 4, "unit": "WEEK"}}
	case "workItemTeamAttributions":
		return map[string]any{"workItemIds": []any{"w1"}, "teamId": opTeamT}
	default:
		return map[string]any{"window": map[string]any{"rangeDays": 14, "compareDays": 14}}
	}
}

func TestCHAOS7202UnrestrictedServedOnePerOperation(t *testing.T) {
	for op, answer := range chaos7202Answers {
		t.Run(op, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, answer }, opHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), op, chaos7202Variables(op))
			reqs := h.upstream.requests()
			if resp.Call != directread.CallServed || len(reqs) != 1 || len(resp.Data) == 0 {
				raw, _ := json.Marshal(resp)
				t.Fatalf("want one served call with data, got %s (%d requests)", raw, len(reqs))
			}
			if !resp.UntrustedContent.Untrusted {
				t.Fatalf("answer not labelled untrusted")
			}
			if got := reqs[0].Header.Values("X-DH-Internal-Org-Id"); len(got) != 1 || got[0] != opOrgA {
				t.Fatalf("org header %v", got)
			}
			if got, _ := opLookup(reqs[0].variables(), "orgId"); got != opOrgA {
				t.Fatalf("orgId variable %v, want the principal org", got)
			}
		})
	}
}

func TestCHAOS7202RestrictedRefusedWithZeroUpstream(t *testing.T) {
	for op, answer := range chaos7202Answers {
		t.Run(op, func(t *testing.T) {
			// A planted leak: the upstream WOULD answer with rows. The refusal must come first.
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, answer }, opHarnessOptions{})
			resp := h.run(t, opRestrictedA(), op, chaos7202Variables(op))
			if resp.Call == directread.CallServed || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalOperationNotServedForCaller {
				raw, _ := json.Marshal(resp)
				t.Fatalf("want operation_not_served_for_caller, got %s", raw)
			}
			if len(resp.Data) != 0 || len(h.upstream.requests()) != 0 {
				t.Fatalf("restricted caller got data or reached the upstream (%d requests)", len(h.upstream.requests()))
			}
		})
	}
}

// r1 P1: ops requires only the outer window; WindowInput defaults value=4 and
// unit=WEEK, so window:{} is answerable and acr must not refuse it.
func TestCHAOS7202RecommendationsServesTheSDLWindowDefaults(t *testing.T) {
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, chaos7202Answers["recommendations"] }, opHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), "recommendations", map[string]any{"team": opTeamT, "window": map[string]any{}})
	if resp.Call != directread.CallServed || len(h.upstream.requests()) != 1 {
		raw, _ := json.Marshal(resp)
		t.Fatalf("window:{} must be served (SDL defaults), got %s", raw)
	}
}

func TestCHAOS7202PolicyMatrixRefusals(t *testing.T) {
	cases := []struct {
		name string
		op   string
		vars map[string]any
		want directread.RefusalCode
	}{
		{"recommendations without team", "recommendations", map[string]any{"window": map[string]any{}}, directread.RefusalScopeRequired},
		{"recommendations without window", "recommendations", map[string]any{"team": opTeamT}, directread.RefusalScopeRequired},
		{"recommendations window over the clamp", "recommendations", map[string]any{"team": opTeamT, "window": map[string]any{"value": 27, "unit": "WEEK"}}, directread.RefusalVariableOutOfRange},
		{"recommendations bad unit", "recommendations", map[string]any{"team": opTeamT, "window": map[string]any{"value": 4, "unit": "YEAR"}}, directread.RefusalVariableNotAllowed},
		{"recommendations client orgId", "recommendations", map[string]any{"orgId": "other", "team": opTeamT, "window": map[string]any{"value": 4, "unit": "WEEK"}}, ""},
		{"attributions too many ids", "workItemTeamAttributions", map[string]any{"workItemIds": make([]any, 201)}, directread.RefusalVariableOutOfRange},
		{"home filters refused", "home", map[string]any{"filters": map[string]any{"scope": map[string]any{"level": "TEAM"}}}, directread.RefusalVariableNotAllowed},
		{"home explicit dates refused", "home", map[string]any{"window": map[string]any{"startDate": "2026-09-01"}}, directread.RefusalVariableNotAllowed},
		{"home rangeDays over the clamp", "home", map[string]any{"window": map[string]any{"rangeDays": 91}}, directread.RefusalVariableOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, chaos7202Answers[tc.op] }, opHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), tc.op, tc.vars)
			if resp.Call == directread.CallServed || resp.Refusal == nil || len(h.upstream.requests()) != 0 {
				raw, _ := json.Marshal(resp)
				t.Fatalf("want a refusal with zero upstream, got %s", raw)
			}
			if tc.want != "" && resp.Refusal.Code != tc.want {
				t.Fatalf("refusal %s, want %s", resp.Refusal.Code, tc.want)
			}
		})
	}
}

func TestCHAOS7202PolicyShape(t *testing.T) {
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "recommendations", "workItemTeamAttributions"} {
		if _, refusal := cat.Lookup(name); refusal != nil {
			t.Fatalf("%s not served: %+v", name, refusal)
		}
		if _, _, refusal := cat.LookupFor(name, directread.CallerRestricted); refusal == nil || refusal.Code != directread.RefusalOperationNotServedForCaller || refusal.Reason == "" {
			t.Fatalf("%s restricted lookup: %+v", name, refusal)
		}
		for _, ns := range cat.NotServed() {
			if ns.Name == name {
				t.Fatalf("%s is still in not_served", name)
			}
		}
	}
	op, _ := cat.Lookup("home")
	for _, o := range op.Outputs {
		if strings.HasPrefix(o.Path, "home.limitingFactor") && o.Exception == "" {
			t.Fatalf("limitingFactor output %s carries no written exception", o.Path)
		}
	}
}
