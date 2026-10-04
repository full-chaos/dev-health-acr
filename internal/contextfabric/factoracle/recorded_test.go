package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const captureDir = "testdata/venue"

// repinEnv makes the recorded-mode test write its outcome as the pinned one
// (`make o4-oracle-repin`) instead of comparing with it.
const repinEnv = "ACR_O4_REPIN"

// loadedCapture reads the capture and proves it was made against the SDL
// this build pins: the shapes and the recorded replies belong to it, and a
// capture of another SDL is refused. The fact providers are not pinned by a
// version: they run in every test, and what they give is held against the
// pinned outcome.
func loadedCapture(t *testing.T) (Manifest, Recording, *Extract) {
	t.Helper()
	manifest, recording, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatalf("load capture: %v", err)
	}
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatalf("root policy: %v", err)
	}
	if got := policy.Catalogue().SchemaDigest(); got != manifest.SchemaDigest {
		t.Fatalf("the capture was recorded against SDL %s and this build pins %s: run `make o4-oracle-capture` on the venue", manifest.SchemaDigest, got)
	}
	if manifest.FixtureOrg != FixtureOrgID || manifest.OpsBuild == "" {
		t.Fatalf("the capture names no ops build or another fixture organization")
	}
	return manifest, recording, extract
}

func oracleFor(t *testing.T, manifest Manifest, planes Planes, reference *Extract) *Oracle {
	t.Helper()
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatalf("root policy: %v", err)
	}
	store, err := NewStore(reference)
	if err != nil {
		t.Fatalf("reference store: %v", err)
	}
	return &Oracle{Policy: policy, Planes: planes, Store: store, Window: manifest.Window, ShapeCases: manifest.ShapeCases,
		ListenerDark: manifest.ListenerDark, OperationDark: manifest.OperationDark}
}

func runOracle(t *testing.T, o *Oracle) *Report {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	report, err := o.Run(ctx)
	if err != nil {
		t.Fatalf("oracle run: %v", err)
	}
	return report
}

// recordedVenueRunReproducesTheVenue is the recorded mode: the replies
// of the real ops listener, replayed through the real graphql_query runner,
// against the real fact providers on the seeded extract of the same store.
// Its outcome must be the venue's outcome, root by root: the same shapes
// run, the same leaves, the same matches, the same named differences and the
// same findings.
func recordedVenueRunReproducesTheVenue(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	dark := darkOperations(t)
	manifest = withoutDarkRoots(t, manifest, dark)
	planes := localPlanes(t, seedStore(t, extract), &recording)
	oracle := oracleFor(t, manifest, planes, extract)
	report := runOracle(t, oracle)
	t.Logf("\n%s\n%s", report.Table(), report.Details())

	if os.Getenv(repinEnv) != "" {
		if err := Repin(captureDir, report, oracle.Residual); err != nil {
			t.Fatal(err)
		}
		t.Logf("pinned outcome written to %s", captureDir)
		return
	}
	if failures := planes.Listener.Failures(); len(failures) > 0 {
		t.Fatalf("the replay listener was asked for something it has no record of: %v", failures)
	}
	unused := planes.Unused()
	darkKeys := darkReplyKeys(recording, dark)
	if len(darkKeys) == 0 {
		t.Fatalf("the capture holds no recorded reply of a dark operation: the exclusion measured nothing")
	}
	unusedSet := map[string]bool{}
	var live []string
	for _, key := range unused {
		unusedSet[key] = true
		if !darkKeys[key] {
			live = append(live, key)
		}
	}
	for key := range darkKeys {
		if !unusedSet[key] {
			t.Fatalf("the recorded reply %s of a dark operation was replayed: the policy marks it as not served", key)
		}
	}
	if len(live) > 0 {
		t.Fatalf("%d recorded replies were never asked for (the run no longer makes the calls of the capture), first: %s", len(live), live[0])
	}
	if err := report.Err(); err != nil {
		t.Fatal(err)
	}
	policy := oracle.Policy
	if len(report.Roots) != len(policy.Roots()) || len(manifest.Expect) != len(policy.Roots()) {
		t.Fatalf("report has %d roots, the capture %d, the policy %d", len(report.Roots), len(manifest.Expect), len(policy.Roots()))
	}
	valueRoots := 0
	for _, rr := range report.Roots {
		want, ok := manifest.Expect[rr.Root]
		if !ok {
			t.Errorf("root %s is not in the capture", rr.Root)
			continue
		}
		got := Expectation(rr)
		sortFindings(got.Findings)
		sortFindings(want.Findings)
		sort.Strings(got.NotJoined)
		sort.Strings(want.NotJoined)
		// The values of a named difference are float sums: they are held to
		// the pinned ones within the tolerance of a reordered sum, everything
		// else exactly.
		if problem := differencesDiffer(got.Differences, want.Differences); problem != "" {
			t.Errorf("root %s: the named differences are not the pinned ones: %s", rr.Root, problem)
		}
		got.Differences, want.Differences = nil, nil
		if !reflect.DeepEqual(got, want) {
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			t.Errorf("root %s differs from the venue outcome\n got: %s\nwant: %s", rr.Root, g, w)
		}
		if rr.ShapesRun == 0 {
			t.Errorf("root %s ran no shape", rr.Root)
		}
		if rr.Mode == ModeValue && rr.Listener == "served" {
			valueRoots++
			if rr.Compared == 0 {
				t.Errorf("root %s is a value root and compared nothing", rr.Root)
			}
		}
	}
	if valueRoots != 5 {
		t.Fatalf("%d value roots were compared, want 5 (analytics, capacityForecasts, catalog, compoundingRisk, throughputForecast)", valueRoots)
	}
	for theme, want := range manifest.Residual {
		if got := oracle.Residual[theme]; math.Abs(got-want) > themeTolerance(got, want) {
			t.Errorf("investment residual of %s is %v, the venue gave %v", theme, got, want)
		}
	}
}

