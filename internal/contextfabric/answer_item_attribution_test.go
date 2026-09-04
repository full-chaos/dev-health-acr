package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The per-bucket split of the item budget must reach the EMITTED LINE.
//
// THESE TESTS DRIVE Engine.Investigate AND READ WHAT THE ENGINE ACTUALLY
// LOGGED, and that is the whole design. A test that calls
// AttributeContextFabricResultItems and checks its arithmetic proves the
// function; it proves nothing about whether an operator ever sees the numbers.
// This seam's review history is four consecutive findings of exactly that
// shape -- a value computed, written to a struct, and read by nobody -- so the
// assertion is on the formatted log record, one step past the last place the
// mistake can hide.
//
// RED AT THE FIX PARENT (7c6eda59) on the assertion, not on a build error:
// this file names no symbol the change introduces. At the parent the engine
// emits the line without any attribution_* dimension, and
// attributionFieldsOf's "carries no attribution_%s at all" branch fires.

// attributionEvidenceRef is the single evidence reference every fixture item
// cites, so the result's own referential closure holds.
const attributionEvidenceRef = "acr:v1:work-item:linear:ATTR-1"

// attributionCohort builds a cohort of n members split across two groups, so
// an item can be about a member, about one group, or about both.
func attributionCohort(members int) *Cohort {
	cohort := budgetStageCohort(members)
	first := cohort.Members[0].Subject.CanonicalID
	rest := []string{}
	for _, member := range cohort.Members[1:] {
		rest = append(rest, member.Subject.CanonicalID)
	}
	cohort.Groups = []contractsv1.ContextFabricCohortGroup{
		{Subject: attributionGroupRef(0), MemberCanonicalIDs: []string{first}, Complete: true, Total: 1},
		{Subject: attributionGroupRef(1), MemberCanonicalIDs: rest, Complete: true, Total: len(rest)},
	}
	return cohort
}

// attributionGroupRef names one of the two groups attributionCohort built.
func attributionGroupRef(index int) SubjectRef {
	id := "group_" + strconv.Itoa(index)
	return SubjectRef{Kind: SubjectTeam, CanonicalID: id, Label: id}
}

// attributionFixtureCounts is the expectation, derived from what the fixture
// PUTS IN rather than from the production attribution function.
//
// Deriving it from AttributeContextFabricResultItems would let the same defect
// decide both sides of the comparison.
type attributionFixtureCounts struct {
	global     int
	member     int
	group      int
	multiGroup int
}

func (c attributionFixtureCounts) total() int {
	return c.global + c.member + c.group + c.multiGroup
}

func (c attributionFixtureCounts) byBucket() map[string]int {
	return map[string]int{"global": c.global, "member": c.member, "group": c.group, "multi_group": c.multiGroup}
}

// assertPairwiseDistinct is a FIXTURE PRECONDITION, and it earns its place
// twice over.
//
// Two buckets expected to hold the same count cannot detect a swap between
// them, and an adversarial round exploited exactly that gap on the arms whose
// assertion was a sum: moving the member count into the global bucket and
// zeroing member left the total unchanged and every test green. Distinct
// expected values are what make the per-bucket assertions below able to see a
// redistribution, so the fixture is required to provide them and says which
// two clash when it does not.
func (c attributionFixtureCounts) assertPairwiseDistinct(t *testing.T, context string) {
	t.Helper()
	seen := map[int]string{}
	for name, value := range c.byBucket() {
		if value == 0 {
			t.Fatalf("%s: the fixture puts nothing in the %s bucket, so a line that dropped it would pass", context, name)
		}
		if other, clash := seen[value]; clash {
			t.Fatalf("%s: buckets %s and %s both expect %d -- a redistribution between them preserves the "+
				"total and would pass unnoticed. Change the fixture spec for this arm.", context, name, other, value)
		}
		seen[value] = name
	}
}

// attributionFixtureSpec declares how many items of each kind the fixture's
// synthesis returns.
//
// It is a PER-ARM input rather than one shared constant because each arm
// measures a different document -- the retry arms measure the pre-narrowing
// result, the retry-still-over arm the re-synthesized one -- so the member
// count differs, and the four expected values have to be pairwise distinct on
// every arm separately.
type attributionFixtureSpec struct {
	members           int
	globalFindings    int
	groupDrivers      int
	multiGroupDrivers int
	memberDrivers     int
	// candidates is how many resolution candidates the resolver PROPOSES.
	// They charge the global bucket, and they are the only thing the outcome
	// layer's reduction may cut -- which is what makes the fifth arm
	// reachable at all.
	candidates int
}

