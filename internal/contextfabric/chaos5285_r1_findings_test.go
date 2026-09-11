package contextfabric

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file holds the pins for the five first-round review findings on the
// group read. Each was reproduced RED at 35239d2d before any fix was written,
// and each pin that asserts a fix has a control that passes on both sides.

// cohortGroupReadLine returns the ONE group-read Info line a turn emitted.
// More than one is a defect of its own (the decision is taken once per turn),
// so it is reported rather than resolved to the first.
func cohortGroupReadLine(t *testing.T, logs *engineLoggerCapture) map[string]any {
	t.Helper()
	lines := linesWithMessage(t, logs.configured.String(), "context fabric cohort group read")
	if len(lines) != 1 {
		t.Fatalf("group-read Info lines = %d, want exactly 1", len(lines))
	}
	if got := lines[0]["level"]; got != slog.LevelInfo.String() {
		t.Errorf("group-read line level = %v, want %q", got, slog.LevelInfo.String())
	}
	if strings.Contains(logs.fallback.String(), "context fabric cohort group read") {
		t.Errorf("the group-read line reached the PROCESS DEFAULT logger -- it must go to the engine's configured one")
	}
	return lines[0]
}

// servingGroupCohort answers the member read with one team-scoped fact per
// member (each member its own team, so `count` members make `count` groups)
// and the group read with nothing.
func servingGroupCohort(count int) *groupReadRecorder {
	memberFacts := groupReadBoundedMemberFacts(count)
	return &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				return bundle
			}
		}
		bundle.Facts = memberFacts
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
}

// TestEveryAuthorizedGroupOfALegalCohortIsAdmittedAndRead is r1 finding P1-1.
//
// The contract carries up to 250 groups, and the group stage authorizes them
// by resolving each as a canonical-id hint. The resolver commits at most
// Options.MaxSubjectCandidates hints and drops the rest exactly as it drops an
// unauthorized one (graphrank.TestTheCallerHintExitCommitsNoMoreThanItsCandidateCap
// measures this on the real resolver). One call with every group in it
// therefore reported groups 51..250 as DENIED -- a legal cohort read one group
// in five, and the trace said the principal could not see the rest.
//
// Every row here is a fully authorized cohort: nothing is denied, so any
// non-zero `groups_denied` is the cap speaking, not authorization.
//
// NOT t.Parallel(): it installs the process default logger.
func TestEveryAuthorizedGroupOfALegalCohortIsAdmittedAndRead(t *testing.T) {
	for _, groups := range []int{51, 100, 250} {
		t.Run(fmt.Sprintf("groups=%d", groups), func(t *testing.T) {
			logs := captureEngineLogger(t)
			recorder := servingGroupCohort(groups)
			var graph *groupAuthorizingGraph
			engine, request := groupReadEngineFixtureConfigured(t, logs.telemetry, recorder, groupReadCohortMembers(groups), nil, SubjectProject, nil, nil,
				func(config *groupReadFixtureConfig) { graph = config.graph })
			if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			line := cohortGroupReadLine(t, logs)
			t.Logf("groups_proposed=%v groups_admitted=%v groups_denied=%v authorization_batches=%v",
				line["groups_proposed"], line["groups_admitted"], line["groups_denied"], line["authorization_batches"])
			if got := line["groups_admitted"]; got != float64(groups) {
				t.Errorf("groups_admitted = %v, want %d -- every group of this cohort is authorized, so a shortfall is the resolver's candidate cap truncating the authorization call", got, groups)
			}
			if got := line["groups_denied"]; got != float64(0) {
				t.Errorf("groups_denied = %v, want 0 -- a group the cap dropped is reported as one the principal may not see, which is a false statement about authorization", got)
			}
			// The line says how the set was authorized, so a reader can check
			// from the line alone that no call exceeded what it could commit.
			size := reuseRecheckOptions.MaxSubjectCandidates
			if got, want := line["authorization_batch_size"], float64(size); got != want {
				t.Errorf("authorization_batch_size = %v, want %v", got, want)
			}
			if got, want := line["authorization_batches"], float64((groups+size-1)/size); got != want {
				t.Errorf("authorization_batches = %v, want %v -- the fewest calls that keep every call within its cap", got, want)
			}
			grouped := recorder.groupRootedRequests(SubjectTeam)
			if len(grouped) != 1 {
				t.Fatalf("group-rooted fact requests = %d, want exactly 1 -- the read is ONE request for the whole admitted set", len(grouped))
			}
			if roots := recorder.rootIDs(grouped[0]); len(roots) != groups {
				t.Errorf("group read roots = %d, want %d", len(roots), groups)
			}
			// No authorization call may be handed more hints than it can
			// commit. This is the property the batching exists to hold, stated
			// directly rather than inferred from the admitted count.
			for index, hints := range graph.hinted {
				if len(hints) == 0 {
					continue
				}
				if limit := graph.hintedCaps[index]; limit <= 0 || len(hints) > limit {
					t.Errorf("authorization call %d carried %d hints under a candidate cap of %d -- the hints past the cap are dropped and read as denied", index, len(hints), limit)
				}
			}
		})
	}
}

