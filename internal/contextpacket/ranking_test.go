package contextpacket_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestAssembler_ranks_goal_provenance_freshness_category_confidence_then_totalTie(t *testing.T) {
	// Given
	request := fixtureRequest("req-comparator", "main", "")
	observed := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	nativeOld := testEvidence("ev-native-old", "ci", observed.Add(-time.Hour))
	nativeNew := testEvidence("ev-native-new", "git", observed.Add(time.Minute))
	nativeNew.Provenance = "native"
	nativeOld.Provenance = "native"
	derivedFresh := testEvidence("ev-derived-fresh", "ci", observed.Add(time.Hour))
	derivedFresh.Provenance = "derived"
	pressureLow := testEvidence("ev-pressure-low", "ci", observed)
	pressureLow.Confidence = 0.6
	pressureHigh := testEvidence("ev-pressure-high", "ci", observed)
	pressureHigh.Confidence = 0.9
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{derivedFresh, nativeOld, pressureLow, nativeNew, pressureHigh},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble comparator packet: %v", err)
	}
	got := itemEvidenceIDs(packet)
	want := []string{"ev-native-new", "ev-pressure-high", "ev-pressure-low", "ev-native-old", "ev-derived-fresh"}
	if !equalStrings(got, want) {
		t.Fatalf("ranked evidence = %v, want %v", got, want)
	}
}

func TestAssembler_deduplicates_canonical_identifier_entity_and_content(t *testing.T) {
	// Given
	request := fixtureRequest("req-dedup", "main", "")
	observed := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	canonical := testEvidence("ev-canonical", "ci", observed)
	duplicateID := canonical
	duplicateID.Citation = "alternate text"
	duplicateEntity := testEvidence("ev-other-id", "ci", observed)
	duplicateEntity.Source.EntityID = canonical.Source.EntityID
	duplicateContent := testEvidence("ev-content", "ci", observed)
	duplicateContent.Source.EntityID = "different-entity"
	duplicateContent.ContentDigest = "sha256:content"
	canonical.ContentDigest = "sha256:content"
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{duplicateContent, duplicateEntity, duplicateID, canonical},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble deduplicated packet: %v", err)
	}
	if len(packet.Items) != 1 {
		t.Fatalf("items = %#v, want one canonical item", packet.Items)
	}
}

func TestAssembler_emits_sorted_deduplicated_actions_per_rule(t *testing.T) {
	// Given
	request := fixtureRequest("req-actions", "main", "")
	observed := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	first := testEvidence("ev-first", "ci", observed)
	second := testEvidence("ev-second", "git", observed)
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{first, second},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble action packet: %v", err)
	}
	if len(packet.RequiredChecks) != 2 || len(packet.RecommendedNextSteps) != 2 {
		t.Fatalf("actions = %#v %#v, want one pair per rule", packet.RequiredChecks, packet.RecommendedNextSteps)
	}
	if !strings.HasSuffix(packet.RequiredChecks[0].CheckID, packet.RequiredChecks[0].RuleID) || packet.RequiredChecks[0].RuleID > packet.RequiredChecks[1].RuleID {
		t.Fatalf("required checks are not stable: %#v", packet.RequiredChecks)
	}
}

func TestAssembler_bounds_derived_item_title_and_identifier(t *testing.T) {
	// Given
	request := fixtureRequest("req-derived-bounds", "main", "")
	ref := testEvidence(strings.Repeat("e", 256), "ci", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	ref.Source.DisplayLabel = strings.Repeat("界", 1_000)
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{ref},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}

	// When
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)

	// Then
	if err != nil {
		t.Fatalf("assemble bounded item: %v", err)
	}
	if got := len([]rune(packet.Items[0].Title)); got != 500 {
		t.Fatalf("title rune length = %d, want 500", got)
	}
	if got := len(packet.Items[0].PacketItemID); got > 256 || got < 8 {
		t.Fatalf("packet item id length = %d", got)
	}
}

func TestAssembler_dedupes_one_pull_request_served_as_pull_request_and_work_item(t *testing.T) {
	observed := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	build := func(id, entityType, entityID, subject string) contractsv1.EvidenceRef {
		ref := testEvidence(id, "dev_health", observed)
		ref.Source.EntityType, ref.Source.EntityID = entityType, entityID
		ref.Metadata = map[string]any{"subject_key": subject}
		return ref
	}
	cases := []struct {
		name     string
		second   string
		wantKept int
	}{
		{name: "same PR under two entity types is one item", second: "pull_request:repo:2", wantKept: 1},
		{name: "the same number in another repository stays distinct", second: "pull_request:other-repo:2", wantKept: 2},
		{name: "an empty subject key never matches", second: "", wantKept: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := fixtureRequest("req-pr-subject", "main", "")
			pr := build("ev2_prod1_pr_a", "pull_request", "2", "pull_request:repo:2")
			workItem := build("ev2_prod1_work_b", "work_item", "ghpr:chaos-fixtures/checkout-api#2", tc.second)
			store := testStore{bundle: storage.EvidenceBundle{
				ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
				Evidence:      []contractsv1.EvidenceRef{workItem, pr},
				QueryVersion:  contextpacket.QueryVersionV1,
			}}
			packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			if len(packet.Items) != tc.wantKept {
				t.Fatalf("items = %d, want %d: %#v", len(packet.Items), tc.wantKept, packet.Items)
			}
			if tc.wantKept == 1 && packet.Items[0].EvidenceRefIDs[0] != "ev2_prod1_pr_a" {
				t.Fatalf("kept %v, want the pull_request row", packet.Items[0].EvidenceRefIDs)
			}
		})
	}
}

func TestAssembler_pull_request_row_wins_over_a_better_scored_work_item_with_the_same_subject(t *testing.T) {
	observed := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	request := fixtureRequest("req-pr-wins", "main", "")
	request.Goal = "checkout payments"
	pr := testEvidence("ev2_prod1_pr_a", "dev_health", observed)
	pr.Source.EntityType, pr.Source.EntityID = "pull_request", "2"
	pr.Metadata = map[string]any{"subject_key": "pull_request:repo:2"}
	workItem := testEvidence("ev2_prod1_work_b", "dev_health", observed)
	workItem.Source.EntityType, workItem.Source.EntityID = "work_item", "ghpr:o/r#2"
	workItem.Source.DisplayLabel, workItem.Citation = "checkout payments", "checkout payments"
	workItem.Metadata = map[string]any{"subject_key": "pull_request:repo:2"}
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      []contractsv1.EvidenceRef{workItem, pr},
		QueryVersion:  contextpacket.QueryVersionV1,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(packet.Items) != 1 || packet.Items[0].EvidenceRefIDs[0] != "ev2_prod1_pr_a" {
		t.Fatalf("items = %#v, want only the pull_request row", packet.Items)
	}
}
