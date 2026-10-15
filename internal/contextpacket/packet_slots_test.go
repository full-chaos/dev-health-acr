package contextpacket_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func slotsEvidence() []contractsv1.EvidenceRef {
	observed := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	path := "saved-payment-methods-at-checkout.go"
	var evidence []contractsv1.EvidenceRef
	for _, kind := range []string{"file_hotspot", "file_complexity", "commit_file"} {
		ref := testEvidence("ev-"+kind, "dev_health", observed)
		ref.Source.EntityType, ref.Source.EntityID, ref.Source.DisplayLabel = kind, path, path
		ref.Metadata = map[string]any{"focus_key": "file:repo:" + path}
		evidence = append(evidence, ref)
	}
	for i := 0; i < 8; i++ {
		ref := testEvidence(fmt.Sprintf("ev-commit-%d", i), "git", observed.Add(-time.Duration(i+1)*time.Hour))
		evidence = append(evidence, ref)
	}
	pressure := testEvidence("ev-pressure", "ci", observed.Add(-72*time.Hour))
	state := testEvidence("ev-state", "dev_health", observed.Add(-72*time.Hour))
	state.Source.EntityType = "deployment"
	state.Citation = "status=success, deployment_id=d1"
	action := testEvidence("ev-action", "dev_health", observed.Add(-72*time.Hour))
	action.Source.EntityType, action.SourceVersion = "recommendation", "operational_deficiencies.v1"
	return append(evidence, pressure, state, action)
}

func TestAssembler_one_subject_does_not_take_the_slots_other_categories_need(t *testing.T) {
	request := fixtureRequest("req-slots-subject", "main", "")
	request.Goal = "saved payment methods checkout"
	request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = 12, 8000, 200000
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      slotsEvidence(), QueryVersion: contextpacket.QueryVersionV1,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	categories, files := map[contractsv1.PacketCategory]int{}, 0
	for _, item := range packet.Items {
		categories[item.Category]++
		if item.RelatedEntities[0].Label == "saved-payment-methods-at-checkout.go" {
			files++
		}
	}
	for _, category := range []contractsv1.PacketCategory{contractsv1.CategoryState, contractsv1.CategoryPressure, contractsv1.CategoryAction} {
		if categories[category] == 0 {
			t.Errorf("category %s has no item", category)
		}
	}
	if files > 1 {
		t.Errorf("one file took %d slots", files)
	}
}

func TestAssembler_second_row_of_a_subject_takes_only_a_slot_nothing_else_wants(t *testing.T) {
	request := fixtureRequest("req-slots-spill", "main", "")
	request.Goal = "saved payment methods checkout"
	request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = 4, 8000, 200000
	var evidence []contractsv1.EvidenceRef
	for _, ref := range slotsEvidence() {
		switch ref.EvidenceRefID {
		case "ev-file_hotspot", "ev-file_complexity", "ev-commit_file", "ev-state", "ev-pressure":
			evidence = append(evidence, ref)
		}
	}
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      evidence, QueryVersion: contextpacket.QueryVersionV1,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	categories, files := map[contractsv1.PacketCategory]int{}, 0
	for _, item := range packet.Items {
		categories[item.Category]++
		if item.RelatedEntities[0].Label == "saved-payment-methods-at-checkout.go" {
			files++
		}
	}
	if len(packet.Items) != 4 || files != 2 || categories[contractsv1.CategoryState] != 1 || categories[contractsv1.CategoryPressure] != 1 {
		t.Fatalf("items=%d files=%d categories=%v, want 4 slots: state, pressure and 2 file rows", len(packet.Items), files, categories)
	}
}