// TestACohortAtTheAuthorizationCapIsAdmittedWhole is the CONTROL for P1-1,
// and it passes on both sides: at exactly the resolver's candidate cap one
// call commits everything. Without it, a fixture that denied groups for some
// other reason would make the pin above read as a batching defect.
//
// NOT t.Parallel(): it installs the process default logger.
func TestACohortAtTheAuthorizationCapIsAdmittedWhole(t *testing.T) {
	logs := captureEngineLogger(t)
	groups := reuseRecheckOptions.MaxSubjectCandidates
	recorder := servingGroupCohort(groups)
	engine, request := groupReadEngineFixtureWith(t, logs.telemetry, recorder, groupReadCohortMembers(groups), nil)
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v", err)
	}
	line := cohortGroupReadLine(t, logs)
	if line["groups_admitted"] != float64(groups) || line["groups_denied"] != float64(0) {
		t.Fatalf("CONTROL BROKEN: at the cap (%d) admitted=%v denied=%v, want %d/0", groups, line["groups_admitted"], line["groups_denied"], groups)
	}
}

// TestTwoTeamsWhoseKeysDifferOnlyByThePrefixStayTwoGroups is r1 finding P1-2,
// at the producer the reviewer named: the grouping map keys on the minted
// identity, so a mint that collapses two raw keys merges two teams.
//
// `x` and `team:x` are different rows of teams.id. The mint made itself
// idempotent on an already-prefixed input, which made both `team:x` -- and
// TeamRawKey then recovered `x` for the second team, so its facts would have
// been read for the first.
func TestTwoTeamsWhoseKeysDifferOnlyByThePrefixStayTwoGroups(t *testing.T) {
	t.Parallel()
	facts := []CanonicalFact{
		teamScopedFact("project_a", "x", "X"),
		teamScopedFact("project_b", "team:x", "Team X"),
	}
	groups, _, outcome := BuildCohortGroups(AnswerPlan{GroupKind: SubjectTeam}, planFixtureCohort("project_a", "project_b"), facts)
	ids := make([]string, 0, len(groups))
	for _, group := range groups {
		ids = append(ids, group.Subject.CanonicalID)
	}
	t.Logf("groups=%v refusal=%q", ids, outcome.Refusal)
	if len(groups) != 2 {
		t.Fatalf("groups = %v, want two -- two distinct teams.id rows merged into one group because their minted identities collided", ids)
	}
	recovered := map[string]bool{}
	for _, group := range groups {
		raw, ok := TeamRawKey(group.Subject.CanonicalID)
		if !ok {
			t.Errorf("group identity %q is not a team identity", group.Subject.CanonicalID)
		}
		recovered[raw] = true
	}
	if !recovered["x"] || !recovered["team:x"] {
		t.Errorf("raw keys recovered from the groups = %v, want both `x` and `team:x` -- a group whose identity does not recover its own row's key has its facts read for another team", recovered)
	}
}

