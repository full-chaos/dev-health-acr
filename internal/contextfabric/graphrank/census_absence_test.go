package graphrank

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/userprincipal"
)

// CHAOS-12498: a complete census count of 0 on the question's one handle is a
// proven absence inside what the caller can read. It says so, on the real resolver
// and a real principal shape, and it offers no handle: the option could only veto
// or loop.

// absenceQuestions is one question per kind a handle can name.
var absenceQuestions = map[string]struct {
	question, value string
	kind            contextfabric.SubjectKind
}{
	"pull request": {"What is the state of pull request 999?", "999", contextfabric.SubjectPullRequest},
	"ci run":       {"What is the state of CI run 9999?", "9999", contractsv1.ContextFabricSubjectCIRun},
	"work item":    {"Why is CHAOS-999 blocked?", "CHAOS-999", contextfabric.SubjectWorkItem},
}

func noHandleOption(t *testing.T, turn listingTurn, value string) {
	t.Helper()
	for _, option := range turn.offers.HandleOptions {
		if option.Value == value {
			t.Fatalf("handle options = %+v: nothing holds %s", turn.offers.HandleOptions, value)
		}
	}
}

func TestACompleteZeroIsAProvenAbsence(t *testing.T) {
	t.Parallel()
	for name, q := range absenceQuestions {
		for caller, principal := range listingCallers(t, 2) {
			t.Run(name+" "+caller, func(t *testing.T) {
				t.Parallel()
				for _, terms := range [][]string{{"the subject"}, {"state"}} {
					turn := runListing(t, listingWorld{principal: principal, census: (&grantCensusSpy{}).census, question: q.question, terms: terms})
					want := contractsv1.ContextFabricCensusAbsentLimitation
					if q.kind == contextfabric.SubjectWorkItem && caller == "restricted" {
						want = contractsv1.ContextFabricWorkItemNoGrantedLinkLimitation
					}
					if turn.resolution.CensusLimitation != want {
						t.Fatalf("limitation = %q, want %q", turn.resolution.CensusLimitation, want)
					}
					if len(turn.resolution.Committed) != 0 || len(turn.resolution.Candidates) != 0 {
						t.Fatalf("committed %v candidates %v, want none", turn.resolution.Committed, candidateIDs(turn.resolution.Candidates))
					}
					noHandleOption(t, turn, q.value)
				}
			})
		}
	}
}

// The zero is a zero of the request, not of the grant. So the sentence names the
// scope, and an option could only loop: the handle verifies in the grant, and the
// narrowed round counts 0 again.
func TestAZeroInARequestedRepositoryStatesTheScope(t *testing.T) {
	t.Parallel()
	narrowed := func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
		r.RequestedScope.RepositorySlugs = []string{listingRepo(1)}
	}
	for name, q := range absenceQuestions {
		if q.kind == contextfabric.SubjectWorkItem {
			continue // Below: this world holds no link walk.
		}
		for caller, principal := range listingCallers(t, 2) {
			t.Run(name+" "+caller, func(t *testing.T) {
				t.Parallel()
				turn := runListing(t, listingWorld{principal: principal, census: (&grantCensusSpy{}).census, question: q.question, terms: []string{"the subject"}, edit: narrowed})
				if turn.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
					t.Fatalf("limitation = %q", turn.resolution.CensusLimitation)
				}
				noHandleOption(t, turn, q.value)
			})
		}
	}
	// The link sentence speaks for the grant, and a zero here does not (CHAOS-12456).
	request := testRequest()
	request.Question = "Why is CHAOS-999 blocked?"
	request.RequestedScope.RepositorySlugs = []string{listingRepo(1)}
	attestation := Attestation{UnscopedVisibility: true, Kinds: []KindAttestation{{Kind: contextfabric.SubjectWorkItem, Complete: true, HandleApplied: true, RepositoryFilterApplied: true}}}
	for caller, principal := range listingCallers(t, 2) {
		listed, handle := listCensusSatisfiers(context.Background(), principal, request, (&fakeGraphBackend{}).deps(), attestation, contextfabric.SubjectResolution{})
		if listed.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation || handle == nil {
			t.Fatalf("work item, %s: limitation %q handle %+v", caller, listed.CensusLimitation, handle)
		}
	}
}

