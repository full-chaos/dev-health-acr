package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-6557 (prod rev15, acr 7705e36c, 2026-09-25 01:22Z, raw-prod-rev14
// 15-g1.json): {"question":"Which teams need attention over the last 30
// days?","evidence_window":{"relative_id":"trailing_30d"}} on the MCP
// surface returned clarification_required with effective_evidence_window
// trailing_30d / inferred_default. The window was named twice by the caller
// and still gated: windowExplicitProvenance (window.go) downgraded an MCP
// explicit field to inferred_default (DP12(b)), canonicalizeEvidenceWindow
// turned that into ExplicitUnconfirmed, and Investigate's gate 1
// (engine.go) intercepted it before Interpret. The question-phrase channel
// gated the same way: composeEffectiveWindow kept a binder-routed span at
// inferred_default, so gate 2 fired.
//
// Ruling (chris, 2026-09-25): a window the caller SUPPLIED -- the
// evidence_window field or an explicit phrase in the question -- is a
// COMMITTED window: no clarification, the answer reads that window and
// reports it. The confirmation turn stays for the INFERRED case (class-table
// default with no stated period).

type explicitWindowRun struct {
	result     InvestigationResult
	factRead   bool
	factWindow *contractsv1.ContextFabricRequestedEvidenceWindow
}

func runExplicitWindowCase(t *testing.T, surface, question string, field *contractsv1.ContextFabricRequestedEvidenceWindow) explicitWindowRun {
	t.Helper()
	return runExplicitWindowCaseWith(t, surface, question, field, bootstrapInterpretation())
}

func runExplicitWindowCaseWith(t *testing.T, surface, question string, field *contractsv1.ContextFabricRequestedEvidenceWindow, interpretation InterpretedQuestion) explicitWindowRun {
	t.Helper()
	project := acceptanceProject()
	var run explicitWindowRun
	facts := factReaderFunc(func(_ context.Context, _ storage.Principal, request CanonicalFactRequest) (CanonicalFactBundle, error) {
		run.factRead = true
		run.factWindow = request.Question.TimeContext.EvidenceWindow
		return bootstrapFactBundle(project), nil
	})
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
	engine := buildAcceptanceEngine(t, graph, facts, interpretation, bootstrapDraft(project), newMapResultStore())

	request := validInvestigationRequest()
	request.Question = question
	request.Consumer.Surface = surface
	request.TimeContext.EvidenceWindow = field

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	run.result = result
	return run
}

func assertCommittedWindow(t *testing.T, run explicitWindowRun, want RelativeWindowID) {
	t.Helper()
	result := run.result
	if result.Status == InvestigationClarificationRequired {
		t.Fatalf("Status = clarification_required (window_clarification=%v), want an answer: a supplied window is committed", result.WindowClarification != nil)
	}
	if result.WindowClarification != nil {
		t.Fatalf("WindowClarification = %#v, want nil: nothing to confirm on a supplied window", result.WindowClarification)
	}
	if result.StructureNeeds != nil {
		for _, missing := range result.StructureNeeds.Missing {
			if missing == contractsv1.ContextFabricStructureNeedWindow {
				t.Fatalf("StructureNeeds.Missing = %#v, want no window need on a supplied window", result.StructureNeeds.Missing)
			}
		}
	}
	window := result.EffectiveEvidenceWindow
	if window == nil {
		t.Fatal("EffectiveEvidenceWindow = nil, want the supplied window reported")
	}
	if window.RelativeID != want {
		t.Fatalf("EffectiveEvidenceWindow.RelativeID = %q, want %q", window.RelativeID, want)
	}
	if window.Provenance != WindowQuestionStated {
		t.Fatalf("EffectiveEvidenceWindow.Provenance = %q, want %q", window.Provenance, WindowQuestionStated)
	}
	if !run.factRead {
		t.Fatal("the canonical fact read never ran: the answer did not serve the supplied window")
	}
	if run.factWindow == nil || run.factWindow.RelativeID != want {
		t.Fatalf("fact-read window = %#v, want relative_id %q: the answer must read the window it reports", run.factWindow, want)
	}
}

// The exact raw-prod-rev14 15-g1 request shape: question phrase AND field.
func TestCHAOS6557_ExplicitFieldCommits_ProdShape(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCase(t, "mcp", "Which teams need attention over the last 30 days?",
		&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing30D})
	assertCommittedWindow(t, run, RelativeWindowTrailing30D)
}

