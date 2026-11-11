package entitlements_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// CHAOS-13566 r1 P2 (acp ruling: a repos key beside kind "organization" is
// refused): the decode refuses on key presence, not on value. encoding/json
// reads a null like an absent key and keeps the last value of a repeated key
// (case-insensitively), so both are refused before the typed read.

func TestPrincipalAccessRefusesAReposKeyBesideAKindWithoutRepos(t *testing.T) {
	for name, scope := range map[string]string{
		"organization, repos null":                     `{"kind":"organization","repos":null}`,
		"organization, repos array then null":          `{"kind":"organization","repos":["acme/api"],"repos":null}`,
		"organization, repos null then array":          `{"kind":"organization","repos":null,"repos":["acme/api"]}`,
		"organization, Repos null (case variant)":      `{"kind":"organization","Repos":null}`,
		"organization, repos array then Repos null":    `{"kind":"organization","repos":["acme/api"],"Repos":null}`,
		"none, repos null":                             `{"kind":"none","repos":null}`,
		"none, repos array then null":                  `{"kind":"none","repos":["acme/api"],"repos":null}`,
		"repos kind, repos array then kind none twice": `{"kind":"repos","repos":["acme/api"],"kind":"none"}`,
	} {
		server, _, _ := accessServer(t, http.StatusOK, accessDocument(false, scope))
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err == nil || access.Member || access.OrganizationWide {
			t.Errorf("%s: access %+v err %v, want an unavailable rejection", name, access, err)
		}
	}
}

func TestPrincipalAccessStillAcceptsKindsWithTheirReposKeyAbsent(t *testing.T) {
	for scope, orgWide := range map[string]bool{`{"kind":"organization"}`: true, `{"kind":"none"}`: false} {
		server, _, _ := accessServer(t, http.StatusOK, accessDocument(false, scope))
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err != nil || !access.Member || access.OrganizationWide != orgWide || access.Repositories != nil {
			t.Fatalf("%s: access %+v err %v", scope, access, err)
		}
	}
}

// A repeated key anywhere in the document is refused: the last value would
// silently replace the first (member false then true reads as a member).
func TestPrincipalAccessRefusesARepeatedKeyAtAnyDepth(t *testing.T) {
	base := accessDocument(true, `{"kind":"repos","repos":["acme/api"]}`)
	noRepos := accessDocument(true, `{"kind":"none"}`)
	open := noRepos[:len(noRepos)-1]
	for name, body := range map[string]string{
		"member false then true":        strings.Replace(base, `"member":true`, `"member":false,"member":true`, 1),
		"member false then Member":      strings.Replace(base, `"member":true`, `"member":false,"Member":true`, 1),
		"member then MEMBER":            strings.Replace(base, `"member":true`, `"member":false,"MEMBER":true`, 1),
		"impersonating true then false": strings.Replace(base, `"impersonating":false,"session"`, `"impersonating":true,"impersonating":false,"session"`, 1),
		"session impersonating twice":   strings.Replace(base, `"live":true,"impersonating":false`, `"live":true,"impersonating":true,"impersonating":false`, 1),
		"ask_dev false then true":       strings.Replace(base, `"ask_dev":true`, `"ask_dev":false,"ask_dev":true`, 1),
		"repository_scope twice":        strings.Replace(base, `"repository_scope":`, `"repository_scope":{"kind":"none"},"repository_scope":`, 1),
		"org_id twice":                  strings.Replace(base, `"org_id":"`, `"org_id":"00000000-0000-4000-8000-000000000000","org_id":"`, 1),
		"project_scope twice":           open + `,"project_scope":{"kind":"none"},"project_scope":{"kind":"projects","projects":[{"provider":"jira","id":"ENG"}]}}`,
		"project pair id twice":         open + `,"project_scope":{"kind":"projects","projects":[{"provider":"jira","id":"X","id":"ENG"}]}}`,
	} {
		server, _, _ := accessServer(t, http.StatusOK, body)
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, accessSession)
		if err == nil || access.Member {
			t.Errorf("%s: access %+v err %v, want an unavailable rejection", name, access, err)
		}
	}
}

