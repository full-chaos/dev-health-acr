package falkorgraph

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// THE ACCEPTANCE PIN, taken at the text retrieval actually matches against
// rather than at the field the producer sets.
//
// A handle a project carries in its alias list but which never reaches its
// search text is a handle no term can find. This composes the projected text
// for a project whose provider key is absent -- the shape of all eighteen
// linear projects in the trial org -- and asserts the slug handle is IN it,
// with a negative control on the same entity so the assertion cannot pass by
// the text merely being long.
func TestAProjectResolvesByItsSlugHandleInTheSearchText(t *testing.T) {
	t.Parallel()
	project := contextfabric.EntityProjection{
		Subject: contractsv1.ContextFabricSubjectRef{
			Kind:        contractsv1.ContextFabricSubjectProject,
			CanonicalID: "project.v2:linear:631fcb5f-c3e9-49ff-b17c-07877aaac9b7",
			Label:       "Chaos Draw",
		},
		// No provider key: the linear shape. The slug handle is the only
		// alias such a project can have.
		Aliases:     []string{"chaos-draw"},
		ProviderIDs: map[string]string{"linear": "631fcb5f-c3e9-49ff-b17c-07877aaac9b7"},
		Properties: map[string]contractsv1.ContextFabricScalarValue{
			"state": {String: stringPointer("backlog")},
		},
	}

	text := projectSearchText(project)
	if !strings.Contains(text, "chaos-draw") {
		t.Fatalf("the slug handle never reached the project's search text: %q", text)
	}
	if !strings.Contains(text, "Chaos Draw") {
		t.Fatalf("the display label left the search text: %q", text)
	}
	// The control: a handle the project does NOT carry must be absent, so the
	// assertion above is about this handle rather than about the template
	// happening to contain most things.
	if strings.Contains(text, "chaos-drawing") {
		t.Fatalf("search text matched a handle the project does not carry: %q", text)
	}

	// WITHOUT the handle, the same project's text no longer carries it --
	// which is the state every linear project was in, and the reason a term
	// spelled the way the URL spells it had nothing to match.
	project.Aliases = nil
	if bare := projectSearchText(project); strings.Contains(bare, "chaos-draw") {
		t.Fatalf("a project with no aliases still published the handle: %q", bare)
	}
}

func stringPointer(value string) *string { return &value }

// THE CUT WOULD HAVE LANDED INSIDE A HANDLE. Three handles whose join is 131
// runes: capping the joined string at 120 ends the line eighteen runes into the
// third handle, publishing a prefix of it as if it were a spelling. Whole-
// handle budgeting leaves the third handle out and keeps the first two intact.
func TestAHandleIsNeverIndexedAsATruncatedPrefix(t *testing.T) {
	t.Parallel()
	first := strings.Repeat("a", 50)
	second := strings.Repeat("b", 50)
	third := strings.Repeat("c", 29)
	handles := []string{first, second, third} // join = 50+1+50+1+29 = 131
	if joined := strings.Join(handles, " "); len([]rune(joined)) <= capHandles {
		t.Fatalf("fixture joins to %d runes, want more than the %d budget or the cut this test is about cannot occur", len([]rune(joined)), capHandles)
	}
	// The old rule, stated as a literal so this test is not computed from the
	// thing under test: the first 120 runes of the join end 18 runes into
	// the third handle.
	if oldCut := capRunes(strings.Join(handles, " "), capHandles); !strings.HasSuffix(oldCut, strings.Repeat("c", 18)) {
		t.Fatalf("fixture no longer places the old cut inside the third handle: %q", oldCut)
	}

	got := retrievalHandles(contextfabric.EntityProjection{Aliases: handles})
	if want := first + " " + second; got != want {
		t.Fatalf("retrievalHandles = %q (%d runes), want the two whole handles that fit and nothing of the third", got, len([]rune(got)))
	}
	if strings.Contains(got, "c") {
		t.Fatalf("a prefix of the handle that did not fit was indexed: %q", got)
	}
}

// A LATER, SHORTER HANDLE THAT STILL FITS IS KEPT. Skipping a handle that
// overflows must not end the line: a short handle after it in sorted order
// still fits the remaining budget.
func TestAShorterHandleAfterAnOverflowingOneIsKept(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("m", 70) // sorts after "a..." and before "z"
	head := strings.Repeat("a", 60) // 60 + 1 + 70 = 131 -> "m..." overflows
	tail := "zz"                    // 60 + 1 + 2 = 63 -> fits after the skip
	got := retrievalHandles(contextfabric.EntityProjection{Aliases: []string{long, head, tail}})
	if want := head + " " + tail; got != want {
		t.Fatalf("retrievalHandles = %q, want %q -- the overflowing handle is skipped, not a stop", got, want)
	}
}

// A SINGLE HANDLE LONGER THAN THE BUDGET IS LEFT OUT WHOLE, never cut to fit.
func TestASingleHandleOverTheBudgetIsLeftOutWhole(t *testing.T) {
	t.Parallel()
	over := strings.Repeat("h", capHandles+1)
	if got := retrievalHandles(contextfabric.EntityProjection{Aliases: []string{over}}); got != "" {
		t.Fatalf("a %d-rune handle produced %q (%d runes), want nothing -- a prefix of it is not its spelling", len([]rune(over)), got, len([]rune(got)))
	}
	exact := strings.Repeat("h", capHandles)
	if got := retrievalHandles(contextfabric.EntityProjection{Aliases: []string{exact}}); got != exact {
		t.Fatalf("a handle of exactly the budget was not kept whole")
	}
}

// BYTE-IDENTICAL UNDER THE CAP. For every entity whose handles fit, the new
// line equals the old capped join exactly, so its search text and embedding
// do not move. Asserted over every templated fixture this package already
// uses, plus a multi-byte case at exactly the budget, rather than one hand-
// picked entity.
func TestHandleLinesUnderTheBudgetAreByteIdentical(t *testing.T) {
	t.Parallel()
	entities := fullTemplateEntities()
	entities = append(entities,
		contextfabric.EntityProjection{Aliases: []string{"BILL", "billing"}, PreviousNames: []string{"Old Billing"}},
		contextfabric.EntityProjection{Aliases: []string{strings.Repeat("é", 59), strings.Repeat("ö", 60)}}, // 59+1+60 = 120 runes, 240 bytes
	)
	compared := 0
	for _, entity := range entities {
		handles := make([]string, 0)
		handles = append(handles, entity.Aliases...)
		handles = append(handles, entity.ProviderAliases...)
		handles = append(handles, entity.PreviousNames...)
		joined := strings.Join(graphrank.UniqueSorted(handles), " ")
		if len([]rune(joined)) > capHandles {
			continue
		}
		compared++
		old := capRunes(joined, capHandles)
		if got := retrievalHandles(entity); got != old {
			t.Errorf("%s: handle line changed under the budget: got %q, want the old %q", entity.Subject.CanonicalID, got, old)
		}
	}
	if compared < 3 {
		t.Fatalf("only %d under-budget entities compared -- the identity control is too thin to mean anything", compared)
	}
}
