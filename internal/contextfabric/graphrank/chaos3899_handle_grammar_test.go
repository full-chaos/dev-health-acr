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

// TestBindHandles_PullRequestSpelledOut pins the CHAOS-4012 widening: the
// spelled-out "pull request" keyword must bind exactly like "PR" already
// did, sourced from the kind's own name/base table (chaos3899_handle_grammar.go
// doc comment), not an invented synonym.
func TestBindHandles_PullRequestSpelledOut(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     string
	}{
		{"pull request 532 failed", "532"},
		{"pull request #532 failed", "532"},
		{"Pull Request 532", "532"},
	}
	for _, tc := range cases {
		bound := BindHandles(tc.question)
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

// TestBindHandles_CIPipelineVariants pins the CHAOS-4012 widening: GitLab's
// own product terminology for this table's concept is "pipeline" (the base
// table is ci_pipeline_runs, chaos3899_census_registry.go; gitlab.com is a
// confirmed live provider, providerQualifiedNamePattern in
// chaos3899_source_native_grammar.go) -- "pipeline"/"CI pipeline"/"pipeline
// run" must bind exactly like "run"/"CI run" already did.
func TestBindHandles_CIPipelineVariants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     string
	}{
		{"did pipeline 18234567 pass?", "18234567"},
		{"did CI pipeline #18234567 pass?", "18234567"},
		{"did pipeline run 18234567 pass?", "18234567"},
	}
	for _, tc := range cases {
		bound := BindHandles(tc.question)
		found := false
		for _, h := range bound {
			if h.Kind == contractsv1.ContextFabricSubjectCIRun && h.Value == tc.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("BindHandles(%q) = %#v, want a ci_pipeline_run handle with value %q", tc.question, bound, tc.want)
		}
	}
}

// TestBindHandles_CIPipelineGrammarNeverBindsOrdinaryEnglish is the
// "pipeline" keyword's own companion to TestBindHandles_CIRunGrammarNeverBindsOrdinaryEnglish
// -- the SAME word-boundary discipline that fix pinned for "run"/"running"
// must hold for "pipeline"/"pipelining" too.
func TestBindHandles_CIPipelineGrammarNeverBindsOrdinaryEnglish(t *testing.T) {
	t.Parallel()
	questions := []string{
		"who is pipelining the deploy?",
		"the pipeline is broken again",
		"can you pipeline this analysis?",
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

// CHAOS-12633: a GitLab merge request is stored as a pull request
// (git_pull_requests.number holds its iid), so the GitLab spellings bind the
// same pull_request_number entry as "PR N" / "pull request N". Executed
// before this change: every one of these bound nothing.
func TestBindHandles_MergeRequestSpellings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		question string
		want     string
	}{
		{"What is the status of merge request !42 in platform/ledger-api?", "42"},
		{"What is the status of MR !42 in platform/ledger-api?", "42"},
		{"What is the status of merge request 42 in platform/ledger-api?", "42"},
		{"Merge Request #42 failed", "42"},
		{"see MR42", "42"},
	}
	for _, tc := range cases {
		bound := BindHandles(tc.question)
		if len(bound) != 1 || bound[0].Kind != contextfabric.SubjectPullRequest || bound[0].Grammar != "pull_request_number" || bound[0].Value != tc.want {
			t.Fatalf("BindHandles(%q) = %#v, want exactly one pull_request_number handle %q", tc.question, bound, tc.want)
		}
	}
}

// The new spellings bind only a keyword followed by digits: ordinary English
// and the repository reference keep their own readings.
func TestBindHandles_MergeRequestSpellingsNeverBindOrdinaryText(t *testing.T) {
	t.Parallel()
	for _, question := range []string{
		"Why are merge requests slow?",
		"Ask Mr Smith about the merge request",
		"merge request review time last quarter",
	} {
		if bound := BindHandles(question); len(bound) != 0 {
			t.Fatalf("BindHandles(%q) = %#v, want none", question, bound)
		}
	}
	bound := BindHandles("What is the status of full.chaos/dev-health-ops!1?")
	if len(bound) != 1 || bound[0].Kind != contextfabric.SubjectWorkItem || bound[0].Grammar != workItemRepoReferenceGrammar {
		t.Fatalf("a repository reference = %#v, want its work_item_repo_reference reading unchanged", bound)
	}
}
