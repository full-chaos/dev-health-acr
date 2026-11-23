package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
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
// (a synthetic organization on the isolated compose venue, ops 2c1fdfb1,
// captured 2026-11-20, docs/o4-venue.md).
const captureSchemaDigest = "sha256:7f8167568a634f87d77d4c28e9fb34efa17078cbff1b5d950b5e0e334d4f26b3"

// contractsAtCapture is contractDigest of every served operation under the SDL
// the capture was taken at. A build that pins another SDL may replay the
// capture only for the operations whose contract is still the one below; an
// operation that changed or is new belongs to a root in notRecordedRoots, or
// the capture is taken again on the venue.
var contractsAtCapture = map[string]string{
	"acrRepositoryScopes":            "sha256:43fa66efd5d4c5dfc3699253378000be29e86dee1640f22dff19f40a0eb68c6a",
	"capacityCompletionDistribution": "sha256:be86b79ee399d54ff8d2e22d77c3a3ef260003daaac51b5e8c66f490c7889d6e",
	"capacityForecast":               "sha256:d819e0dba5f5b07865dd70702f3e0a8aba74c49b68ac387904f74b829d3b970c",
	"capacityForecasts":              "sha256:c08fb7e162371f005c8a151a2504b88a521e0f5ad855d96087848000821dc7bf",
	"catalogValues":                  "sha256:00ad023c9a2febf03c47ca10f24c66bdcc7916bac2e5ffc688a2a65d8e3d0258",
	"cognitiveLoad":                  "sha256:52ce7277e9e7033d638b213773e41b478f6cd9af0c540655998dad13dabc4dfb",
	"complexityTimeseries":           "sha256:5795ecd7c05b216647191e6eca4b6b397c4612a21e2225137590930bf1c65569",
	"compoundingRisk":                "sha256:ded079a60583d070f41a5d5682bd77c32b22923d21843c2c9f2bdfe1ce0360e2",
	"home":                           "sha256:518dd7b5e00d9ab67289ce26a8dfe601dc961d971883fc05c2977929fbc2c525",
	"hotspots":                       "sha256:30999b82cd7e907ea1711e271b90aae84e66e60f9e93bbee835acf377cdbab23",
	"investmentBreakdown":            "sha256:dcaf3bfe8899a3ba1fff623bb9dff5380212d515c51f073c1f2eb670f1c353df",
	"investmentBreakdownRepository":  "sha256:7eedeea132fc8a31c8c1f2c6af506b587119aa75ece405425df15abff43b2026",
	"investmentBreakdownTeam":        "sha256:67f8a9744d92d26d7363e88b2c1189a090fa329d0f00813adaaf0e298a32dd02",
	"investmentFull":                 "sha256:961a15df3b4a1ee6ecd80c0a66a7d856256e97b475039d58d08fbfcd82176dd3",
	"recommendations":                "sha256:2a2a5d0a1c1dcddae195d7c4d7d73517e4113694e6396620222c28a250003ee1",
	"securityOverview":               "sha256:df174e0fc36768a4385f9a9337e64f4e38f1a782704d8394e2e98253dccee9cb",
	"sourceHealth":                   "sha256:1e37288fae98f7a84a5f7a2a8d6bea26514c6e4bf284f8d9e528a7e0819ba876",
	"teamWorkItemBreakdown":          "sha256:3676af7fcae8d1fdc031fae2141b9e44e10b5dcea313b298ddb7f8c8aa798c32",
	"throughputForecast":             "sha256:080099573a1f73dd8f739ed711627f037cddc166b3e2bdc5ba97ada243d0c24f",
	"workGraphArtifacts":             "sha256:551ed4f4200e13f02a988d6deab6a9ffc41e41a2d2c72c9d66f224b0d5fe86fc",
	"workGraphEdges":                 "sha256:22f4a10da08615255631c5f866313fdeda9c65bd17a6b621f332984e61ce602e",
	"workGraphFlow":                  "sha256:d442757b8633c7f9542f48a92a34ef1dbcfacdd994e68c76ad7a31cce1428cf1",
	"workItemTeamAttributions":       "sha256:e3e1700f64773540a6192f8239068aac3b780a4d5cf37c5252f766bf819dda58",
}

