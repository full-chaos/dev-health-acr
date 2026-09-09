package devhealthfacts_test

// CHAOS-5438 -- a FULL PAGE and a TRUNCATED page must be distinguishable.
//
// WHAT THIS IS AN ORACLE FOR. Every work-item-target provider computes
// `Truncated: len(rows) >= maxFactRowsPerQuery` against a statement whose own
// LIMIT is that same 200 (devhealthfacts/shared.go:84,94-96 for the two
// dependency providers; github.com/full-chaos/dev-health-go/readers'
// DefaultRowLimit for the other five). Reading N rows under `LIMIT N` cannot
// tell "there were exactly N" from "there were more and we stopped", so a
// population of exactly 200 is reported as truncated and the answer is
// degraded for a page that was in fact COMPLETE. That is the precise ambiguity
// the CHAOS-4099 ruling's own invariant 8 forbids and that the limit+1
// discipline exists to prevent -- the same discipline the scope expander
// already honours (fact_scope.go's expand() reads Limit+1 and enforces the cap
// from the overflow row).
//
// THE FIX these pin: probe 201 rows, serve at most 200 facts, and set
// Truncated from the presence of the 201st row -- never from a full page.
//
// RED-FIRST at 0945a53dfdad0e84ba8244c59e8e9d85a7d195f2 (the unfixed tree):
// the exactly-200 arms fail on Truncated, the 201 arms fail on the served
// fact bound, and the statement arm fails on `LIMIT 200`. The 199 arm is the
// NEGATIVE CONTROL: it passes on the unfixed tree too, so a uniformly broken
// harness cannot masquerade as a real red.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// factRowProbeLimit is what a work-item provider's statement must READ, and
// factRowOutputBound is what it may SERVE. They differ by exactly one so the
// 201st row is the truncation evidence and never part of the answer.
const (
	factRowProbeLimit  = 201
	factRowOutputBound = 200
)

// probeArm is one work-item-target provider under the probe contract.
//
// rowsFor(n) must build n rows the provider will COUNT, and subjectsFor(n) the
// subjects those rows resolve against -- for the two dependency providers one
// subject carries every row (dependency cardinality exceeds subject
// cardinality, which is exactly why the provider bound is separate from the
// scope cap); for the other five it is one row per subject.
type probeArm struct {
	name        string
	kind        contextfabric.FactKind
	match       string
	subjectsFor func(n int) []contextfabric.SubjectRef
	rowsFor     func(n int) [][]any
}

func workItemSubjects(n int) []contextfabric.SubjectRef {
	subjects := make([]contextfabric.SubjectRef, n)
	for i := 0; i < n; i++ {
		subjects[i] = workItemSubject("repo-1", "WIDGET-"+strconv.Itoa(i))
	}
	return subjects
}

func oneWorkItemSubject(int) []contextfabric.SubjectRef {
	return []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")}
}

// repositorySubjects is CHAOS-5474's addition. The repository-subject
// branches of IdentityProvider and MembershipProvider OR into the SAME
// result-level Truncated flag as their work-item branches, so a full
// repository page marked the whole result truncated however honest the
// work-item side became -- reproduced live at 10d5405f before the fix.
func repositorySubjects(n int) []contextfabric.SubjectRef {
	subjects := make([]contextfabric.SubjectRef, n)
	for i := 0; i < n; i++ {
		subjects[i] = repoSubject("repo-" + strconv.Itoa(i))
	}
	return subjects
}

