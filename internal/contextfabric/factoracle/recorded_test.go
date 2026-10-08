package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const captureDir = "testdata/venue"

// captureSchemaDigest is the SDL digest the recorded capture was taken at
// (the venue at ops 5c9a3d32, pin 18).
const captureSchemaDigest = "sha256:54a0f7d6ee428bc2f8c8ef6329680c8efeb4be94a7b41bdc12655006069335e4"

// contractsAtCapture is contractDigest of every served operation under the SDL
// the capture was taken at. A build that pins another SDL may replay the
// capture only for the operations whose contract is still the one below; an
// operation that changed or is new belongs to a root in notRecordedRoots, or
// the capture is taken again on the venue.
var contractsAtCapture = map[string]string{
	"acrRepositoryScopes":            "sha256:43fa66efd5d4c5dfc3699253378000be29e86dee1640f22dff19f40a0eb68c6a",
	"capacityCompletionDistribution": "sha256:4deebbadf5ad5bd86c3f2be8bef3bbc1e622e110080038cf5359d02fe03dca02",
	"capacityForecast":               "sha256:f93d3cf5c8b5ecd443fbacaeeb7a21aa84a06d7177f748732c2c06a13a8478dc",
	"capacityForecasts":              "sha256:c08fb7e162371f005c8a151a2504b88a521e0f5ad855d96087848000821dc7bf",
	"catalogValues":                  "sha256:00ad023c9a2febf03c47ca10f24c66bdcc7916bac2e5ffc688a2a65d8e3d0258",
	"cognitiveLoad":                  "sha256:52ce7277e9e7033d638b213773e41b478f6cd9af0c540655998dad13dabc4dfb",
	"complexityTimeseries":           "sha256:5795ecd7c05b216647191e6eca4b6b397c4612a21e2225137590930bf1c65569",
	"compoundingRisk":                "sha256:77ac36ec578a347763cf97ea1946219d43481774b06878514f34c2f166042525",
	"home":                           "sha256:342fa4e571ab2bd02005a65a7fe68a3f2ae8c1fce6ab9b1fefaf5fc1a4bb48f6",
	"hotspots":                       "sha256:30999b82cd7e907ea1711e271b90aae84e66e60f9e93bbee835acf377cdbab23",
	"investmentBreakdown":            "sha256:8742ea2b7d3c3014c7a429a115108e167caf8a250230f5e59c16e3332f7b1749",
	"investmentFull":                 "sha256:179a09e19ce83802c67b1b40fe4c2c1b3c003617d4b49acca225146db3737111",
	"recommendations":                "sha256:2a2a5d0a1c1dcddae195d7c4d7d73517e4113694e6396620222c28a250003ee1",
	"securityOverview":               "sha256:df174e0fc36768a4385f9a9337e64f4e38f1a782704d8394e2e98253dccee9cb",
	"sourceHealth":                   "sha256:1e37288fae98f7a84a5f7a2a8d6bea26514c6e4bf284f8d9e528a7e0819ba876",
	"throughputForecast":             "sha256:ad5d774fa899197d84842001d2f9fcee48155c5356a27238e1d726e1aff4db51",
	"workGraphArtifacts":             "sha256:551ed4f4200e13f02a988d6deab6a9ffc41e41a2d2c72c9d66f224b0d5fe86fc",
	"workGraphEdges":                 "sha256:22f4a10da08615255631c5f866313fdeda9c65bd17a6b621f332984e61ce602e",
	"workGraphFlow":                  "sha256:d442757b8633c7f9542f48a92a34ef1dbcfacdd994e68c76ad7a31cce1428cf1",
	"workItemTeamAttributions":       "sha256:e3e1700f64773540a6192f8239068aac3b780a4d5cf37c5252f766bf819dda58",
}

