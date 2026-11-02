package contextfabric

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12416. The words of a closed vocabulary are composed at serve, on a
// served copy, from the values in this result.

// wordRow is one served field: the value it sets, and the exact map it gives.
type wordRow struct {
	name string
	set  func(*InvestigationResult)
	want map[string]string
}

func wordRows() []wordRow {
	correction := func(result *InvestigationResult, on **contractsv1.ContextFabricStatusCorrection) {
		*on = &contractsv1.ContextFabricStatusCorrection{
			ReplacedStatus: contractsv1.ContextFabricInvestigationComplete, Direction: contractsv1.ContextFabricStatusCorrectionCompleteToPartial,
			DecidingRequirement: "health/subject/team", DecidingOutcome: contractsv1.ContextFabricRequirementNarrowed,
		}
		result.Status = contractsv1.ContextFabricInvestigationPartial
	}
	correctedWords := map[string]string{
		"answer_status:partial":        "Partial answer",
		"answer_status:complete":       "Answer",
		"requirement_outcome:narrowed": "narrowed",
		"status_correction:note":       "The service lowered this answer from Answer to Partial answer: health was read only in part.",
	}
	return []wordRow{
		{"status", func(r *InvestigationResult) { r.Status = contractsv1.ContextFabricInvestigationDegraded }, map[string]string{"answer_status:degraded": "Degraded answer"}},
		{"terminal status", func(r *InvestigationResult) {
			r.Completeness.TerminalStatus = contractsv1.ContextFabricInvestigationNoMatch
		}, map[string]string{"answer_status:no_match": "No answer"}},
		{"a complete status beside complete completeness", func(r *InvestigationResult) {
			r.Status = contractsv1.ContextFabricInvestigationComplete
			r.Completeness.State = contractsv1.ContextFabricAnswerCompletenessComplete
		}, map[string]string{"answer_status:complete": "Full answer"}},
		{"a complete status beside partial completeness", func(r *InvestigationResult) {
			r.Status = contractsv1.ContextFabricInvestigationComplete
			r.Completeness.State = contractsv1.ContextFabricAnswerCompletenessPartial
		}, map[string]string{"answer_status:complete": "Answer, not fully covered"}},
		{"a complete status with no completeness", func(r *InvestigationResult) {
			r.Status = contractsv1.ContextFabricInvestigationComplete
		}, map[string]string{"answer_status:complete": "Answer"}},
		{"requirement outcome", func(r *InvestigationResult) {
			r.Completeness.Outcomes = []contractsv1.ContextFabricPlanRequirementOutcomeRow{{Outcome: contractsv1.ContextFabricRequirementSatisfied}}
		}, map[string]string{"requirement_outcome:satisfied": "met"}},
		{"status correction", func(r *InvestigationResult) { correction(r, &r.StatusCorrection) }, correctedWords},
		{"the completeness mirror of a status correction", func(r *InvestigationResult) { correction(r, &r.Completeness.StatusCorrection) }, correctedWords},
		{"a correction whose direction this build does not know", func(r *InvestigationResult) {
			r.Status = contractsv1.ContextFabricInvestigationDegraded
			r.StatusCorrection = &contractsv1.ContextFabricStatusCorrection{ReplacedStatus: contractsv1.ContextFabricInvestigationPartial, Direction: "partial_to_something"}
		}, map[string]string{
			"answer_status:degraded": "Degraded answer", "answer_status:partial": "Partial answer",
			"status_correction:note": "The service changed this answer from Partial answer to Degraded answer.",
		}},
		{"candidate state", func(r *InvestigationResult) {
			r.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{{State: contractsv1.ContextFabricResolutionAmbiguous}}
		}, map[string]string{"candidate_state:ambiguous": "more than one match"}},
		{"prior receipt disposition", func(r *InvestigationResult) {
			r.SubjectResolution.PriorSubjectReceiptDispositions = []contractsv1.ContextFabricPriorSubjectReceiptEntry{{Disposition: contractsv1.ContextFabricPriorSubjectReceiptApplied}}
		}, map[string]string{"prior_receipt_disposition:applied": "used"}},
		{"structure disposition", func(r *InvestigationResult) {
			r.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{Disposition: contractsv1.ContextFabricStructureDispositionNotEvaluated}}
		}, map[string]string{"structure_disposition:not_evaluated": "not checked"}},
		{"cohort kind", func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{Kind: contractsv1.ContextFabricSubjectRepository}
		}, map[string]string{"subject_kind:repository": "repository", "subject_kind_plural:repository": "repositories"}},
		{"coverage detail kind", func(r *InvestigationResult) {
			r.Coverage.Details = []contractsv1.ContextFabricCoverageDetail{{Kind: contractsv1.ContextFabricSubjectTeam}}
		}, map[string]string{"subject_kind:team": "team", "subject_kind_plural:team": "teams"}},
		{"member outcome", func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{Members: []contractsv1.ContextFabricCohortMember{{Outcome: contractsv1.ContextFabricCohortOutcomeProvisional}}}
		}, map[string]string{"member_outcome:provisional": "provisional"}},
		{"missing signal", func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{Members: []contractsv1.ContextFabricCohortMember{{MissingSignals: []string{"investment_mix"}}}}
		}, map[string]string{"ranking_signal:investment_mix": "investment mix"}},
		{"driver signal", func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{Members: []contractsv1.ContextFabricCohortMember{{Drivers: []contractsv1.ContextFabricCohortMemberDriver{{Signal: "health.compounding_risk"}}}}}
		}, map[string]string{"ranking_signal:health.compounding_risk": "health risk"}},
		{"driver window", func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{Members: []contractsv1.ContextFabricCohortMember{{Drivers: []contractsv1.ContextFabricCohortMemberDriver{{Window: contractsv1.ContextFabricCohortMemberDriverWindowCurrent}}}}}
		}, map[string]string{"driver_window:current": "current period"}},
		{"driver threshold", func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{Members: []contractsv1.ContextFabricCohortMember{{Drivers: []contractsv1.ContextFabricCohortMemberDriver{{ThresholdLabels: []string{"investment_mix.mix_shift_other"}}}}}}
		}, map[string]string{"threshold_label:investment_mix.mix_shift_other": "mix shifted"}},
		{"driver standing", func(r *InvestigationResult) {
			r.Drivers = []contractsv1.ContextFabricDriverJudgment{{Standing: contractsv1.ContextFabricDriverPrincipal}}
		}, map[string]string{"driver_standing:principal": "main driver"}},
		{"remaining work kind", func(r *InvestigationResult) {
			r.RemainingWork = []contractsv1.ContextFabricFinding{{Kind: "relationship"}}
		}, map[string]string{"fact_kind:relationship": "relationship"}},
		{"readiness gap kind", func(r *InvestigationResult) {
			r.ReadinessGaps = []contractsv1.ContextFabricFinding{{Kind: "narrative"}}
		}, map[string]string{"fact_kind:narrative": "narrative"}},
		{"conflict kind", func(r *InvestigationResult) {
			r.Conflicts = []contractsv1.ContextFabricFinding{{Kind: "operational_deficiency"}}
		}, map[string]string{"fact_kind:operational_deficiency": "operational deficiency"}},
		{"claimed fact kind", func(r *InvestigationResult) {
			r.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{Kind: contractsv1.ContextFabricFactBlockers}}
		}, map[string]string{"fact_kind:blockers": "blockers"}},
		{"driver source state", func(r *InvestigationResult) {
			r.Drivers = []contractsv1.ContextFabricDriverJudgment{{SourceState: contractsv1.ContextFabricSourceStale}}
		}, map[string]string{"driver_source_state:stale": "may be out of date"}},
		{"cohort score meaning", func(r *InvestigationResult) {
			r.Cohort = &contractsv1.ContextFabricCohort{ScoreMeaning: contractsv1.ContextFabricCohortScoreMeaningAttention}
		}, map[string]string{"cohort_score_meaning:attention": "Attention score: higher means more pressure. It does not measure performance."}},
		{"relationship type", func(r *InvestigationResult) {
			r.Paths = []contractsv1.ContextFabricRelationshipPath{{Edges: []contractsv1.ContextFabricRelationshipEdge{{Type: contractsv1.ContextFabricRelationshipPartOf}}}}
		}, map[string]string{"relationship_type:PART_OF": "part of"}},
		{"claimed fact field", func(r *InvestigationResult) {
			r.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{Field: "completion_ratio"}}
		}, map[string]string{"fact_field:completion_ratio": "Completion ratio"}},
		{"window relative id", func(r *InvestigationResult) {
			r.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: contractsv1.ContextFabricRelativeWindowAllTime}
		}, map[string]string{"window_relative_id:all_time": "all time"}},
		{"window provenance", func(r *InvestigationResult) {
			r.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{Provenance: contractsv1.ContextFabricWindowServerDefault}
		}, map[string]string{"window_provenance:server_default": "the window the reads used"}},
	}
}