// The field alone, with no phrase to lean on, on every surface.
func TestCHAOS6557_ExplicitFieldCommits_AnySurface(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"mcp", "workbench"} {
		for _, id := range []RelativeWindowID{RelativeWindowTrailing30D, RelativeWindowTrailing90D, RelativeWindowTrailing365D, RelativeWindowAllTime} {
			t.Run(surface+"/"+string(id), func(t *testing.T) {
				t.Parallel()
				run := runExplicitWindowCase(t, surface, validInvestigationRequest().Question,
					&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: id})
				assertCommittedWindow(t, run, id)
			})
		}
	}
}

// The field wins over a question phrase that names another width.
func TestCHAOS6557_ExplicitFieldWinsOverQuestionPhrase(t *testing.T) {
	t.Parallel()
	run := runExplicitWindowCase(t, "mcp", "Which teams need attention over the last 30 days?",
		&contractsv1.ContextFabricRequestedEvidenceWindow{RelativeID: RelativeWindowTrailing90D})
	assertCommittedWindow(t, run, RelativeWindowTrailing90D)
}

// The exact raw-prod-rev14 02-q1 request shape: phrase only, no field.
func TestCHAOS6557_ExplicitPhraseCommits_ProdShape(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"mcp", "workbench"} {
		t.Run(surface, func(t *testing.T) {
			t.Parallel()
			run := runExplicitWindowCase(t, surface, "What is the team investment mix over the last 30 days?", nil)
			assertCommittedWindow(t, run, RelativeWindowTrailing30D)
		})
	}
}

// The other direction: no supplied period, so the window IS inferred and the
// confirmation turn (winr_ receipts) stays. An ambiguous phrase (two spans)
// names no single window and stays inferred too.
func TestCHAOS6557_InferredWindowStillClarifies(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no period stated":       validInvestigationRequest().Question,
		"two conflicting spans":  "Compare the last 30 days with the last 90 days for the team.",
		"width outside registry": "What shipped in the last 14 days?",
	}
	for name, question := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run := runExplicitWindowCase(t, "mcp", question, nil)
			result := run.result
			if result.Status != InvestigationClarificationRequired {
				t.Fatalf("Status = %q, want clarification_required for an inferred window", result.Status)
			}
			if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
				t.Fatal("WindowClarification is nil or empty, want winr_ receipt-bound options")
			}
			if result.EffectiveEvidenceWindow == nil || result.EffectiveEvidenceWindow.Provenance != WindowInferredDefault {
				t.Fatalf("EffectiveEvidenceWindow = %#v, want a disclosed inferred_default window", result.EffectiveEvidenceWindow)
			}
			if run.factRead {
				t.Fatal("canonical fact read ran for an unconfirmed inferred window")
			}
		})
	}
}

// A stated period is not a guess: it commits whatever window class the
// interpreter picked, including state_snapshot (which has no INFERRED default
// and used to swallow the binder's stated window before it could commit).
func TestCHAOS6557_StatedPhraseCommitsForEveryWindowClass(t *testing.T) {
	t.Parallel()
	for _, class := range []WindowClass{WindowClassStateSnapshot, WindowClassTrendAssessment, WindowClassRecentActivityLookup, ""} {
		t.Run(string(class), func(t *testing.T) {
			t.Parallel()
			interpretation := bootstrapInterpretation()
			interpretation.WindowClass = class
			run := runExplicitWindowCaseWith(t, "mcp", "What is the team investment mix over the last 30 days?", nil, interpretation)
			assertCommittedWindow(t, run, RelativeWindowTrailing30D)
		})
	}
}

// chris 2026-09-25: "in the last month" is a TRAILING window (committed); a
// bare "last month" names the previous CALENDAR month, which the closed
// trailing grammar cannot bound, so it is never committed as trailing_30d:
// it stays an inferred proposal and the confirmation turn stands.
func TestCHAOS6557_BareLastMonthIsNeverCommittedAsTrailing(t *testing.T) {
	t.Parallel()
	for _, question := range []string{
		"Which repository carried the most operational/support work last month?",
		"Last month, which repository carried the most operational/support work?",
		"Which repository carried the most operational/support work last quarter?",
	} {
		run := runExplicitWindowCase(t, "mcp", question, nil)
		if run.result.Status != InvestigationClarificationRequired || run.result.WindowClarification == nil {
			t.Fatalf("%q: status=%q window_clarification=%v, want the confirmation turn", question, run.result.Status, run.result.WindowClarification != nil)
		}
		if w := run.result.EffectiveEvidenceWindow; w == nil || w.Provenance != WindowInferredDefault {
			t.Fatalf("%q: EffectiveEvidenceWindow = %#v, want an inferred_default proposal", question, w)
		}
	}
	for _, question := range []string{
		"Which repository carried the most operational/support work in the last month?",
		"Which repository carried the most operational/support work over the past month?",
	} {
		assertCommittedWindow(t, runExplicitWindowCase(t, "mcp", question, nil), RelativeWindowTrailing30D)
	}
}
