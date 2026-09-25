package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func chaos6558OrderResult(driverEvidence [][]string, driverPaths [][]int, paths int) InvestigationResult {
	result := InvestigationResult{}
	for index := 0; index < paths; index++ {
		result.Paths = append(result.Paths, RelationshipPath{PathID: fmt.Sprintf("p%d", index)})
	}
	for index, cites := range driverPaths {
		driver := DriverJudgment{DriverID: fmt.Sprintf("d%d", index), EvidenceRefIDs: driverEvidence[index]}
		for _, path := range cites {
			driver.PathIDs = append(driver.PathIDs, fmt.Sprintf("p%d", path))
		}
		result.Drivers = append(result.Drivers, driver)
	}
	return result
}

// Uncited paths go first, from the end of the list; then cited paths whose
// EVERY citing driver keeps an evidence ref, from the end of the list.
func TestCHAOS6558PathDropOrder(t *testing.T) {
	t.Parallel()
	// p1 cited by d0 (has evidence); p3 cited by d0 and d1 (d1 has none);
	// p4 cited by d1 only (no evidence).
	result := chaos6558OrderResult([][]string{{"e0"}, {}}, [][]int{{1, 3}, {3, 4}}, 6)
	order, cited := pathDropOrder(result)
	if want := []int{5, 2, 0, 1}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v (uncited 5,2,0 then the one droppable cited path 1)", order, want)
	}
	if !cited["p1"] || !cited["p3"] || !cited["p4"] || cited["p0"] {
		t.Fatalf("cited = %v", cited)
	}
	dropped, citedDropped, ok := dropPaths(result, order, cited, 4)
	if !ok || citedDropped != 1 || len(dropped.Paths) != 2 {
		t.Fatalf("dropPaths: ok=%v cited=%d paths=%d", ok, citedDropped, len(dropped.Paths))
	}
	if !reflect.DeepEqual(dropped.Drivers[0].PathIDs, []string{"p3"}) || !reflect.DeepEqual(dropped.Drivers[1].PathIDs, []string{"p3", "p4"}) {
		t.Fatalf("driver path ids = %v / %v, want the dropped p1 removed from d0 only", dropped.Drivers[0].PathIDs, dropped.Drivers[1].PathIDs)
	}
	if len(result.Paths) != 6 || len(result.Drivers[0].PathIDs) != 2 {
		t.Fatalf("dropPaths wrote through to its input")
	}
}

// The Info line names which paths went, so an order regression is visible
// there and not only in the returned document.
func TestCHAOS6558PathDropInfoLineNamesDroppedPathIDs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	telemetry := NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	telemetry.RecordPathDrop(context.Background(), storage.Principal{OrgID: "org_1"}, PathDropEvent{DroppedPathIDs: []string{"path_b", "path_a"}, Served: true})
	var line struct {
		Msg            string   `json:"msg"`
		DroppedPathIDs []string `json:"dropped_path_ids"`
	}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	if line.Msg != "context fabric path drop" || strings.Join(line.DroppedPathIDs, ",") != "path_b,path_a" {
		t.Fatalf("path drop line = %s, want dropped_path_ids [path_b path_a] in drop order", buf.String())
	}
}