// TestTeamIdentityIsInjectiveAndRoundTripsOverItsWholeDomain is the domain
// table for the mint and its inverse, every cell executed.
//
// Injectivity is checked across the WHOLE table at once, not per cell: a mint
// is injective only if no two distinct inputs anywhere share an output, and a
// per-cell check cannot see a collision between two cells.
func TestTeamIdentityIsInjectiveAndRoundTripsOverItsWholeDomain(t *testing.T) {
	t.Parallel()
	cells := []struct {
		name, raw, want string
	}{
		{"bare", "x", "team:x"},
		{"prefixed", "team:x", "team:team:x"},
		{"double-prefixed", "team:team:x", "team:team:team:x"},
		{"empty", "", ""},
		{"prefix only", "team:", "team:team:"},
		{"whitespace", " ", "team: "},
		{"surrounding whitespace", " x ", "team: x "},
		{"unicode", "équipe-ß", "team:équipe-ß"},
		{"colon inside", "gl:full.chaos", "team:gl:full.chaos"},
		{"other kind prefix", "project:x", "team:project:x"},
	}
	byID := map[string]string{}
	for _, cell := range cells {
		got := TeamCanonicalID(cell.raw)
		raw, ok := TeamRawKey(got)
		t.Logf("%-22s raw=%q -> id=%q -> raw=%q ok=%v", cell.name, cell.raw, got, raw, ok)
		if got != cell.want {
			t.Errorf("%s: TeamCanonicalID(%q) = %q, want %q", cell.name, cell.raw, got, cell.want)
		}
		if cell.raw == "" {
			// No identity for no key: "team:" names no team.
			if ok {
				t.Errorf("%s: TeamRawKey(%q) reported a team identity", cell.name, got)
			}
			continue
		}
		if !ok || raw != cell.raw {
			t.Errorf("%s: TeamRawKey(TeamCanonicalID(%q)) = %q ok=%v, want the raw key back", cell.name, cell.raw, raw, ok)
		}
		if other, taken := byID[got]; taken {
			t.Errorf("%s: raw keys %q and %q both mint %q -- two teams, one identity", cell.name, other, cell.raw, got)
		}
		byID[got] = cell.raw
	}
}

// metadataConflictRecorder answers the member read with member facts and a
// health version, and the group read with a DIFFERENT health version, so the
// reconcile refuses the composition after the group read was issued.
func metadataConflictRecorder(groupVersion string) *groupReadRecorder {
	return &groupReadRecorder{facts: func(request CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		for _, subject := range request.Subjects {
			if subject.Kind == SubjectTeam {
				bundle.Facts = []CanonicalFact{groupKindFact("team_security", FactHealth), groupKindFact("team_security", FactWorkload)}
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
				bundle.Versions = map[FactKind]string{FactHealth: groupVersion}
				return bundle
			}
		}
		bundle.Facts = groupReadMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		bundle.Versions = map[FactKind]string{FactHealth: "health-v1"}
		return bundle
	}}
}

// TestAGroupReadRefusedAtReconcileIsStillReportedAsIssued is r1 finding P2-3.
//
// The request went out -- a provider was asked and answered -- and the
// composition was then refused because the two reads disagree about one kind's
// version. `group_read_issued` is the field that separates "a provider was
// asked" from "no provider was asked", and it read false here, so the trace
// said the group axis was never queried on a turn that queried it.
//
// NOT t.Parallel(): it installs the process default logger.
func TestAGroupReadRefusedAtReconcileIsStillReportedAsIssued(t *testing.T) {
	logs := captureEngineLogger(t)
	recorder := metadataConflictRecorder("health-v2")
	engine, request := groupReadEngineFixture(t, logs.telemetry, recorder)
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	line := cohortGroupReadLine(t, logs)
	t.Logf("fact_requests=%d issued=%v refused=%v refusal=%v merged=%v returned=%v",
		len(recorder.requests), line["group_read_issued"], line["group_read_refused"], line["group_read_refusal"], line["group_facts_merged"], line["group_facts_returned"])
	if len(recorder.requests) != 2 || len(recorder.groupRootedRequests(SubjectTeam)) != 1 {
		t.Fatalf("fact requests = %d (group-rooted %d), want 2 with one group-rooted -- the probe needs the read to have been issued", len(recorder.requests), len(recorder.groupRootedRequests(SubjectTeam)))
	}
	if line["group_read_issued"] != true {
		t.Errorf("group_read_issued = %v, want true -- the group-rooted request was sent and answered", line["group_read_issued"])
	}
	if line["group_read_refused"] != true || line["group_read_refusal"] != string(GroupReadRefusalMetadataConflict) {
		t.Errorf("refused=%v refusal=%v, want true/%q", line["group_read_refused"], line["group_read_refusal"], GroupReadRefusalMetadataConflict)
	}
	if line["group_facts_merged"] != float64(0) {
		t.Errorf("group_facts_merged = %v, want 0 -- a refused composition merges nothing", line["group_facts_merged"])
	}
	if line["group_facts_returned"] != float64(2) {
		t.Errorf("group_facts_returned = %v, want 2 -- what the provider answered", line["group_facts_returned"])
	}
}

