package devhealthfacts_test

// CHAOS-7073 oracle O1 (design CHAOS-7036 J.4): the read_facts response
// against the registry BUNDLE for the same typed request, captured at the
// registry seam.
//
// Path 1 is the direct tool (directread.FactsReader: subject gate, fact
// reader, embedded-subject gate, wire encoder). Path 2 is what the REAL
// registry, over the REAL devhealthfacts providers on a REAL ClickHouse,
// returned at the seam for the request the tool actually sent. No engine
// runs, so no model.
//
// Hardening (AGENTS.md "differential oracle"):
//   - cases come from the real producer (seeded ClickHouse rows through the
//     production providers), never hand-written fact JSON;
//   - the compared field set is the CATALOGUE declaration
//     (FactCapability.Fields from the production registry), so a pair cannot
//     quietly compare three chosen fields; a declared field present on one
//     side and missing on the other fails;
//   - every leaf is tagged {"t": type, "v": string}; the response side is
//     decoded with UseNumber, so no bare JSON number reaches a comparison
//     through float64;
//   - freshness by execution: the providers run; nothing is digested;
//   - the seam request is compared too: kinds, subjects and the time
//     context the caller asked for.
//
// Planted defects it must keep finding (J.4): the direct path sends the
// current axis when the caller asked for a range; the direct path drops the
// prior_theme_* fields; the encoder rounds a float. See the PR body for the
// executed plant table.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// seamCapture records the request and the bundle at the registry seam.
type seamCapture struct {
	registry *contextfabric.FactCapabilityRegistry
	requests []contextfabric.CanonicalFactRequest
	bundles  []contextfabric.CanonicalFactBundle
}

func (s *seamCapture) ReadFacts(ctx context.Context, principal storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	bundle, err := s.registry.ReadFacts(ctx, principal, request)
	s.requests = append(s.requests, request)
	s.bundles = append(s.bundles, bundle)
	return bundle, err
}

func (s *seamCapture) Capabilities() []contextfabric.FactCapability { return s.registry.Capabilities() }

// admitAllGraph is an organization graph in which every requested subject
// exists and is visible (the oracle compares encodings, not authorization;
// T14 owns the gate).
type admitAllGraph struct{}

func (admitAllGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, nil
}

