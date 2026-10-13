package contextfabric

import (
	"encoding/json"
	"strings"
	"testing"
)

const projectStatusPartialLabel = "(partial: the source read was truncated)"

func truncatedReadinessFacts(p SubjectRef) []CanonicalFact {
	facts := projectStatusFacts(p)
	facts[2].SourceState = SourceTruncated
	return facts
}

func TestProjectStatusTruncatedReadinessIsServedAsAPartialDriver(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, truncatedReadinessFacts)
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := driverTitles(result.Drivers), "Backlog: 40 items|Estimate coverage: 18 of 40 items estimated|Risk severity: medium"; got != want {
		t.Fatalf("drivers = %q, want %q", got, want)
	}
	if len(result.ClaimedFacts) != 3 {
		t.Errorf("claimed facts = %d, want 3", len(result.ClaimedFacts))
	}
	for _, driver := range result.Drivers {
		partial := strings.Contains(driver.Summary, projectStatusPartialLabel)
		if want := driver.Category == "readiness"; partial != want {
			t.Errorf("driver %q summary %q: partial label = %v, want %v", driver.DriverID, driver.Summary, partial, want)
		}
		wantState := SourceState("")
		if driver.Category == "readiness" {
			wantState = SourceTruncated
		}
		if driver.SourceState != wantState {
			t.Errorf("driver %q source_state = %q, want %q", driver.DriverID, driver.SourceState, wantState)
		}
	}
}

func TestProjectStatusTruncatedReadinessCandidateCarriesThePartialLabel(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, truncatedReadinessFacts)
	if _, _, err := rig.investigate(clientRequest()); err != nil {
		t.Fatal(err)
	}
	if len(rig.encoded) == 0 || strings.Count(string(rig.encoded[0]), "project-status-") != 3 {
		t.Fatalf("synthesis_input must carry 3 project status candidates")
	}
	if got := strings.Count(string(rig.encoded[0]), "partial: the source read was truncated"); got != 1 {
		t.Errorf("synthesis_input carries %d partial labels, want 1", got)
	}
	var decoded any
	if err := json.Unmarshal(rig.encoded[0], &decoded); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	collectCandidateStates(decoded, states)
	if len(states) != 3 || states["project-status-2-readiness-candidate"] != "truncated" || states["project-status-1-workload-candidate"] != "" || states["project-status-3-health-candidate"] != "" {
		t.Errorf("candidate source_state by driver = %v, want truncated on readiness only", states)
	}
}

func TestProjectStatusAbsentReadinessIsMissingAndTruncatedIsPartial(t *testing.T) {
	project := acceptanceProject()
	interpretation := InterpretedQuestion{FactRequirements: projectStatusRequirements}
	absent := projectStatusFacts(project)
	absent = append(absent[:2], absent[3:]...)
	drivers, _, event := projectStatusDrivers(interpretation, []SubjectRef{project}, absent, nil, nil, ItemAllocation{})
	if len(drivers) != 2 || len(event.Missing) != 1 || event.Missing[0] != FactReadiness || len(event.Partial) != 0 {
		t.Errorf("absent: drivers=%d missing=%v partial=%v", len(drivers), event.Missing, event.Partial)
	}
	drivers, _, event = projectStatusDrivers(interpretation, []SubjectRef{project}, truncatedReadinessFacts(project), nil, nil, ItemAllocation{})
	if len(drivers) != 3 || len(event.Missing) != 0 || len(event.Partial) != 1 || event.Partial[0] != FactReadiness {
		t.Errorf("truncated: drivers=%d missing=%v partial=%v", len(drivers), event.Missing, event.Partial)
	}
	noHealth := projectStatusFacts(project)[:3]
	_, _, event = projectStatusDrivers(interpretation, []SubjectRef{project}, noHealth, nil, nil, ItemAllocation{})
	if len(event.Missing) != 1 || event.Missing[0] != FactHealth {
		t.Errorf("absent health: missing=%v, want [health]", event.Missing)
	}
}

func collectCandidateStates(node any, states map[string]string) {
	switch value := node.(type) {
	case map[string]any:
		if id, ok := value["driver_id"].(string); ok && strings.HasSuffix(id, "-candidate") {
			state, _ := value["source_state"].(string)
			states[id] = state
		}
		for _, child := range value {
			collectCandidateStates(child, states)
		}
	case []any:
		for _, child := range value {
			collectCandidateStates(child, states)
		}
	}
}

func TestProjectStatusStaleFactIsUsableWithAStaleLabel(t *testing.T) {
	rig := projectStatusRig(t, projectStatusRequirements, func(p SubjectRef) []CanonicalFact {
		facts := projectStatusFacts(p)
		facts[1].SourceState = SourceStale
		return facts
	})
	result, _, err := rig.investigate(serverRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := driverTitles(result.Drivers), "Backlog: 40 items|Estimate coverage: 18 of 40 items estimated|Risk severity: medium"; got != want {
		t.Fatalf("drivers = %q, want %q", got, want)
	}
	for _, driver := range result.Drivers {
		stale := driver.Category == "workload"
		if got := strings.Contains(driver.Summary, "(stale: the source read is out of date)"); got != stale {
			t.Errorf("driver %q summary %q: stale label = %v, want %v", driver.DriverID, driver.Summary, got, stale)
		}
		wantState := SourceState("")
		if stale {
			wantState = SourceStale
		}
		if driver.SourceState != wantState {
			t.Errorf("driver %q source_state = %q, want %q", driver.DriverID, driver.SourceState, wantState)
		}
	}
	event := rig.telemetry.projectStatusDrivers[0]
	if len(event.Missing) != 0 || len(event.PartialStates) != 1 || event.PartialStates[0] != SourceStale {
		t.Errorf("event missing=%v partial states=%v, want none missing and [stale]", event.Missing, event.PartialStates)
	}
}

func TestProjectStatusUnusableFactStatesStayMissingAndNameTheState(t *testing.T) {
	for _, state := range []SourceState{SourceNoData, SourceUnavailable, SourceConflicted} {
		t.Run(string(state), func(t *testing.T) {
			rig := projectStatusRig(t, projectStatusRequirements, func(p SubjectRef) []CanonicalFact {
				facts := projectStatusFacts(p)
				facts[2].SourceState = state
				return facts
			})
			result, _, err := rig.investigate(serverRequest())
			if err != nil {
				t.Fatal(err)
			}
			if got, want := driverTitles(result.Drivers), "Backlog: 40 items|Risk severity: medium"; got != want {
				t.Errorf("drivers = %q, want %q", got, want)
			}
			event := rig.telemetry.projectStatusDrivers[0]
			if len(event.Missing) != 1 || event.Missing[0] != FactReadiness || len(event.MissingStates) != 1 || event.MissingStates[0] != string(state) {
				t.Errorf("missing = %v states = %v, want readiness named %s", event.Missing, event.MissingStates, state)
			}
		})
	}
}

func TestSuppliedDraftCannotHideAStaleFact(t *testing.T) {
	t.Parallel()
	if got := suppliedDriverSourceState(t, SourceStale, ""); got != SourceStale {
		t.Errorf("served driver source_state = %q, want stale from the grounded fact", got)
	}
}