// expect is what the split must be for a document carrying membersMeasured
// cohort rows.
//
// Arithmetic over the fixture's OWN literals. It never calls
// AttributeContextFabricResultItems, so the expectation and the value under
// test cannot share a defect; and membersMeasured comes from the arm's own
// emitted line (its before/after counts), not from a number written here.
func (s attributionFixtureSpec) expect(membersMeasured, candidatesInDocument int) attributionFixtureCounts {
	return attributionFixtureCounts{
		// Resolution candidates are global items. On the arm where the
		// reduction CUT some, the survivors are what the served document
		// carries -- so this takes the count from the line, not from the
		// number the resolver proposed.
		global: s.globalFindings + candidatesInDocument,
		// The cohort member ROWS plus the drivers about a member. The rows
		// are the item class the earlier design of this seam charged and
		// never accounted for, so they are counted explicitly.
		member:     membersMeasured + s.memberDrivers,
		group:      s.groupDrivers,
		multiGroup: s.multiGroupDrivers,
	}
}

// defaultAttributionSpec is the shape the served and refusal tests use.
func defaultAttributionSpec() attributionFixtureSpec {
	return attributionFixtureSpec{members: 3, globalFindings: 5, groupDrivers: 3, multiGroupDrivers: 2, memberDrivers: 1}
}

// attributionEngine builds an engine whose synthesis returns a result with a
// KNOWN item in every bucket, logging through a real slog handler so a test
// can read the formatted line.
//
// It builds its own synthesizer rather than reusing budgetStageEngine's
// because that one emits claims only: every charged item lands in the member
// bucket, three of the four dimensions read zero, and a test over four zeros
// cannot tell a correct split from a dropped one.
func attributionEngine(t *testing.T, spec attributionFixtureSpec, sink *bytes.Buffer, options EngineOptions, synthesisCohortSizes *[]int) (*Engine, attributionFixtureSpec) {
	t.Helper()
	if synthesisCohortSizes == nil {
		synthesisCohortSizes = &[]int{}
	}

	cohort := attributionCohort(spec.members)
	memberRef := cohort.Members[0].Subject
	stranger := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_unowned", Label: "repo_unowned"}

	// Every driver and finding is a VALID v1 document, because the served
	// arm of this test runs the engine's own validation: a fixture that
	// cannot be served proves nothing about what a served line carries.
	// Category "relationship" is deliberate -- it is outside the
	// category->FactKind table, so these items need no claimed fact and the
	// fixture's item counts stay exactly what this function writes.
	driver := func(id string, subjects []SubjectRef) DriverJudgment {
		return DriverJudgment{
			DriverID: id, Standing: DriverPrincipal, Category: "relationship",
			Title: "Attribution fixture driver", Summary: "A driver supplied by the attribution fixture.",
			AffectedSubjects: subjects, EvidenceRefIDs: []string{attributionEvidenceRef},
			Derivation: DerivationCanonicalStructured, EpistemicStatus: EpistemicObserved,
			Confidence: 0.9, Current: true,
		}
	}
	finding := func(id string, subjects []SubjectRef) Finding {
		return Finding{
			FindingID: id, Kind: "relationship", Summary: "A finding supplied by the attribution fixture.",
			Subjects: subjects, EvidenceRefIDs: []string{attributionEvidenceRef},
		}
	}
	drivers := []DriverJudgment{}
	for index := 0; index < spec.groupDrivers; index++ {
		// group: exactly ONE group named. Alternating which group keeps
		// this from being a single-group special case.
		drivers = append(drivers, driver("d_group_"+strconv.Itoa(index), []SubjectRef{attributionGroupRef(index % 2)}))
	}
	for index := 0; index < spec.multiGroupDrivers; index++ {
		// multi_group: both groups named. The order alternates so a rule
		// that keyed on first-named rather than on distinct-count would
		// show up as an inconsistency rather than a uniform shift.
		refs := []SubjectRef{attributionGroupRef(0), attributionGroupRef(1)}
		if index%2 == 1 {
			refs = []SubjectRef{attributionGroupRef(1), attributionGroupRef(0)}
		}
		drivers = append(drivers, driver("d_multi_"+strconv.Itoa(index), refs))
	}
	for index := 0; index < spec.memberDrivers; index++ {
		drivers = append(drivers, driver("d_member_"+strconv.Itoa(index), []SubjectRef{memberRef}))
	}
	findings := []Finding{}
	for index := 0; index < spec.globalFindings; index++ {
		// global: the first names a subject the cohort does not contain
		// (the fail-safe direction), the rest name nothing at all.
		var subjects []SubjectRef
		if index == 0 {
			subjects = []SubjectRef{stranger}
		}
		findings = append(findings, finding("f_global_"+strconv.Itoa(index), subjects))
	}

	graphCohort := cohort
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{
				Shape: ShapeDiscoveredCohort, RequestedJudgment: "status",
				TimeContext:      TimeContext{Axis: TemporalCurrent},
				FactRequirements: []FactRequirement{{Kind: FactStatus}},
			}, nil
		}),
		Graph: &capturingGraphReader{
			resolution: SubjectResolution{Candidates: outcomeAssemblyCandidates(spec.candidates), Committed: []SubjectRef{}},
			context: GraphContext{
				Cohort: graphCohort,
				Paths:  []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
				FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
				Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
			},
		},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(_ context.Context, _ storage.Principal, input SynthesisInput) (InvestigationResult, error) {
			// RECORD THE COHORT SYNTHESIS WAS GIVEN, per call.
			//
			// This is the seam that makes the retry arm's member count
			// knowable independently. That arm serves no document, so its
			// measured cohort used to be read off the emitted line -- and a
			// review showed a mutant could move the line's member count and
			// its total together and stay inside a bounded check. What
			// synthesis was HANDED is observed here, before the event
			// exists, so no edit to the emitter can forge it.
			members := 0
			if input.Graph.Cohort != nil {
				members = len(input.Graph.Cohort.Members)
			}
			*synthesisCohortSizes = append(*synthesisCohortSizes, members)
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Fine.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: drivers, RemainingWork: findings,
				ReadinessGaps: []Finding{}, Paths: []RelationshipPath{}, Conflicts: []Finding{},
				Limitations: []string{}, EvidenceRefIDs: []string{attributionEvidenceRef}, ClaimedFacts: []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Fine, based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Telemetry: NewSlogEngineTelemetry(slog.New(slog.NewTextHandler(sink, nil))),
	}, options)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, spec
}

