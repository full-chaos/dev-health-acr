package projectionrun

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

type opaqueVersionSource struct{ version string }

func (opaqueVersionSource) NextProjectionBatch(context.Context, contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	return contextfabric.ProjectionBatch{}, false, nil
}

func (s opaqueVersionSource) CurrentProjectionSourceVersion() string { return s.version }

func keyFor(versions map[string]string) string {
	c := &Coordinator{sources: map[string]contextfabric.ProjectionSource{}}
	for name, version := range versions {
		c.sourceNames = append(c.sourceNames, name)
		c.sources[name] = opaqueVersionSource{version}
	}
	return c.sourceVersionsKey()
}

func TestSourceVersionsKeyDistinguishesSeparatorLookalikes(t *testing.T) {
	first := keyFor(map[string]string{"a": "x;b=y", "b": "z"})
	second := keyFor(map[string]string{"a": "x", "b": "y;b=z"})
	if first == second {
		t.Fatalf("distinct version sets share the key %q", first)
	}
}
