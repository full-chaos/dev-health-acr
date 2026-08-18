package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestBindHandles_PR532NeverBindsPR53 is R3's own load-bearing pin (design
// brief v5 §1.2): maximal-munch/word-boundary semantics mean "PR 532" can
// never bind handle "PR 53".
func TestBindHandles_PR532NeverBindsPR53(t *testing.T) {
	t.Parallel()
	bound := BindHandles("Why did PR 532 fail in CI?")
	if len(bound) != 1 {
		t.Fatalf("BindHandles = %#v, want exactly 1 handle", bound)
	}
	if bound[0].Kind != contextfabric.SubjectPullRequest || bound[0].Value != "532" {
		t.Fatalf("bound[0] = %#v, want pull_request/532", bound[0])
	}
	for _, h := range bound {
		if h.Value == "53" {
			t.Fatalf("BindHandles bound the substring \"53\" out of \"532\" -- R3 violated")
		}
	}
}

func TestBindHandles_PRVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     string
	}{
		{"PR 532 failed", "532"},
		{"PR#532 failed", "532"},
		{"pr 532", "532"},
		{"see PR532", "532"}, // no space/# needed: \s*#?\s* matches zero characters, so "PR" directly abutting the digits still binds
	}
	for _, tc := range cases {
		bound := BindHandles(tc.question)
		if tc.want == "" {
			continue
		}
		found := false
		for _, h := range bound {
			if h.Kind == contextfabric.SubjectPullRequest && h.Value == tc.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("BindHandles(%q) = %#v, want a pull_request handle with value %q", tc.question, bound, tc.want)
		}
	}
}

func TestBindHandles_WorkItemTicketKey(t *testing.T) {
	t.Parallel()
	bound := BindHandles("what's the status of CHAOS-3896?")
	if len(bound) != 1 || bound[0].Kind != contextfabric.SubjectWorkItem || bound[0].Value != "CHAOS-3896" {
		t.Fatalf("BindHandles = %#v, want one work_item handle CHAOS-3896", bound)
	}
}

func TestBindHandles_CIRunID(t *testing.T) {
	t.Parallel()
	bound := BindHandles("did CI run 18234567890 pass?")
	if len(bound) != 1 || bound[0].Kind != contractsv1.ContextFabricSubjectCIRun || bound[0].Value != "18234567890" {
		t.Fatalf("BindHandles = %#v, want one ci_pipeline_run handle", bound)
	}
}

// TestBindHandles_CIRunGrammarNeverBindsOrdinaryEnglish is an adversarial
// review regression pin: an earlier version of the ci_run_id pattern had
// no word boundary after the "run" keyword and accepted any alphanumeric
// token, so it bound ordinary English words ("running" -> value "ning",
// "run break" -> value "break") as if they were CI run ids -- minting a
// handle-bound census predicate, and with no other keyed discriminator
// present, a STRUCTURALLY FALSE would_no_match (exactly the class D0/§3
// forbid). Every case here must bind NOTHING.
func TestBindHandles_CIRunGrammarNeverBindsOrdinaryEnglish(t *testing.T) {
	t.Parallel()
	questions := []string{
		"who is running the deploy pipeline?",
		"did the nightly run break main?",
		"is the build run stable this week",
		"can you run analysis on this?",
	}
	for _, question := range questions {
		bound := BindHandles(question)
		for _, h := range bound {
			if h.Grammar == "ci_run_id" {
				t.Fatalf("BindHandles(%q) bound a ci_run_id handle %#v from ordinary English -- R3 violated", question, h)
			}
		}
	}
}

// TestBindHandles_CIRunGrammarNeverCollidesWithOtherHandles pins that "run"
// immediately followed by a PR number or a ticket key never ALSO binds as
// a ci_run_id (which would falsely trigger multi_handle for what is
// really a single-subject question).
func TestBindHandles_CIRunGrammarNeverCollidesWithOtherHandles(t *testing.T) {
	t.Parallel()
	if bound := BindHandles("can you run analysis on PR 532?"); IsMultiHandle(bound) {
		t.Fatalf("BindHandles = %#v, want exactly the PR handle, not a spurious ci_run_id collision", bound)
	}
	if bound := BindHandles("did CI run CHAOS-3896 break the build?"); IsMultiHandle(bound) {
		t.Fatalf("BindHandles = %#v, want exactly the work_item handle, not a spurious ci_run_id collision", bound)
	}
}

func TestBindHandles_NoMatchIsEmpty(t *testing.T) {
	t.Parallel()
	if bound := BindHandles("how healthy is the auth service?"); len(bound) != 0 {
		t.Fatalf("BindHandles = %#v, want no bound handles", bound)
	}
}

func TestBindHandles_ExactSourceSpan(t *testing.T) {
	t.Parallel()
	question := "Why did PR 532 fail?"
	bound := BindHandles(question)
	if len(bound) != 1 {
		t.Fatalf("BindHandles = %#v", bound)
	}
	span := question[bound[0].SpanStart:bound[0].SpanEnd]
	if span != "PR 532" {
		t.Fatalf("span = %q, want %q", span, "PR 532")
	}
}

func TestIsMultiHandle(t *testing.T) {
	t.Parallel()
	if IsMultiHandle(BindHandles("PR 532 failed")) {
		t.Fatalf("single handle reported as multi-handle")
	}
	if !IsMultiHandle(BindHandles("PR 532 and PR 533 both failed")) {
		t.Fatalf("two same-kind handles: want IsMultiHandle=true")
	}
	if !IsMultiHandle(BindHandles("PR 532 failed in CI run 18234567890")) {
		t.Fatalf("two different-kind handles: want IsMultiHandle=true")
	}
}

func TestIsCensusKindRegistered(t *testing.T) {
	t.Parallel()
	registered := []CensusKind{contextfabric.SubjectPullRequest, contextfabric.SubjectWorkItem, contractsv1.ContextFabricSubjectCIRun, contractsv1.ContextFabricSubjectPullRequestReview}
	for _, kind := range registered {
		if !IsCensusKindRegistered(kind) {
			t.Fatalf("IsCensusKindRegistered(%s) = false, want true", kind)
		}
	}
	if IsCensusKindRegistered(contextfabric.SubjectRepository) {
		t.Fatalf("IsCensusKindRegistered(repository) = true, want false -- repository is not a Slice-1 stall kind")
	}
	if IsCensusKindRegistered(contextfabric.SubjectIncident) {
		t.Fatalf("IsCensusKindRegistered(incident) = true, want false")
	}
}
