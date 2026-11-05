package contextfabric

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12620 (codex r1): a result stored before the reader-form labels keeps the
// "<noun>: <ref id>" label it was saved with, and a by-id read serves it without a
// rebuild. The served copy and the cited evidence expansion read it in reader
// words; a stored label that is not that old form stands.
const storedRepoID = "920f9442-07df-4217-4dc4-c5833c0b8268"

func storedLabelResult() (InvestigationResult, map[string]string) {
	repo := "acr:v1:repository:" + storedRepoID
	item := "acr:v1:work-item:" + storedRepoID + ":linear:CHAOS-2311"
	episode := "acr:v1:episode:ep_0501bca3e78a45b86930192bc8c50444"
	pull := "acr:v1:pull-request:" + storedRepoID + ":869"
	current := "acr:v1:team:linear:CHAOS"
	result := InvestigationResult{EvidenceRefLabels: map[string]string{
		repo:    "Repository: " + storedRepoID,
		item:    "Work item: " + storedRepoID + ":linear:CHAOS-2311",
		episode: "Episode: ep_0501bca3e78a45b86930192bc8c50444",
		pull:    "Pull request: #869 in the web repository",
		current: "Team: CHAOS",
	}}
	result.SubjectResolution.Committed = []SubjectRef{{Kind: SubjectRepository, CanonicalID: RepositoryCanonicalID(storedRepoID), Label: "full-chaos/dev-health-ops"}}
	return result, map[string]string{
		repo:    "Repository: full-chaos/dev-health-ops",
		item:    "Work item: CHAOS-2311",
		episode: "Episode",
		pull:    "Pull request: #869 in the web repository",
		current: "Team: CHAOS",
	}
}

func TestAStoredEvidenceLabelServesInReaderWords(t *testing.T) {
	t.Parallel()
	result, want := storedLabelResult()
	stored := result.EvidenceRefLabels[("acr:v1:repository:" + storedRepoID)]
	served, _ := ServeVocabularyLabels(result, 0)
	for ref, label := range want {
		if got := served.EvidenceRefLabels[ref]; got != label {
			t.Errorf("%s served %q, want %q", ref, got, label)
		}
	}
	if result.EvidenceRefLabels["acr:v1:repository:"+storedRepoID] != stored {
		t.Fatalf("the stored result's own map changed: serve must label a copy")
	}
}

func TestACitedEvidenceExpansionReadsAStoredLabelInReaderWords(t *testing.T) {
	t.Parallel()
	result, want := storedLabelResult()
	for ref, label := range want {
		expanded := citedEvidenceExpansion(result, ref, "", "", time.Unix(0, 0))
		if got := expanded.Evidence.Source.DisplayLabel; got != label {
			t.Errorf("%s expanded %q, want %q", ref, got, label)
		}
	}
}

// A project the source names by its own id (an empty source name) holds no name,
// and neither does a UUID label: the label is the noun alone (codex r2).
func TestASubjectLabelThatIsItsIDIsNoName(t *testing.T) {
	t.Parallel()
	project, _, err := identity.Derive(identity.KindProject, []string{"linear", "c67b1602-31db-4422-8dec-a4a02bbcc513"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result InvestigationResult
	result.SubjectResolution.Committed = []SubjectRef{
		{Kind: SubjectProject, CanonicalID: project, Label: "c67b1602-31db-4422-8dec-a4a02bbcc513"},
		{Kind: SubjectRepository, CanonicalID: RepositoryCanonicalID(storedRepoID), Label: storedRepoID},
	}
	names := evidenceSubjectNames(result)
	for ref, want := range map[string]string{
		"acr:v1:project:linear:c67b1602-31db-4422-8dec-a4a02bbcc513": "Project",
		"acr:v1:repository:" + storedRepoID:                          "Repository",
	} {
		if got, _ := contractsv1.ContextFabricEvidenceRefLabelNamed(ref, names); got != want {
			t.Errorf("%s: label %q, want %q", ref, got, want)
		}
	}
}

// Every serve tier carries the restated labels: the copy that falls back to the
// input when the words do not fit carries them too (codex r2).
func TestAStoredEvidenceLabelIsRestatedInTheByteBoundFallback(t *testing.T) {
	t.Parallel()
	result, want := storedLabelResult()
	served, _ := ServeVocabularyLabels(result, 1)
	for ref, label := range want {
		if got := served.EvidenceRefLabels[ref]; got != label {
			t.Errorf("%s served %q in the fallback, want %q", ref, got, label)
		}
	}
}

// A nameless project with a UUID id reads as the kind's word in the graph
// (CHAOS-12619). That word is no name: the evidence label stays the noun, never
// "Project: project".
func TestTheKindWordOfANamelessProjectIsNoName(t *testing.T) {
	t.Parallel()
	project, _, err := identity.Derive(identity.KindProject, []string{"linear", "c67b1602-31db-4422-8dec-a4a02bbcc513"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result InvestigationResult
	result.SubjectResolution.Committed = []SubjectRef{{Kind: SubjectProject, CanonicalID: project, Label: ReadableSourceID(SubjectProject, "c67b1602-31db-4422-8dec-a4a02bbcc513")}}
	if got, _ := contractsv1.ContextFabricEvidenceRefLabelNamed("acr:v1:project:linear:c67b1602-31db-4422-8dec-a4a02bbcc513", evidenceSubjectNames(result)); got != "Project" {
		t.Fatalf("label %q, want the noun alone", got)
	}
}
