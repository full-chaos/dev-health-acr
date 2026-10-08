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
				Impact      string `json:"impact"`
				Cause       string `json:"cause_overrun"`
				Observed    bool   `json:"cause_observed"`
				Refinements []struct {
					Stage string `json:"stage"`
				} `json:"refinements"`
			} `json:"outcomes"`
		} `json:"completeness"`
	} `json:"structured"`
}

func askCountOverCohort(t *testing.T, size, refsPerMember, memberCap int) servedCountOutcomes {
	t.Helper()
	scenario := servedSentencesScenario{
		frame:      servedSentencesCountFrame(contextfabric.SubjectTeam),
		resolution: contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{servedSentencesRepository}, Candidates: []contextfabric.SubjectCandidate{servedSentencesCandidate(servedSentencesRepository)}},
		bases:      contextfabric.CommitBasisSet{},
		cohort:     servedSentencesCohort(contextfabric.SubjectTeam, size),
		facts:      servedSentencesFacts{capabilities: servedSentencesCapabilities()},
		window:     contractsv1.ContextFabricRelativeWindowTrailing30D,
		maxItems:   50,
	}
	for index := range scenario.cohort.Members {
		for ref := 0; ref < refsPerMember; ref++ {
			scenario.cohort.Members[index].EvidenceRefIDs = append(scenario.cohort.Members[index].EvidenceRefIDs, fmt.Sprintf("evidence_member_%03d_%d", index, ref))
		}
	}
	scenario.bases.Record(servedSentencesRepository, contextfabric.CommitBasisCallerCanonicalID)
	rig := newServedSentencesRig(t, scenario)
	answer := callRealMCPTool(t, rig.boot, "investigate_question", contractsv1.MCPInvestigateQuestionRequest{
		Question:       "how many teams own repository named one",
		EvidenceWindow: &contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D},
		Budget:         &contractsv1.MCPInvestigationBudget{MaxCohortMembers: memberCap},
	})
	var node servedCountOutcomes
	if err := json.Unmarshal(answer.structured, &node); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestACountBesideACutMemberSetReachesTheClientAsTheServedCount(t *testing.T) {
	const canonical = 20
	node := askCountOverCohort(t, canonical, 2, 12)
	served := len(node.Structured.Cohort.Members)
	if served == 0 || served >= canonical {
		t.Fatalf("served members = %d of %d, want a cut by the caller's member budget", served, canonical)
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
	if last.Stage != "assembled_result" || last.Outcome != "narrowed" || last.Served != served || last.Declared != canonical || last.Requirement == "" ||
		last.Impact != "scope" || !last.Observed {
		t.Errorf("effective count row = %+v, want assembled-result narrowed %d/%d", last, served, canonical)
	}
	if node.Structured.Completeness.State != "partial" {
		t.Errorf("completeness state = %q, want partial", node.Structured.Completeness.State)
	}
}

func TestACountBesideAnUncutMemberSetIsServedUnchanged(t *testing.T) {
	node := askCountOverCohort(t, 4, 2, 0)
	for _, row := range node.Structured.Completeness.Outcomes {
		if row.Obligation == "count" && row.Stage == "projection" {
			t.Errorf("a projection that cut nothing served a projection-stage count row: %+v", row)
		}
	}
}

// A flat listing at the planned hard cap is served whole through the route's
// own item gate: the plan raised its ceiling, so the route must honour it.
func TestAFlatListingAtTheHardCapIsServedWholeThroughTheRoute(t *testing.T) {
	node := askCountOverCohort(t, 100, 1, 100)
	if got := len(node.Structured.Cohort.Members); got != 100 {
		t.Fatalf("served members = %d, want all 100", got)
	}
	for _, row := range node.Structured.Completeness.Outcomes {
		if row.Obligation == "count" && row.Outcome == "narrowed" {
			t.Errorf("a whole listing served a narrowed count row: %+v", row)
		}
	}
}
