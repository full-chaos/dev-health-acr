package contextfabric

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The committed resolution subjects -- the anchors a question names -- are
// read alongside the cohort members (investigationSubjects), so a turn holds
// facts rooted on them that are neither a member nor a group. The grouped
// retention rule is an allow-list; built from members and groups alone, it
// deleted every anchor fact on every narrowed grouped answer while the served
// document still declared the anchor committed. The flat path kept them.
//
// These pins drive BOTH production callers of the rule through
// Engine.Investigate -- stage 2's budget narrowing (engine.go) and stage 3's
// retry narrowing (chaos4636_budget_stage3.go) -- plus the rule itself over
// every subject kind, and the Info line that must name what was dropped.

// anchorRetentionMembers is four projects across two teams, so stage 2's
// clamped allowance narrows to one member per group.
func anchorRetentionMembers() []CohortMember {
	members := make([]CohortMember, 0, 4)
	for index, id := range []string{"project_a", "project_b", "project_c", "project_d"} {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	return members
}

func anchorFact(subject SubjectRef, field string) CanonicalFact {
	value := field
	return CanonicalFact{
		Kind: FactMetrics, Subject: subject,
		Fields:      map[string]FactValue{field: {String: &value}},
		SourceState: SourceAvailable, Source: "ops", SourceVersion: "v1",
	}
}

// anchorRetentionRecorder serves the member facts plus `extra` on every
// member-rooted read, and nothing but a health source on a team-rooted one.
func anchorRetentionRecorder(extra ...CanonicalFact) *groupReadRecorder {
	return &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Facts = append([]CanonicalFact{
			teamScopedFact("project_a", "team_security", "Security"),
			teamScopedFact("project_b", "team_security", "Security"),
			teamScopedFact("project_c", "team_platform", "Platform"),
			teamScopedFact("project_d", "team_platform", "Platform"),
		}, extra...)
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
}

func lastSynthesisSubjects(t *testing.T) []string {
	t.Helper()
	if len(groupReadSynthesisPasses) == 0 {
		t.Fatal("no synthesis pass captured -- the fixture never reached synthesis, so nothing below measures retention")
	}
	last := groupReadSynthesisPasses[len(groupReadSynthesisPasses)-1]
	ids := make([]string, 0, len(last.Facts))
	for _, fact := range last.Facts {
		ids = append(ids, string(fact.Subject.Kind)+"/"+fact.Subject.CanonicalID)
	}
	return ids
}

// runAnchorRetentionTurn drives stage 2 (MaxItems below the grouped headroom)
// with one committed repository anchor, and returns the subjects the FINAL
// synthesis pass received.
func runAnchorRetentionTurn(t *testing.T, maxItems int) []string {
	t.Helper()
	anchor := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_anchor", Label: "anchor"}
	groupReadSynthesisPasses = nil
	t.Cleanup(func() { groupReadSynthesisPasses = nil })
	options := EngineOptions{MaxItems: maxItems}
	engine, request := groupReadEngineFixtureConfigured(t, &recordingTelemetry{}, anchorRetentionRecorder(anchorFact(anchor, "anchor_metric")),
		anchorRetentionMembers(), nil, SubjectProject, &options, nil, func(config *groupReadFixtureConfig) {
			config.graph.resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{anchor}}
			config.graph.bases = provenCommitBases(anchor)
		})
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	return lastSynthesisSubjects(t)
}

// TestACommittedAnchorSurvivesGroupedNarrowing is the executed reproduction:
// stage 2 narrows a grouped cohort and the committed anchor's evidence must
// still reach synthesis.
//
// NOT t.Parallel(): it reads the package-level synthesis capture.
func TestACommittedAnchorSurvivesGroupedNarrowing(t *testing.T) {
	subjects := runAnchorRetentionTurn(t, 6)
	t.Logf("final synthesis fact subjects: %v", subjects)
	// The narrowing must be REAL, or the anchor survives for the wrong reason.
	if containsString(subjects, "project/project_b") && containsString(subjects, "project/project_d") &&
		containsString(subjects, "project/project_a") && containsString(subjects, "project/project_c") {
		t.Fatalf("every member reached synthesis -- stage 2 did not narrow, so this run cannot say anything about retention: %v", subjects)
	}
	if !containsString(subjects, "repository/repo_anchor") {
		t.Fatalf("committed anchor was dropped from final synthesis input; final fact subjects=%v", subjects)
	}
}