// assembledResultLine returns the single assembled-result plan-narrowing line.
//
// The stage selector is not decoration. One request emits several
// plan-narrowing events -- cardinality, synthesis_input and assembled_result --
// and only the last one has measured a document, so a selector that matched on
// the message alone would read a line whose measurement fields are all zero
// and call the absence a pass.
func assembledResultLine(t *testing.T, emitted string) string {
	t.Helper()
	matches := []string{}
	for _, line := range strings.Split(emitted, "\n") {
		if strings.Contains(line, "context fabric plan narrowing") && strings.Contains(line, "stage=assembled_result") {
			matches = append(matches, line)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly 1 assembled_result plan-narrowing line, got %d.\nemitted:\n%s", len(matches), emitted)
	}
	return matches[0]
}

var attributionFieldPattern = regexp.MustCompile(`attribution_(global|member|multi_group|group)=(-?\d+)`)

var measuredItemsPattern = regexp.MustCompile(`measured_items=(-?\d+)`)

// attributionFieldsOf reads the four bucket dimensions off an emitted line and
// FAILS when any of them is absent -- absence is the defect, not a zero.
func attributionFieldsOf(t *testing.T, line string) map[string]int {
	t.Helper()
	fields := map[string]int{}
	for _, match := range attributionFieldPattern.FindAllStringSubmatch(line, -1) {
		value, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("attribution_%s is not an integer on the emitted line: %v", match[1], err)
		}
		fields[match[1]] = value
	}
	for _, name := range []string{"global", "member", "group", "multi_group"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("the emitted line carries no attribution_%s at all -- an operator reading this "+
				"line can see how many items were charged and not what any of them were about.\nline: %s", name, line)
		}
	}
	return fields
}

func measuredItemsOf(t *testing.T, line string) int {
	t.Helper()
	match := measuredItemsPattern.FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the emitted line carries no measured_items: %s", line)
	}
	value, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("measured_items is not an integer: %v", err)
	}
	return value
}

// TestTheServedAnswerLineSaysWhatItsChargedItemsWereAbout is the harm
// assertion: an operator reading the line the engine emitted for a SERVED
// answer can see the per-bucket split of the items it charged.
func TestTheServedAnswerLineSaysWhatItsChargedItemsWereAbout(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	spec := defaultAttributionSpec()
	engine, _ := attributionEngine(t, spec, &sink, budgetStageOptions(200, 0), nil)
	want := spec.expect(spec.members, spec.candidates)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v, want a served answer", err)
	}

	// The fixture's own claim FIRST. The expectation below is counted off
	// the synthesizer literal, so it is only true while nothing else adds
	// or drops items on the way to the served document -- narration mints
	// drivers and claims on some cohorts. If that ever starts happening
	// here, this test must be re-derived rather than have its numbers
	// nudged, and this is where it says so.
	wantDrivers := spec.groupDrivers + spec.multiGroupDrivers + spec.memberDrivers
	if got := len(result.Drivers); got != wantDrivers {
		t.Fatalf("the served result carries %d drivers, want the %d the fixture supplied: "+
			"something between synthesis and assembly is adding or dropping items, so the "+
			"expectation below no longer describes this answer", got, wantDrivers)
	}
	if got := len(result.ClaimedFacts); got != 0 {
		t.Fatalf("the served result carries %d claimed facts, want 0: same reason as above", got)
	}
	want.assertPairwiseDistinct(t, "served arm")

	line := assembledResultLine(t, sink.String())
	fields := attributionFieldsOf(t, line)

	for name, wantCount := range want.byBucket() {
		if fields[name] != wantCount {
			t.Errorf("attribution_%s = %d, want %d (counted off the fixture, not recomputed)\nline: %s",
				name, fields[name], wantCount, line)
		}
	}

	// The split must describe the SAME document the count beside it
	// describes. A split of one answer printed beside a count of another is
	// worse than no split, because both numbers look authoritative.
	sum := fields["global"] + fields["member"] + fields["group"] + fields["multi_group"]
	measured := measuredItemsOf(t, line)
	if sum != measured {
		t.Errorf("the four attribution dimensions sum to %d but measured_items is %d on the same line: "+
			"the split describes a different document from the count\nline: %s", sum, measured, line)
	}
	if measured != want.total() {
		t.Errorf("measured_items = %d, want %d from the fixture\nline: %s", measured, want.total(), line)
	}
}

