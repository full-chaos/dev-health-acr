package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// CHAOS-12842: a discovered cohort ("which teams are struggling") names no subject, so acr commits
// none: the synthesis request carries the members in `cohort` and an empty
// `subject_resolution.committed`. The oracle refused every such request, even when its plan asked
// for no driver, so every scripted cohort turn ended in acr's facts-only fallback (askdev-1,
// CHAOS-11946). A committed subject is needed only to attribute a driver.
func TestACohortSynthesisWithNoDriverNeedsNoCommittedSubject(t *testing.T) {
	request := `{"subject_resolution":{"committed":[]},
	"cohort":{"kind":"team","members":[{"subject":{"kind":"team","canonical_id":"team:a","label":"A"}}]},
	"canonical_facts":[
	 {"id":"fact_a","kind":"health","subject":{"kind":"team","canonical_id":"team:a","label":"A"},"fields":{"severity":{"string":"high"}},"evidence_ref_ids":["ev_a"]}]}`

	plan := &ServiceSynthesis{Status: "complete", DirectJudgment: "j", CurrentState: "c", DeterministicAnswer: "d"}
	reply, err := buildSynthesisReply(&ServiceQuestion{Synthesis: plan}, request)
	if err != nil {
		t.Fatalf("a plan with no driver and no committed subject: err = %v, want an answer", err)
	}
	var out struct {
		Drivers  []json.RawMessage `json:"drivers"`
		Evidence []string          `json:"evidence_ref_ids"`
		Claims   []struct {
			FactID string `json:"fact_id"`
		} `json:"claimed_facts"`
	}
	if err := json.Unmarshal([]byte(reply), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Drivers) != 0 {
		t.Fatalf("drivers = %d, want none: the plan asks for none", len(out.Drivers))
	}
	if len(out.Evidence) != 1 || out.Evidence[0] != "ev_a" {
		t.Fatalf("evidence = %v, want the request's [ev_a]", out.Evidence)
	}
	if len(out.Claims) != 1 || out.Claims[0].FactID != "fact_a" {
		t.Fatalf("claims = %+v, want the first scalar claim of fact_a", out.Claims)
	}

	// A plan that asks for a driver still needs a committed subject to attribute it to: the oracle
	// never invents one, and it says so.
	plan.Driver = ServiceDriver{Title: "Status reads in progress", Summary: "s"}
	if _, err := buildSynthesisReply(&ServiceQuestion{Synthesis: plan}, request); err == nil ||
		!strings.Contains(err.Error(), "driver") || !strings.Contains(err.Error(), "committed subject") {
		t.Fatalf("a driver plan with no committed subject: err = %v, want a loud refusal naming the driver", err)
	}
}
