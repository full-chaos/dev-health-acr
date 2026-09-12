package falkorgraph

import (
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
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
