//go:build race

package limits

import "testing"

func TestPlantedRaceOnlyBreak(t *testing.T) {
	t.Fatal("planted break: fails only under -race")
}
