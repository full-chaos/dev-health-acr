package graphrank

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The ticket's repro: any tracker's key in free text has the handle's shape.
func TestAnyTrackerKeyInFreeTextBindsAHandle(t *testing.T) {
	t.Parallel()
	for question, want := range map[string]string{
		"what is the status of CHAOS-204?":  "CHAOS-204",
		"what is the status of OPS-204?":    "OPS-204",
		"what is the status of ENG-204?":    "ENG-204",
		"is PROJ-1 done?":                   "PROJ-1",
		"what blocks acme/widget#7?":        "acme/widget#7",
		"is group/sub/proj!12 merged?":      "group/sub/proj!12",
		"what blocks full-chaos/.github#3?": "full-chaos/.github#3",
	} {
		bound := BindHandles(question)
		if len(bound) != 1 || bound[0].Value != want || bound[0].Kind != contextfabric.SubjectWorkItem {
			t.Errorf("BindHandles(%q) = %+v, want one work item handle %q", question, bound, want)
		}
	}
}

// A key inside a repository reference is the reference, not a second handle;
// nothing else in the registry can hold a key.
func TestAKeyShapeStaysDisjointFromEveryOtherHandle(t *testing.T) {
	t.Parallel()
	for question, want := range map[string][]string{
		"what blocks acme/OPS-2#7?":            {"work_item_repo_reference"},
		"is PR-532 open?":                      {"work_item_ticket_key"},
		"is RUN-12345 green?":                  {"work_item_ticket_key"},
		"did PR 532 fix OPS-9 in run 1234567?": {"pull_request_number", "work_item_ticket_key", "ci_run_id"},
		"why did pipeline 18234567 fail":       {"ci_run_id"},
		"utf-8 and gpt-5 in lower case":        nil,
	} {
		bound := BindHandles(question)
		var got []string
		for _, handle := range bound {
			got = append(got, handle.Grammar)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("BindHandles(%q) grammars = %v, want %v", question, got, want)
		}
		for i := range bound {
			for j := range bound {
				if i != j && bound[i].SpanStart < bound[j].SpanEnd && bound[j].SpanStart < bound[i].SpanEnd {
					t.Errorf("%q: spans of %+v and %+v overlap", question, bound[i], bound[j])
				}
			}
		}
	}
}

type prefixReads struct {
	held     map[string]bool
	err      error
	calls    []string
	grants   [][]string
	unscoped bool
}

func (r *prefixReads) read(ctx context.Context, _ string, prefix string) (WorkItemKeyPrefixOutcome, error) {
	r.calls = append(r.calls, prefix)
	grant, bound := contextfabric.CensusGrantFrom(ctx)
	if bound {
		r.grants = append(r.grants, grant)
	}
	if r.err != nil {
		return WorkItemKeyPrefixOutcome{}, r.err
	}
	return WorkItemKeyPrefixOutcome{Seen: r.held[prefix], GrantApplied: bound && !r.unscoped}, nil
}

func gateHandles(t *testing.T, principal storage.Principal, question string, reads *prefixReads, tracer ResolutionTracer) []BoundHandle {
	t.Helper()
	deps := ResolveDeps{ResolutionTracer: tracer}
	if reads != nil {
		deps.WorkItemKeyPrefix = reads.read
	}
	request := testRequest()
	request.Question = question
	return bindQuestionHandles(context.Background(), principal, request, deps)
}

func grammarsOf(bound []BoundHandle) string {
	var out []string
	for _, handle := range bound {
		out = append(out, handle.Grammar+":"+handle.Value)
	}
	return strings.Join(out, " ")
}

// The plan's case table. A key counts when the caller can read its prefix; a word
// of the same shape does not, and does not take the outcome from a pull request.
func TestAKeyCountsOnlyWhenTheCallerHoldsItsPrefix(t *testing.T) {
	t.Parallel()
	reads := &prefixReads{held: map[string]bool{"OPS-": true, "CHAOS-": true, "acme/widget#": true}}
	for question, want := range map[string]string{
		"what is the status of OPS-204?":     "work_item_ticket_key:OPS-204",
		"what is the status of OPS-99999?":   "work_item_ticket_key:OPS-99999",
		"what is the status of CHAOS-204?":   "work_item_ticket_key:CHAOS-204",
		"How is the UTF-8 migration going?":  "",
		"Are we on HTTP-2 yet?":              "",
		"Who owns ISO-27001 work?":           "",
		"Which teams use GPT-5?":             "",
		"What did CVE-2026 cost us?":         "",
		"Does PR 532 fix the UTF-8 bug?":     "pull_request_number:532",
		"what blocks acme/widget#7?":         "work_item_repo_reference:acme/widget#7",
		"what blocks acme/other#7?":          "",
		"see docs.example.com/acme/widget#7": "",
		"see https://docs.example.com/a/b#7": "",
		"Does OPS-1 block CHAOS-2 or UTF-8?": "work_item_ticket_key:OPS-1 work_item_ticket_key:CHAOS-2",
	} {
		if got := grammarsOf(gateHandles(t, storage.Principal{OrgID: "org_1"}, question, reads, nil)); got != want {
			t.Errorf("%q: handles %q, want %q", question, got, want)
		}
	}
}