// advancedPastCapture are the operations whose contract moved after the
// capture: the recorded replies answer an older document. A recorded run
// leaves their cases out and states each one on its root (Oracle.NotRecorded),
// so it never replays a reply to a request it did not record. Each entry must
// have a case in the capture and a contract that is not the one in
// contractsAtCapture (TestAdvancedPastCaptureEntriesAreRecordedAndAdvanced), so
// the list cannot outlive a capture taken again.
var advancedPastCapture = map[string]string{
	"capacityForecast":   "the capture predates the backlogState and backlogAsOfDay selection of the document: its recorded replies answer the older document",
	"capacityForecasts":  "the capture predates the backlogState and backlogAsOfDay selection of the document: its recorded replies answer the older document",
	"throughputForecast": "the capture predates the wipCongestion, estimateCoverage and staleWip read-state selection of the document: its recorded replies answer the older document",
}

// recordedNotRecorded is Oracle.NotRecorded of a recorded run: the operations
// the capture has no case for and the operations advanced past it.
func recordedNotRecorded() map[string]string {
	out := maps.Clone(notRecordedOperations)
	maps.Copy(out, advancedPastCapture)
	return out
}

// withoutAdvancedCases returns the shape cases of the capture without those of
// the operations advanced past it.
func withoutAdvancedCases(cases []ShapeCase) []ShapeCase {
	return slices.DeleteFunc(slices.Clone(cases), func(c ShapeCase) bool {
		parts := strings.SplitN(c.ShapeID, "/", 3)
		if len(parts) < 2 {
			return false
		}
		_, advanced := advancedPastCapture[parts[1]]
		return advanced
	})
}

// captureBaseline is the contract digest per operation the capture was taken
// at: the manifest's own, or contractsAtCapture for a manifest that records none.
func captureBaseline(manifest Manifest) map[string]string {
	if len(manifest.ContractDigests) > 0 {
		return manifest.ContractDigests
	}
	return contractsAtCapture
}