// An anchor narrows the count as a requested repository does.
func TestAZeroUnderAnAnchorStatesTheScope(t *testing.T) {
	t.Parallel()
	request := testRequest()
	request.Question = "What is the state of pull request 999?"
	attestation := Attestation{UnscopedVisibility: true, Kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, HandleApplied: true, AnchorApplied: true}}}
	for caller, principal := range listingCallers(t, 2) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			listed, handle := listCensusSatisfiers(context.Background(), principal, request, (&fakeGraphBackend{}).deps(), attestation, contextfabric.SubjectResolution{})
			if listed.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
				t.Fatalf("limitation = %q", listed.CensusLimitation)
			}
			if handle == nil || handle.Value != "999" {
				t.Fatalf("handle = %+v, want the absent handle dropped", handle)
			}
		})
	}
	// A work item under an anchor takes the scope's sentence too: the link sentence
	// speaks for all the caller can read.
	item := request
	item.Question = "Why is CHAOS-999 blocked?"
	anchored := Attestation{UnscopedVisibility: true, Kinds: []KindAttestation{{Kind: contextfabric.SubjectWorkItem, Complete: true, HandleApplied: true, AnchorApplied: true}}}
	listed, _ := listCensusSatisfiers(context.Background(), restrictedCaller(t, listingRepo(0)), item, (&fakeGraphBackend{}).deps(), anchored, contextfabric.SubjectResolution{})
	if listed.CensusLimitation != contractsv1.ContextFabricCensusAbsentInScopeLimitation {
		t.Fatalf("work item: limitation = %q", listed.CensusLimitation)
	}
}

// A near match is a real subject the caller may mean. It stays, its handle stays,
// and the sentence states the zero of the handle the caller named.
func TestANearMatchStaysBesideAZero(t *testing.T) {
	t.Parallel()
	near := candidateNode(contextfabric.SubjectPullRequest, "pull_request:repo-00:5", "Release 999 prep", 0.8, []string{listingRepo(0)})
	near.Attributes[authorizationTeamsAttr] = "*"
	for caller, principal := range listingCallers(t, 2) {
		t.Run(caller, func(t *testing.T) {
			t.Parallel()
			turn := runListing(t, listingWorld{principal: principal, census: (&grantCensusSpy{}).census, pool: []CandidateNode{near}, truncated: true, question: "What is the state of pull request 999?", terms: []string{"pull request 999"}})
			if got := candidateIDs(turn.resolution.Candidates); !slices.Equal(got, []string{"pull_request:repo-00:5"}) {
				t.Fatalf("candidates = %v, want the near match", got)
			}
			if turn.resolution.CensusLimitation != contractsv1.ContextFabricCensusAbsentLimitation {
				t.Fatalf("limitation = %q", turn.resolution.CensusLimitation)
			}
			noHandleOption(t, turn, "999")
			if !slices.ContainsFunc(turn.offers.HandleOptions, func(option contractsv1.ContextFabricHandleOption) bool { return option.Value == "5" }) {
				t.Fatalf("handle options = %+v, want the near match's", turn.offers.HandleOptions)
			}
		})
	}
}