func serveWords(t *testing.T, result InvestigationResult) map[string]string {
	t.Helper()
	served, tier := ServeVocabularyLabels(result, true, 0)
	if tier != contractsv1.ContextFabricVocabularyLabelsFull {
		t.Fatalf("nothing bounded this document, yet it reached tier %q", tier)
	}
	return served.VocabularyLabels
}

// Each row sets one served field and asserts the WHOLE map, so a word from a
// value that is not in this result fails here too.
func TestTheMapHoldsExactlyTheValuesInThisResult(t *testing.T) {
	for _, row := range wordRows() {
		t.Run(row.name, func(t *testing.T) {
			var result InvestigationResult
			row.set(&result)
			if got := serveWords(t, result); !maps.Equal(got, row.want) {
				t.Fatalf("map = %v, want %v", got, row.want)
			}
		})
	}
}

// A vocabulary the walk does not reach serves nothing, silently. So the rows
// above must reach every published vocabulary and the note.
func TestTheWalkReachesEveryVocabulary(t *testing.T) {
	reached := map[string]bool{}
	for _, row := range wordRows() {
		for key := range row.want {
			vocabulary, _, _ := strings.Cut(key, ":")
			reached[vocabulary] = true
		}
	}
	if !reached["status_correction"] {
		t.Error("no row reaches the correction note")
	}
	names := contractsv1.ContextFabricVocabularyNames()
	if len(names) == 0 {
		t.Fatal("no vocabulary is published: this measured nothing")
	}
	for _, name := range names {
		if !reached[string(name)] {
			t.Errorf("vocabulary %q is reached by no served field", name)
		}
	}
}

