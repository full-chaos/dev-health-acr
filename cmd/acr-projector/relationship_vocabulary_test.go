package main

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestEveryRecognizedRelationshipTypeHasAProducer is AC-3779-9's first
// direction (CHAOS-3779, closing drift item D12 permanently): every type
// graphrank.RecognizedRelationshipTypes lists as driver-admission-worthy
// must actually be written by a real projection producer. Before
// CHAOS-3779, graphrank recognized nine types but only BLOCKS had a
// producer -- the other eight were dead code, silently falling to the
// default context standing forever because nothing ever wrote them. A
// recognizer entry with no producer is a defect, not a placeholder.
//
// cmd/acr-projector is the composition root that already wires
// devhealthsource, falkorgraph, and graphrank together (see runtime.go),
// so this is the natural, cycle-free place for the cross-package check:
// none of those three packages may import each other for this purpose
// without creating an import cycle (devhealthsource and falkorgraph both
// depend on contextfabric; graphrank does too), but this package already
// depends on all three.
func TestEveryRecognizedRelationshipTypeHasAProducer(t *testing.T) {
	produced := producedRelationshipTypeSet()
	for _, recognized := range graphrank.RecognizedRelationshipTypes() {
		if !produced[contractsv1.ContextFabricRelationshipType(recognized)] {
			t.Fatalf("graphrank recognizes relationship type %q for driver admission, but no projection producer writes it -- a recognizer entry with no producer is a defect, not a placeholder (TRD §19.5.5/AC-3779-9)", recognized)
		}
	}
}

// TestEveryProducedRelationshipTypeIsAClosedVocabularyMember is AC-3779-9's
// second, mirrored direction (TRD §19.13 Correction 1: "AC-3779-9 gains a
// mirror requirement: every produced type must be a member of the closed
// vocabulary, not only every recognized type must have a producer"). A
// producer that emits a type outside ContextFabricRelationshipType's
// closed enum would fail loudly at Validate() time in production -- this
// test catches that at build/test time instead, for every type either
// producer package claims to emit.
func TestEveryProducedRelationshipTypeIsAClosedVocabularyMember(t *testing.T) {
	for produced := range producedRelationshipTypeSet() {
		if !contractsv1.ValidContextFabricRelationshipType(produced) {
			t.Fatalf("relationship type %q is claimed as produced but is not a member of the closed ContextFabricRelationshipType vocabulary", produced)
		}
	}
}

// producedRelationshipTypeSet unions devhealthsource's and falkorgraph's
// own ProducedRelationshipTypes -- the two packages that between them
// write every relationship type in this deployment (devhealthsource
// through ContextFabricRelationshipProjection.Type; falkorgraph through
// the DOCUMENTED_BY/HAS_EPISODE edge properties it synthesizes directly
// from content/episode projections).
func producedRelationshipTypeSet() map[contractsv1.ContextFabricRelationshipType]bool {
	produced := make(map[contractsv1.ContextFabricRelationshipType]bool)
	for _, t := range devhealthsource.ProducedRelationshipTypes() {
		produced[t] = true
	}
	for _, t := range falkorgraph.ProducedRelationshipTypes() {
		produced[contractsv1.ContextFabricRelationshipType(t)] = true
	}
	return produced
}
