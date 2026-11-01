package v1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xeipuuv/gojsonschema"
)

// CHAOS-12226: a work-item entity may state its current project membership
// as canonical project.v2 identities. nil means "not stated"; an empty list
// means "stated: no project". Only a work item may carry it.

func chaos12226WorkItem(identities *[]string) ContextFabricEntityProjection {
	return ContextFabricEntityProjection{
		Subject:           ContextFabricSubjectRef{Kind: ContextFabricSubjectWorkItem, CanonicalID: "work_item.v2:00000000-0000-0000-0000-000000000000:LIN-1", Label: "LIN-1"},
		Authorization:     ContextFabricAuthorizationScope{RepositorySlugs: []string{ContextFabricNoRepositorySentinel}},
		ProjectIdentities: identities,
		EvidenceRefIDs:    []string{"evidence_12226_0001"},
		ObservedAt:        time.Unix(1, 0).UTC(),
		SourceVersion:     "ops-v1",
	}
}

func chaos12226List(values ...string) *[]string {
	list := append([]string{}, values...)
	return &list
}

func TestAWorkItemMayStateItsProjectIdentities(t *testing.T) {
	t.Parallel()
	for name, identities := range map[string]*[]string{
		"not stated":   nil,
		"stated empty": chaos12226List(),
		"one project":  chaos12226List("project.v2:github:P-1"),
		"two projects": chaos12226List("project.v2:jira:ENG", "project.v2:linear:a%3Ab"),
	} {
		if err := chaos12226WorkItem(identities).Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
}

func TestProjectIdentitiesRejectAnythingButCanonicalProjectIDs(t *testing.T) {
	t.Parallel()
	tooMany := make([]string, ContextFabricEntityProjectIdentitiesMax+1)
	for i := range tooMany {
		tooMany[i] = "project.v2:jira:P" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
	}
	for name, identities := range map[string]*[]string{
		"wildcard":              chaos12226List("*"),
		"legacy raw id":         chaos12226List("P-1"),
		"other kind":            chaos12226List("team.v2:github:T-1"),
		"one segment":           chaos12226List("project.v2:github"),
		"three segments":        chaos12226List("project.v2:github:P:1"),
		"empty provider":        chaos12226List("project.v2::P-1"),
		"empty id":              chaos12226List("project.v2:github:"),
		"noncanonical escape":   chaos12226List("project.v2:github:a%3ab"),
		"bare percent":          chaos12226List("project.v2:github:50%"),
		"separator":             chaos12226List("project.v2:github:a|b"),
		"surrounding space":     chaos12226List(" project.v2:github:P-1"),
		"duplicate":             chaos12226List("project.v2:github:P-1", "project.v2:github:P-1"),
		"above the entry bound": &tooMany,
	} {
		if err := chaos12226WorkItem(identities).Validate(); err == nil {
			t.Errorf("%s: Validate() = nil, want an error", name)
		}
	}
}

func TestOnlyAWorkItemOrAPullRequestMayCarryProjectIdentities(t *testing.T) {
	t.Parallel()
	pullRequest := chaos12226WorkItem(chaos12226List("project.v2:github:P-1"))
	pullRequest.Subject = ContextFabricSubjectRef{Kind: ContextFabricSubjectPullRequest, CanonicalID: "pull_request:repo-1:7", Label: "PR 7"}
	if err := pullRequest.Validate(); err != nil {
		t.Errorf("pull request entity with project_identities: Validate() = %v, want nil", err)
	}
	team := chaos12226WorkItem(chaos12226List())
	team.Subject = ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
	team.Authorization = ContextFabricAuthorizationScope{TeamIDs: []string{"platform"}}
	if err := team.Validate(); err == nil {
		t.Error("team entity with project_identities accepted; only a work item or a pull request may carry it")
	}
	team.ProjectIdentities = nil
	if err := team.Validate(); err != nil {
		t.Errorf("team entity without project_identities: Validate() = %v, want nil", err)
	}
}

// The wire keeps "not stated" and "stated empty" apart.
func TestProjectIdentitiesKeepNotStatedApartFromEmpty(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		identities *[]string
		want       string
	}{
		"not stated":   {identities: nil, want: ""},
		"stated empty": {identities: chaos12226List(), want: `"project_identities":[]`},
		"one project":  {identities: chaos12226List("project.v2:github:P-1"), want: `"project_identities":["project.v2:github:P-1"]`},
	} {
		raw, err := json.Marshal(chaos12226WorkItem(tc.identities))
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		has := strings.Contains(string(raw), `"project_identities"`)
		if tc.want == "" && has {
			t.Errorf("%s: %s carries project_identities, want it omitted", name, raw)
		}
		if tc.want != "" && !strings.Contains(string(raw), tc.want) {
			t.Errorf("%s: %s, want it to contain %s", name, raw, tc.want)
		}
		var back ContextFabricEntityProjection
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if (back.ProjectIdentities == nil) != (tc.identities == nil) {
			t.Errorf("%s: round trip stated=%v, want %v", name, back.ProjectIdentities != nil, tc.identities != nil)
		}
	}
}

// The published schema agrees with the Go validator on every value above
// (codex r1 P3): the same canonical spellings pass and the same malformed
// ones fail the items pattern, and the schema, too, admits the property on a
// work item or a pull request only.
func TestTheSchemaAgreesWithGoOnProjectIdentities(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../contracts/jsonschema/v1/context_fabric_common.v1.schema.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	entity := document["$defs"].(map[string]any)["EntityProjection"].(map[string]any)
	property := entity["properties"].(map[string]any)["project_identities"]
	conditional := entity["allOf"]
	validate := func(schema any, instance any) bool {
		t.Helper()
		encoded, err := json.Marshal(instance)
		if err != nil {
			t.Fatalf("marshal instance: %v", err)
		}
		result, err := gojsonschema.Validate(gojsonschema.NewGoLoader(schema), gojsonschema.NewBytesLoader(encoded))
		if err != nil {
			t.Fatalf("validate %s: %v", encoded, err)
		}
		return result.Valid()
	}
	for _, value := range []string{"project.v2:github:P-1", "project.v2:jira:ENG", "project.v2:linear:a%3Ab", "project.v2:github:50%25"} {
		goErr := chaos12226WorkItem(chaos12226List(value)).Validate()
		if goErr != nil || !validate(property, []string{value}) {
			t.Errorf("%q: go err=%v, schema valid=%v; want both to accept", value, goErr, validate(property, []string{value}))
		}
	}
	for _, value := range []string{"*", "P-1", "team.v2:github:T-1", "project.v2:github", "project.v2:github:P:1", "project.v2::P-1", "project.v2:github:", "project.v2:github:50%", "project.v2:github:a%3ab", "project.v2:github:a|b"} {
		goErr := chaos12226WorkItem(chaos12226List(value)).Validate()
		if goErr == nil || validate(property, []string{value}) {
			t.Errorf("%q: go err=%v, schema valid=%v; want both to refuse", value, goErr, validate(property, []string{value}))
		}
	}
	for kind, want := range map[string]bool{"work_item": true, "pull_request": true, "team": false, "project": false} {
		instance := map[string]any{"subject": map[string]any{"kind": kind}, "project_identities": []string{}}
		if got := validate(map[string]any{"allOf": conditional}, instance); got != want {
			t.Errorf("schema: %s entity with project_identities valid=%v, want %v", kind, got, want)
		}
		delete(instance, "project_identities")
		if !validate(map[string]any{"allOf": conditional}, instance) {
			t.Errorf("schema: %s entity without project_identities refused", kind)
		}
	}
}