// TestARefusalLineAlsoSaysWhatItsChargedItemsWereAbout is the mirror arm.
//
// A refusal is where the split matters MOST -- it is the line that says the
// answer was too big -- and it is emitted from a different function, with its
// own event construction. This seam has already shipped a version where the
// served emitter carried a decision dimension and both refusal arms carried
// zeros for it, so an arm covered only by the served test is an arm not
// covered.
func TestARefusalLineAlsoSaysWhatItsChargedItemsWereAbout(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	// A ceiling of one item is under every fixture bucket, so the answer
	// cannot fit, cannot be narrowed into fitting, and reaches the refusal.
	spec := defaultAttributionSpec()
	engine, _ := attributionEngine(t, spec, &sink, budgetStageOptions(1, 0), nil)
	want := spec.expect(spec.members, spec.candidates)

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err == nil {
		t.Fatal("Investigate() returned no error; this fixture is meant to reach a budget refusal")
	}

	refusals := []string{}
	for _, line := range strings.Split(sink.String(), "\n") {
		if strings.Contains(line, "context fabric plan narrowing") &&
			strings.Contains(line, "stage=assembled_result") &&
			strings.Contains(line, "refusal_planned=true") {
			refusals = append(refusals, line)
		}
	}
	if len(refusals) == 0 {
		t.Fatalf("no assembled_result refusal line was emitted.\nemitted:\n%s", sink.String())
	}
	line := refusals[len(refusals)-1]
	fields := attributionFieldsOf(t, line)

	sum := fields["global"] + fields["member"] + fields["group"] + fields["multi_group"]
	measured := measuredItemsOf(t, line)
	if sum != measured {
		t.Errorf("on the refusal line the four attribution dimensions sum to %d but measured_items is %d\nline: %s",
			sum, measured, line)
	}
	if sum == 0 {
		t.Errorf("the refusal line reports a zero split for an answer that was over budget -- "+
			"the one reading that cannot be true\nline: %s", line)
	}
	// The group buckets are the ones a flat count cannot express, so the
	// refusal is asserted to carry them rather than merely to carry four
	// keys. Both are non-zero in this fixture by construction.
	if fields["group"] == 0 || fields["multi_group"] == 0 {
		t.Errorf("attribution_group=%d attribution_multi_group=%d on the refusal line, want both non-zero "+
			"(the fixture put %d and %d there)\nline: %s",
			fields["group"], fields["multi_group"], want.group, want.multiGroup, line)
	}
}

// uncoveredArm is an assembled-result arm no scenario below drives.
//
// IT IS A CLAIM, AND A CLAIM NEEDS A PROBE. The first version was a bare
// string, and a review proved the string false: it asserted that no fixture in
// this family could reach the candidate-reduction arm, and an existing test in
// this very package already reached it. The damage was not the wrong sentence
// -- it was that the arm-count reconciliation BALANCED around it while that arm
// was reached and unasserted, because the arithmetic checked the COUNT and
// never the CLAIM.
//
// The second version required the entry to name a test that EXISTS. A review
// defeated that too: naming any passing test satisfied it, so a "probe" that
// could never fail was accepted.
//
// So an entry must now name a test that FAILS IF THE ARM EXECUTES, and the
// checker requires that test to call assertArmNeverExecuted -- the one helper
// that can express it. Both failure modes are planted as controls in
// TestEveryUncoveredArmClaimCarriesAReachProbe: a name that does not exist, and
// a name that exists but is not a probe.
type uncoveredArm struct {
	what string
	// reachProbeTest names a Test function in this package that calls
	// assertArmNeverExecuted for this arm. Checked against the syntax tree,
	// not taken on trust.
	reachProbeTest string
}

// armsWithNoBehaviouralCase is EMPTY: every assembled-result arm is driven by a
// case below. It stays as a mechanism because the next arm added may not be
// reachable at once, and the rule for registering one is written above rather
// than rediscovered.
var armsWithNoBehaviouralCase = []uncoveredArm{}

// assertArmNeverExecuted is the only shape a reach probe may take.
//
// It fails when the named arm executed during the package's own tests. A probe
// author wires observed to whatever records execution -- a counter incremented
// in the arm, or a recovered panic. The point is that the assertion is
// UNCONDITIONAL and negative: a probe that cannot fail is not a probe, which is
// exactly what the previous version of this rule accepted.
func assertArmNeverExecuted(t *testing.T, arm string, observed int) {
	t.Helper()
	if observed != 0 {
		t.Fatalf("arm %q executed %d times under this package's tests, so it is REACHABLE and must be "+
			"driven by a case in assembledResultArmCases rather than registered as uncovered", arm, observed)
	}
}