// TestAGroupReadThatComposesIsReportedAsIssuedAndMerged is the control for
// P2-3: the same fixture with agreeing versions composes, and must pass on
// both sides.
//
// NOT t.Parallel(): it installs the process default logger.
func TestAGroupReadThatComposesIsReportedAsIssuedAndMerged(t *testing.T) {
	logs := captureEngineLogger(t)
	engine, request := groupReadEngineFixture(t, logs.telemetry, metadataConflictRecorder("health-v1"))
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v", err)
	}
	line := cohortGroupReadLine(t, logs)
	if line["group_read_issued"] != true || line["group_read_refused"] != false || line["group_facts_merged"] != float64(2) {
		t.Fatalf("CONTROL BROKEN: issued=%v refused=%v merged=%v, want true/false/2", line["group_read_issued"], line["group_read_refused"], line["group_facts_merged"])
	}
}

const planGroupAxisCollapsedMessage = "context fabric plan group axis collapsed"

// TestThePlanSeamI6RefusalIsNamedAtInfo is r1 finding P2-4.
//
// The plan seam refuses a group axis that collapsed onto the member kind with
// the frame gate's own invariant and basis, and the served document says
// `frame_invariant_violated`. At Info, though, the only line was the generic
// subjectless terminal -- the frame-validation line for this turn had already
// been emitted as VALID, because the frame was legal. So no line said which
// invariant refused the turn, and this refusal read the same as every other
// frame-gate refusal.
//
// NOT t.Parallel(): it installs the process default logger.
func TestThePlanSeamI6RefusalIsNamedAtInfo(t *testing.T) {
	logs := captureEngineLogger(t)
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixtureSelfGroup(t, logs.telemetry, recorder)
	result, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("refusal basis = %q -- the fixture must reach the plan-seam refusal", result.RefusalBasis)
	}
	lines := linesWithMessage(t, logs.configured.String(), planGroupAxisCollapsedMessage)
	if len(lines) != 1 {
		t.Fatalf("%q lines = %d, want exactly 1 -- the plan-seam refusal must name its invariant at Info", planGroupAxisCollapsedMessage, len(lines))
	}
	line := lines[0]
	t.Logf("plan-seam line: %v", line)
	want := map[string]any{
		"level":            slog.LevelInfo.String(),
		"seam":             "plan",
		"failed_invariant": string(FrameInvariantI6),
		"failed_phase":     string(FrameValidationPhaseA1),
		"failure_detail":   string(FrameFailureGroupEqualsMember),
		"group_kind":       string(SubjectTeam),
		"member_kind":      string(SubjectTeam),
		"frame_gate":       "rejected:" + string(FrameInvariantI6),
		"refusal_basis":    string(contractsv1.ContextFabricRefusalBasisFrameInvariantViolated),
		"request_id":       "req_0123456789abcdef0123456789abcdef",
	}
	for key, value := range want {
		if line[key] != value {
			t.Errorf("%s = %v, want %v", key, line[key], value)
		}
	}
	if strings.Contains(logs.fallback.String(), planGroupAxisCollapsedMessage) {
		t.Errorf("the plan-seam line reached the PROCESS DEFAULT logger -- it must go to the engine's configured one")
	}
}

// TestAGroupedTurnThatKeepsItsAxisEmitsNoPlanSeamLine is the control for
// P2-4, and passes on both sides: a line that fired on every grouped turn
// would satisfy the pin above while saying nothing.
//
// NOT t.Parallel(): it installs the process default logger.
func TestAGroupedTurnThatKeepsItsAxisEmitsNoPlanSeamLine(t *testing.T) {
	logs := captureEngineLogger(t)
	engine, request := groupReadEngineFixture(t, logs.telemetry, groupReadServing("team_security", "team_platform"))
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v", err)
	}
	if lines := linesWithMessage(t, logs.configured.String(), planGroupAxisCollapsedMessage); len(lines) != 0 {
		t.Fatalf("CONTROL BROKEN: %d plan-seam lines on a turn whose axis did not collapse", len(lines))
	}
}

