package factoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// advancedPastCaptureProblems lists why an entry of advanced does not belong
// there: no case in the capture, not a served operation, or a contract that is
// the captured one (the capture was taken again, or the change was reverted).
func advancedPastCaptureProblems(policy *directread.GraphQLPolicy, cases []ShapeCase, baseline map[string]string, advanced map[string]string) []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(advanced)) {
		had := false
		for _, c := range cases {
			if parts := strings.SplitN(c.ShapeID, "/", 3); len(parts) >= 2 && parts[1] == name {
				had = true
			}
		}
		if !had {
			out = append(out, name+": the capture has no case for it (list it in notRecordedOperations)")
		}
		op, refusal := policy.Catalogue().Lookup(name)
		if refusal != nil || op == nil {
			out = append(out, name+": not a served operation")
			continue
		}
		if captured, ok := baseline[name]; !ok || captured == contractDigest(op) {
			out = append(out, fmt.Sprintf("%s: its contract is the captured one (%q): remove it from advancedPastCapture", name, captured))
		}
	}
	return out
}

func TestAdvancedPastCaptureEntriesAreRecordedAndAdvanced(t *testing.T) {
	manifest, _, _, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t)
	if problems := advancedPastCaptureProblems(policy, manifest.ShapeCases, captureBaseline(manifest), advancedPastCapture); len(problems) != 0 {
		t.Fatalf("advancedPastCapture: %v", problems)
	}
	// Each guard fails on the input it exists for.
	op, _ := policy.Catalogue().Lookup("investmentBreakdownTeam")
	recaptured := maps.Clone(contractsAtCapture)
	recaptured["investmentBreakdownTeam"] = contractDigest(op)
	for name, tc := range map[string]struct {
		cases    []ShapeCase
		baseline map[string]string
		advanced map[string]string
		want     string
	}{
		"capture taken again":  {manifest.ShapeCases, recaptured, map[string]string{"investmentBreakdownTeam": "x"}, "is the captured one"},
		"no case":              {casesWithout(manifest.ShapeCases, "investmentBreakdownTeam"), contractsAtCapture, map[string]string{"investmentBreakdownTeam": "x"}, "has no case"},
		"not served":           {manifest.ShapeCases, contractsAtCapture, map[string]string{"noSuchOperation": "x"}, "not a served operation"},
		"never in the capture": {manifest.ShapeCases, contractsAtCapture, map[string]string{"investmentFull": "x"}, "is the captured one"},
	} {
		t.Run(name, func(t *testing.T) {
			problems := advancedPastCaptureProblems(policy, tc.cases, tc.baseline, tc.advanced)
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), tc.want) {
				t.Fatalf("problems %v, want one naming %q", problems, tc.want)
			}
		})
	}
	// A recorded run sends no case of an advanced operation, and names each one as not recorded.
	for _, c := range withoutAdvancedCases(manifest.ShapeCases) {
		if parts := strings.SplitN(c.ShapeID, "/", 3); len(parts) >= 2 {
			if _, advanced := advancedPastCapture[parts[1]]; advanced {
				t.Fatalf("case %s of an advanced operation is still run", c.ShapeID)
			}
		}
	}
	for name, why := range advancedPastCapture {
		if got := recordedNotRecorded()[name]; got != why {
			t.Fatalf("%s is not named as not recorded: %q", name, got)
		}
	}
}

// A root every operation of which is advanced past the capture is skipped by
// name and stated as not measured; a root that keeps one operation that is
// not advanced is not skipped, and a missing case of that operation still
// stops the run.
func TestARootIsSkippedOnlyWhenEveryOperationIsAdvanced(t *testing.T) {
	policy := mustPolicy(t)
	// The capture is current: no root is skipped.
	if roots := advancedRoots(policy, advancedPastCapture); len(roots) != 0 {
		t.Fatalf("the current capture skips roots %v", roots)
	}
	// A root whose only operation is advanced is skipped by name.
	var lines []string
	for _, root := range advancedRoots(policy, map[string]string{"throughputForecast": "planted: advanced past the capture"}) {
		lines = append(lines, notMeasuredLine(root))
	}
	if want := []string{"throughputForecast: NOT MEASURED (advanced past capture; re-capture 14023)"}; !slices.Equal(lines, want) {
		t.Fatalf("not measured roots = %q, want %q", lines, want)
	}

	planted := map[string]string{"investmentBreakdownTeam": "planted: advanced past the capture"}
	if got := advancedRoots(policy, planted); len(got) != 0 {
		t.Fatalf("a root that keeps operations that are not advanced is skipped: %v", got)
	}
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	shapes, err := Shapes(policy)
	if err != nil {
		t.Fatal(err)
	}
	dropped := ""
	for _, shape := range shapes {
		if _, advanced := planted[shape.Operation]; shape.Root == "analytics" && !advanced {
			dropped = shape.ID()
			break
		}
	}
	if dropped == "" {
		t.Fatal("root analytics has no shape of an operation that is not advanced")
	}
	var cases []ShapeCase
	for _, c := range manifest.ShapeCases {
		parts := strings.SplitN(c.ShapeID, "/", 3)
		if _, advanced := planted[parts[1]]; c.ShapeID != dropped && !advanced {
			cases = append(cases, c)
		}
	}
	oracle := &Oracle{Policy: policy, Store: store, Window: manifest.Window, Today: captureDay(manifest), ShapeCases: cases, OnlyRoots: []string{"analytics"}, NotRecorded: planted,
		Planes: fakePlanes{graphQL: func(Shape, map[string]any) (json.RawMessage, error) {
			return json.RawMessage(`{"call":"operation_unavailable"}`), nil
		}}}
	if _, err := oracle.Run(context.Background()); err == nil || !strings.Contains(err.Error(), dropped+" has no case") {
		t.Fatalf("a root with one advanced operation and a missing case of another ran to %v", err)
	}
}