// TestEveryUncoveredArmClaimCarriesAReachProbe enforces the rule, and proves it
// is enforceable by planting both ways it can be broken.
func TestEveryUncoveredArmClaimCarriesAReachProbe(t *testing.T) {
	t.Parallel()
	declared, probes := packageTestFunctions(t)

	for _, arm := range armsWithNoBehaviouralCase {
		switch {
		case arm.reachProbeTest == "":
			t.Errorf("uncovered arm %q names no reach probe", arm.what)
		case !declared[arm.reachProbeTest]:
			t.Errorf("uncovered arm %q names reach probe %q, which is not a Test function in this package",
				arm.what, arm.reachProbeTest)
		case !probes[arm.reachProbeTest]:
			t.Errorf("uncovered arm %q names %q as its reach probe, but that test never calls "+
				"assertArmNeverExecuted -- a test that cannot fail when the arm executes is not a probe, "+
				"and accepting one is how a false unreachability claim shipped before",
				arm.what, arm.reachProbeTest)
		}
	}

	// CONTROL 1: a name that does not exist must be rejected.
	if declared["TestThisNameIsDeliberatelyNotDeclaredAnywhere"] {
		t.Fatal("the control name exists; pick another")
	}
	// CONTROL 2, and this is the one the previous version failed: a test that
	// EXISTS but is not a probe must be rejected. Any test in this file that
	// does not call assertArmNeverExecuted will do.
	const existsButIsNotAProbe = "TestEveryAssembledResultArmEmitsASplitThatDescribesIt"
	if !declared[existsButIsNotAProbe] {
		t.Fatalf("control test %q is missing; the control cannot run", existsButIsNotAProbe)
	}
	if probes[existsButIsNotAProbe] {
		t.Fatalf("control test %q calls assertArmNeverExecuted, so it cannot serve as the "+
			"exists-but-is-not-a-probe control", existsButIsNotAProbe)
	}
	// And the checker must recognise a REAL probe, or every entry would be
	// rejected and the rule would be unusable.
	if !probes["TestTheReachProbeShapeIsRecognised"] {
		t.Fatal("the reference probe is not recognised as one; the checker cannot tell a probe from a " +
			"non-probe, which makes the rule either vacuous or unusable")
	}
	// CONTROL 3, the one a review used to defeat the previous rule: a test
	// that is a probe in every visible respect but is NOT COMPILED. It lives
	// at answer_item_attribution_neverbuilt_test.go under `//go:build never`.
	// If it appears here, the probe set came from the filesystem rather than
	// from the compiler, and a probe that never runs would be accepted as
	// proof that an arm is unreachable.
	const neverBuilt = "TestABuildConstrainedProbeMustNotCount"
	if declared[neverBuilt] || probes[neverBuilt] {
		t.Fatalf("%s is build-excluded (//go:build never) yet appears in the probe set: the enumeration "+
			"is reading the filesystem instead of the compiler's view, so a probe that can never run "+
			"would satisfy an unreachability claim", neverBuilt)
	}
}

// TestTheReachProbeShapeIsRecognised is the reference probe: it exists so the
// checker above has a positive example, and it demonstrates the shape a real
// entry must use. No arm is currently uncovered, so it observes zero by
// construction.
func TestTheReachProbeShapeIsRecognised(t *testing.T) {
	t.Parallel()
	assertArmNeverExecuted(t, "reference probe (no arm is currently uncovered)", 0)
}

// packageTestFunctions returns every Test function the COMPILER will build for
// this package, and which of them call assertArmNeverExecuted.
//
// THE FILE LIST COMES FROM `go list`, NOT FROM THE FILESYSTEM, and that is the
// whole point. The previous version walked os.ReadDir and parsed whatever
// ended in _test.go, without applying build constraints -- so a file tagged
// `//go:build never` registered as a valid reach probe even though `go test`
// never compiles it, and a probe that cannot run cannot fail. A review found
// that; answer_item_attribution_neverbuilt_test.go is the permanent control
// that keeps it found.
//
// `go list` answers with the build context the test binary is actually
// compiled under, so the probe set is the set that can really execute.
func packageTestFunctions(t *testing.T) (declared, probes map[string]bool) {
	t.Helper()
	const tmpl = `{{range .TestGoFiles}}{{.}}
{{end}}{{range .XTestGoFiles}}{{.}}
{{end}}`
	out, err := exec.Command("go", "list", "-f", tmpl, ".").Output()
	if err != nil {
		t.Fatalf("go list -f TestGoFiles: %v", err)
	}
	names := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		t.Fatal("go list reported no test files for this package; the enumeration is broken and an " +
			"empty probe set would accept any name")
	}

	fset := token.NewFileSet()
	declared, probes = map[string]bool{}, map[string]bool{}
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Body == nil {
				continue
			}
			declared[fn.Name.Name] = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, isCall := node.(*ast.CallExpr)
				if !isCall {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "assertArmNeverExecuted" {
					probes[fn.Name.Name] = true
				}
				return true
			})
		}
	}

	// EXACT RECONCILIATION against the filesystem, in the direction that
	// matters: every file go list named must exist, and the files it did NOT
	// name are the build-excluded ones. A file on disk that go list omits is
	// the case this whole function exists for, so it is not an error -- but a
	// file go list names that is missing on disk means the two views have
	// diverged and neither can be trusted.
	for _, name := range names {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("go list named %s but it is not on disk: %v", name, err)
		}
	}
	return declared, probes
}

