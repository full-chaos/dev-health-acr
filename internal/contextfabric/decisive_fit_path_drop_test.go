package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const decisivePathDropCeiling = 75000

// lateWarnings adds about n bytes as valid warnings (each within the contract's
// per-warning bound), standing in for a writer that runs after the fit.
func lateWarnings(n int) func(InvestigationResult) InvestigationResult {
	return func(result InvestigationResult) InvestigationResult {
		warnings := append([]string{}, result.Warnings...)
		for index, left := 0, n; left > 0; index++ {
			size := min(left, 1500)
			warnings = append(warnings, strings.Repeat(string(rune('a'+index%26)), size))
			left -= size
		}
		result.Warnings = warnings
		return result
	}
}

func investigateWithLateBytes(t *testing.T, ceiling int64, late int, telemetry *recordingTelemetry, shape chaos6558PathShape) (InvestigationResult, error) {
	t.Helper()
	calls := 0
	shape.maxBytes = ceiling
	engine := chaos6558PathEngine(t, &calls, telemetry, shape)
	engine.servedLateHook = lateWarnings(late)
	return engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, chaos6558Request())
}

// A writer after the stage-3 fit pushes a document whose rows are already at
// the floor over the ceiling. Paths are the only section left to drop, and the
// stage-3 fit would have dropped them: the answer is served, not refused.
func TestADecisiveFitOverTheCeilingDropsPathsBeforeRefusing(t *testing.T) {
	telemetry := &recordingTelemetry{}
	result, err := investigateWithLateBytes(t, decisivePathDropCeiling, 6000, telemetry, chaos6558PathShape{})
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused (%s) at %d bytes against %d, nothing_left_to_cut=%v: paths were still droppable", refusal.Overrun, refusal.MeasuredBytes, refusal.MaxSerializedBytes, refusal.NothingLeftToCut)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	if got := servedBytes(t, result); got > decisivePathDropCeiling {
		t.Fatalf("served %d bytes against %d", got, decisivePathDropCeiling)
	}
	if len(result.Paths) == 0 || len(result.Paths) >= chaos6558PathCount {
		t.Fatalf("served %d of %d paths: want some dropped, not all", len(result.Paths), chaos6558PathCount)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("the served answer is not valid: %v", err)
	}
	if result.Status != InvestigationPartial || !result.Coverage.Partial {
		t.Fatalf("status %q partial=%v: a cut answer is partial", result.Status, result.Coverage.Partial)
	}
	want, _ := contractsv1.ContextFabricPathDropLimitation(len(result.Paths), chaos6558PathCount)
	disclosures := 0
	for _, limitation := range result.Limitations {
		if contractsv1.IsContextFabricPathDropLimitation(limitation) {
			disclosures++
			if limitation != want {
				t.Fatalf("limitation = %q, want %q", limitation, want)
			}
		}
	}
	rowsOutcome := 0
	for _, row := range result.Completeness.Outcomes {
		if reflect.DeepEqual(row, pathDropOutcomeRow(len(result.Paths), chaos6558PathCount)) {
			rowsOutcome++
		}
	}
	if disclosures != 1 || rowsOutcome != 1 {
		t.Fatalf("path-drop limitations %d, outcome rows %d, want 1 and 1: %q", disclosures, rowsOutcome, result.Limitations)
	}
	kept := map[string]bool{}
	for _, path := range result.Paths {
		kept[path.PathID] = true
	}
	for _, driver := range result.Drivers {
		for _, id := range driver.PathIDs {
			if !kept[id] {
				t.Fatalf("driver %s cites dropped path %s", driver.DriverID, id)
			}
		}
	}
	drops := 0
	for _, event := range telemetry.pathDrops {
		if event.Pass == answerPassDecisive {
			drops++
			if event.MinimumBytes <= 0 || event.MinimumBytes >= event.BytesAfter {
				t.Fatalf("decisive path-drop event reports minimum %d against served %d: the minimum is the size with every droppable path gone, below the fewest-drop document", event.MinimumBytes, event.BytesAfter)
			}
			if !event.Served || event.PathsBefore-event.PathsAfter != chaos6558PathCount-len(result.Paths) || len(event.DroppedPathIDs) != event.PathsBefore-event.PathsAfter || event.BytesBefore <= decisivePathDropCeiling || event.BytesAfter > decisivePathDropCeiling {
				t.Fatalf("decisive path-drop event = %+v, served %d paths", event, len(result.Paths))
			}
		}
	}
	if drops != 1 {
		t.Fatalf("decisive path-drop events = %d, want 1", drops)
	}
}

