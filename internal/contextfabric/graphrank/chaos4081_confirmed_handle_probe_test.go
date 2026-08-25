package graphrank

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4081 (team-lead ruling, path (a), 2026-08-25): request.SubjectHandles
// never reached the shadow evidence round -- it fed only handleOfferMaterial
// (offer ranking) and explicit-structure stamping. This is the RED pin for
// closing that gap for ATTESTATION/TRACE ONLY: a confirmed explicit
// subject_handle hint now reaches RunShadowEvidenceRound
// (ShadowEvidenceRoundInput.ConfirmedHandle) and populates
// HandleInsensitivityEvaluated/HandleInsensitivityOutcome, WITHOUT ever
// being able to change the round's own decisive Outcome/Kinds/
// NonCensusedSurvivor -- see TestConfirmedHandleProbeIsWriteFree, the
// structural proof of that guarantee.

// multiKindCensus scripts ONE outcome per kind, independently -- unlike
// withCensus (single kind), this test file needs two kinds censused in the
// SAME round: the pool's own anchor-driven kind, and ConfirmedHandle's own
// kind, which never overlaps it.
func multiKindCensus(byKind map[CensusKind]CensusOutcome) *fakeCensus {
	f := &fakeCensus{byKind: map[CensusKind][]struct {
		outcome CensusOutcome
		err     error
	}{}}
	for kind, outcome := range byKind {
		f.byKind[kind] = append(f.byKind[kind], struct {
			outcome CensusOutcome
			err     error
		}{outcome, nil})
	}
	return f
}

// confirmedHandleScenario builds the shared shape every test below reuses:
// a decisive round on its OWN terms (pull_request pooled, reached via a
// CONFIRMED ANCHOR on repository -- never via any handle at all), plus a
// ConfirmedHandle naming a COMPLETELY DIFFERENT kind (work_item) that never
// overlaps the pool or the anchor's own FK reach. That separation is what
// makes "the round's real decision never moves" a structural property of
// the test, not just an observed one: nothing about work_item's own census
// can feed pull_request's decisive computation.
func confirmedHandleScenario(census CensusFunc) ShadowEvidenceRoundInput {
	input := baseInput()
	input.Question = "how did the repository do?"
	input.PooledKinds = []CensusKind{contextfabric.SubjectPullRequest}
	input.ConfirmedAnchor = &AnchorBinding{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/widgets"}
	input.CensusFunc = census
	return input
}

func confirmedHandle() *BoundHandle {
	return &BoundHandle{Kind: contractsv1.ContextFabricSubjectWorkItem, Grammar: "work_item_ticket_key", Value: "CHAOS-1"}
}

// TestConfirmedHandleProbeIsWriteFree is the STRUCTURAL guarantee CHAOS-4081
// exists to hold: adding ConfirmedHandle changes the returned Attestation in
// the two HandleInsensitivity* fields and in NOTHING else -- reflect-driven
// for the identical reason TestObservedKindInsensitivityProbeIsWriteFree is
// (CHAOS-4079): a hand-written field list would silently keep passing while
// a future edit widened what this path writes into a decision-bearing field.
func TestConfirmedHandleProbeIsWriteFree(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name        string
		workItem    CensusOutcome
		wantVerdict kindInsensitivityOutcome
	}{
		{
			name:        "confirmed handle kind has exactly one satisfier",
			workItem:    CensusOutcome{Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "work_item:linear:CHAOS-1"},
			wantVerdict: kindInsensitivityCommitSound,
		},
		{
			name:        "confirmed handle kind has zero satisfiers",
			workItem:    CensusOutcome{Count: 0, CensusReadAt: readAt},
			wantVerdict: kindInsensitivityNoMatchSound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pullRequestOutcome := CensusOutcome{Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "pull_request:acme/widgets:532"}

			before := confirmedHandleScenario(multiKindCensus(map[CensusKind]CensusOutcome{
				contextfabric.SubjectPullRequest:         pullRequestOutcome,
				contractsv1.ContextFabricSubjectWorkItem: tc.workItem,
			}).fn)
			beforeAtt := RunShadowEvidenceRound(context.Background(), before, nil)

			if beforeAtt.Outcome != ShadowWouldCommit {
				t.Fatalf("precondition: before.Outcome = %v, want would_commit (the pool's own anchor-driven decision, unrelated to any handle)", beforeAtt.Outcome)
			}
			if beforeAtt.HandleInsensitivityEvaluated {
				t.Fatalf("precondition: before.HandleInsensitivityEvaluated = true, want false -- no ConfirmedHandle was set")
			}

			after := confirmedHandleScenario(multiKindCensus(map[CensusKind]CensusOutcome{
				contextfabric.SubjectPullRequest:         pullRequestOutcome,
				contractsv1.ContextFabricSubjectWorkItem: tc.workItem,
			}).fn)
			after.ConfirmedHandle = confirmedHandle()
			afterAtt := RunShadowEvidenceRound(context.Background(), after, nil)

			// The complete, closed set of fields the ConfirmedHandle probe
			// path may write.
			observability := map[string]bool{
				"HandleInsensitivityEvaluated": true,
				"HandleInsensitivityOutcome":   true,
			}
			bv, av := reflect.ValueOf(beforeAtt), reflect.ValueOf(afterAtt)
			for i := 0; i < bv.NumField(); i++ {
				name := bv.Type().Field(i).Name
				if observability[name] {
					continue
				}
				if !reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
					t.Errorf("Attestation.%s differs with ConfirmedHandle set (%#v -> %#v) -- ConfirmedHandle must reach ONLY HandleInsensitivity* fields; if this field was added to that path deliberately, it is decision-bearing until proven otherwise and needs its own Slice C consumer audit",
						name, bv.Field(i).Interface(), av.Field(i).Interface())
				}
			}

			// And the probe itself actually happened (a write-free no-op
			// would pass the loop above vacuously).
			if !afterAtt.HandleInsensitivityEvaluated || afterAtt.HandleInsensitivityOutcome != tc.wantVerdict {
				t.Fatalf("after = evaluated:%v outcome:%q, want true/%q", afterAtt.HandleInsensitivityEvaluated, afterAtt.HandleInsensitivityOutcome, tc.wantVerdict)
			}
		})
	}
}

