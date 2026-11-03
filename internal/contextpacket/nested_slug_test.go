package contextpacket

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/reposlug/cells"
)

func TestPacketOutputSlugGuardHoldsTheCellTable(t *testing.T) {
	for _, cell := range cells.Table() {
		if got := validSlug(cell.Slug); got != cell.Valid {
			t.Errorf("%s: validSlug(%q) = %v, want %v", cell.Name, cell.Slug, got, cell.Valid)
		}
	}
}