func requireCaptureContractsUnchanged(t *testing.T, policy *directread.GraphQLPolicy, manifest Manifest, pinned string) {
	t.Helper()
	recorded := manifest.SchemaDigest
	if len(manifest.ContractDigests) == 0 && recorded != captureSchemaDigest {
		t.Fatalf("the capture was recorded against SDL %s, not the %s this test knows it was taken at: it was changed", recorded, captureSchemaDigest)
	}
	rootsOf := map[string][]string{}
	for _, root := range policy.Roots() {
		for _, name := range root.Operations() {
			rootsOf[name] = append(rootsOf[name], root.Field)
		}
	}
	for _, name := range operationsWithChangedContract(policy, captureBaseline(manifest)) {
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
	return slices.DeleteFunc(operationsPastBaseline(policy, baseline), func(name string) bool {
		_, advanced := advancedPastCapture[name]
		return advanced
	})
}

// operationsPastBaseline names the served operations, not listed as not
// recorded, whose contract digest is not the baseline's. An operation the
// baseline does not hold is one of them: a missing digest is never a match.
func operationsPastBaseline(policy *directread.GraphQLPolicy, baseline map[string]string) []string {
	var changed []string
	for _, op := range policy.Catalogue().Operations(directread.CallerUnrestricted) {
		if _, notRecorded := notRecordedOperations[op.Name]; notRecorded {
			continue
		}
		if digest, ok := baseline[op.Name]; !ok || digest != contractDigest(op) {
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
		requireCaptureContractsUnchanged(t, policy, manifest, got)
	}
	if problems := advancedPastCaptureProblems(policy, manifest.ShapeCases, captureBaseline(manifest), advancedPastCapture); len(problems) != 0 {
		t.Fatalf("advancedPastCapture does not match the capture: %v", problems)
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
	return &Oracle{Policy: policy, Planes: planes, Store: store, Window: manifest.Window, Today: captureDay(manifest), ShapeCases: withoutAdvancedCases(manifest.ShapeCases), NotRecorded: recordedNotRecorded(),
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
	manifest, advanced := withoutAdvancedRoots(t, manifest)
	skipped = append(skipped, advanced...)
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
	advancedUnused := map[string]int{}
	for _, key := range unused {
		unusedSet[key] = true
		if op := advancedKeyOperation(key); op != "" {
			advancedUnused[op]++
			continue
		}
		if !darkKeys[key] && !isNotRecordedKey(t, key, skipped) {
			live = append(live, key)
		}
	}
	// The exclusion is measured: every advanced operation has recorded replies, and none was replayed.
	for key := range recording.Replies {
		if op := advancedKeyOperation(key); op != "" && !unusedSet[key] {
			t.Fatalf("the recorded reply %s of %s, which is advanced past the capture, was replayed", key, op)
		}
	}
	for op := range advancedPastCapture {
		if advancedUnused[op] == 0 {
			t.Fatalf("the capture holds no recorded reply of %s, which is listed as advanced past it: the exclusion measured nothing", op)
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
	measured := slices.DeleteFunc([]string{"analytics", "capacityForecasts", "catalog", "compoundingRisk", "throughputForecast"}, func(root string) bool {
		return slices.Contains(advanced, root)
	})
	if valueRoots != len(measured) {
		t.Fatalf("%d value roots were compared, want %d (%s; not measured: %v)", valueRoots, len(measured), strings.Join(measured, ", "), advanced)
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

// notRecordedOperations are the operations every recorded-mode run leaves out
// (Oracle.NotRecorded): every generated shape of each, and nothing else.
// Their root runs with its other candidates. The oracle refuses a listed
// operation that has a case, so the list cannot outlive a re-capture. The
// capture records every operation, so the list is empty; an operation added
// here again must say why the capture lacks it.
var notRecordedOperations = map[string]string{}

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

// recaptureTicket re-captures the venue, so the roots advanced past the
// capture are measured again.
const recaptureTicket = "14023"

// advancedRoots are the roots every operation of which is in advanced, and the
// roots whose window probe (temporaryAllowances) runs on an advanced operation:
// a recorded run has no case for any of them. A root that keeps one operation
// not in advanced and probes no advanced one is not one: it runs, and a missing
// case of it still stops the run.
func advancedRoots(policy *directread.GraphQLPolicy, advanced map[string]string) []string {
	var out []string
	for _, root := range policy.Roots() {
		ops := root.Operations()
		if len(ops) == 0 {
			continue
		}
		all := true
		for _, op := range ops {
			if _, ok := advanced[op]; !ok {
				all = false
				break
			}
		}
		if allowance, ok := temporaryAllowances[root.Field]; ok && allowance.Window != "" {
			if _, probed := advanced[allowance.Operation]; probed {
				all = true
			}
		}
		if all {
			out = append(out, root.Field)
		}
	}
	sort.Strings(out)
	return out
}

// skippedUnadvancedOperations are the operations that are not advanced past the
// capture but sit on a root the recorded run skips (the root's window probe runs
// on an advanced operation). Their recorded replies are not replayed.
func skippedUnadvancedOperations(policy *directread.GraphQLPolicy, advanced map[string]string) []string {
	skipped := advancedRoots(policy, advanced)
	var out []string
	for _, root := range policy.Roots() {
		if !slices.Contains(skipped, root.Field) {
			continue
		}
		for _, op := range root.Operations() {
			if _, ok := advanced[op]; !ok {
				out = append(out, op)
			}
		}
	}
	sort.Strings(out)
	return out
}

// notMeasuredLine is how a recorded run states a root it skips because every
// operation of it is advanced past the capture.
func notMeasuredLine(root string) string {
	return fmt.Sprintf("%s: NOT MEASURED (advanced past capture; re-capture %s)", root, recaptureTicket)
}

// withoutAdvancedRoots returns the manifest without the shape cases and the
// pinned outcome of the roots every operation of which is advanced past the
// capture, and the sorted names of those roots, each stated by name as not
// measured. Such a root is not coverage: it is left out of the run and of
// every count, never read as a pass.
func withoutAdvancedRoots(t *testing.T, manifest Manifest) (Manifest, []string) {
	t.Helper()
	roots := advancedRoots(mustPolicy(t), advancedPastCapture)
	for _, root := range roots {
		t.Log(notMeasuredLine(root))
	}
	for _, op := range skippedUnadvancedOperations(mustPolicy(t), advancedPastCapture) {
		t.Logf("%s: NOT MEASURED (its root is skipped with the advanced window-probe operation; re-capture %s)", op, recaptureTicket)
	}
	cases := make([]ShapeCase, 0, len(manifest.ShapeCases))
	for _, c := range manifest.ShapeCases {
		if !slices.Contains(roots, strings.SplitN(c.ShapeID, "/", 2)[0]) {
			cases = append(cases, c)
		}
	}
	expect := map[string]RootExpectation{}
	for root, want := range manifest.Expect {
		if !slices.Contains(roots, root) {
			expect[root] = want
		}
	}
	manifest.ShapeCases, manifest.Expect = cases, expect
	return manifest, roots
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
// advancedKeyOperation is the operation of a recorded reply key (a shape key
// root/operation/shape#vars or a run_operation key run_operation/operation/...)
// when that operation is advanced past the capture, else "".
func advancedKeyOperation(key string) string {
	parts := strings.Split(strings.SplitN(key, "#", 2)[0], "/")
	if len(parts) < 2 {
		return ""
	}
	if _, advanced := advancedPastCapture[parts[1]]; advanced {
		return parts[1]
	}
	return ""
}

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

// The capture of the venue records every root: nothing is excluded, so a root
// added to notRecordedRoots has to be a root the capture really lacks. The
// ops side of root analytics is compared again on COUNT: the root
// runs in full.
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

// The capture of CHAOS-12808 records the scoped investment breakdowns
// (CHAOS-12698): every generated shape of root analytics has a case, those of
// investmentBreakdownTeam and investmentBreakdownRepository included, and each
// of their cases has a recorded reply. The oracle still refuses an operation
// listed as not recorded once one of its shapes has a case.
func TestTheCaptureRecordsTheScopedInvestmentShapes(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	for name := range notRecordedOperations {
		if name != "teamWorkItemBreakdown" {
			t.Fatalf("not recorded operation %s: the capture records every operation but teamWorkItemBreakdown (CHAOS-13001)", name)
		}
	}
	policy := mustPolicy(t)
	shapes, err := Shapes(policy)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]int{}
	for _, c := range manifest.ShapeCases {
		cases[c.ShapeID]++
	}
	scoped := map[string]int{}
	for _, shape := range shapes {
		if shape.Root != "analytics" {
			continue
		}
		if _, notRecorded := notRecordedOperations[shape.Operation]; notRecorded {
			continue
		}
		if cases[shape.ID()] == 0 {
			t.Fatalf("shape %s has no case: it would not run", shape.ID())
		}
		if shape.Operation == "investmentBreakdownTeam" || shape.Operation == "investmentBreakdownRepository" {
			scoped[shape.Operation] += cases[shape.ID()]
		}
	}
	if scoped["investmentBreakdownTeam"] == 0 || scoped["investmentBreakdownRepository"] == 0 {
		t.Fatalf("scoped cases in the capture = %v, want cases of both operations", scoped)
	}
	for _, c := range manifest.ShapeCases {
		shape, ok := ShapeByID(shapes, c.ShapeID)
		if !ok {
			t.Fatalf("case %s has no shape", c.ShapeID)
		}
		if shape.Operation != "investmentBreakdownTeam" && shape.Operation != "investmentBreakdownRepository" {
			continue
		}
		if _, ok := recording.Replies[CaseKey(shape, c.Variables)]; !ok {
			t.Fatalf("case %s has no recorded reply", c.ShapeID)
		}
	}

	// A recorded case of a listed operation stops the run.
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	data := `{"analytics":{"__typename":"AnalyticsResult","breakdowns":[],"evidenceQualityDistribution":{"low":1},"evidenceQualityStats":{"__typename":"EvidenceQualityStats","mean":0.4,"stddev":0.2,"total":10,"bandCounts":{"low":1},"uncategorizedCount":0}}}`
	listed := &Oracle{Policy: policy, Store: store, Window: manifest.Window, Today: captureDay(manifest), ShapeCases: manifest.ShapeCases, OnlyRoots: []string{"analytics"},
		NotRecorded: map[string]string{"investmentBreakdownTeam": "planted: listed although the capture records it"},
		Planes: fakePlanes{
			graphQL: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
				return servedAnswer(t, "analytics", shape.Operation, data, len(data), 32768), nil
			},
			operation: func(shape Shape, _ map[string]any) (json.RawMessage, error) {
				return operationAnswer(t, shape.Operation, data), nil
			},
		}}
	if _, err := listed.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "is listed as not recorded") {
		t.Fatalf("a recorded case of a listed operation did not stop the run: %v", err)
	}
}