// TestConfirmedHandleProbeReachesTraceEvent proves the observability side of
// the same guarantee: the evidence_round trace event carries
// ShadowHandleInsensitivityEvaluated/ShadowHandleInsensitivityOutcome when
// ConfirmedHandle is set.
func TestConfirmedHandleProbeReachesTraceEvent(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	census := multiKindCensus(map[CensusKind]CensusOutcome{
		contextfabric.SubjectPullRequest:         {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "pull_request:acme/widgets:532"},
		contractsv1.ContextFabricSubjectWorkItem: {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "work_item:linear:CHAOS-1"},
	})
	input := confirmedHandleScenario(census.fn)
	input.ConfirmedHandle = confirmedHandle()
	tracer := &captureResolutionTracer{}

	RunShadowEvidenceRound(context.Background(), input, tracer)

	events := tracer.eventsForStage("evidence_round")
	if len(events) != 1 {
		t.Fatalf("evidence_round events = %d, want 1", len(events))
	}
	event := events[0]
	if !event.ShadowHandleInsensitivityEvaluated || event.ShadowHandleInsensitivityOutcome != string(kindInsensitivityCommitSound) {
		t.Fatalf("evidence_round event = %+v, want ShadowHandleInsensitivityEvaluated=true ShadowHandleInsensitivityOutcome=%q", event, kindInsensitivityCommitSound)
	}
}

// TestConfirmedHandleProbeNeverEvaluatesWithoutConfirmedHandle pins the
// no-op default: a round that carries no ConfirmedHandle at all (every
// caller before this ticket) leaves both fields at their zero value.
func TestConfirmedHandleProbeNeverEvaluatesWithoutConfirmedHandle(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	input := confirmedHandleScenario(multiKindCensus(map[CensusKind]CensusOutcome{
		contextfabric.SubjectPullRequest: {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "pull_request:acme/widgets:532"},
	}).fn)

	att := RunShadowEvidenceRound(context.Background(), input, nil)

	if att.HandleInsensitivityEvaluated || att.HandleInsensitivityOutcome != "" {
		t.Fatalf("att = %+v, want HandleInsensitivityEvaluated=false Outcome=\"\" with no ConfirmedHandle set", att)
	}
}