// TestAnOverBoundGroupReadLineKeepsTheRequestedGroupKind is r1 finding P2-5.
//
// An over-bound group list drops the axis so the turn answers flat, and it did
// so by clearing the plan's group kind BEFORE the group-read line was built --
// so the one line that says "251 groups were proposed and refused" said they
// were groups of nothing.
//
// NOT t.Parallel(): it installs the process default logger.
func TestAnOverBoundGroupReadLineKeepsTheRequestedGroupKind(t *testing.T) {
	logs := captureEngineLogger(t)
	recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
		bundle := emptyFactBundle()
		bundle.Facts = groupReadOverBoundMemberFacts()
		bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
		return bundle
	}}
	engine, request := groupReadEngineFixtureOverBound(t, logs.telemetry, recorder)
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	line := cohortGroupReadLine(t, logs)
	t.Logf("over-bound line: group_kind=%q proposed=%v refusal=%v", line["group_kind"], line["groups_proposed"], line["group_read_refusal"])
	if line["group_read_refusal"] != string(GroupReadRefusalOverContractBound) {
		t.Fatalf("refusal = %v -- the fixture must reach the over-bound refusal", line["group_read_refusal"])
	}
	if line["group_kind"] != string(SubjectTeam) {
		t.Errorf("group_kind = %q, want %q -- the axis this turn proposed and refused; an empty kind reads as a turn that never grouped", line["group_kind"], SubjectTeam)
	}
}

// TestAnAtBoundGroupReadLineCarriesItsGroupKind is the control for P2-5, and
// passes on both sides.
//
// NOT t.Parallel(): it installs the process default logger.
func TestAnAtBoundGroupReadLineCarriesItsGroupKind(t *testing.T) {
	logs := captureEngineLogger(t)
	engine, request := groupReadEngineFixtureWith(t, logs.telemetry, servingGroupCohort(2), groupReadCohortMembers(2), nil)
	if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("CONTROL BROKEN: Investigate() error = %v", err)
	}
	if line := cohortGroupReadLine(t, logs); line["group_kind"] != string(SubjectTeam) {
		t.Fatalf("CONTROL BROKEN: group_kind = %v on an in-bound turn", line["group_kind"])
	}
}