// An unknown value has no key: the consumer takes its generic path, and no raw
// token reaches the map as a label.
func TestAnUnknownValueTakesTheGenericPath(t *testing.T) {
	var result InvestigationResult
	result.Status = "a_future_status"
	result.Completeness.TerminalStatus = "a_future_status"
	result.Completeness.Outcomes = []contractsv1.ContextFabricPlanRequirementOutcomeRow{{Outcome: "a_future_outcome"}}
	result.SubjectResolution.Candidates = []contractsv1.ContextFabricSubjectCandidate{{State: "a_future_state"}}
	result.Cohort = &contractsv1.ContextFabricCohort{Kind: "a_future_kind", Members: []contractsv1.ContextFabricCohortMember{{
		Outcome: "a_future_outcome", MissingSignals: []string{"a_future_signal"},
		Drivers: []contractsv1.ContextFabricCohortMemberDriver{{Signal: "a_future_signal", Window: "a_future_window", ThresholdLabels: []string{"a_future.threshold"}}},
	}}}
	result.Drivers = []contractsv1.ContextFabricDriverJudgment{{Standing: "a_future_standing"}}
	result.ClaimedFacts = []contractsv1.ContextFabricClaimedFact{{Kind: "a_future_kind"}}
	result.RemainingWork = []contractsv1.ContextFabricFinding{{Kind: "a_future_kind"}}
	result.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{RelativeID: "a_future_window", Provenance: "a_future_provenance"}
	if got := serveWords(t, result); got != nil {
		t.Fatalf("map = %v, want none", got)
	}
	encoded, err := json.Marshal(func() InvestigationResult {
		served, _ := ServeVocabularyLabels(result, true, 0)
		return served
	}())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "vocabulary_labels") {
		t.Fatal("a result with no known value still carries the key")
	}
}