func TestAssembler_subject_cap_never_starves_a_category_quota(t *testing.T) {
	request := fixtureRequest("req-slots-quota", "main", "")
	request.Goal = "saved payment methods checkout"
	request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = 10, 8000, 200000
	observed := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	var evidence []contractsv1.EvidenceRef
	for _, ref := range slotsEvidence() {
		if ref.Metadata != nil {
			evidence = append(evidence, ref)
		}
	}
	for i := 0; i < 4; i++ {
		state := testEvidence(fmt.Sprintf("ev-state-%d", i), "dev_health", observed.Add(-time.Duration(i+1)*time.Hour))
		state.Source.EntityType, state.Citation = "deployment", "status=success, deployment_id=d1"
		pressure := testEvidence(fmt.Sprintf("ev-pressure-%d", i), "ci", observed.Add(-time.Duration(i+1)*time.Hour))
		action := testEvidence(fmt.Sprintf("ev-action-%d", i), "dev_health", observed.Add(-time.Duration(i+1)*time.Hour))
		action.Source.EntityType, action.SourceVersion = "recommendation", "operational_deficiencies.v1"
		evidence = append(evidence, state, pressure, action)
	}
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      evidence, QueryVersion: contextpacket.QueryVersionV1,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	evidenceItems := 0
	for _, item := range packet.Items {
		if item.Category == contractsv1.CategoryEvidence {
			evidenceItems++
		}
	}
	if evidenceItems != 2 {
		t.Fatalf("evidence items = %d (%v), want its quota of 2 filled by the second row of the file", evidenceItems, itemEvidenceIDs(packet))
	}
}

func TestAssembler_rows_without_a_focus_key_are_allocated_as_before(t *testing.T) {
	request := fixtureRequest("req-slots-nofocus", "main", "")
	request.Goal = "saved payment methods checkout"
	request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = 12, 8000, 200000
	evidence := slotsEvidence()
	for i := range evidence {
		evidence[i].Metadata = nil
	}
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      evidence, QueryVersion: contextpacket.QueryVersionV1,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	// The served ids and their order on main (ranker.v6) for the same input.
	want := []string{"ev-commit_file", "ev-file_complexity", "ev-file_hotspot", "ev-commit-0", "ev-commit-1", "ev-commit-2", "ev-commit-3", "ev-commit-4", "ev-commit-5", "ev-state", "ev-pressure", "ev-action"}
	if got := itemEvidenceIDs(packet); !slices.Equal(got, want) {
		t.Fatalf("served = %v, want the main order %v: rows without a focus key keep their old allocation", got, want)
	}
}

func TestAssembler_output_budget_keeps_one_slot_per_subject_before_a_second(t *testing.T) {
	request := fixtureRequest("req-slots-budget", "main", "")
	request.Goal = "saved payment methods checkout"
	request.Options.MaxItems, request.Options.MaxOutputTokens, request.Options.MaxSerializedBytes = 50, 500, 200000
	observed := time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC)
	path := "saved-payment-methods-at-checkout.go"
	var evidence []contractsv1.EvidenceRef
	for i := 0; i < 30; i++ {
		ref := testEvidence(fmt.Sprintf("ev-file-%02d", i), "dev_health", observed.Add(-time.Duration(i)*time.Minute))
		ref.Source.EntityType, ref.Source.DisplayLabel = "commit_file", path
		ref.Metadata = map[string]any{"focus_key": "file:repo:" + path}
		evidence = append(evidence, ref)
	}
	for i := 0; i < 5; i++ {
		ref := testEvidence(fmt.Sprintf("ev-other-%d", i), "git", observed.Add(-48*time.Hour))
		evidence = append(evidence, ref)
	}
	store := testStore{bundle: storage.EvidenceBundle{
		ResolvedScope: contractsv1.ResolvedScope{RepoID: "repo", RepoSlug: request.Repository.Slug, Resolution: contractsv1.ScopeBranchFiltered},
		Evidence:      evidence, QueryVersion: contextpacket.QueryVersionV1,
	}}
	packet, err := contextpacket.NewAssembler(store, fixedOptions()).Assemble(context.Background(), fixturePrincipal(), request)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	others := 0
	for _, id := range itemEvidenceIDs(packet) {
		if strings.HasPrefix(id, "ev-other-") {
			others++
		}
	}
	t.Logf("items=%d others=%d", len(packet.Items), others)
	if others != 5 {
		t.Fatalf("other subjects served = %d of 5; the token budget let one file's rows crowd them out (%v)", others, itemEvidenceIDs(packet))
	}
}
