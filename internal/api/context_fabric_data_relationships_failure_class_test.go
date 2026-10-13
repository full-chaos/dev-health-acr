package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread/gatevocab"
)

func TestRelationshipsInternalFailureClassNamesTheVocabularyRefusal(t *testing.T) {
	proof := fmt.Errorf("%w: %w", directread.ErrRelationshipsInternal, errors.New("consume"))
	vocabulary := fmt.Errorf("%w: %w: %w", directread.ErrRelationshipsInternal, directread.ErrRelationshipsServedVocabulary, errors.New("axis"))
	if got := relationshipsInternalFailureClass(proof); got != gatevocab.RelationshipsFailureProof {
		t.Errorf("proof error class = %q", got)
	}
	if got := relationshipsInternalFailureClass(vocabulary); got != gatevocab.RelationshipsFailureVocabulary {
		t.Errorf("vocabulary error class = %q", got)
	}
}
