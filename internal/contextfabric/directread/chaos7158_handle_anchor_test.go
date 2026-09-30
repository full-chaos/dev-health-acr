package directread

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func anchorOf(kind contractsv1.ContextFabricSubjectKind, id string) *contractsv1.MCPFindSubjectsAnchor {
	return &contractsv1.MCPFindSubjectsAnchor{Kind: string(kind), ID: id}
}

// anchorSupportFixture mirrors the production census registry
// (devhealthsource chaos3899_census_registry.go anchorColumns): PR and CI-run
// handles anchor on a repository only; a work item on a project only.
func anchorSupportFixture(kind graphrank.CensusKind, anchorKind contextfabric.SubjectKind) bool {
	if kind == contractsv1.ContextFabricSubjectWorkItem {
		return anchorKind == contractsv1.ContextFabricSubjectProject
	}
	return anchorKind == contractsv1.ContextFabricSubjectRepository
}

// CHAOS-7158: an optional repository or project anchor narrows a handle to
// one subject with ONE census; the anchor takes a gate decision like any
// candidate; a refused or missing anchor answers exactly like an anchor with
// no match; an unsupported pair is a static refusal for everyone.
func TestChaos7158_HandleAnchor(t *testing.T) {
	pr := graphrank.CensusOutcome{Count: 1, SatisfierCanonicalID: prA.CanonicalID}
	newLookup := func(calls *[]censusCall) *SubjectLookup {
		return newModesLookup(handleGraph(), anchoredCensus(graphrank.CensusOutcome{Count: 2, SatisfierCanonicalIDs: []string{prA.CanonicalID, prB.CanonicalID}},
			map[string]graphrank.CensusOutcome{repoA.CanonicalID: pr}, calls)).WithCensusAnchorSupport(anchorSupportFixture)
	}

	t.Run("anchor resolves an ambiguous PR number with one census", func(t *testing.T) {
		var calls []censusCall
		open, err := newLookup(&calls).Find(relCtx("a-open"), unrestricted, FindRequest{Handle: "PR 532", Anchor: anchorOf(contractsv1.ContextFabricSubjectRepository, repoA.CanonicalID)})
		if err != nil || open.Status != FindComplete || foundIDs(open) != prA.CanonicalID {
			t.Fatalf("anchored = %v %+v", err, open)
		}
		if len(calls) != 1 || calls[0].anchor != repoA.CanonicalID || calls[0].value != "532" {
			t.Fatalf("census calls = %+v, want one anchored on repository a", calls)
		}
		calls = nil
		wide, err := newLookup(&calls).Find(relCtx("a-wide"), unrestricted, FindRequest{Handle: "PR 532"})
		if err != nil || wide.Status != FindAmbiguous {
			t.Fatalf("unanchored = %v %+v", err, wide)
		}
	})

	t.Run("a refused, missing or matchless anchor answers identically", func(t *testing.T) {
		answer := func(name string, anchor *contractsv1.MCPFindSubjectsAnchor, calls *[]censusCall) string {
			response, err := newLookup(calls).Find(relCtx("id-"+name), restrictedA, FindRequest{Handle: "PR 532", Anchor: anchor})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			encoded, _ := json.Marshal(response)
			return string(encoded)
		}
		var refusedCalls, missingCalls, matchlessCalls []censusCall
		refused := answer("refused", anchorOf(contractsv1.ContextFabricSubjectRepository, repoB.CanonicalID), &refusedCalls)
		missing := answer("missing", anchorOf(contractsv1.ContextFabricSubjectRepository, "repository:nope"), &missingCalls)
		// restrictedA may read repository a, whose census has no PR 532 here.
		matchless := func() string {
			var calls []censusCall
			lookup := newModesLookup(handleGraph(), anchoredCensus(graphrank.CensusOutcome{}, nil, &calls)).WithCensusAnchorSupport(anchorSupportFixture)
			response, err := lookup.Find(relCtx("id-matchless"), restrictedA, FindRequest{Handle: "PR 532", Anchor: anchorOf(contractsv1.ContextFabricSubjectRepository, repoA.CanonicalID)})
			if err != nil {
				t.Fatal(err)
			}
			matchlessCalls = calls
			encoded, _ := json.Marshal(response)
			return string(encoded)
		}()
		if refused != missing || refused != matchless {
			t.Fatalf("anchor states are distinguishable:\nrefused   %s\nmissing   %s\nmatchless %s", refused, missing, matchless)
		}
		if len(refusedCalls) != 1 || len(missingCalls) != 1 || len(matchlessCalls) != 1 {
			t.Fatalf("census calls refused=%d missing=%d matchless=%d, want 1 1 1 (same execution, codex r1 P1)", len(refusedCalls), len(missingCalls), len(matchlessCalls))
		}
	})

	t.Run("static refusals are the same for every caller", func(t *testing.T) {
		repo := anchorOf(contractsv1.ContextFabricSubjectRepository, repoA.CanonicalID)
		for _, tc := range []struct {
			name string
			req  FindRequest
		}{
			{"work item on a repository", FindRequest{Handle: "CHAOS-42", Anchor: repo}},
			{"PR on a project", FindRequest{Handle: "PR 532", Anchor: anchorOf(contractsv1.ContextFabricSubjectProject, projectQ.CanonicalID)}},
			{"team anchor", FindRequest{Handle: "PR 532", Anchor: anchorOf(contractsv1.ContextFabricSubjectTeam, "team:t")}},
			{"empty id", FindRequest{Handle: "PR 532", Anchor: anchorOf(contractsv1.ContextFabricSubjectRepository, " ")}},
			{"no handle", FindRequest{Query: "x", Anchor: repo}},
			{"anchor alone", FindRequest{Kind: "repository", Anchor: repo}},
		} {
			for who, principal := range map[string]storage.Principal{"unrestricted": unrestricted, "restricted": restrictedA} {
				var calls []censusCall
				_, err := newLookup(&calls).Find(relCtx("s-"+tc.name+who), principal, tc.req)
				if !errors.Is(err, ErrFindInvalidRequest) || errors.Is(err, ErrFindScopeRequired) && tc.name != "work item on a repository" {
					t.Errorf("%s/%s: err = %v, want invalid_find_request", tc.name, who, err)
				}
				if len(calls) != 0 {
					t.Errorf("%s/%s: %d census calls, want 0", tc.name, who, len(calls))
				}
			}
		}
	})

	t.Run("work item key with a project anchor; restricted stays scope_required", func(t *testing.T) {
		var calls []censusCall
		project := anchorOf(contractsv1.ContextFabricSubjectProject, projectQ.CanonicalID)
		if _, err := newLookup(&calls).Find(relCtx("wi-open"), unrestricted, FindRequest{Handle: "CHAOS-42", Anchor: project}); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0].anchor != projectQ.CanonicalID || calls[0].kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Fatalf("census calls = %+v", calls)
		}
		calls = nil
		_, err := newLookup(&calls).Find(relCtx("wi-restricted"), restrictedA, FindRequest{Handle: "CHAOS-42", Anchor: project})
		if !errors.Is(err, ErrFindScopeRequired) || len(calls) != 0 {
			t.Fatalf("restricted work item with anchor: err=%v calls=%d, want scope_required and no census", err, len(calls))
		}
	})
}