// TestTheGroupReadLineIsTrueOnEveryExit is the SWEEP behind P2-3 and P2-5:
// every exit the group stage has, driven through Engine.Investigate onto the
// configured logger, with the line's claims checked against what the turn
// actually did.
//
// The invariant is `group_read_issued` <=> a group-rooted provider request was
// recorded, on EVERY exit -- the two P2 findings were each one exit where the
// line and the execution disagreed, and a pin per finding would not see the
// next exit that drifts. The axis is checked on every row for the same reason.
//
// NOT t.Parallel(): it installs the process default logger.
func TestTheGroupReadLineIsTrueOnEveryExit(t *testing.T) {
	onlyMemberRow := stubRequirementDeriver{rows: []DerivedRequirement{{
		RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleMember, Subject: SubjectProject},
		Kind:                  ObligationKindRead,
		FactKinds:             []FactKind{FactMetrics},
		Scope:                 CompletionScopeEachMember,
		Quantifier:            CompletionQuantifierAtLeastOne,
	}}}
	twoMembers := []CohortMember{
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_a", Label: "project_a"}, Rank: 1, InclusionReasons: []string{"matched"}},
		{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "project_b", Label: "project_b"}, Rank: 2, InclusionReasons: []string{"matched"}},
	}
	type exit struct {
		name    string
		refusal GroupReadRefusal
		issued  bool
		build   func(t *testing.T, telemetry EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder)
	}
	exits := []exit{
		{"served", GroupReadRefusalNone, true, func(t *testing.T, tel EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder) {
			recorder := groupReadServing("team_security", "team_platform")
			engine, request := groupReadEngineFixture(t, tel, recorder)
			return engine, request, recorder
		}},
		{"over_contract_bound", GroupReadRefusalOverContractBound, false, func(t *testing.T, tel EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder) {
			recorder := &groupReadRecorder{facts: func(CanonicalFactRequest) CanonicalFactBundle {
				bundle := emptyFactBundle()
				bundle.Facts = groupReadOverBoundMemberFacts()
				bundle.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:metrics", State: SourceAvailable}}
				return bundle
			}}
			engine, request := groupReadEngineFixtureOverBound(t, tel, recorder)
			return engine, request, recorder
		}},
		{"no_read_requirement", GroupReadRefusalNoReadRequirement, false, func(t *testing.T, tel EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder) {
			recorder := groupReadServing("team_security", "team_platform")
			engine, request := groupReadEngineFixtureConfigured(t, tel, recorder, twoMembers, nil, SubjectProject, nil, nil,
				func(config *groupReadFixtureConfig) { config.deriver = onlyMemberRow })
			return engine, request, recorder
		}},
		{"authorization_unavailable", GroupReadRefusalAuthorizationUnavailable, false, func(t *testing.T, tel EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder) {
			recorder := groupReadServing("team_security", "team_platform")
			engine, request := groupReadEngineFixtureConfigured(t, tel, recorder, twoMembers, nil, SubjectProject, nil, nil,
				func(config *groupReadFixtureConfig) {
					config.graph.authorizationErr = fmt.Errorf("injected: authorizer unavailable")
				})
			return engine, request, recorder
		}},
		{"no_group_admitted", GroupReadRefusalNoGroupAdmitted, false, func(t *testing.T, tel EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder) {
			recorder := groupReadServing("team_security", "team_platform")
			engine, request := groupReadEngineFixtureDenying(t, tel, recorder, TeamCanonicalID("team_security"), TeamCanonicalID("team_platform"))
			return engine, request, recorder
		}},
		{"read_failed", GroupReadRefusalReadFailed, true, func(t *testing.T, tel EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder) {
			recorder := groupReadServing()
			engine, request := groupReadEngineFixtureFull(t, tel, &groupReadFailingReader{inner: recorder}, twoMembers, nil, SubjectProject, nil, nil)
			return engine, request, recorder
		}},
		{"metadata_conflict", GroupReadRefusalMetadataConflict, true, func(t *testing.T, tel EngineTelemetry) (*Engine, InvestigationRequest, *groupReadRecorder) {
			recorder := metadataConflictRecorder("health-v2")
			engine, request := groupReadEngineFixture(t, tel, recorder)
			return engine, request, recorder
		}},
	}
	covered := map[GroupReadRefusal]bool{}
	for _, row := range exits {
		t.Run(row.name, func(t *testing.T) {
			logs := captureEngineLogger(t)
			engine, request, recorder := row.build(t, logs.telemetry)
			if _, err := engine.Investigate(canonicalRequestContext(), storage.Principal{OrgID: "org_1"}, request); err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			line := cohortGroupReadLine(t, logs)
			sent := len(recorder.groupRootedRequests(SubjectTeam))
			t.Logf("exit=%s refusal=%v issued=%v refused=%v group_kind=%v group_requests_sent=%d",
				row.name, line["group_read_refusal"], line["group_read_issued"], line["group_read_refused"], line["group_kind"], sent)
			if line["group_read_refusal"] != string(row.refusal) {
				t.Fatalf("refusal = %v, want %q -- the fixture must reach this exit", line["group_read_refusal"], row.refusal)
			}
			covered[row.refusal] = true
			if got := line["group_read_issued"]; got != row.issued || got != (sent > 0) {
				t.Errorf("group_read_issued = %v with %d group-rooted request(s) sent -- the line must say whether a provider was asked, and it disagrees with the execution", got, sent)
			}
			if got, want := line["group_read_refused"], row.refusal != GroupReadRefusalNone; got != want {
				t.Errorf("group_read_refused = %v, want %v", got, want)
			}
			if line["group_kind"] != string(SubjectTeam) {
				t.Errorf("group_kind = %v, want %q -- the axis this turn proposed", line["group_kind"], SubjectTeam)
			}
		})
	}
	// Every member of the refusal vocabulary is an exit, and every exit is a
	// row: a refusal added later without a row here fails this check.
	for _, refusal := range []GroupReadRefusal{GroupReadRefusalNone, GroupReadRefusalOverContractBound, GroupReadRefusalNoReadRequirement,
		GroupReadRefusalAuthorizationUnavailable, GroupReadRefusalNoGroupAdmitted, GroupReadRefusalReadFailed, GroupReadRefusalMetadataConflict} {
		if !covered[refusal] {
			t.Errorf("exit %q was not reached by any row", refusal)
		}
	}
	if len(canonicalGroupReadRefusals) != len(covered) {
		t.Errorf("the refusal vocabulary has %d members and the sweep reached %d -- an exit has no row", len(canonicalGroupReadRefusals), len(covered))
	}
}