// differencesDiffer says how two pinned difference lists differ, "" when
// they are the same classes, keys and values.
func differencesDiffer(got, want []PinnedDifference) string {
	if len(got) != len(want) {
		return fmt.Sprintf("%d differences, the pinned outcome has %d", len(got), len(want))
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.Pair != w.Pair || g.Key != w.Key || g.Class != w.Class || g.Exact != w.Exact || len(g.Values) != len(w.Values) {
			return fmt.Sprintf("difference %d is %s/%s/%s, the pinned one %s/%s/%s", i, g.Pair, g.Class, g.Key, w.Pair, w.Class, w.Key)
		}
		for name, value := range g.Values {
			pinned, ok := w.Values[name]
			if !ok || math.Abs(value-pinned) > sumTolerance(value, pinned) {
				return fmt.Sprintf("difference %s/%s/%s: value %s is %v, the pinned one %v", g.Pair, g.Class, g.Key, name, value, pinned)
			}
		}
	}
	return ""
}

func sortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Pair != b.Pair {
			return a.Pair < b.Pair
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Detail < b.Detail
	})
}

// darkOperations are the operations the policy marks as not served because
// their root field is not enabled on the ops query service. The recorded
// capture is evidence from the real producer and stays as recorded; the test
// side excludes these operations and asserts they are never replayed.
func darkOperations(t *testing.T) map[string]bool {
	t.Helper()
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatalf("root policy: %v", err)
	}
	dark := map[string]bool{}
	for _, ns := range policy.Catalogue().NotServed() {
		if strings.Contains(ns.Reason, "root_field_not_enabled") {
			dark[ns.Name] = true
		}
	}
	if len(dark) == 0 {
		t.Fatal("the policy marks no operation as dark")
	}
	return dark
}

func isDarkCase(id string, dark map[string]bool) bool {
	parts := strings.Split(strings.SplitN(id, "#", 2)[0], "/")
	if len(parts) >= 2 && parts[0] == "run_operation" {
		return dark[parts[1]]
	}
	return len(parts) >= 2 && (dark[parts[0]] || dark[parts[1]])
}

func darkReplyKeys(recording Recording, dark map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key := range recording.Replies {
		if isDarkCase(key, dark) {
			out[key] = true
		}
	}
	return out
}

// withoutDarkRoots returns the manifest without the shape cases and the
// pinned outcome of a dark root. A root missing from the policy that is not
// dark is an error, never an exclusion.
func withoutDarkRoots(t *testing.T, manifest Manifest, dark map[string]bool) Manifest {
	t.Helper()
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatalf("root policy: %v", err)
	}
	cases := make([]ShapeCase, 0, len(manifest.ShapeCases))
	for _, c := range manifest.ShapeCases {
		root := strings.SplitN(c.ShapeID, "/", 2)[0]
		if _, allowed := policy.Root(root); allowed {
			cases = append(cases, c)
			continue
		}
		if !dark[root] {
			t.Fatalf("shape case %s names root %s, which the policy does not allow and does not mark dark", c.ShapeID, root)
		}
	}
	expect := map[string]RootExpectation{}
	for root, want := range manifest.Expect {
		if _, allowed := policy.Root(root); allowed {
			expect[root] = want
			continue
		}
		if !dark[root] {
			t.Fatalf("the capture pins root %s, which the policy does not allow and does not mark dark", root)
		}
	}
	manifest.ShapeCases, manifest.Expect = cases, expect
	return manifest
}

func TestRecordedCaptureKeepsTheDarkOperationAndTheTestExcludesIt(t *testing.T) {
	manifest, recording, _ := loadedCapture(t)
	dark := darkOperations(t)
	if got := len(darkReplyKeys(recording, dark)); got != 5 {
		t.Fatalf("the capture holds %d recorded replies of a dark operation, want 5", got)
	}
	filtered := withoutDarkRoots(t, manifest, dark)
	policy := mustPolicy(t)
	if len(filtered.Expect) != len(policy.Roots()) {
		t.Fatalf("filtered capture pins %d roots, the policy allows %d", len(filtered.Expect), len(policy.Roots()))
	}
	if len(manifest.Expect) != len(filtered.Expect)+len(dark) || len(manifest.ShapeCases) != len(filtered.ShapeCases)+4 {
		t.Fatalf("the recorded manifest was changed: %d pinned roots, %d shape cases", len(manifest.Expect), len(manifest.ShapeCases))
	}
}