func servedFixture() InvestigationResult {
	var result InvestigationResult
	result.Status = contractsv1.ContextFabricInvestigationComplete
	result.Completeness.State = contractsv1.ContextFabricAnswerCompletenessComplete
	result.Coverage.Sources = []contractsv1.ContextFabricSourceObservation{
		{Source: "canonical_fact:blockers", State: contractsv1.ContextFabricSourceTruncated, Label: "", StateLabel: "limited"},
		{Source: "context-fabric:graph", State: contractsv1.ContextFabricSourceAvailable},
	}
	result.Cohort = &contractsv1.ContextFabricCohort{Kind: contractsv1.ContextFabricSubjectRepository}
	return result
}

func TestComposingTwiceChangesNothing(t *testing.T) {
	once, _ := ServeVocabularyLabels(servedFixture(), true, 0)
	twice, _ := ServeVocabularyLabels(once, true, 0)
	if !reflect.DeepEqual(once, twice) {
		t.Fatalf("second serve changed the document:\n once %+v\ntwice %+v", once, twice)
	}
}

// The serving point composes on a copy: the document it was handed, which a
// reused or stored row shares, is not written.
func TestTheInputDocumentIsNotRewritten(t *testing.T) {
	stored := servedFixture()
	before, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := ServeVocabularyLabels(stored, true, 0)
	if served.VocabularyLabels == nil || served.Coverage.Sources[0].StateLabel == stored.Coverage.Sources[0].StateLabel {
		t.Fatalf("the copy carries no words, so this proves nothing: %+v", served.Coverage.Sources)
	}
	after, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("the input was rewritten:\nbefore %s\n after %s", before, after)
	}
}

// A stored row reads today's word, from the structured state, and the flag does
// not gate it: the fields are on every pin.
func TestSourceWordsAreStampedInBothFlagStates(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		served, tier := ServeVocabularyLabels(servedFixture(), enabled, 0)
		if tier.Shed() {
			t.Fatalf("enabled=%v: tier %q with no bound", enabled, tier)
		}
		first, second := served.Coverage.Sources[0], served.Coverage.Sources[1]
		if first.Label != "Canonical facts — blockers" || first.StateLabel != "partly included" {
			t.Errorf("enabled=%v: stored source reads %q / %q", enabled, first.Label, first.StateLabel)
		}
		if second.Label != "Relationship graph" || second.StateLabel != "available" {
			t.Errorf("enabled=%v: source with no stored word reads %q / %q", enabled, second.Label, second.StateLabel)
		}
	}
}

func TestAnEmptySourceListStaysAnEmptyList(t *testing.T) {
	result := servedFixture()
	result.Coverage.Sources = []contractsv1.ContextFabricSourceObservation{}
	served, _ := ServeVocabularyLabels(result, true, 0)
	if served.Coverage.Sources == nil {
		t.Fatal("an empty list came back nil: the contract requires the array")
	}
}

// Off serves no map, whatever the document held. Nothing stores one, but the
// validator admits it, and a flag that is off must keep it off the wire.
func TestOffServesNoMap(t *testing.T) {
	result := servedFixture()
	result.VocabularyLabels = map[string]string{"answer_status:complete": "a word from elsewhere"}
	served, tier := ServeVocabularyLabels(result, false, 0)
	if served.VocabularyLabels != nil || tier != contractsv1.ContextFabricVocabularyLabelsOff {
		t.Fatalf("off served %v (tier %q)", served.VocabularyLabels, tier)
	}
	on, _ := ServeVocabularyLabels(result, true, 0)
	if on.VocabularyLabels["answer_status:complete"] != "Full answer" {
		t.Fatalf("on kept a word it did not compose: %v", on.VocabularyLabels)
	}
}