// Everything that is not a complete zero on one handle stays as it was: the handle
// may still name a real subject, and nothing proved otherwise.
func TestAZeroThatProvesNothingKeepsTheOption(t *testing.T) {
	t.Parallel()
	const question = "What is the state of pull request 999?"
	failing := func(context.Context, string, CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (CensusOutcome, error) {
		return CensusOutcome{}, errors.New("census unavailable")
	}
	restricted := restrictedCaller(t, listingScopes(2)...)
	for name, world := range map[string]listingWorld{
		"the census failed":          {principal: restricted, census: failing},
		"the closure did not hold":   {principal: restricted, census: countOnlyCensus(CensusOutcome{ClosureMismatch: true})},
		"the set did not close":      {principal: restricted, census: countOnlyCensus(CensusOutcome{SatisfierSetClosureMismatch: true})},
		"unrestricted, census error": {principal: userprincipal.Authenticated(t, "org_1", nil), census: failing},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			world.question, world.terms = question, []string{"the subject"}
			turn := runListing(t, world)
			if turn.resolution.CensusLimitation != "" {
				t.Fatalf("limitation = %q, want none: nothing was proved", turn.resolution.CensusLimitation)
			}
			if len(turn.offers.HandleOptions) != 1 || turn.offers.HandleOptions[0].Value != "999" {
				t.Fatalf("handle options = %+v, want it kept", turn.offers.HandleOptions)
			}
		})
	}
	t.Run("two handles: no census", func(t *testing.T) {
		t.Parallel()
		turn := runListing(t, listingWorld{principal: restricted, census: (&grantCensusSpy{}).census, question: "Compare pull request 999 and pull request 998", terms: []string{"the subject"}})
		if turn.resolution.CensusLimitation != "" || len(turn.offers.HandleOptions) != 2 {
			t.Fatalf("limitation %q options %+v", turn.resolution.CensusLimitation, turn.offers.HandleOptions)
		}
	})
	t.Run("a non-current axis: no census", func(t *testing.T) {
		t.Parallel()
		at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		turn := runListing(t, listingWorld{principal: restricted, census: (&grantCensusSpy{}).census, question: question, terms: []string{"the subject"}, timeAxis: &contextfabric.TimeContext{Axis: contextfabric.TemporalObservedTime, AsOf: &at}})
		if turn.resolution.CensusLimitation != "" || len(turn.offers.HandleOptions) != 1 {
			t.Fatalf("limitation %q options %+v", turn.resolution.CensusLimitation, turn.offers.HandleOptions)
		}
	})
	t.Run("project-only: no census and no option", func(t *testing.T) {
		t.Parallel()
		turn := runListing(t, listingWorld{principal: userprincipal.ProjectOnly(t, "org_1", []string{"project.v2:github:P-1"}), census: (&grantCensusSpy{}).census, question: question, terms: []string{"the subject"}})
		if turn.resolution.CensusLimitation != "" || len(turn.offers.HandleOptions) != 0 {
			t.Fatalf("limitation %q options %+v", turn.resolution.CensusLimitation, turn.offers.HandleOptions)
		}
	})
	t.Run("an explicit handle of another value stays", func(t *testing.T) {
		t.Parallel()
		turn := runListing(t, listingWorld{principal: restricted, census: (&grantCensusSpy{}).census, question: "What is the state of that change?", terms: []string{"that change"}, edit: func(r *contextfabric.InvestigationRequest, _ *ResolveDeps) {
			r.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: contextfabric.SubjectPullRequest, PatternID: "pull_request_number", Value: "999"}}
		}})
		if turn.resolution.CensusLimitation != "" || len(turn.offers.HandleOptions) != 1 {
			t.Fatalf("limitation %q options %+v: the question binds no handle, so no census ran", turn.resolution.CensusLimitation, turn.offers.HandleOptions)
		}
	})
}

// A committed turn is the answer. A zero beside a committed repository is
// CHAOS-12523, and it writes nothing here.
func TestACommitTakesNoAbsenceSentence(t *testing.T) {
	t.Parallel()
	request := testRequest()
	request.Question = "What is the state of pull request 999?"
	attestation := Attestation{UnscopedVisibility: true, Kinds: []KindAttestation{{Kind: contextfabric.SubjectPullRequest, Complete: true, HandleApplied: true}}}
	resolution := contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/r00"}}}
	listed, handle := listCensusSatisfiers(context.Background(), storage.Principal{OrgID: "org_1"}, request, (&fakeGraphBackend{}).deps(), attestation, resolution)
	if listed.CensusLimitation != "" || handle != nil {
		t.Fatalf("limitation %q handle %+v", listed.CensusLimitation, handle)
	}
}