// The fewest drops: one fewer path must not have fit.
func TestADecisivePathDropTakesTheFewestPathsThatFit(t *testing.T) {
	result, err := investigateWithLateBytes(t, decisivePathDropCeiling, 6000, &recordingTelemetry{}, chaos6558PathShape{})
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	// One path measures about 1.9 kB; the served document must sit within one
	// path's size of the ceiling or a drop was wasted.
	if headroom := decisivePathDropCeiling - servedBytes(t, result); headroom >= 2300 {
		t.Fatalf("served %d bytes against %d: %d bytes spare, more paths were dropped than needed", servedBytes(t, result), decisivePathDropCeiling, headroom)
	}
}

// A document the stage-3 fit already path-dropped keeps ONE disclosure naming
// the paths the answer found.
func TestADecisivePathDropRestatesAnEarlierPathDropDisclosure(t *testing.T) {
	telemetry := &recordingTelemetry{}
	result, err := investigateWithLateBytes(t, chaos6558PathMaxBytes, 1500, telemetry, chaos6558PathShape{})
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	earlier, decisive := 0, 0
	for _, event := range telemetry.pathDrops {
		if event.Served && event.Pass == answerPassDecisive {
			decisive++
		} else if event.Served {
			earlier++
		}
	}
	if earlier != 1 || decisive != 1 {
		t.Fatalf("served path drops: stage-3 %d, decisive %d, want 1 and 1 (the case must drop twice)", earlier, decisive)
	}
	if got := servedBytes(t, result); got > chaos6558PathMaxBytes {
		t.Fatalf("served %d bytes against %d", got, chaos6558PathMaxBytes)
	}
	disclosures := 0
	for _, limitation := range result.Limitations {
		if served, declared, ok := contractsv1.ParseContextFabricPathDropLimitation(limitation); ok {
			disclosures++
			if declared != chaos6558PathCount || served != len(result.Paths) {
				t.Fatalf("limitation states %d of %d, served %d of %d", served, declared, len(result.Paths), chaos6558PathCount)
			}
		}
	}
	if disclosures != 1 {
		t.Fatalf("path-drop limitations = %d, want 1: %q", disclosures, result.Limitations)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The refusal stands for the minimum answer: every droppable path gone and the
// document still over.
func TestADecisiveFitRefusesWhenEveryDroppablePathIsGoneAndTheAnswerIsStillOver(t *testing.T) {
	_, err := investigateWithLateBytes(t, decisivePathDropCeiling, 90000, &recordingTelemetry{}, chaos6558PathShape{})
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) || refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes || !refusal.NothingLeftToCut {
		t.Fatalf("err = %v, want a bytes refusal saying nothing was left to cut", err)
	}
}

// A path a driver cites without evidence cannot go: the driver would be left
// with neither a path nor an evidence ref. Uncited paths go, and every cited
// path is still there.
func TestADecisivePathDropKeepsPathsNoDriverCanLose(t *testing.T) {
	result, err := investigateWithLateBytes(t, decisivePathDropCeiling, 20000, &recordingTelemetry{}, chaos6558PathShape{driversWithoutEvidence: true})
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	kept := map[string]bool{}
	for _, path := range result.Paths {
		kept[path.PathID] = true
	}
	for _, driver := range result.Drivers {
		for _, id := range driver.PathIDs {
			if !kept[id] {
				t.Fatalf("driver %s lost its path %s with no evidence ref to stand on", driver.DriverID, id)
			}
		}
	}
	if len(result.Paths) >= chaos6558PathCount {
		t.Fatalf("served all %d paths: the case did not exercise the lever", len(result.Paths))
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}