// TestRunShadowEvidenceRoundForResolution_ConfirmedHandleWiring is the
// resolve.go integration half: request.SubjectHandles, validated through
// deps.HandleGrammarChecker exactly like handleOfferMaterial validates it,
// must reach ShadowEvidenceRoundInput.ConfirmedHandle -- and an entry the
// checker rejects must be skipped, not threaded through anyway.
func TestRunShadowEvidenceRoundForResolution_ConfirmedHandleWiring(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	principal := storage.Principal{OrgID: "org-1"}
	request := contextfabric.InvestigationRequest{
		RequestID: "req-4081", Question: "how did the repository do?",
		SubjectHandles: []contractsv1.ContextFabricRequestedHandle{
			{Kind: contractsv1.ContextFabricSubjectWorkItem, PatternID: "work_item_ticket_key", Value: "CHAOS-1"},
		},
	}
	interpreted := contextfabric.InterpretedQuestion{TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}}
	resolution := contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{
		{Subject: contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:acme/widgets:532"}},
	}}
	confirmedAnchor := &contextfabric.ConfirmedAnchorSelection{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/widgets"}

	t.Run("grammar-valid entry reaches the round", func(t *testing.T) {
		census := multiKindCensus(map[CensusKind]CensusOutcome{
			contextfabric.SubjectPullRequest:         {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "pull_request:acme/widgets:532"},
			contractsv1.ContextFabricSubjectWorkItem: {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "work_item:linear:CHAOS-1"},
		})
		deps := ResolveDeps{
			CensusFunc: census.fn,
			HandleGrammarChecker: func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
				return "work_items.work_item_id", true
			},
		}
		att := runShadowEvidenceRoundForResolution(context.Background(), principal, request, interpreted, resolution, nil, true, true, deps, nil, confirmedAnchor)
		if !att.HandleInsensitivityEvaluated || att.HandleInsensitivityOutcome != kindInsensitivityCommitSound {
			t.Fatalf("att = %+v, want HandleInsensitivityEvaluated=true Outcome=%q", att, kindInsensitivityCommitSound)
		}
	})

	t.Run("grammar-invalid entry is skipped, never threaded through", func(t *testing.T) {
		census := multiKindCensus(map[CensusKind]CensusOutcome{
			contextfabric.SubjectPullRequest: {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "pull_request:acme/widgets:532"},
		})
		deps := ResolveDeps{
			CensusFunc: census.fn,
			HandleGrammarChecker: func(kind contractsv1.ContextFabricSubjectKind, patternID, value string) (string, bool) {
				return "", false
			},
		}
		att := runShadowEvidenceRoundForResolution(context.Background(), principal, request, interpreted, resolution, nil, true, true, deps, nil, confirmedAnchor)
		if att.HandleInsensitivityEvaluated {
			t.Fatalf("att = %+v, want HandleInsensitivityEvaluated=false -- a grammar-rejected SubjectHandles entry must never reach ConfirmedHandle", att)
		}
	})

	t.Run("nil HandleGrammarChecker is the safe no-op default", func(t *testing.T) {
		census := multiKindCensus(map[CensusKind]CensusOutcome{
			contextfabric.SubjectPullRequest: {Count: 1, CensusReadAt: readAt, SatisfierCanonicalID: "pull_request:acme/widgets:532"},
		})
		deps := ResolveDeps{CensusFunc: census.fn}
		att := runShadowEvidenceRoundForResolution(context.Background(), principal, request, interpreted, resolution, nil, true, true, deps, nil, confirmedAnchor)
		if att.HandleInsensitivityEvaluated {
			t.Fatalf("att = %+v, want HandleInsensitivityEvaluated=false with no HandleGrammarChecker wired", att)
		}
	})
}
