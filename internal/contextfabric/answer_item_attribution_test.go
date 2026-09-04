package contextfabric

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"log/slog"
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
// decide both sides of the comparison. Every number below is counted off the
// synthesizer literal in attributionEngine, and the four are deliberately
// PAIRWISE DISTINCT so that a swap between any two dimensions on the emitted
// line fails rather than cancelling out.
type attributionFixtureCounts struct {
	global     int
	member     int
	group      int
	multiGroup int
}

func (c attributionFixtureCounts) total() int {
	return c.global + c.member + c.group + c.multiGroup
}

// attributionEngine builds an engine whose synthesis returns a result with a
// KNOWN item in every bucket, logging through a real slog handler so a test
// can read the formatted line.
//
// It builds its own synthesizer rather than reusing budgetStageEngine's
// because that one emits claims only: every charged item lands in the member
// bucket, three of the four dimensions read zero, and a test over four zeros
// cannot tell a correct split from a dropped one.
func attributionEngine(t *testing.T, cohort *Cohort, sink *bytes.Buffer, options EngineOptions) (*Engine, attributionFixtureCounts) {
	t.Helper()

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
	drivers := []DriverJudgment{
		// group: exactly one group named, three of them.
		driver("d_group_0", []SubjectRef{attributionGroupRef(0)}),
		driver("d_group_1", []SubjectRef{attributionGroupRef(1)}),
		driver("d_group_2", []SubjectRef{attributionGroupRef(0)}),
		// multi_group: both groups named, two of them.
		driver("d_multi_0", []SubjectRef{attributionGroupRef(0), attributionGroupRef(1)}),
		driver("d_multi_1", []SubjectRef{attributionGroupRef(1), attributionGroupRef(0)}),
		// member: one driver about a cohort member.
		driver("d_member_0", []SubjectRef{memberRef}),
	}
	findings := []Finding{
		// global: a subject the cohort does not contain, and three naming
		// nothing at all.
		finding("f_global_0", []SubjectRef{stranger}),
		finding("f_global_1", nil),
		finding("f_global_2", nil),
		finding("f_global_3", nil),
		// The fifth is not padding: it is what makes the global count (5)
		// differ from the member count (4), so a swap between those two
		// dimensions on the emitted line fails instead of cancelling out.
		finding("f_global_4", nil),
	}
	want := attributionFixtureCounts{
		global: len(findings),
		// The cohort member ROWS plus the one driver about a member. The
		// rows are the item class the earlier design of this seam charged
		// and never accounted for, so they are counted explicitly here.
		member:     len(cohort.Members) + 1,
		group:      3,
		multiGroup: 2,
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
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
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
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
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
	return engine, want
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
	cohort := attributionCohort(3)
	engine, want := attributionEngine(t, cohort, &sink, budgetStageOptions(200, 0))

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
	if got := len(result.Drivers); got != 6 {
		t.Fatalf("the served result carries %d drivers, want the 6 the fixture supplied: "+
			"something between synthesis and assembly is adding or dropping items, so the "+
			"expectation below no longer describes this answer", got)
	}
	if got := len(result.ClaimedFacts); got != 0 {
		t.Fatalf("the served result carries %d claimed facts, want 0: same reason as above", got)
	}
	// The four expected values must DIFFER pairwise, or a swap between two
	// dimensions on the line would pass unnoticed.
	seen := map[int]string{}
	for name, value := range map[string]int{
		"global": want.global, "member": want.member, "group": want.group, "multi_group": want.multiGroup,
	} {
		if value == 0 {
			t.Fatalf("the fixture puts nothing in the %s bucket: the test would pass on a line that dropped it", name)
		}
		if other, clash := seen[value]; clash {
			t.Fatalf("buckets %s and %s both expect %d: a swap between them could not be detected", name, other, value)
		}
		seen[value] = name
	}

	line := assembledResultLine(t, sink.String())
	fields := attributionFieldsOf(t, line)

	for name, wantCount := range map[string]int{
		"global": want.global, "member": want.member, "group": want.group, "multi_group": want.multiGroup,
	} {
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
	cohort := attributionCohort(3)
	// A ceiling of one item is under every fixture bucket, so the answer
	// cannot fit, cannot be narrowed into fitting, and reaches the refusal.
	engine, want := attributionEngine(t, cohort, &sink, budgetStageOptions(1, 0))

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

// armsWithNoBehaviouralCase names the assembled-result arms no scenario below
// reaches, and it is ENFORCED rather than merely disclosed.
//
// A gap written only in a comment is indistinguishable from an omission, so
// the count is reconciled against the number of `recordMeasurement` call sites
// the package actually has. Covering one of these without removing it from the
// list fails; adding a sixth arm without covering it fails too.
var armsWithNoBehaviouralCase = []string{
	"the outcome layer serving a candidate narrowing (recordCandidateNarrowing): " +
		"reaching it needs resolution candidates present in the served result, which this " +
		"fixture family cannot produce without entering the subject-clarification flow",
}

// assembledResultArmCase is one scenario, the arm it must reach, and how to
// recognise that arm on the emitted line.
type assembledResultArmCase struct {
	name string
	// discriminator is the substring that identifies THIS arm's line. A
	// scenario that does not produce one is a fixture that silently landed
	// somewhere else, which is a failure and not a skip.
	discriminator string
	drive         func(t *testing.T, sink *bytes.Buffer)
}

func assembledResultArmCases() []assembledResultArmCase {
	return []assembledResultArmCase{
		{
			name:          "measured fit",
			discriminator: "overrun=fits",
			drive: func(t *testing.T, sink *bytes.Buffer) {
				engine, _ := attributionEngine(t, attributionCohort(3), sink, budgetStageOptions(200, 0))
				if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err != nil {
					t.Fatalf("Investigate() error = %v, want a served answer", err)
				}
			},
		},
		{
			name:          "planned refusal, nothing to narrow",
			discriminator: "retry_declined=nothing_to_narrow",
			drive: func(t *testing.T, sink *bytes.Buffer) {
				engine, _ := attributionEngine(t, attributionCohort(1), sink, budgetStageOptions(1, 0))
				if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err == nil {
					t.Fatal("Investigate() returned no error, want a planned budget refusal")
				}
			},
		},
		{
			name: "retry synthesis FAILED",
			// This is the arm an adversarial review reached by aliasing
			// past the one-stamp pin. It emitted a correct measured_items
			// beside four zero dimensions and nothing objected, because no
			// behavioural case drove it.
			discriminator: "retry_failed=true",
			drive: func(t *testing.T, sink *bytes.Buffer) {
				engine, _ := attributionEngine(t, attributionCohort(3), sink, budgetStageOptions(10, time.Second))
				attempts := 0
				engine.synthesizer = chaos4809FailOnSecondCall(engine.synthesizer, &attempts)
				_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
				if err == nil {
					t.Fatal("Investigate() returned no error, want the retry's own propagated failure")
				}
				if errors.Is(err, ErrAnswerExceedsBudget) {
					t.Fatalf("error = %v, want the retry's OWN fault, not a budget refusal -- this fixture must reach the retry-FAILURE arm", err)
				}
				if attempts != 2 {
					t.Fatalf("synthesis attempted %d times, want 2 -- the retry must have RUN for this arm to be under test", attempts)
				}
			},
		},
		{
			name:          "retry ran and still did not fit",
			discriminator: "retry_attempted=true retry_fit=false retry_failed=false",
			drive: func(t *testing.T, sink *bytes.Buffer) {
				engine, _ := attributionEngine(t, attributionCohort(3), sink, budgetStageOptions(10, time.Second))
				if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow()); err == nil {
					t.Fatal("Investigate() returned no error, want a refusal after a retry that did not fit")
				}
			},
		},
	}
}

// TestEveryAssembledResultArmEmitsASplitThatDescribesIt is the behavioural
// counterpart to the structural one-stamp pin, and it is the load-bearing one.
//
// WHY IT EXISTS, and it is worth stating because the structural pin was
// written FIRST and looked sufficient. An adversarial round defeated that pin
// with two lines -- `attribution := &event.Attribution; *attribution = ...{}`
// -- because the pin matches assignments whose left-hand side is a selector,
// and a write through a pointer alias is not one. The mutation compiled, vetted
// and left every test green: the retry-synthesis-failure arm emitted a correct
// measured_items beside four zeroes.
//
// The pin was not the real gap. The real gap was that three of the five arms
// had no behavioural case at all, so the pin was the ONLY thing standing
// between a zeroed arm and a green suite -- and a static check is exactly the
// wrong thing to make load-bearing, because the set of ways to evade one is
// open. This test closes it from the other side: it drives each arm through
// the public entry point and reads what the engine emitted, so an arm that
// stops describing its own document fails no matter HOW it was made to.
//
// The arms are reconciled against the production call-site count, so this is a
// population rather than a list.
func TestEveryAssembledResultArmEmitsASplitThatDescribesIt(t *testing.T) {
	t.Parallel()
	cases := assembledResultArmCases()

	// The population check. `recordMeasurement` call sites are the arms that
	// emit a measured assembled-result event; every one is either driven
	// below or named in the enforced gap list.
	sites := 0
	fset := token.NewFileSet()
	for _, name := range packageProductionFiles(t) {
		parsed, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		sites += countRecordMeasurementCalls(parsed)
	}
	if got := len(cases) + len(armsWithNoBehaviouralCase); got != sites {
		t.Fatalf("this test accounts for %d arms (%d driven + %d named as uncovered) but the package has "+
			"%d recordMeasurement call sites: an arm is neither covered nor disclosed",
			got, len(cases), len(armsWithNoBehaviouralCase), sites)
	}

	// Two scenarios sharing a discriminator would silently test one arm
	// twice while leaving another untouched.
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
			one.drive(t, &sink)

			// REACH CHECK, hard failure. A fixture that landed on a
			// different arm would otherwise assert about a line this
			// scenario is not responsible for.
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

			fields := attributionFieldsOf(t, line)
			sum := fields["global"] + fields["member"] + fields["group"] + fields["multi_group"]
			measured := measuredItemsOf(t, line)
			if sum != measured {
				t.Errorf("the four attribution dimensions sum to %d but measured_items is %d on the same line: "+
					"this arm's split describes a different document from its own count\nline: %s", sum, measured, line)
			}
			if measured > 0 && sum == 0 {
				t.Errorf("this arm charged %d items and reported an all-zero split -- the one reading that "+
					"cannot be true\nline: %s", measured, line)
			}
		})
	}
}
