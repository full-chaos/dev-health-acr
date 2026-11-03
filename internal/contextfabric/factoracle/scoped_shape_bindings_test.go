package factoracle

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// scopedCases runs the shape bindings on store and returns, per shape of a
// scoped investment operation, the scope level and ids of each generated case. The
// capacity probe answers with a backlog for every team, so the subject team
// of the capacity shapes is the first team of the store.
func scopedCases(t *testing.T, store *Store, window Window) (map[string][][2]any, error) {
	t.Helper()
	backlog := servedAnswer(t, "capacityForecast", "capacityForecast", `{"capacityForecast":{"backlogSize":3}}`, 10, 1000)
	oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: window, shapes: mustShapes(t),
		Planes: fakePlanes{graphQL: func(Shape, map[string]any) (json.RawMessage, error) { return backlog, nil }}}
	cases, err := oracle.generatedCases(context.Background())
	if err != nil {
		return nil, err
	}
	out := map[string][][2]any{}
	for _, c := range cases {
		shape, ok := ShapeByID(oracle.shapes, c.ShapeID)
		if !ok {
			t.Fatalf("case %s has no shape", c.ShapeID)
		}
		if shape.Operation != "investmentBreakdownTeam" && shape.Operation != "investmentBreakdownRepository" {
			continue
		}
		batch, _ := c.Variables["batch"].(map[string]any)
		filters, _ := batch["filters"].(map[string]any)
		scope, _ := filters["scope"].(map[string]any)
		out[shape.ID()] = append(out[shape.ID()], [2]any{scope["level"], scope["ids"]})
	}
	return out, nil
}

// CHAOS-12808: a capture asks each scoped investment entry for exactly one
// subject, of the kind its policy entry binds, and the team owns the
// repository on the window's last day, so both scopes read rows.
func TestTheScopedInvestmentShapesAskForOneOwnedSubject(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	got, err := scopedCases(t, store, manifest.Window)
	if err != nil {
		t.Fatalf("the bindings stopped: %v", err)
	}
	reading, err := store.OwnerShares(manifest.Window.End.AddDate(0, 0, -1))
	if err != nil {
		t.Fatal(err)
	}
	subjects := map[string]map[string]bool{"TEAM": {}, "REPO": {}}
	shapes := map[string]int{}
	for shapeID, cases := range got {
		level := "TEAM"
		if strings.HasPrefix(shapeID, "analytics/investmentBreakdownRepository/") {
			level = "REPO"
		}
		shapes[level]++
		if len(cases) != 3 {
			t.Fatalf("%s has %d cases, want 3 (THEME, SUBCATEGORY, WORK_TYPE)", shapeID, len(cases))
		}
		for _, c := range cases {
			ids, _ := c[1].([]any)
			if c[0] != level || len(ids) != 1 {
				t.Fatalf("%s asks scope %v with ids %v, want level %s and exactly one id", shapeID, c[0], c[1], level)
			}
			subjects[level][ids[0].(string)] = true
		}
	}
	if shapes["TEAM"] == 0 || shapes["REPO"] == 0 || len(subjects["TEAM"]) != 1 || len(subjects["REPO"]) != 1 {
		t.Fatalf("shapes %v ask for subjects %v, want shapes of both entries and one subject per entry", shapes, subjects)
	}
	teamID, repoID := sortedKeys(subjects["TEAM"])[0], sortedKeys(subjects["REPO"])[0]
	if !strings.HasPrefix(teamID, "team:") || !strings.HasPrefix(repoID, "repository:") {
		t.Fatalf("subject ids %q and %q do not carry the policy's subject prefixes", teamID, repoID)
	}
	team := strings.TrimPrefix(teamID, "team:")
	if share := reading.Share(strings.TrimPrefix(repoID, "repository:"), team); share <= 0 {
		t.Fatalf("team %s does not own repository %s on the window's last day (share %v)", team, repoID, share)
	}
}

// A store in which no team owns a repository can measure neither scope: the
// bindings stop the run instead of asking for a subject with no rows.
func TestAStoreWithNoOwnedRepositoryStopsTheScopedShapes(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	extract.Tables[tableTeamRepoOwnership] = nil
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	// The ownership check is asked on the team entry first: without it the
	// team entry names a subject that owns nothing ("team:", or a team with
	// no repository), and that is the defect, named here before the repository
	// entry is reached.
	oracle := &Oracle{Store: store, Window: manifest.Window}
	for _, chosen := range []string{"", "team:" + store.TeamIDs()[0]} {
		level, id, err := oracle.scopedInvestmentSubject("investmentBreakdownTeam", chosen)
		if err == nil || !strings.Contains(err.Error(), "no team owns a repository") {
			t.Fatalf("with no owned repository, the team entry (capacity subject %q) asks scope %s for %q (error %v): a subject that owns nothing", chosen, level, id, err)
		}
	}
	if _, err := scopedCases(t, store, manifest.Window); err == nil || !strings.Contains(err.Error(), "no team owns a repository") {
		t.Fatalf("a store with no owned repository ended with %v", err)
	}
}