// The class sweep of null == absent on every other field: a null required
// field is refused exactly as an absent one; a null optional object (session
// not asked, project_scope) reads as absent and gives the same answer, so it is
// left as it is.
func TestPrincipalAccessNullFieldsReadAsTheirAbsentForm(t *testing.T) {
	session := accessDocument(true, `{"kind":"none"}`)
	plain := accessDocument(false, `{"kind":"none"}`)
	projects := plain[:len(plain)-1] + `,"project_scope":{"kind":"projects","projects":[{"provider":"jira","id":"ENG"}]}}`
	refused := map[string][2]string{
		"schema_version":            {strings.Replace(plain, `"acr_principal_access.v1"`, `null`, 1), ""},
		"org_id":                    {strings.Replace(plain, `"`+accessOrg+`"`, `null`, 1), ""},
		"user_id":                   {strings.Replace(plain, `"`+accessUser+`"`, `null`, 1), ""},
		"impersonating":             {strings.Replace(plain, `"impersonating":false`, `"impersonating":null`, 1), ""},
		"entitlements":              {strings.Replace(plain, `{"ask_dev":true,"agent_context_runtime":false}`, `null`, 1), ""},
		"agent_context_runtime":     {strings.Replace(plain, `"agent_context_runtime":false`, `"agent_context_runtime":null`, 1), ""},
		"repository_scope":          {strings.Replace(plain, `{"kind":"none"}`, `null`, 1), ""},
		"repository_scope.kind":     {strings.Replace(plain, `{"kind":"none"}`, `{"kind":null}`, 1), ""},
		"repos of kind repos":       {strings.Replace(plain, `{"kind":"none"}`, `{"kind":"repos","repos":null}`, 1), ""},
		"session asked, null":       {strings.Replace(session, `{"live":true,"impersonating":false}`, `null`, 1), accessSession},
		"session.live":              {strings.Replace(session, `"live":true`, `"live":null`, 1), accessSession},
		"project_scope.kind":        {plain[:len(plain)-1] + `,"project_scope":{"kind":null}}`, ""},
		"projects of kind projects": {plain[:len(plain)-1] + `,"project_scope":{"kind":"projects","projects":null}}`, ""},
		"projects of kind none":     {plain[:len(plain)-1] + `,"project_scope":{"kind":"none","projects":null}}`, ""},
		"project provider":          {strings.Replace(projects, `"provider":"jira"`, `"provider":null`, 1), ""},
		"project id":                {strings.Replace(projects, `"id":"ENG"`, `"id":null`, 1), ""},
	}
	for name, cell := range refused {
		server, _, _ := accessServer(t, http.StatusOK, cell[0])
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, cell[1])
		if err == nil || access.Member {
			t.Errorf("null %s: access %+v err %v, want refused as when absent", name, access, err)
		}
	}
	for name, body := range map[string]string{
		"session not asked, null": plain[:len(plain)-1] + `,"session":null}`,
		"project_scope null":      plain[:len(plain)-1] + `,"project_scope":null}`,
	} {
		server, _, _ := accessServer(t, http.StatusOK, body)
		access, err := newAccessClient(t, server).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		absentServer, _, _ := accessServer(t, http.StatusOK, plain)
		absent, absentErr := newAccessClient(t, absentServer).PrincipalAccess(context.Background(), accessOrg, accessUser, "")
		if err != nil || absentErr != nil || access.Session != nil || access.Projects != nil || access.Member != absent.Member ||
			access.OrganizationWide != absent.OrganizationWide || access.Repositories != nil {
			t.Errorf("%s: access %+v err %v, want the answer of the absent form %+v", name, access, err, absent)
		}
	}
}