// TestACommittedAnchorSurvivesWithoutNarrowing is the discriminating control:
// the SAME fixture with a budget that narrows nothing. It passes before and
// after the fix; if it failed, the fixture would be losing the anchor
// somewhere other than retention.
//
// NOT t.Parallel(): it reads the package-level synthesis capture.
func TestACommittedAnchorSurvivesWithoutNarrowing(t *testing.T) {
	subjects := runAnchorRetentionTurn(t, 40)
	t.Logf("final synthesis fact subjects: %v", subjects)
	for _, want := range []string{"project/project_a", "project/project_b", "project/project_c", "project/project_d", "repository/repo_anchor"} {
		if !containsString(subjects, want) {
			t.Fatalf("CONTROL BROKEN: %s missing with nothing narrowed; subjects=%v", want, subjects)
		}
	}
}

// TestTheRetryNarrowingKeepsCommittedAnchors drives the OTHER production
// caller: stage 3's retry. The budget leaves every member in place at stage 2
// (so stage 2's retention never runs), the first answer goes over on claims,
// and the retry narrows the grouped cohort.
//
// NOT t.Parallel(): it changes the fixture's claims-per-member and reads the
// package-level synthesis capture.
func TestTheRetryNarrowingKeepsCommittedAnchors(t *testing.T) {
	previousClaims := groupReadClaimsPerMember
	groupReadClaimsPerMember = 5
	t.Cleanup(func() { groupReadClaimsPerMember = previousClaims })
	groupReadSynthesisPasses = nil
	t.Cleanup(func() { groupReadSynthesisPasses = nil })

	anchor := SubjectRef{Kind: SubjectOrganization, CanonicalID: "org_anchor", Label: "org"}
	recorder := &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		facts := make([]CanonicalFact, 0, 7)
		for index, id := range groupReadRetryMemberIDs() {
			team := "team_security"
			if index >= 3 {
				team = "team_platform"
			}
			facts = append(facts, teamScopedFact(id, team, team))
		}
		bundle.Facts = append(facts, anchorFact(anchor, "org_metric"))
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	synthesisCalls := 0
	members := make([]CohortMember, 0, 6)
	for index, id := range groupReadRetryMemberIDs() {
		members = append(members, CohortMember{
			Subject:          SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id},
			Rank:             index + 1,
			InclusionReasons: []string{"matched"},
		})
	}
	options := EngineOptions{MaxItems: 26, SynthesisDeadlineReserve: time.Hour}
	telemetry := &recordingTelemetry{}
	engine, request := groupReadEngineFixtureConfigured(t, telemetry, recorder, members, nil, SubjectProject, &options, &synthesisCalls,
		func(config *groupReadFixtureConfig) {
			config.graph.resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{anchor}}
			config.graph.bases = provenCommitBases(anchor)
		})
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if synthesisCalls != 2 {
		t.Fatalf("synthesis calls = %d, want 2 -- the retry did not run, so this test is not measuring stage 3's retention", synthesisCalls)
	}
	stages := make([]contractsv1.ContextFabricPlanNarrowingStage, 0, 2)
	for _, event := range telemetry.factRetentions {
		stages = append(stages, event.Stage)
	}
	if !reflect.DeepEqual(stages, []contractsv1.ContextFabricPlanNarrowingStage{contractsv1.ContextFabricPlanNarrowingAssembledResult}) {
		t.Fatalf("retention stages = %v, want exactly one assembled_result pass -- a stage-2 pass would let the engine caller, not the retry caller, decide this run", stages)
	}
	first := groupReadSynthesisPasses[0]
	if len(groupReadSynthesisPasses[len(groupReadSynthesisPasses)-1].Members) >= len(first.Members) {
		t.Fatalf("the retry did not narrow the cohort (%d -> %d members)", len(first.Members), len(groupReadSynthesisPasses[len(groupReadSynthesisPasses)-1].Members))
	}
	subjects := lastSynthesisSubjects(t)
	t.Logf("retry synthesis fact subjects: %v", subjects)
	if !containsString(subjects, "organization/org_anchor") {
		t.Fatalf("the retry narrowing dropped the committed anchor's facts; retry fact subjects=%v", subjects)
	}
	decision := telemetry.factRetentions[0].Decision
	if decision.AnchorFactsRetained != 1 || decision.DroppedGroups != 0 || len(decision.Anchors) != 1 {
		t.Fatalf("retry retention decision = %+v, want the one anchor fact retained and no group drop", decision)
	}
}