// assembledResultArmCase is one scenario, the arm it must reach, how to
// recognise that arm on the emitted line, and where its expectation comes from.
type assembledResultArmCase struct {
	name string
	// discriminator is the substring that identifies THIS arm's line. A
	// scenario that does not produce one is a fixture that silently landed
	// somewhere else, which is a failure and not a skip.
	discriminator string
	// spec is per-arm because the expected bucket values must be pairwise
	// DISTINCT on the document THIS arm measured, and the arms measure
	// cohorts of different sizes.
	spec attributionFixtureSpec
	// measuresNarrowedCohort says the event measured the RE-SYNTHESIZED
	// document rather than the one synthesis first ran against. That arm's
	// cohort size is read from the LAST cohort synthesis was handed, captured
	// at the synthesizer seam -- so it is exact, like every other arm.
	measuresNarrowedCohort bool
	// drive runs the scenario. It returns the SERVED result when the arm
	// serves one, and served=false when the answer was refused or the retry
	// failed, in which case there is no served document to count.
	// drive runs the scenario. It returns the SERVED result when the arm
	// serves one; when it does not, cohortSizes carries the member count of
	// every cohort synthesis was handed, observed at the synthesizer seam
	// before any event exists.
	drive func(t *testing.T, sink *bytes.Buffer, spec attributionFixtureSpec, cohortSizes *[]int) (result InvestigationResult, served bool)
}

func assembledResultArmCases() []assembledResultArmCase {
	return []assembledResultArmCase{
		{
			name:          "measured fit",
			discriminator: "overrun=fits",
			spec:          defaultAttributionSpec(),
			drive: func(t *testing.T, sink *bytes.Buffer, spec attributionFixtureSpec, cohortSizes *[]int) (InvestigationResult, bool) {
				engine, _ := attributionEngine(t, spec, sink, budgetStageOptions(200, 0), cohortSizes)
				result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
				if err != nil {
					t.Fatalf("Investigate() error = %v, want a served answer", err)
				}
				return result, true
			},
		},
		{
			name:          "planned refusal, nothing to narrow",
			discriminator: "retry_declined=nothing_to_narrow",
			spec:          attributionFixtureSpec{members: 1, globalFindings: 5, groupDrivers: 3, multiGroupDrivers: 4, memberDrivers: 1},
			drive: func(t *testing.T, sink *bytes.Buffer, spec attributionFixtureSpec, cohortSizes *[]int) (InvestigationResult, bool) {
				engine, _ := attributionEngine(t, spec, sink, budgetStageOptions(1, 0), cohortSizes)
				if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err == nil {
					t.Fatal("Investigate() returned no error, want a planned budget refusal")
				}
				return InvestigationResult{}, false
			},
		},
		{
			name:          "retry synthesis FAILED",
			discriminator: "retry_failed=true",
			spec:          defaultAttributionSpec(),
			drive: func(t *testing.T, sink *bytes.Buffer, spec attributionFixtureSpec, cohortSizes *[]int) (InvestigationResult, bool) {
				engine, _ := attributionEngine(t, spec, sink, budgetStageOptions(10, time.Second), cohortSizes)
				attempts := 0
				engine.synthesizer = chaos4809FailOnSecondCall(engine.synthesizer, &attempts)
				_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
				if err == nil {
					t.Fatal("Investigate() returned no error, want the retry's own propagated failure")
				}
				if errors.Is(err, ErrAnswerExceedsBudget) {
					t.Fatalf("error = %v, want the retry's OWN fault, not a budget refusal", err)
				}
				if attempts != 2 {
					t.Fatalf("synthesis attempted %d times, want 2 -- the retry must have RUN for this arm to be under test", attempts)
				}
				return InvestigationResult{}, false
			},
		},
		{
			name:                   "retry ran and still did not fit",
			discriminator:          "retry_attempted=true retry_fit=false retry_failed=false",
			measuresNarrowedCohort: true,
			spec:                   attributionFixtureSpec{members: 3, globalFindings: 6, groupDrivers: 4, multiGroupDrivers: 5, memberDrivers: 1},
			drive: func(t *testing.T, sink *bytes.Buffer, spec attributionFixtureSpec, cohortSizes *[]int) (InvestigationResult, bool) {
				engine, _ := attributionEngine(t, spec, sink, budgetStageOptions(10, time.Second), cohortSizes)
				if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err == nil {
					t.Fatal("Investigate() returned no error, want a refusal after a retry that did not fit")
				}
				return InvestigationResult{}, false
			},
		},
		{
			name: "outcome layer served a candidate narrowing",
			// The arm a review proved I had wrongly declared unreachable.
			// One member means the cohort cannot be narrowed, so stage three
			// declines the retry and goes straight to the reduction; seven
			// proposed candidates against a 20-item ceiling leave four.
			discriminator: "outcome_reduction_applied=true",
			spec: attributionFixtureSpec{
				members: 1, globalFindings: 3, groupDrivers: 5, multiGroupDrivers: 6,
				memberDrivers: 1, candidates: 7,
			},
			drive: func(t *testing.T, sink *bytes.Buffer, spec attributionFixtureSpec, cohortSizes *[]int) (InvestigationResult, bool) {
				engine, _ := attributionEngine(t, spec, sink, budgetStageOptions(20, 0), cohortSizes)
				result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
				if err != nil {
					t.Fatalf("Investigate() error = %v, want an answer SERVED by the candidate reduction", err)
				}
				return result, true
			},
		},
	}
}

