package contextfabric

import (
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