func encodedSize(t *testing.T, result InvestigationResult) int64 {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return int64(len(encoded))
}

func correctedFixture() InvestigationResult {
	result := servedFixture()
	result.Status = contractsv1.ContextFabricInvestigationPartial
	result.Completeness.State = contractsv1.ContextFabricAnswerCompletenessPartial
	result.StatusCorrection = &contractsv1.ContextFabricStatusCorrection{
		ReplacedStatus: contractsv1.ContextFabricInvestigationComplete, Direction: contractsv1.ContextFabricStatusCorrectionCompleteToPartial,
		DecidingRequirement: "health/subject/team", DecidingOutcome: contractsv1.ContextFabricRequirementNarrowed,
	}
	return result
}

// The ladder, each tier at its own byte: the richest document within the bound.
func TestABoundShedsTheMapByTierThenTheStampedWords(t *testing.T) {
	const (
		full   = contractsv1.ContextFabricVocabularyLabelsFull
		status = contractsv1.ContextFabricVocabularyLabelsStatusOnly
		none   = contractsv1.ContextFabricVocabularyLabelsNone
		off    = contractsv1.ContextFabricVocabularyLabelsOff
	)
	bare := correctedFixture()
	stamped, _ := ServeVocabularyLabels(bare, false, 0)
	labeled, _ := ServeVocabularyLabels(bare, true, 0)
	statusOnly := stamped
	statusOnly.VocabularyLabels = contractsv1.ContextFabricStatusVocabularyLabels(labeled.VocabularyLabels)
	bareBytes, stampedBytes, statusBytes, labeledBytes := encodedSize(t, bare), encodedSize(t, stamped), encodedSize(t, statusOnly), encodedSize(t, labeled)
	if bareBytes >= stampedBytes || stampedBytes >= statusBytes || statusBytes >= labeledBytes {
		t.Fatalf("the fixture must grow at each step: %d %d %d %d", bareBytes, stampedBytes, statusBytes, labeledBytes)
	}
	for name, row := range map[string]struct {
		enabled bool
		bound   int64
		want    int64
		tier    contractsv1.ContextFabricVocabularyLabelsTier
	}{
		"exactly the full map":           {true, labeledBytes, labeledBytes, full},
		"one byte short of the full map": {true, labeledBytes - 1, statusBytes, status},
		"exactly the status words":       {true, statusBytes, statusBytes, status},
		"one byte short of them":         {true, statusBytes - 1, stampedBytes, none},
		"exactly the stamped document":   {true, stampedBytes, stampedBytes, none},
		"one byte short of the stamps":   {true, stampedBytes - 1, bareBytes, none},
		"nothing fits: the input stands": {true, bareBytes - 1, bareBytes, none},
		"no bound":                       {true, 0, labeledBytes, full},
		"off, the stamps fit":            {false, stampedBytes, stampedBytes, off},
		"off, the stamps do not fit":     {false, stampedBytes - 1, bareBytes, off},
	} {
		t.Run(name, func(t *testing.T) {
			served, tier := ServeVocabularyLabels(bare, row.enabled, row.bound)
			if got := encodedSize(t, served); got != row.want || tier != row.tier {
				t.Fatalf("served %d bytes at tier %q, want %d at %q", got, tier, row.want, row.tier)
			}
			switch row.tier {
			case status:
				if !maps.Equal(served.VocabularyLabels, map[string]string{
					"answer_status:partial": "Partial answer", "answer_status:complete": "Answer, not fully covered",
					"status_correction:note": "The service lowered this answer from Answer, not fully covered to Partial answer: health was read only in part.",
				}) {
					t.Fatalf("the status tier holds %v", served.VocabularyLabels)
				}
			case none, off:
				if served.VocabularyLabels != nil {
					t.Fatalf("tier %q holds a map: %v", row.tier, served.VocabularyLabels)
				}
			}
		})
	}
}