// TestRetentionAdmitsEveryCommittedAnchorKind is the class, executed: every
// subject kind as a committed anchor x {flat, grouped} x its relation to the
// cohort, through the one rule both callers use. The only anchor that loses
// its facts is one that is ALSO a removed member -- on both paths, because
// the member rule wins -- and a subject that is neither member, group nor
// anchor is still dropped by the group rule, so the fix did not simply turn
// the allow-list off.
func TestRetentionAdmitsEveryCommittedAnchorKind(t *testing.T) {
	t.Parallel()
	kinds := []SubjectKind{
		contractsv1.ContextFabricSubjectOrganization, contractsv1.ContextFabricSubjectTeam,
		contractsv1.ContextFabricSubjectProject, contractsv1.ContextFabricSubjectRepository,
		contractsv1.ContextFabricSubjectWorkItem, contractsv1.ContextFabricSubjectPullRequest,
		contractsv1.ContextFabricSubjectDeployment, contractsv1.ContextFabricSubjectIncident,
		contractsv1.ContextFabricSubjectDocument, contractsv1.ContextFabricSubjectDecision,
		contractsv1.ContextFabricSubjectEpisode, contractsv1.ContextFabricSubjectMetric,
		contractsv1.ContextFabricSubjectPullRequestReview, contractsv1.ContextFabricSubjectCIRun,
		contractsv1.ContextFabricSubjectWorkItemRef,
	}
	member := func(id string) CohortMember {
		return CohortMember{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: id, Label: id}}
	}
	teamA := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:a", Label: "a"}
	teamB := SubjectRef{Kind: SubjectTeam, CanonicalID: "team:b", Label: "b"}
	build := func(grouped bool) (*Cohort, []CohortMember, []CanonicalFact) {
		cohort := &Cohort{Kind: SubjectProject, Members: []CohortMember{member("p1"), member("p3")}}
		facts := []CanonicalFact{
			anchorFact(member("p1").Subject, "m"), anchorFact(member("p2").Subject, "m"),
			anchorFact(member("p3").Subject, "m"), anchorFact(member("p4").Subject, "m"),
		}
		if grouped {
			cohort.Groups = []contractsv1.ContextFabricCohortGroup{
				{Subject: teamA, MemberCanonicalIDs: []string{"p1"}},
				{Subject: teamB, MemberCanonicalIDs: []string{"p3"}},
			}
			facts = append(facts, anchorFact(teamA, "g"), anchorFact(teamB, "g"))
		}
		return cohort, []CohortMember{member("p2"), member("p4")}, facts
	}
	kept := func(facts []CanonicalFact, subject SubjectRef) bool {
		for _, fact := range facts {
			if fact.Subject == subject {
				return true
			}
		}
		return false
	}
	type cell struct {
		path, relation string
		subject        SubjectRef
		anchor         bool
		narrow         bool
		wantKept       bool
	}
	cells := make([]cell, 0, 48)
	for _, path := range []string{"flat", "grouped"} {
		for _, kind := range kinds {
			cells = append(cells, cell{path, "unrelated_anchor", SubjectRef{Kind: kind, CanonicalID: "anchor_" + string(kind), Label: "anchor"}, true, true, true})
		}
		cells = append(cells,
			cell{path, "anchor_is_surviving_member", member("p1").Subject, true, true, true},
			cell{path, "anchor_is_removed_member", member("p2").Subject, true, true, false},
			cell{path, "anchor_is_group_identity", teamA, true, true, true},
			cell{path, "anchor_same_kind_as_group_not_a_group", SubjectRef{Kind: SubjectTeam, CanonicalID: "team:z", Label: "z"}, true, true, true},
			cell{path, "anchor_same_kind_as_members_not_a_member", SubjectRef{Kind: SubjectProject, CanonicalID: "p9", Label: "p9"}, true, true, true},
			cell{path, "control_no_narrowing", SubjectRef{Kind: SubjectRepository, CanonicalID: "anchor_ctl", Label: "anchor"}, true, false, true},
			// NOT an anchor: the group rule must still drop it on the grouped
			// path, and the flat path (no allow-list) must still keep it.
			cell{path, "not_an_anchor_not_a_member_not_a_group", SubjectRef{Kind: SubjectRepository, CanonicalID: "stray", Label: "stray"}, false, true, path == "flat"},
		)
	}
	for _, c := range cells {
		cohort, removed, facts := build(c.path == "grouped")
		if !kept(facts, c.subject) {
			facts = append(facts, anchorFact(c.subject, "a"))
		}
		var anchors []SubjectRef
		if c.anchor {
			anchors = []SubjectRef{c.subject}
		}
		if !c.narrow {
			removed = nil
		}
		out, decision := RetainFactsForCohortWithDecision(facts, cohort, removed, anchors)
		got := kept(out, c.subject)
		t.Logf("CELL path=%s relation=%s kind=%s kept=%v anchor_facts_retained=%d anchor_facts_dropped=%d dropped_groups=%d",
			c.path, c.relation, c.subject.Kind, got, decision.AnchorFactsRetained, decision.AnchorFactsDropped, decision.DroppedGroups)
		if got != c.wantKept {
			t.Errorf("path=%s relation=%s kind=%s: kept=%v, want %v", c.path, c.relation, c.subject.Kind, got, c.wantKept)
		}
		if c.anchor && got && decision.AnchorFactsRetained != 1 {
			t.Errorf("path=%s relation=%s kind=%s: anchor kept but anchor_facts_retained=%d, want 1", c.path, c.relation, c.subject.Kind, decision.AnchorFactsRetained)
		}
		if c.anchor && !got && (decision.AnchorFactsDropped != 1 || !reflect.DeepEqual(decision.DroppedAnchors, []SubjectRef{c.subject})) {
			t.Errorf("path=%s relation=%s: anchor dropped but anchor_facts_dropped=%d dropped_anchors=%v -- a dropped anchor must be named",
				c.path, c.relation, decision.AnchorFactsDropped, decision.DroppedAnchors)
		}
	}
}

