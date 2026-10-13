package sidecar

import (
	"strings"
	"testing"
)

func TestRenderedDriverStatesTheServerNoteRightAfterTheSummary(t *testing.T) {
	projection := baseProjection()
	projection.PrincipalDrivers[0].Summary = "All 12 teams are ready."
	projection.PrincipalDrivers[0].SourceStateNote = "The service marks this driver as partial: the source read was truncated."
	rendered, _ := RenderAnswerProjectionMarkdown(projection, 200000)
	if !strings.Contains(rendered, "> All 12 teams are ready.\n> The service marks this driver as partial") {
		t.Errorf("note not rendered right after the summary:\n%s", rendered)
	}
}