// One read per prefix per turn, and never past the bound.
func TestAPrefixIsReadOncePerTurnAndWithinTheBound(t *testing.T) {
	t.Parallel()
	reads := &prefixReads{held: map[string]bool{"OPS-": true}}
	gateHandles(t, storage.Principal{OrgID: "org_1"}, "OPS-1, OPS-2 and OPS-3", reads, nil)
	if len(reads.calls) != 1 {
		t.Fatalf("reads = %v, want one", reads.calls)
	}
	many := &prefixReads{held: map[string]bool{}}
	var question []string
	for _, prefix := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J"} {
		many.held[prefix+"-"] = true
		question = append(question, prefix+"-1")
	}
	bound := gateHandles(t, storage.Principal{OrgID: "org_1"}, strings.Join(question, " "), many, nil)
	if len(many.calls) != maxWorkItemKeyPrefixReads || len(bound) != maxWorkItemKeyPrefixReads {
		t.Fatalf("reads = %d, bound = %d, want %d of each", len(many.calls), len(bound), maxWorkItemKeyPrefixReads)
	}
}

// A failed read is read once per prefix like any other, and counts toward the
// bound, so an outage cannot multiply the reads one question makes.
func TestAFailedPrefixReadIsNotRetriedInTheTurn(t *testing.T) {
	t.Parallel()
	reads := &prefixReads{err: errors.New("down")}
	var question []string
	for i := 1; i <= 9; i++ {
		question = append(question, "OPS-"+strconv.Itoa(i))
	}
	for i := 0; i < 12; i++ {
		question = append(question, string(rune('A'+i))+"-1")
	}
	if bound := gateHandles(t, storage.Principal{OrgID: "org_1"}, strings.Join(question, " "), reads, nil); len(bound) != 0 {
		t.Fatalf("bound %v, want nothing", bound)
	}
	if len(reads.calls) != maxWorkItemKeyPrefixReads {
		t.Fatalf("reads = %v, want %d: one per prefix, failures counted", reads.calls, maxWorkItemKeyPrefixReads)
	}
}

// A restricted caller's read carries its grant, the same grant its census reads
// under; an unrestricted caller's carries none.
func TestThePrefixReadRunsUnderTheCallersGrant(t *testing.T) {
	t.Parallel()
	restricted := &prefixReads{held: map[string]bool{"OPS-": true}}
	if got := gateHandles(t, restrictedCaller(t, "acme/inside"), "is OPS-1 done?", restricted, nil); len(got) != 1 {
		t.Fatalf("restricted: handles %v", got)
	}
	if len(restricted.grants) != 1 || strings.Join(restricted.grants[0], ",") != "acme/inside" {
		t.Fatalf("restricted read grants = %v, want the caller's", restricted.grants)
	}
	open := &prefixReads{held: map[string]bool{"OPS-": true}}
	gateHandles(t, storage.Principal{OrgID: "org_1"}, "is OPS-1 done?", open, nil)
	if len(open.grants) != 0 {
		t.Fatalf("unrestricted read grants = %v, want none", open.grants)
	}
}

// A read that cannot answer leaves the key unbound, the pull request bound, and
// a warning with counts and no token.
func TestAnUnreadPrefixLeavesTheKeyUnboundAndWarns(t *testing.T) {
	t.Parallel()
	for name, reads := range map[string]*prefixReads{
		"no read wired":        nil,
		"read fails":           {err: errors.New("boom")},
		"grant not applied":    {held: map[string]bool{"OPS-": true}, unscoped: true},
		"grant not applicable": {err: ErrCensusGrantNotApplied},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
			principal := restrictedCaller(t, "acme/inside")
			if got := grammarsOf(gateHandles(t, principal, "does PR 5 fix OPS-1?", reads, tracer)); got != "pull_request_number:5" {
				t.Fatalf("handles %q, want the pull request alone", got)
			}
			log, err := certify.Parse(buf.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.WorkItemKeyPrefixUnread, Want: map[string]any{"request_id": testRequest().RequestID, "key_shapes": 1, "held": 0, "unread": 1}}); err != nil {
				t.Fatalf("Certify = %v", err)
			}
			if strings.Contains(buf.String(), "OPS") {
				t.Fatalf("the warning names the key: %s", buf.String())
			}
		})
	}
}

// A turn whose keys were all read says nothing at Warn.
func TestAReadPrefixWarnsNothing(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	tracer := NewSlogResolutionTracer(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	gateHandles(t, storage.Principal{OrgID: "org_1"}, "is UTF-8 or OPS-1 done?", &prefixReads{held: map[string]bool{"OPS-": true}}, tracer)
	if strings.Contains(buf.String(), eventspec.WorkItemKeyPrefixUnread.Msg) {
		t.Fatalf("warned on a turn with no unread prefix: %s", buf.String())
	}
}

// A pool candidate of any tracker is offered as a handle it can redeem.
func TestAPoolWorkItemOfAnyTrackerIsOfferedAsAHandle(t *testing.T) {
	t.Parallel()
	for alias, grammar := range map[string]string{
		"OPS-204":           workItemTicketKeyGrammar,
		"acme/widget#7":     workItemRepoReferenceGrammar,
		"group/sub/proj!12": workItemRepoReferenceGrammar,
	} {
		if !ValidateHandleGrammar(contextfabric.SubjectWorkItem, grammar, alias) {
			t.Errorf("ValidateHandleGrammar(%s, %q) = false", grammar, alias)
		}
		if column, ok := HandleSourceColumn(contextfabric.SubjectWorkItem, grammar); !ok || column != "work_items.work_item_id" {
			t.Errorf("HandleSourceColumn(%s) = %q %v", grammar, column, ok)
		}
	}
	for _, value := range []string{"utf-8", "OPS-", "acme/widget", "acme/widget#x"} {
		if ValidateHandleGrammar(contextfabric.SubjectWorkItem, workItemTicketKeyGrammar, value) || ValidateHandleGrammar(contextfabric.SubjectWorkItem, workItemRepoReferenceGrammar, value) {
			t.Errorf("ValidateHandleGrammar accepted %q", value)
		}
	}
}