func (admitAllGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range out {
		out[index] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}

func (admitAllGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

type taggedLeaf struct {
	T string `json:"t"`
	V string `json:"v"`
}

func tagFactValue(value contextfabric.FactValue, declared contextfabric.FactFieldType) taggedLeaf {
	switch {
	case value.Null:
		return taggedLeaf{T: "null"}
	case value.String != nil:
		return taggedLeaf{T: "string", V: *value.String}
	case value.Integer != nil:
		return taggedLeaf{T: "integer", V: strconv.FormatInt(*value.Integer, 10)}
	case value.Number != nil:
		if math.IsNaN(*value.Number) || math.IsInf(*value.Number, 0) {
			return taggedLeaf{T: "null"}
		}
		return taggedLeaf{T: "number", V: strconv.FormatFloat(*value.Number, 'g', -1, 64)}
	case value.Boolean != nil:
		return taggedLeaf{T: "boolean", V: strconv.FormatBool(*value.Boolean)}
	default:
		return taggedLeaf{T: "null"}
	}
}

func tagWireValue(t *testing.T, value any, declared contextfabric.FactFieldType) taggedLeaf {
	t.Helper()
	switch v := value.(type) {
	case nil:
		return taggedLeaf{T: "null"}
	case string:
		if declared == contextfabric.FactFieldInteger {
			return taggedLeaf{T: "integer", V: v}
		}
		return taggedLeaf{T: "string", V: v}
	case json.Number:
		parsed, err := strconv.ParseFloat(v.String(), 64)
		if err != nil {
			t.Fatalf("wire number %q: %v", v, err)
		}
		return taggedLeaf{T: "number", V: strconv.FormatFloat(parsed, 'g', -1, 64)}
	case bool:
		return taggedLeaf{T: "boolean", V: strconv.FormatBool(v)}
	default:
		t.Fatalf("wire leaf of unexpected JSON type %T", value)
		return taggedLeaf{}
	}
}

// wireFact is the response fact decoded with UseNumber.
type wireFact struct {
	Kind    string `json:"kind"`
	Subject struct {
		Kind        string `json:"kind"`
		CanonicalID string `json:"canonical_id"`
	} `json:"subject"`
	Fields map[string]any `json:"fields"`
	Tables map[string]struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	} `json:"tables"`
	Withheld []struct {
		Field  string `json:"field"`
		Reason string `json:"reason"`
	} `json:"withheld"`
}

type wireResponse struct {
	Facts []wireFact `json:"facts"`
}

// compareO1 is the oracle: every declared field of every bundle fact is on
// the wire with the same tagged value, and nothing declared is on the wire
// that the bundle does not hold.
func compareO1(t *testing.T, capabilities map[contextfabric.FactKind]contextfabric.FactCapability, bundle contextfabric.CanonicalFactBundle, response directread.FactsResponse) []string {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var wire wireResponse
	if err := decoder.Decode(&wire); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	byKey := map[string]wireFact{}
	for _, fact := range wire.Facts {
		byKey[fact.Kind+"\x00"+fact.Subject.Kind+"\x00"+fact.Subject.CanonicalID] = fact
	}
	var diffs []string
	compared := 0
	for _, fact := range bundle.Facts {
		key := string(fact.Kind) + "\x00" + string(fact.Subject.Kind) + "\x00" + fact.Subject.CanonicalID
		served, ok := byKey[key]
		if !ok {
			diffs = append(diffs, fmt.Sprintf("%s %s: in the bundle, not on the wire", fact.Kind, fact.Subject.CanonicalID))
			continue
		}
		delete(byKey, key)
		capability := capabilities[fact.Kind]
		for _, declaration := range capability.Fields {
			if !declaration.AppliesTo(fact.Subject.Kind) {
				continue
			}
			name := declaration.Name
			bundleValue, inBundle := fact.Fields[name]
			if declaration.Type == contextfabric.FactFieldTable {
				wireTable, onWire := served.Tables[name]
				if inBundle != onWire {
					diffs = append(diffs, fmt.Sprintf("%s %s table %s: bundle %v wire %v", fact.Kind, fact.Subject.CanonicalID, name, inBundle, onWire))
					continue
				}
				if !inBundle {
					continue
				}
				if len(wireTable.Rows) != len(bundleValue.Rows) {
					diffs = append(diffs, fmt.Sprintf("%s %s table %s: %d rows in the bundle, %d on the wire", fact.Kind, fact.Subject.CanonicalID, name, len(bundleValue.Rows), len(wireTable.Rows)))
					continue
				}
				for rowIndex, row := range bundleValue.Rows {
					for columnIndex, column := range wireTable.Columns {
						columnDeclaration, _ := declaration.Column(column)
						want := taggedLeaf{T: "null"}
						if cell, ok := row.Fields[column]; ok {
							want = tagFactValue(cell, columnDeclaration.Type)
						}
						got := tagWireValue(t, wireTable.Rows[rowIndex][columnIndex], columnDeclaration.Type)
						compared++
						if got != want {
							diffs = append(diffs, fmt.Sprintf("%s %s %s[%d].%s: bundle %+v wire %+v", fact.Kind, fact.Subject.CanonicalID, name, rowIndex, column, want, got))
						}
					}
				}
				continue
			}
			wireValue, onWire := served.Fields[name]
			if inBundle != onWire {
				diffs = append(diffs, fmt.Sprintf("%s %s field %s: bundle %v wire %v", fact.Kind, fact.Subject.CanonicalID, name, inBundle, onWire))
				continue
			}
			if !inBundle {
				continue
			}
			compared++
			if want, got := tagFactValue(bundleValue, declaration.Type), tagWireValue(t, wireValue, declaration.Type); want != got {
				diffs = append(diffs, fmt.Sprintf("%s %s field %s: bundle %+v wire %+v", fact.Kind, fact.Subject.CanonicalID, name, want, got))
			}
		}
		for name := range fact.Fields {
			if _, declared := capability.FieldDeclaration(name, fact.Subject.Kind); !declared {
				diffs = append(diffs, fmt.Sprintf("%s %s field %s: produced but not declared (T4 owns this; O1 refuses to compare an undeclared field)", fact.Kind, fact.Subject.CanonicalID, name))
			}
		}
	}
	for key := range byKey {
		diffs = append(diffs, fmt.Sprintf("%q: on the wire, not in the bundle", key))
	}
	if compared == 0 {
		// Rule 4: a comparison that compared nothing is a failure.
		diffs = append(diffs, "O1 compared zero leaves")
	}
	sort.Strings(diffs)
	return diffs
}

func TestChaos7073OracleO1AgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	createCHAOS5930Tables(t, ctx, direct)
	const orgID = "org-o1"
	seededAt := ts(2026, 9, 20, 0, 0, 0)
	for _, label := range []string{"o1-a", "o1-b"} {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repoUUID(label), orgID, "acme/"+label, "github", seededAt); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "github", "team-o1", repoUUID(label), "acme/"+label, "exact", "native", uint8(1), uint16(100), int32(0), ts(2026, 1, 1, 0, 0, 0), nil, seededAt); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	seedWU := func(id, repoLabel string, at time.Time, effort float64, themes map[string]float64) {
		t.Helper()
		if err := direct.Exec(ctx,
			`INSERT INTO work_unit_investments (work_unit_id, from_ts, to_ts, repo_id, effort_value, theme_distribution_json, subcategory_distribution_json, structural_evidence_json, computed_at, org_id) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, at, at, repoUUID(repoLabel), effort, themes, map[string]float64{"bugfix": 1.0 / 3.0}, `{"issues":[],"prs":[]}`, seededAt, orgID); err != nil {
			t.Fatalf("seed wu %s: %v", id, err)
		}
	}
	// Current window (Sep 1 - Sep 20) and the prior window of equal length
	// (Aug 13 - Sep 1). Efforts and shares chosen so the served shares are
	// non-terminating binary fractions: a rounding encoder cannot hide.
	current := ts(2026, 9, 10, 0, 0, 0)
	prior := ts(2026, 8, 20, 0, 0, 0)
	seedWU("o1-wu1", "o1-a", current, 7, map[string]float64{"feature_delivery": 2.0 / 3.0, "operational": 1.0 / 3.0})
	seedWU("o1-wu2", "o1-b", current, 11, map[string]float64{"maintenance": 0.7, "quality": 0.3})
	seedWU("o1-wu3", "o1-a", prior, 13, map[string]float64{"risk": 0.9, "feature_delivery": 0.1})

	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	seam := &seamCapture{registry: registry.WithoutScopeExpansion()}
	gate := directread.NewSubjectGate(admitAllGraph{}, nil)
	reader := directread.NewFactsReader(gate, directread.NewFactReader(seam), nil)
	principal := storage.Principal{OrgID: orgID, Subject: "o1", CredentialID: "o1"}
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 20, 0, 0, 0)
	request := directread.FactsRequest{
		Kinds: []string{"investment"},
		Subjects: []directread.RequestSubject{
			{Kind: "repository", CanonicalID: "repository:" + repoUUID("o1-a")},
			{Kind: "repository", CanonicalID: "repository:" + repoUUID("o1-b")},
			{Kind: "team", CanonicalID: "team:team-o1"},
		},
		Window: &directread.RequestWindow{Mode: directread.WindowRange, Start: &start, End: &end},
	}
	response, err := reader.Read(ctx, principal, request)
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	if len(seam.requests) != 1 {
		t.Fatalf("seam reads = %d, want 1", len(seam.requests))
	}

	// The seam request is the caller's typed request.
	sent := seam.requests[0]
	if sent.Question.TimeContext.Axis != contextfabric.TemporalRange || sent.Question.TimeContext.Start == nil || !sent.Question.TimeContext.Start.Equal(start) || sent.Question.TimeContext.End == nil || !sent.Question.TimeContext.End.Equal(end) {
		t.Errorf("O1 seam: time context %+v, want range %s..%s", sent.Question.TimeContext, start, end)
	}
	if len(sent.Requirements) != 1 || sent.Requirements[0].Kind != contextfabric.FactInvestment || len(sent.Subjects) != 3 {
		t.Errorf("O1 seam: requirements %+v subjects %v", sent.Requirements, sent.Subjects)
	}

	capabilities := map[contextfabric.FactKind]contextfabric.FactCapability{}
	for _, capability := range registry.Capabilities() {
		capabilities[capability.Kind] = capability
	}
	bundle := seam.bundles[0]
	// Rule 1 of the oracle's own acceptance: the seeded data must produce
	// the facts that exercise the planted-defect surfaces, or a pass is
	// vacuous.
	var sawPrior, sawFraction bool
	for _, fact := range bundle.Facts {
		for name, value := range fact.Fields {
			if len(name) > 12 && name[:12] == "prior_theme_" {
				sawPrior = true
			}
			if value.Number != nil && *value.Number != math.Round(*value.Number*1000)/1000 {
				sawFraction = true
			}
		}
	}
	if len(bundle.Facts) != 3 || !sawPrior || !sawFraction {
		t.Fatalf("O1 fixture is vacuous: facts %d prior_theme %v non-terminating number %v (%+v)", len(bundle.Facts), sawPrior, sawFraction, bundle.Coverage)
	}
	for _, diff := range compareO1(t, capabilities, bundle, response) {
		t.Errorf("O1: %s", diff)
	}
}