var beforeAfterPattern = regexp.MustCompile(`before=(-?\d+) after=(-?\d+)`)

var reductionAppliedPattern = regexp.MustCompile(`outcome_reduction_applied=(true|false)`)

var outcomeItemsServedPattern = regexp.MustCompile(`outcome_items_served=(-?\d+)`)

// cohortCountsOf reads the member counts the arm's own line reports.
func cohortCountsOf(t *testing.T, line string) (before, after int) {
	t.Helper()
	match := beforeAfterPattern.FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the emitted line carries no before/after member counts: %s", line)
	}
	before, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("before is not an integer: %v", err)
	}
	after, err = strconv.Atoi(match[2])
	if err != nil {
		t.Fatalf("after is not an integer: %v", err)
	}
	return before, after
}

// lineReportsReductionApplied reads the reduction flag and, when set, the
// number of candidates the line CLAIMS were served.
func lineReportsReductionApplied(t *testing.T, line string) (applied bool, served int) {
	t.Helper()
	match := reductionAppliedPattern.FindStringSubmatch(line)
	if match == nil {
		t.Fatalf("the emitted line carries no outcome_reduction_applied: %s", line)
	}
	if match[1] != "true" {
		return false, 0
	}
	servedMatch := outcomeItemsServedPattern.FindStringSubmatch(line)
	if servedMatch == nil {
		t.Fatalf("the line says the reduction applied but carries no outcome_items_served: %s", line)
	}
	value, err := strconv.Atoi(servedMatch[1])
	if err != nil {
		t.Fatalf("outcome_items_served is not an integer: %v", err)
	}
	return true, value
}

