package sidecar

import (
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestRenderedAnswerStatesTheTimeItAnswersFor is the display half of
// CHAOS-3781's exposure into the MCP surface.
//
// The rendering prints a "## Current state" heading. On a historical answer
// that heading is a false claim on its own, so the temporal label must
// appear -- and appear ABOVE it, where a reader meets it before the prose
// rather than after having already read the answer as current.
//
// Mutation: deleting the temporalLines loop from
// RenderAnswerProjectionMarkdown fails every subtest; moving it below the
// judgment fails the ordering assertion alone.
func TestRenderedAnswerStatesTheTimeItAnswersFor(t *testing.T) {
	requested := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	effective := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)

	projection := baseProjection()
	projection.Temporal = &contractsv1.ContextFabricTemporalLabel{
		Requested:        contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &requested},
		Effective:        contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalValidTime, AsOf: &effective},
		Grain:            contractsv1.ContextFabricGrainDay,
		CoverageComplete: false,
	}

	rendered, _ := RenderAnswerProjectionMarkdown(projection, 200000)

	// The axis is inline-escaped like every other closed-vocabulary value
	// this renderer prints, so the expectation is built through the same
	// helper rather than restating the escaped spelling.
	for _, want := range []string{
		safeInline(string(contractsv1.ContextFabricTemporalValidTime)),
		effective.Format(time.RFC3339),
		requested.Format(time.RFC3339),
		"Time grain: day",
		"could not answer for this time",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered answer never states %q, so a reader cannot tell which time it covers", want)
		}
	}

	// Ordering: the label must precede the judgment and the current-state
	// heading, not trail them.
	label := strings.Index(rendered, "Answers for:")
	judgment := strings.Index(rendered, "## Judgment")
	state := strings.Index(rendered, "## Current state")
	if label < 0 {
		t.Fatal("the rendering carries no temporal line at all")
	}
	if judgment >= 0 && label > judgment {
		t.Error("the temporal label renders below the judgment, so the reader meets the answer before its time")
	}
	if state >= 0 && label > state {
		t.Error("the temporal label renders below the current-state heading it qualifies")
	}
}

// TestRenderedRangeAnswerStatesBothEndpoints covers the range axis, whose
// SHAPE differs: two instants rather than one. A renderer that handled only
// the point-in-time axis would print an empty or misleading window here.
func TestRenderedRangeAnswerStatesBothEndpoints(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	effectiveEnd := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)

	projection := baseProjection()
	projection.Temporal = &contractsv1.ContextFabricTemporalLabel{
		Requested:        contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalRange, Start: &start, End: &end},
		Effective:        contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalRange, Start: &start, End: &effectiveEnd},
		Grain:            contractsv1.ContextFabricGrainInstant,
		CoverageComplete: true,
	}

	rendered, _ := RenderAnswerProjectionMarkdown(projection, 200000)

	for _, want := range []string{start.Format(time.RFC3339), effectiveEnd.Format(time.RFC3339), end.Format(time.RFC3339)} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered range answer never states %q", want)
		}
	}
	// A complete-coverage answer must not carry the caveat: a warning that
	// fires on every answer is a warning a reader learns to ignore.
	if strings.Contains(rendered, "could not answer for this time") {
		t.Error("a fully covered answer carries the incomplete-coverage caveat")
	}
}

// TestRenderedCurrentAnswerCarriesNoTemporalLine keeps the current-axis
// rendering byte-identical to what it was before CHAOS-3781. Every answer
// today is a current answer; a stray "Answers for: current" line on all of
// them would be noise, not information.
func TestRenderedCurrentAnswerCarriesNoTemporalLine(t *testing.T) {
	projection := baseProjection()
	projection.Temporal = nil

	rendered, _ := RenderAnswerProjectionMarkdown(projection, 200000)

	for _, unwanted := range []string{"Answers for:", "Time grain:", "Requested:"} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("a current answer renders %q", unwanted)
		}
	}
}
