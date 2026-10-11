package directread_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestSecurityOverviewTopRepoIDRoundTripsIntoRepoIDsFilter(t *testing.T) {
	body := `{"data":{"securityOverview":{"topRepos":[{"repoId":"` + opRepoA + `","repoName":"o/r","count":3}]}}}`
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, body }, opHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), "securityOverview", map[string]any{})
	if resp.Call != directread.CallServed {
		t.Fatalf("not served: %+v", resp)
	}
	var data struct {
		SecurityOverview struct {
			TopRepos []struct {
				RepoID string `json:"repoId"`
			} `json:"topRepos"`
		} `json:"securityOverview"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil || len(data.SecurityOverview.TopRepos) != 1 {
		t.Fatalf("data: %s %v", resp.Data, err)
	}
	served := data.SecurityOverview.TopRepos[0].RepoID
	again := h.run(t, opUnrestricted(opOrgA), "securityOverview", map[string]any{"filters": map[string]any{"repoIds": []any{served}}})
	if again.Call != directread.CallServed {
		t.Fatalf("served repoId %q refused when sent back: %+v", served, again.Refusal)
	}
	_ = context.Background
}

const emitFileID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:src/main.go"

func TestServedRepositoryIDsCarryTheAcrFormAtEveryMarkedOutput(t *testing.T) {
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	wantMarked := []string{
		"complexityTimeseries.points[*].scopeId",
		"compoundingRisk.rows[*].scopeId",
		"hotspots.repos[*].repoId",
		"hotspots.rows[*].repoId",
		"securityOverview.topRepos[*].repoId",
		"workGraphEdges.edges[*].repoId",
	}
	var marked []string
	for _, name := range []string{"complexityTimeseries", "compoundingRisk", "hotspots", "securityOverview", "workGraphEdges"} {
		op, _ := cat.Lookup(name)
		for _, out := range op.Outputs {
			if out.EmitSubject == nil {
				continue
			}
			marked = append(marked, out.Path)
			if out.EmitSubject.AcrPrefix != "repository:" || out.EmitSubject.Kind != "repository" {
				t.Fatalf("%s: marker %+v", out.Path, out.EmitSubject)
			}
			var serveWith func(id any, sibling string, vars map[string]any) string
			serve := func(id any, sibling string) string {
				return serveWith(id, sibling, requestVars(out.EmitSubject, out.EmitSubject.WhenVariableEquals))
			}
			serveWith = func(id any, sibling string, vars map[string]any) string {
				root := opBuild(out.Path, id)
				if sibling != "" {
					list, leaf, _ := strings.Cut(out.Path, "[*].")
					_ = leaf
					opPlace(root, strings.Split(list+"[*]."+out.EmitSubject.WhenSibling, "."), sibling)
				}
				raw, _ := json.Marshal(root)
				res, err := directread.FilterResponseFor(op, raw, vars)
				if err != nil {
					t.Fatal(err)
				}
				return string(res.Data)
			}
			sibling := out.EmitSubject.WhenEquals
			if got := serve(opRepoA, sibling); !strings.Contains(got, `"repository:`+opRepoA+`"`) {
				t.Errorf("%s: bare uuid served as %s", out.Path, got)
			}
			if got := serve(emitFileID, sibling); !strings.Contains(got, `"`+emitFileID+`"`) || strings.Contains(got, "repository:") {
				t.Errorf("%s: composite id not served byte-equal: %s", out.Path, got)
			}
			if got := serve(nil, sibling); strings.Contains(got, "repository:") {
				t.Errorf("%s: null id prefixed: %s", out.Path, got)
			}
			if out.EmitSubject.WhenVariable != "" {
				for _, other := range []string{"FILE", ""} {
					if got := serveWith(opRepoA, sibling, requestVars(out.EmitSubject, other)); strings.Contains(got, "repository:") || !strings.Contains(got, `"`+opRepoA+`"`) {
						t.Errorf("%s: uuid-shaped id rewritten for request value %q: %s", out.Path, other, got)
					}
				}
			}
			if out.EmitSubject.WhenSibling != "" {
				if got := serve(opRepoA, "TEAM"); strings.Contains(got, "repository:") {
					t.Errorf("%s: id prefixed under another scope: %s", out.Path, got)
				}
			}
		}
	}
	slices.Sort(marked)
	if !slices.Equal(marked, wantMarked) {
		t.Fatalf("marked outputs = %v, want %v", marked, wantMarked)
	}
}

func requestVars(e *directread.SubjectEmit, value string) map[string]any {
	if e.WhenVariable == "" || value == "" {
		return nil
	}
	vars := map[string]any{}
	segs := strings.Split(e.WhenVariable, ".")
	cur := vars
	for _, seg := range segs[:len(segs)-1] {
		next := map[string]any{}
		cur[seg] = next
		cur = next
	}
	cur[segs[len(segs)-1]] = value
	return vars
}