func probeArms() []probeArm {
	perSubject := func(build func(i int) []any) func(n int) [][]any {
		return func(n int) [][]any {
			rows := make([][]any, n)
			for i := 0; i < n; i++ {
				rows[i] = build(i)
			}
			return rows
		}
	}
	return []probeArm{
		{
			name: "status", kind: contextfabric.FactStatus, match: "FROM work_items",
			subjectsFor: workItemSubjects,
			rowsFor: perSubject(func(i int) []any {
				return []any{"WIDGET-" + strconv.Itoa(i), "open", "repo-1"}
			}),
		},
		{
			name: "work", kind: contextfabric.FactWork, match: "FROM work_items",
			subjectsFor: workItemSubjects,
			rowsFor: perSubject(func(i int) []any {
				return []any{"WIDGET-" + strconv.Itoa(i), "Investigate checkout flake", "repo-1"}
			}),
		},
		{
			name: "actual_completion", kind: contextfabric.FactActualCompletion, match: "FROM work_items",
			subjectsFor: workItemSubjects,
			rowsFor: perSubject(func(i int) []any {
				return []any{"WIDGET-" + strconv.Itoa(i), uint8(0), time.Unix(0, 0).UTC(), "repo-1"}
			}),
		},
		{
			name: "identity", kind: contextfabric.FactIdentity, match: "FROM work_items",
			subjectsFor: workItemSubjects,
			rowsFor: perSubject(func(i int) []any {
				return []any{"WIDGET-" + strconv.Itoa(i), "Investigate checkout flake", "repo-1"}
			}),
		},
		{
			name: "membership", kind: contextfabric.FactMembership, match: "FROM work_items",
			subjectsFor: workItemSubjects,
			rowsFor: perSubject(func(i int) []any {
				return []any{"WIDGET-" + strconv.Itoa(i), "repo-1", "example-org/widget-service"}
			}),
		},
		{
			// One blocked work item with a pathological number of blockers:
			// the shape maxFactRowsPerQuery was introduced for (H6/H7).
			name: "blockers", kind: contextfabric.FactBlockers, match: "FROM work_item_dependencies",
			subjectsFor: oneWorkItemSubject,
			rowsFor: perSubject(func(i int) []any {
				return []any{"BLOCKER-" + strconv.Itoa(i), "WIDGET-101", "repo-1"}
			}),
		},
		{
			// CHAOS-5474: the repository-subject half of the SAME two
			// providers, whose shared truncation flag is what made the
			// original scope boundary unusable.
			name: "identity_repository", kind: contextfabric.FactIdentity, match: "FROM repos",
			subjectsFor: repositorySubjects,
			rowsFor: perSubject(func(i int) []any {
				return []any{"repo-" + strconv.Itoa(i), "acme/r" + strconv.Itoa(i), "github"}
			}),
		},
		{
			name: "membership_repository", kind: contextfabric.FactMembership, match: "FROM repos",
			subjectsFor: repositorySubjects,
			rowsFor: perSubject(func(i int) []any {
				return []any{"repo-" + strconv.Itoa(i)}
			}),
		},
		{
			name: "required_children", kind: contextfabric.FactRequiredChildren, match: "FROM work_item_dependencies",
			subjectsFor: oneWorkItemSubject,
			rowsFor: perSubject(func(i int) []any {
				return []any{"WIDGET-101", "CHILD-" + strconv.Itoa(i), "requires", "repo-1"}
			}),
		},
	}
}

func readProbeArm(t *testing.T, arm probeArm, rowCount int) (*fakeClient, contextfabric.FactProviderResult) {
	t.Helper()
	client := &fakeClient{tables: []fakeTable{{match: arm.match, rows: arm.rowsFor(rowCount)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), arm.kind)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time:     contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind:     arm.kind,
		Subjects: arm.subjectsFor(rowCount),
	})
	if err != nil {
		t.Fatalf("%s: ReadFacts() error = %v", arm.name, err)
	}
	return client, result
}

// TestChaos5438_AnExactlyFullPageIsNotTruncated is the defect itself. A
// population of EXACTLY the output bound is COMPLETE: every row that exists
// was read and served, so degrading the answer as truncated asserts a
// shortfall that did not happen.
func TestChaos5438_AnExactlyFullPageIsNotTruncated(t *testing.T) {
	t.Parallel()
	for _, arm := range probeArms() {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			_, result := readProbeArm(t, arm, factRowOutputBound)
			if result.Truncated {
				t.Fatalf("%s: Truncated = true on an exactly-full page of %d rows -- a complete population read as a shortfall", arm.name, factRowOutputBound)
			}
			if len(result.Facts) != factRowOutputBound {
				t.Fatalf("%s: len(Facts) = %d, want %d -- every row of a complete page is servable", arm.name, len(result.Facts), factRowOutputBound)
			}
		})
	}
}