// advancedByBaseline is the advanced-past-capture set a baseline implies.
func advancedByBaseline(policy *directread.GraphQLPolicy, baseline map[string]string) map[string]string {
	out := map[string]string{}
	for _, name := range operationsPastBaseline(policy, baseline) {
		out[name] = "contract is not the baseline's"
	}
	return out
}

func notMeasuredLines(policy *directread.GraphQLPolicy, advanced map[string]string) []string {
	var lines []string
	for _, root := range advancedRoots(policy, advanced) {
		lines = append(lines, notMeasuredLine(root))
	}
	return lines
}

// A capture taken again at the current SDL records the current contracts, so
// the advancedPastCapture entries the old capture needed are refused.
func TestAFreshCaptureRetiresTheAdvancedEntries(t *testing.T) {
	manifest, _, _, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	policy := mustPolicy(t)
	fresh := manifest
	fresh.ContractDigests = contractDigestsOf(policy)
	problems := advancedPastCaptureProblems(policy, fresh.ShapeCases, captureBaseline(fresh), advancedPastCapture)
	joined := strings.Join(problems, "; ")
	for name := range advancedPastCapture {
		if !strings.Contains(joined, name+": its contract is the captured one") {
			t.Errorf("a fresh capture keeps the entry %s: %v", name, problems)
		}
	}
}

// The manifest of the 2026-11-03 capture holds no digests: contractsAtCapture
// judges it, and the recorded run leaves out the same roots as before.
func TestAManifestWithoutDigestsIsJudgedByContractsAtCapture(t *testing.T) {
	manifest, _, _, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.ContractDigests) != 0 {
		t.Skip("the capture on disk records its digests")
	}
	policy := mustPolicy(t)
	baseline := captureBaseline(manifest)
	if !maps.Equal(baseline, contractsAtCapture) {
		t.Fatal("a manifest without digests is not judged by contractsAtCapture")
	}
	want := notMeasuredLines(policy, advancedPastCapture)
	if got := notMeasuredLines(policy, advancedByBaseline(policy, baseline)); !slices.Equal(got, want) {
		t.Fatalf("not measured roots = %q, want %q", got, want)
	}
}

// One changed digest, or a missing one, takes the operation past the capture:
// a missing digest is never a match.
func TestAChangedOrMissingManifestDigestMakesTheRootNotMeasured(t *testing.T) {
	policy := mustPolicy(t)
	want := []string{notMeasuredLine("throughputForecast")}
	for name, edit := range map[string]func(map[string]string){
		"changed": func(d map[string]string) { d["throughputForecast"] = "sha256:0" },
		"missing": func(d map[string]string) { delete(d, "throughputForecast") },
	} {
		t.Run(name, func(t *testing.T) {
			digests := contractDigestsOf(policy)
			edit(digests)
			if got := notMeasuredLines(policy, advancedByBaseline(policy, captureBaseline(Manifest{ContractDigests: digests}))); !slices.Equal(got, want) {
				t.Fatalf("not measured roots = %q, want %q", got, want)
			}
		})
	}
	if got := notMeasuredLines(policy, advancedByBaseline(policy, contractDigestsOf(policy))); len(got) != 0 {
		t.Fatalf("a baseline equal to the contracts leaves roots out: %q", got)
	}
}

func TestTheManifestKeepsItsContractDigests(t *testing.T) {
	policy := mustPolicy(t)
	in := Manifest{ContractDigests: contractDigestsOf(policy)}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Manifest
	if err := decodeNumbered(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(in.ContractDigests) == 0 || !maps.Equal(in.ContractDigests, out.ContractDigests) {
		t.Fatalf("digests lost in the manifest round trip: %d in, %d out", len(in.ContractDigests), len(out.ContractDigests))
	}
}

// casesWithout returns the shape cases without those of operation.
func casesWithout(cases []ShapeCase, operation string) []ShapeCase {
	return slices.DeleteFunc(slices.Clone(cases), func(c ShapeCase) bool {
		parts := strings.SplitN(c.ShapeID, "/", 3)
		return len(parts) >= 2 && parts[1] == operation
	})
}