// TestEveryAssembledResultArmEmitsASplitThatDescribesIt is the load-bearing
// behavioural test, and its expectation is deliberately NOT taken from the line
// it checks.
//
// THREE REVIEWS, THREE VERSIONS, AND THE REASON FOR THIS ONE.
//
//  1. The first version drove two arms. A review zeroed a third arm's split
//     through a pointer alias and nothing objected, because three of the five
//     arms had no behavioural case at all.
//  2. The second drove every arm but asserted only that the four buckets summed
//     to measured_items. A review REDISTRIBUTED two buckets -- Global += Member,
//     Member = 0 -- which preserves the total, and passed.
//  3. The third asserted the four values, but derived the candidate count from
//     `outcome_items_served` ON THE SAME LINE. A review then corrupted
//     Attribution.Global, MeasuredItems and OutcomeItemsServed TOGETHER: a
//     self-consistent forged line, which passed while the served document was
//     unchanged.
//
// Version three's mistake is the one worth naming: moving the expectation off
// the production attribution function and onto the emitted line looked like
// independence and was not, because the line is written by the same code. An
// expectation must never be computed by the thing it checks, and a log record
// IS the thing being checked here.
//
// So the expectation now comes from two sources the emitter cannot forge:
//   - the fixture's own literals (globalFindings, groupDrivers, multiGroupDrivers,
//     memberDrivers), which no production code can move; and
//   - the SERVED RESULT DOCUMENT -- the candidates actually present in
//     result.SubjectResolution and the members actually in result.Cohort -- for
//     the arms that serve one.
//
// The line is then asserted to AGREE with that independent expectation, which
// is a second check rather than the source of the first.
func TestEveryAssembledResultArmEmitsASplitThatDescribesIt(t *testing.T) {
	t.Parallel()
	cases := assembledResultArmCases()

	// The population check: every recordMeasurement call site is either
	// driven below or registered as uncovered with a probe.
	sites := recordMeasurementSiteCount(t)
	if got := len(cases) + len(armsWithNoBehaviouralCase); got != sites {
		t.Fatalf("this test accounts for %d arms (%d driven + %d registered uncovered) but the package has "+
			"%d recordMeasurement sites: an arm is neither covered nor disclosed",
			got, len(cases), len(armsWithNoBehaviouralCase), sites)
	}

	seen := map[string]string{}
	for _, one := range cases {
		if other, clash := seen[one.discriminator]; clash {
			t.Fatalf("scenarios %q and %q share the discriminator %q, so one arm is untested",
				one.name, other, one.discriminator)
		}
		seen[one.discriminator] = one.name
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			t.Parallel()
			var sink bytes.Buffer
			cohortSizes := []int{}
			result, servedDocument := one.drive(t, &sink, one.spec, &cohortSizes)

			line := ""
			for _, candidate := range strings.Split(sink.String(), "\n") {
				if strings.Contains(candidate, "context fabric plan narrowing") &&
					strings.Contains(candidate, "stage=assembled_result") &&
					strings.Contains(candidate, one.discriminator) {
					line = candidate
				}
			}
			if line == "" {
				t.Fatalf("no assembled_result line carrying %q was emitted: this fixture did not reach the arm "+
					"it claims to test.\nemitted:\n%s", one.discriminator, sink.String())
			}

			// ---- the INDEPENDENT expectation ----
			//
			// Candidates and members come from the SERVED DOCUMENT when there
			// is one. When the answer was refused there is no served document,
			// and the measured one is the pre-narrowing result, whose size is
			// the fixture's own literal.
			candidatesInDoc := one.spec.candidates
			membersMeasured := one.spec.members
			switch {
			case servedDocument:
				candidatesInDoc = len(result.SubjectResolution.Candidates)
				if result.Cohort != nil {
					membersMeasured = len(result.Cohort.Members)
				}
			case one.measuresNarrowedCohort:
				// The arm that measures the RE-SYNTHESIZED document and
				// serves nothing. Its cohort is the LAST one synthesis was
				// handed -- captured at the synthesizer seam, before any
				// event exists, so an edit to the emitter cannot move it.
				// This is what makes the member bucket exact here rather
				// than bounded: a review showed a bounded check admits a
				// mutant that raises the member count and the total together.
				if len(cohortSizes) < 2 {
					t.Fatalf("this arm must have re-synthesized, so synthesis should have been handed at "+
						"least two cohorts; it saw %d -- the fixture did not reach the retry", len(cohortSizes))
				}
				membersMeasured = cohortSizes[len(cohortSizes)-1]
				if membersMeasured >= cohortSizes[0] {
					t.Fatalf("the retry was handed %d members and the first pass %d: the cohort did not "+
						"narrow, so this arm is not doing what the case claims", membersMeasured, cohortSizes[0])
				}
			}

			want := one.spec.expect(membersMeasured, candidatesInDoc)
			want.assertPairwiseDistinct(t, one.name)

			fields := attributionFieldsOf(t, line)

			// global, group and multi_group are fixture literals plus the
			// served document's own candidate count on every arm, so they are
			// pinned exactly everywhere. A redistribution INTO global fails
			// here whatever it does to the rest of the line.
			for _, name := range []string{"global", "group", "multi_group"} {
				if fields[name] != want.byBucket()[name] {
					t.Errorf("attribution_%s = %d, want %d -- from the fixture's own items and the %d "+
						"candidates the SERVED DOCUMENT carries, never from this line\nline: %s",
						name, fields[name], want.byBucket()[name], candidatesInDoc, line)
				}
			}
			if fields["member"] != want.member {
				t.Errorf("attribution_member = %d, want %d -- from the fixture's own items and the %d "+
					"members the measured document carries, never from this line\nline: %s",
					fields["member"], want.member, membersMeasured, line)
			}

			// The line must AGREE with the independent expectation. This is
			// the second check, not the source of the first: a forged
			// outcome_items_served now contradicts the served document.
			if applied, servedCandidates := lineReportsReductionApplied(t, line); applied {
				if !servedDocument {
					t.Fatalf("the line says the reduction applied but this arm served no document to check it "+
						"against\nline: %s", line)
				}
				if servedCandidates != candidatesInDoc {
					t.Errorf("the line reports outcome_items_served=%d but the SERVED DOCUMENT carries %d "+
						"candidates: the line and the answer disagree about what was served\nline: %s",
						servedCandidates, candidatesInDoc, line)
				}
				if servedCandidates >= one.spec.candidates {
					t.Errorf("the line says the reduction applied but served %d of %d proposed candidates -- "+
						"it cut nothing\nline: %s", servedCandidates, one.spec.candidates, line)
				}
			}

			sum := fields["global"] + fields["member"] + fields["group"] + fields["multi_group"]
			measured := measuredItemsOf(t, line)
			if sum != measured {
				t.Errorf("the four attribution dimensions sum to %d but measured_items is %d on the same line: "+
					"this arm's split describes a different document from its own count\nline: %s", sum, measured, line)
			}
			if measured != want.total() {
				t.Errorf("measured_items = %d, want %d from the fixture and the served document\nline: %s",
					measured, want.total(), line)
			}
			if !servedDocument && one.spec.candidates != 0 {
				t.Fatalf("this arm serves no document but its fixture proposes %d candidates, so the "+
					"expectation would have to come from the line", one.spec.candidates)
			}
		})
	}
}
