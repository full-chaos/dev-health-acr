package main

import "testing"

// These cover CHAOS-3565's cohort-scoping of record_episode_never_observed:
// the check must stay the unconditional "never observed" default for every
// existing/unset oracle, and only flip for a task whose oracle explicitly
// marks it as design-partner-cohort-scoped via episode_write=true.

func TestRecordEpisodeExpectation_defaultsToNeverObserved(t *testing.T) {
	expect, label := recordEpisodeExpectation(Oracle{})
	if expect || label != "not observed" {
		t.Fatalf("default expectation = (%t, %q), want (false, %q)", expect, label, "not observed")
	}
}

func TestRecordEpisodeExpectation_explicitFalseStaysNeverObserved(t *testing.T) {
	write := false
	expect, label := recordEpisodeExpectation(Oracle{EpisodeWrite: &write})
	if expect || label != "not observed" {
		t.Fatalf("explicit-false expectation = (%t, %q), want (false, %q)", expect, label, "not observed")
	}
}

func TestRecordEpisodeExpectation_cohortScopedTrueExpectsObserved(t *testing.T) {
	write := true
	expect, label := recordEpisodeExpectation(Oracle{EpisodeWrite: &write})
	if !expect || label != "observed" {
		t.Fatalf("cohort-scoped expectation = (%t, %q), want (true, %q)", expect, label, "observed")
	}
}
