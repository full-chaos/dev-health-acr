package factoracle

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const captureDir = "testdata/venue"

// loadedCapture reads the capture once per test binary and proves it was
// made against this build's SDL and fact queries. A capture of another
// build is refused: it is replaced by a new capture, never trusted.
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
	if manifest.FactQueryVersion != devhealthfacts.QueryVersion {
		t.Fatalf("the capture was recorded against fact queries %s and this build is %s: run `make o4-oracle-capture` on the venue", manifest.FactQueryVersion, devhealthfacts.QueryVersion)
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
	return &Oracle{Policy: policy, Planes: planes, Store: store, Window: manifest.Window, ShapeCases: manifest.ShapeCases}
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

// TestRecordedVenueRunReproducesTheVenue is the recorded mode: the replies
// of the real ops listener, replayed through the real graphql_query runner,
// against the real fact providers on the seeded extract of the same store.
// Its outcome must be the venue's outcome, root by root: the same shapes
// run, the same leaves, the same matches, the same named differences and the
// same findings.
func TestRecordedVenueRunReproducesTheVenue(t *testing.T) {
	manifest, recording, extract := loadedCapture(t)
	planes := localPlanes(t, seedStore(t, extract), &recording)
	oracle := oracleFor(t, manifest, planes, extract)
	report := runOracle(t, oracle)
	t.Logf("\n%s\n%s", report.Table(), report.Details())

	if failures := planes.Listener.Failures(); len(failures) > 0 {
		t.Fatalf("the replay listener was asked for something it has no record of: %v", failures)
	}
	if unused := planes.Unused(); len(unused) > 0 {
		t.Fatalf("%d recorded replies were never asked for (the run no longer makes the calls of the capture), first: %s", len(unused), unused[0])
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
