package devhealthfacts

import (
	"os"
	"testing"
)

func TestPlantedRaceFailsOnFirstAttempt(t *testing.T) {
	if plantedRaceEnabled && os.Getenv("GITHUB_RUN_ATTEMPT") == "1" {
		t.Fatal("planted: the race build fails on attempt 1")
	}
}
