package contextfabric

import (
	"testing"
	"time"
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
	for _, enabled := range []bool{false, true} {
		served, _ := ServeVocabularyLabels(result, enabled, 0)
		for ref, label := range want {
			if got := served.EvidenceRefLabels[ref]; got != label {
				t.Errorf("enabled %v: %s served %q, want %q", enabled, ref, got, label)
			}
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