func requireCaptureContractsUnchanged(t *testing.T, policy *directread.GraphQLPolicy, recorded, pinned string) {
	t.Helper()
	if recorded != captureSchemaDigest {
		t.Fatalf("the capture was recorded against SDL %s, not the %s this test knows it was taken at: it was changed", recorded, captureSchemaDigest)
	}
	rootsOf := map[string][]string{}
	for _, root := range policy.Roots() {
		for _, name := range root.Operations() {
			rootsOf[name] = append(rootsOf[name], root.Field)
		}
	}
	for _, name := range operationsWithChangedContract(policy, contractsAtCapture) {
		for _, root := range rootsOf[name] {
			if _, listed := notRecordedRoots[root]; !listed {
				t.Fatalf("the contract of %s changed since the capture (SDL %s, this build pins %s) and root %s is not listed as not recorded: run `make o4-oracle-capture` on the venue", name, recorded, pinned, root)
			}
		}
	}
	t.Logf("capture taken at SDL %s replays under pinned SDL %s: every recorded operation contract is the captured one", recorded, pinned)
}

// operationsWithChangedContract names the served operations whose contract
// digest is not the baseline's. A capture replays under any pinned SDL
// while this list is empty: the oracle compares operation documents, and an
// SDL field no document selects cannot change a reply.
func operationsWithChangedContract(policy *directread.GraphQLPolicy, baseline map[string]string) []string {
	var changed []string
	for _, op := range policy.Catalogue().Operations(directread.CallerUnrestricted) {
		if baseline[op.Name] != contractDigest(op) {
			changed = append(changed, op.Name)
		}
	}
	return changed
}

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
		requireCaptureContractsUnchanged(t, policy, manifest.SchemaDigest, got)
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
		ListenerDark: manifest.ListenerDark, OperationDark: manifest.OperationDark, DeniedTeams: manifest.DeniedTeams}
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
	manifest, skipped := withoutNotRecordedRoots(t, manifest)
	planes := localPlanes(t, seedStore(t, extract), &recording)
	oracle := oracleFor(t, manifest, planes, extract)
	oracle.OnlyRoots = rootsExcept(t, skipped)
	report := runOracle(t, oracle)
	t.Logf("\n%s\n%s", report.Table(), report.Details())

	if os.Getenv(repinEnv) != "" {
		if len(skipped) > 0 {
			t.Fatalf("a repin would drop the pinned outcome of the roots the capture does not record (%v): recapture on the venue instead", skipped)
		}
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
	if len(darkKeys) == 0 && len(darkCases(t, recording, dark)) > 0 {
		t.Fatalf("the capture holds cases of a dark operation and no recorded reply of it: the exclusion measured nothing")
	}
	unusedSet := map[string]bool{}
	var live []string
	for _, key := range unused {
		unusedSet[key] = true
		if !darkKeys[key] && !isNotRecordedKey(t, key, skipped) {
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
	if want := len(policy.Roots()) - len(skipped); len(report.Roots) != want || len(manifest.Expect) != want {
		t.Fatalf("report has %d roots, the capture %d, the policy %d less %d not recorded", len(report.Roots), len(manifest.Expect), len(policy.Roots()), len(skipped))
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
	if got := len(darkReplyKeys(recording, dark)); got != 0 {
		t.Fatalf("the capture holds %d recorded replies of a dark operation, want 0: the venue registry does not offer one", got)
	}
	filtered := withoutDarkRoots(t, manifest, dark)
	policy := mustPolicy(t)
	if len(filtered.Expect) != len(policy.Roots()) {
		t.Fatalf("filtered capture pins %d roots, the policy allows %d", len(filtered.Expect), len(policy.Roots()))
	}
	if len(manifest.Expect) != len(filtered.Expect) || len(manifest.ShapeCases) != len(filtered.ShapeCases) {
		t.Fatalf("the recorded manifest was changed: %d pinned roots, %d shape cases", len(manifest.Expect), len(manifest.ShapeCases))
	}
}

// notRecordedRoots are the roots the recorded capture cannot replay: a root
// whose registry document or shape the venue did not serve when the capture
// was taken. The capture of the venue at ops 5c9a3d32 records every root, so
// the list is empty; a root added here again must say why the capture lacks it.
var notRecordedRoots = map[string]string{}

// withoutNotRecordedRoots returns the manifest without the shape cases and
// the pinned outcome of a root the capture does not record, and the sorted
// names of those roots. The exclusion is checked, not assumed: the root must
// be allowed by the policy, and one of its generated shapes must have no case
// in the capture.
func withoutNotRecordedRoots(t *testing.T, manifest Manifest) (Manifest, []string) {
	t.Helper()
	policy := mustPolicy(t)
	shapes, err := Shapes(policy)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range manifest.ShapeCases {
		have[c.ShapeID] = true
	}
	var skipped []string
	for root, why := range notRecordedRoots {
		if _, allowed := policy.Root(root); !allowed {
			t.Fatalf("root %s is listed as not recorded (%s) but the policy does not allow it", root, why)
		}
		missing := false
		for _, shape := range shapes {
			if shape.Root == root && !have[shape.ID()] {
				missing = true
			}
		}
		if !missing {
			t.Fatalf("root %s is listed as not recorded, but the capture has a case for every shape of it: remove it from notRecordedRoots", root)
		}
		skipped = append(skipped, root)
		t.Logf("NOT RECORDED: root %s: %s", root, why)
	}
	sort.Strings(skipped)
	cases := make([]ShapeCase, 0, len(manifest.ShapeCases))
	for _, c := range manifest.ShapeCases {
		if _, drop := notRecordedRoots[strings.SplitN(c.ShapeID, "/", 2)[0]]; !drop {
			cases = append(cases, c)
		}
	}
	expect := map[string]RootExpectation{}
	for root, want := range manifest.Expect {
		if _, drop := notRecordedRoots[root]; !drop {
			expect[root] = want
		}
	}
	manifest.ShapeCases, manifest.Expect = cases, expect
	return manifest, skipped
}

func rootsExcept(t *testing.T, skipped []string) []string {
	t.Helper()
	var only []string
	for _, root := range mustPolicy(t).Roots() {
		if !slices.Contains(skipped, root.Field) {
			only = append(only, root.Field)
		}
	}
	return only
}

// isNotRecordedKey says whether a recorded reply belongs to a not recorded
// root: a graphql_query key starts with the root, a run_operation key with
// run_operation and the operation, which is matched against the operations
// of the skipped roots.
func isNotRecordedKey(t *testing.T, key string, skipped []string) bool {
	t.Helper()
	parts := strings.Split(strings.SplitN(key, "#", 2)[0], "/")
	if parts[0] != "run_operation" {
		return slices.Contains(skipped, parts[0])
	}
	policy := mustPolicy(t)
	for _, root := range skipped {
		if r, ok := policy.Root(root); ok && len(parts) > 1 && slices.Contains(r.Operations(), parts[1]) {
			return true
		}
	}
	return false
}

// The capture of the venue at ops 5c9a3d32 records every root: nothing is
// excluded, so a root added to notRecordedRoots has to be a root the capture
// really lacks.
func TestRecordedRunNamesTheRootsItDoesNotMeasure(t *testing.T) {
	manifest, recording, _ := loadedCapture(t)
	filtered, skipped := withoutNotRecordedRoots(t, manifest)
	if len(skipped) != 0 {
		t.Fatalf("not recorded roots = %v", skipped)
	}
	if len(manifest.Expect) != len(filtered.Expect) || len(manifest.ShapeCases) != len(filtered.ShapeCases) {
		t.Fatalf("the recorded manifest was changed: %d pinned roots, %d shape cases", len(manifest.Expect), len(manifest.ShapeCases))
	}
	for key := range recording.Replies {
		if isNotRecordedKey(t, key, skipped) {
			t.Fatalf("reply %s belongs to a not recorded root", key)
		}
	}
	if _, ok := manifest.Expect["capacityForecast"]; !ok {
		t.Fatal("the capture pins no outcome for capacityForecast")
	}
}

// darkCases are the shape cases of the manifest that belong to a dark operation.
func darkCases(t *testing.T, _ Recording, dark map[string]bool) []string {
	t.Helper()
	manifest, _, _ := loadedCapture(t)
	var out []string
	for _, c := range manifest.ShapeCases {
		if isDarkCase(c.ShapeID, dark) {
			out = append(out, c.ShapeID)
		}
	}
	return out
}
