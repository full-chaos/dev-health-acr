package api

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type servedCountOutcomes struct {
	Structured struct {
		Cohort struct {
			Members []json.RawMessage `json:"members"`
		} `json:"cohort"`
		Completeness struct {
			State    string `json:"state"`
			Outcomes []struct {
				Stage       string `json:"stage"`
				Requirement string `json:"requirement"`
				Obligation  string `json:"obligation"`
				Outcome     string `json:"outcome"`
				Served      int    `json:"served"`
				Declared    int    `json:"declared"`
			} `json:"outcomes"`
		} `json:"completeness"`
	} `json:"structured"`
}

func askCountOverCohort(t *testing.T, size, refsPerMember int) servedCountOutcomes {
	t.Helper()
	scenario := servedSentencesScenario{
		frame:      servedSentencesCountFrame(contextfabric.SubjectTeam),
		resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{servedSentencesRepository}, Candidates: []contextfabric.SubjectCandidate{servedSentencesCandidate(servedSentencesRepository)}},
		bases:      contextfabric.CommitBasisSet{},
		cohort:     servedSentencesCohort(contextfabric.SubjectTeam, size),
		facts:      servedSentencesFacts{capabilities: servedSentencesCapabilities()},
		window:     contractsv1.ContextFabricRelativeWindowTrailing30D,
	}
	for index := range scenario.cohort.Members {
		for ref := 0; ref < refsPerMember; ref++ {
			scenario.cohort.Members[index].EvidenceRefIDs = append(scenario.cohort.Members[index].EvidenceRefIDs, fmt.Sprintf("evidence_member_%03d_%d", index, ref))
		}
	}
	scenario.bases.Record(servedSentencesRepository, contextfabric.CommitBasisCallerCanonicalID)
	answer := newServedSentencesRig(t, scenario).ask(t, "how many teams own repository named one", contractsv1.ContextFabricRelativeWindowTrailing30D)
	var node servedCountOutcomes
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestACountBesideACutMemberSetReachesTheClientAsTheServedCount(t *testing.T) {
	const canonical = 20
	node := askCountOverCohort(t, canonical, 2)
	served := len(node.Structured.Cohort.Members)
	if served == 0 || served >= canonical {
		t.Fatalf("served members = %d of %d, want a cut by the evidence index", served, canonical)
	}
	var count []int
	for index, row := range node.Structured.Completeness.Outcomes {
		if row.Obligation == "count" {
			count = append(count, index)
		}
	}
	if len(count) == 0 {
		t.Fatalf("no count row served: %+v", node.Structured.Completeness.Outcomes)
	}
	last := node.Structured.Completeness.Outcomes[count[len(count)-1]]
	if last.Stage != "projection" || last.Outcome != "narrowed" || last.Served != served || last.Declared != canonical || last.Requirement == "" {
		t.Errorf("effective count row = %+v, want projection-stage narrowed %d/%d", last, served, canonical)
	}
	if node.Structured.Completeness.State != "partial" {
		t.Errorf("completeness state = %q, want partial", node.Structured.Completeness.State)
	}
}

func TestACountBesideAnUncutMemberSetIsServedUnchanged(t *testing.T) {
	node := askCountOverCohort(t, 4, 2)
	for _, row := range node.Structured.Completeness.Outcomes {
		if row.Obligation == "count" && row.Stage == "projection" {
			t.Errorf("a projection that cut nothing served a projection-stage count row: %+v", row)
		}
	}
}