// TestTheRetentionLineNamesTheCommittedAnchors pins the Info line an operator
// reads to rebuild the decision: which anchors the pass admitted, how many of
// their facts it kept, and WHICH anchor it dropped (one that was also a
// narrowed-out member). The fixture makes every count that means something
// different come out different, so no field can pass by carrying another
// field's value.
//
// NOT t.Parallel(): it installs the process-global default logger and reads
// the package-level synthesis capture.
func TestTheRetentionLineNamesTheCommittedAnchors(t *testing.T) {
	logs := captureEngineLogger(t)
	groupReadSynthesisPasses = nil
	t.Cleanup(func() { groupReadSynthesisPasses = nil })

	repo := SubjectRef{Kind: SubjectRepository, CanonicalID: "repo_anchor", Label: "repo"}
	doc := SubjectRef{Kind: SubjectDocument, CanonicalID: "doc_anchor", Label: "doc"}
	named := SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}
	projectD := SubjectRef{Kind: SubjectProject, CanonicalID: "project_d", Label: "project_d"}
	extra := []CanonicalFact{
		anchorFact(repo, "r1"), anchorFact(repo, "r2"), anchorFact(repo, "r3"), anchorFact(repo, "r4"),
		anchorFact(doc, "d1"),
		anchorFact(projectD, "x1"), anchorFact(projectD, "x2"),
	}
	options := EngineOptions{MaxItems: 6}
	engine, request := groupReadEngineFixtureConfigured(t, logs.telemetry, anchorRetentionRecorder(extra...),
		anchorRetentionMembers(), nil, SubjectProject, &options, nil, func(config *groupReadFixtureConfig) {
			config.graph.resolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{repo, doc, named}}
			config.graph.bases = provenCommitBases(repo, doc, named)
		})
	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	lines := linesWithMessage(t, logs.configured.String(), "context fabric fact retention")
	if len(lines) != 1 {
		t.Fatalf("retention lines = %d, want exactly one (stage 2)", len(lines))
	}
	if stray := linesWithMessage(t, logs.fallback.String(), "context fabric fact retention"); len(stray) != 0 {
		t.Fatalf("%d retention line(s) reached the process default logger", len(stray))
	}
	line := lines[0]
	t.Logf("retention line: %v", line)
	if got := line["level"]; got != slog.LevelInfo.String() {
		t.Errorf("level = %v, want INFO", got)
	}
	want := map[string]any{
		"anchors":               float64(3),
		"anchor_facts_retained": float64(5),
		"anchor_facts_dropped":  float64(1),
		"dropped_members":       float64(4),
		"dropped_groups":        float64(0),
		"retained_groups":       float64(2),
	}
	for field, value := range want {
		if line[field] != value {
			t.Errorf("%s = %v, want %v", field, line[field], value)
		}
	}
	if got, wantIDs := fmt.Sprint(line["anchor_ids"]), "[repository/repo_anchor document/doc_anchor project/project_b]"; got != wantIDs {
		t.Errorf("anchor_ids = %s, want %s", got, wantIDs)
	}
	if got, wantIDs := fmt.Sprint(line["dropped_anchor_ids"]), "[project/project_b]"; got != wantIDs {
		t.Errorf("dropped_anchor_ids = %s, want %s -- the anchor that lost its evidence must be named on the line", got, wantIDs)
	}
	subjects := lastSynthesisSubjects(t)
	for _, wantSubject := range []string{"repository/repo_anchor", "document/doc_anchor"} {
		if !containsString(subjects, wantSubject) {
			t.Errorf("%s missing from synthesis: %v", wantSubject, subjects)
		}
	}
	if containsString(subjects, "project/project_b") {
		t.Errorf("project_b was narrowed out and must not reach synthesis: %v", subjects)
	}
}