// TestChaos5438_TheProbeRowProvesTruncationAndIsNeverServed pins the other
// half: the 201st row is EVIDENCE, not content. Serving it would breach the
// 200-fact output bound D-a keeps independent of the scope cap.
func TestChaos5438_TheProbeRowProvesTruncationAndIsNeverServed(t *testing.T) {
	t.Parallel()
	for _, arm := range probeArms() {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			_, result := readProbeArm(t, arm, factRowProbeLimit)
			if !result.Truncated {
				t.Fatalf("%s: Truncated = false with a %dst row present -- the overflow row is the only truncation evidence there is", arm.name, factRowProbeLimit)
			}
			if len(result.Facts) > factRowOutputBound {
				t.Fatalf("%s: len(Facts) = %d, want <= %d -- the probe row must never reach the answer", arm.name, len(result.Facts), factRowOutputBound)
			}
		})
	}
}

// TestChaos5438_TheStatementReadsOneMoreRowThanItServes is the mechanism, not
// the symptom: without LIMIT 201 in the executed SQL the two tests above are
// only satisfiable by guessing.
func TestChaos5438_TheStatementReadsOneMoreRowThanItServes(t *testing.T) {
	t.Parallel()
	for _, arm := range probeArms() {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			client, _ := readProbeArm(t, arm, 1)
			want := "LIMIT " + strconv.Itoa(factRowProbeLimit)
			for _, query := range client.queries {
				if strings.Contains(query.statement, arm.match) {
					if !strings.Contains(query.statement, want) {
						t.Fatalf("%s: statement = %q, want a %q clause", arm.name, query.statement, want)
					}
					return
				}
			}
			t.Fatalf("%s: no statement matching %q was executed; queries = %#v", arm.name, arm.match, client.queries)
		})
	}
}

// TestChaos5438_BelowTheBoundIsStillNotTruncated is the NEGATIVE CONTROL.
// It passes on the unfixed tree as well, which is what makes the three reds
// above discriminating rather than an artefact of this file's harness.
func TestChaos5438_BelowTheBoundIsStillNotTruncated(t *testing.T) {
	t.Parallel()
	for _, arm := range probeArms() {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			_, result := readProbeArm(t, arm, factRowOutputBound-1)
			if result.Truncated {
				t.Fatalf("%s: Truncated = true below the bound", arm.name)
			}
			if len(result.Facts) != factRowOutputBound-1 {
				t.Fatalf("%s: len(Facts) = %d, want %d", arm.name, len(result.Facts), factRowOutputBound-1)
			}
		})
	}
}

// TestChaos5438_ZeroRowsIsNeverTruncated is round-483-r1's P3(b): the matrix
// covered 199/200/201 but no provider had a dedicated ZERO-row case, so
// "nothing there" rested on inspection rather than on a run.
//
// It matters more than it looks. Zero is the one population where a
// truncation flag has no honest reading at all -- there is nothing to have
// been cut -- and it is also the value every counter takes when a query
// silently matched nothing, which is exactly when a wrong flag would be
// least likely to be noticed.
func TestChaos5438_ZeroRowsIsNeverTruncated(t *testing.T) {
	t.Parallel()
	for _, arm := range probeArms() {
		arm := arm
		t.Run(arm.name, func(t *testing.T) {
			t.Parallel()
			_, result := readProbeArm(t, arm, 0)
			if result.Truncated {
				t.Fatalf("%s: Truncated = true on an EMPTY population -- there was nothing to cut", arm.name)
			}
			if len(result.Facts) != 0 {
				t.Fatalf("%s: len(Facts) = %d on zero rows", arm.name, len(result.Facts))
			}
		})
	}
}
