package contextfabric

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
)

// CHAOS-12619 (codex r3): a result saved before the reader-form labels keeps the
// raw source ids its graph reads gave it, and a read by id serves the stored row
// with no rebuild. Both serve doors pass ServeVocabularyLabels, so the served copy
// restates them: every subject whose label is its own raw id, a path's
// why_relevant from its one edge, and a graph driver's title and summary from the
// path it cites. Anything else stands, and the stored row is not changed.
func storedRawTextResult(t *testing.T) InvestigationResult {
	t.Helper()
	itemID, _, err := identity.Derive(identity.KindWorkItem, []string{"00000000-0000-0000-0000-000000000000", "linear:CHAOS-2320"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _, err := identity.Derive(identity.KindProject, []string{"linear", "c67b1602-31db-4422-8dec-a4a02bbcc513"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := SubjectRef{Kind: SubjectWorkItem, CanonicalID: itemID, Label: "linear:CHAOS-2320"}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: projectID, Label: "c67b1602-31db-4422-8dec-a4a02bbcc513"}
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: TeamCanonicalID("linear:CHAOS"), Label: "linear:CHAOS"}
	pr := SubjectRef{Kind: SubjectPullRequest, CanonicalID: "pull_request:920f9442-07df-4217-4dc4-c5833c0b8268:5063", Label: "PR #5063"}
	titled := SubjectRef{Kind: SubjectWorkItem, CanonicalID: itemID, Label: "Generate taxonomy table"}
	named := SubjectRef{Kind: SubjectProject, CanonicalID: projectID, Label: "Investment View Documentation"}
	var result InvestigationResult
	result.SubjectResolution.Committed = []SubjectRef{item, pr}
	result.SubjectResolution.Candidates = []SubjectCandidate{{Subject: team}}
	result.Cohort = &Cohort{Members: []CohortMember{{Subject: project}}}
	result.Paths = []RelationshipPath{
		{PathID: "path-links", Nodes: []SubjectRef{item, pr}, Edges: []RelationshipEdge{{Type: "LINKS_PULL_REQUEST", From: item, To: pr}}, WhyRelevant: "linear:CHAOS-2320 links pull request PR #5063"},
		{PathID: "path-project", Nodes: []SubjectRef{titled, project}, Edges: []RelationshipEdge{{Type: "BELONGS_TO_PROJECT", From: titled, To: project}}, WhyRelevant: "linear:CHAOS-2320 belongs to project c67b1602-31db-4422-8dec-a4a02bbcc513"},
		{PathID: "path-custom", Nodes: []SubjectRef{item, pr}, Edges: []RelationshipEdge{{Type: "LINKS_PULL_REQUEST", From: item, To: pr}}, WhyRelevant: "linear:CHAOS-2320 links pull request PR #5063; escalation note"},
		{PathID: "path-named", Nodes: []SubjectRef{titled, named}, Edges: []RelationshipEdge{{Type: "BELONGS_TO_PROJECT", From: titled, To: named}}, WhyRelevant: "Generate taxonomy table belongs to project Investment View Documentation"},
	}
	result.Drivers = []DriverJudgment{
		{DriverID: "driver-project", Title: "Belongs To Project: c67b1602-31db-4422-8dec-a4a02bbcc513", Summary: "linear:CHAOS-2320 belongs to project c67b1602-31db-4422-8dec-a4a02bbcc513", AffectedSubjects: []SubjectRef{titled}, PathIDs: []string{"path-project"}, EvidenceRefIDs: []string{"acr:v1:graph:rel-project"}},
		{DriverID: "driver-team", Title: "Owned By Team: linear:CHAOS", Summary: "A human-written summary that names linear:CHAOS", AffectedSubjects: []SubjectRef{team}},
		// The path's To end has its name, so graphrank did not compose this title
		// from a raw id: it stands.
		// Codex r4: the path's To end is the raw id, but this title is not the
		// relationship's words: it is not graphrank's composition and stands.
		{DriverID: "driver-custom", Title: "Escalated by hand: c67b1602-31db-4422-8dec-a4a02bbcc513", Summary: "Human-written driver summary stays exact", AffectedSubjects: []SubjectRef{titled}, PathIDs: []string{"path-project"}},
		{DriverID: "driver-named", Title: "Escalated by hand: c67b1602-31db-4422-8dec-a4a02bbcc513", Summary: "Generate taxonomy table belongs to project Investment View Documentation", AffectedSubjects: []SubjectRef{titled}, PathIDs: []string{"path-named"}},
	}
	result.ClaimedFacts = []ClaimedFact{{ClaimID: "claim-item", Subject: item}}
	return result
}

func TestAStoredResultServesItsGraphTextInReaderWords(t *testing.T) {
	t.Parallel()
	stored := storedRawTextResult(t)
	before, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := ServeVocabularyLabels(stored, 0)
	for name, got := range map[string][2]string{
		"committed work item":    {served.SubjectResolution.Committed[0].Label, "CHAOS-2320"},
		"committed pull request": {served.SubjectResolution.Committed[1].Label, "PR #5063"},
		"candidate team":         {served.SubjectResolution.Candidates[0].Subject.Label, "CHAOS"},
		"cohort project":         {served.Cohort.Members[0].Subject.Label, "project"},
		"path node":              {served.Paths[0].Nodes[0].Label, "CHAOS-2320"},
		"path edge end":          {served.Paths[0].Edges[0].From.Label, "CHAOS-2320"},
		"path project node":      {served.Paths[1].Nodes[1].Label, "project"},
		"titled node":            {served.Paths[1].Nodes[0].Label, "Generate taxonomy table"},
		"why_relevant":           {served.Paths[0].WhyRelevant, "CHAOS-2320 links pull request PR #5063"},
		"why_relevant project":   {served.Paths[1].WhyRelevant, "CHAOS-2320 belongs to project"},
		"custom why_relevant":    {served.Paths[2].WhyRelevant, "linear:CHAOS-2320 links pull request PR #5063; escalation note"},
		"driver title":           {served.Drivers[0].Title, "Belongs To Project: project"},
		"driver summary":         {served.Drivers[0].Summary, "CHAOS-2320 belongs to project"},
		"driver affected":        {served.Drivers[1].AffectedSubjects[0].Label, "CHAOS"},
		"driver no path title":   {served.Drivers[1].Title, "Owned By Team: linear:CHAOS"},
		"custom title raw end":   {served.Drivers[2].Title, "Escalated by hand: c67b1602-31db-4422-8dec-a4a02bbcc513"},
		"custom summary raw end": {served.Drivers[2].Summary, "Human-written driver summary stays exact"},
		"title not composed":     {served.Drivers[3].Title, "Escalated by hand: c67b1602-31db-4422-8dec-a4a02bbcc513"},
		"named why_relevant":     {served.Paths[3].WhyRelevant, "Generate taxonomy table belongs to project Investment View Documentation"},
		"human summary":          {served.Drivers[1].Summary, "A human-written summary that names linear:CHAOS"},
		"claimed fact subject":   {served.ClaimedFacts[0].Subject.Label, "CHAOS-2320"},
	} {
		if got[0] != got[1] {
			t.Errorf("%s: served %q, want %q", name, got[0], got[1])
		}
	}
	after, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("serving changed the stored result itself")
	}
	if strings.Contains(string(after), "CHAOS-2320") == false {
		t.Fatalf("the stored row lost its raw ids")
	}
}

// A result with no raw label serves exactly as it is: the restatement copies
// nothing it does not change.
func TestAResultWithNoRawTextServesUnchanged(t *testing.T) {
	t.Parallel()
	result := storedRawTextResult(t)
	served, _ := ServeVocabularyLabels(result, 0)
	again, _ := ServeVocabularyLabels(served, 0)
	if !reflect.DeepEqual(again.SubjectResolution, served.SubjectResolution) || !reflect.DeepEqual(again.Paths, served.Paths) || !reflect.DeepEqual(again.Drivers, served.Drivers) || !reflect.DeepEqual(again.ClaimedFacts, served.ClaimedFacts) {
		t.Fatalf("a served copy changed when served again")
	}
	if &again.Paths[0] != &served.Paths[0] {
		t.Fatalf("a result with nothing to restate was copied")
	}
}

// The cited evidence expansion quotes the drivers that cite a ref; a stored
// driver's raw title reads in reader words there too.
func TestACitedExpansionQuotesAStoredDriverInReaderWords(t *testing.T) {
	t.Parallel()
	expanded := citedEvidenceExpansion(storedRawTextResult(t), "acr:v1:graph:rel-project", "graph", "rel-project", time.Unix(0, 0))
	if !strings.Contains(expanded.Excerpt, "Driver: Belongs To Project: project") || strings.Contains(expanded.Excerpt, "c67b1602") {
		t.Fatalf("expansion excerpt %q, want the driver title in reader words", expanded.Excerpt)
	}
}