// A document with no status word has no status tier: an empty map must not
// stand for one, or the shed line would claim words the reader never got.
func TestNoStatusWordMeansNoStatusTier(t *testing.T) {
	result := servedFixture()
	result.Status = "a_future_status"
	labeled, _ := ServeVocabularyLabels(result, true, 0)
	if labeled.VocabularyLabels == nil {
		t.Fatal("the fixture needs another word, or this proves nothing")
	}
	served, tier := ServeVocabularyLabels(result, true, encodedSize(t, labeled)-1)
	if tier != contractsv1.ContextFabricVocabularyLabelsNone || served.VocabularyLabels != nil {
		t.Fatalf("tier %q map %v, want none", tier, served.VocabularyLabels)
	}
}

// CHAOS-12416 lifts CHAOS-12415's ban and keeps its reach: the map is set by the
// serve function, copied by Project, narrowed by ShedVocabularyLabels and
// restated by restateStatusWords, and by no other production code. A second writer could emit it past the flag or the
// bound, or put it on a stored document.
func TestOnlyTheServeFunctionSetsTheMap(t *testing.T) {
	root := repopath.Root(t)
	const declaringFields = 2 // the result and the projection
	want := map[string]bool{"ServeVocabularyLabels": true, "Project": true, "ShedVocabularyLabels": true, "restateStatusWords": true, "dropDisplacedCountWord": true}
	files, declared := 0, 0
	writers := map[string]bool{}
	var strays []string
	fileSet := token.NewFileSet()
	for _, tree := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, tree), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(fileSet, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			files++
			inspectLabelWrites(parsed, func(function string, position token.Pos) {
				if want[function] {
					writers[function] = true
					return
				}
				strays = append(strays, function+" at "+fileSet.Position(position).String())
			}, &declared)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files < 500 {
		t.Fatalf("read only %d production files: this measured almost nothing", files)
	}
	if declared != declaringFields {
		t.Fatalf("found %d VocabularyLabels declarations, want %d: the field moved, so this scan no longer proves anything", declared, declaringFields)
	}
	if len(strays) != 0 {
		slices.Sort(strays)
		t.Fatalf("production code sets vocabulary_labels outside the serve function:\n  %s", strings.Join(strays, "\n  "))
	}
	for function := range want {
		if !writers[function] {
			t.Errorf("%s no longer writes the map: this scan or that function moved", function)
		}
	}
}

// inspectLabelWrites reports each write with the function that holds it. A write
// outside any function (a package variable) reports the empty name.
func inspectLabelWrites(file *ast.File, report func(function string, position token.Pos), declared *int) {
	inspect := func(function string, node ast.Node) {
		ast.Inspect(node, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.Field:
				for _, name := range current.Names {
					if name.Name == "VocabularyLabels" {
						*declared++
					}
				}
			case *ast.KeyValueExpr:
				if key, isIdent := current.Key.(*ast.Ident); isIdent && key.Name == "VocabularyLabels" {
					report(function, key.Pos())
				}
			case *ast.AssignStmt:
				for _, left := range current.Lhs {
					if writesVocabularyLabels(left) {
						report(function, left.Pos())
					}
				}
			}
			return true
		})
	}
	for _, declaration := range file.Decls {
		if function, isFunction := declaration.(*ast.FuncDecl); isFunction {
			inspect(function.Name.Name, function)
			continue
		}
		inspect("", declaration)
	}
}

// writesVocabularyLabels sees through an index, so r.VocabularyLabels[key] = v
// counts as a write too.
func writesVocabularyLabels(expression ast.Expr) bool {
	if index, isIndex := expression.(*ast.IndexExpr); isIndex {
		expression = index.X
	}
	selector, isSelector := expression.(*ast.SelectorExpr)
	return isSelector && selector.Sel.Name == "VocabularyLabels"
}
