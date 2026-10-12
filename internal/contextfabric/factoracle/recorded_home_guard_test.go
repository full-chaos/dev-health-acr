package factoracle

import (
	"strings"
	"testing"
)

func TestTheCaptureHoldsNoHomeCaseWhileItsContractIsAdvanced(t *testing.T) {
	manifest, _, _ := loadedCapture(t)
	for _, c := range manifest.ShapeCases {
		if strings.SplitN(c.ShapeID, "/", 2)[0] == "home" {
			t.Fatalf("the capture records a home case (%s) while contractsAtCapture[home] is advanced past it: retake the capture on the venue and restore the captured digest", c.ShapeID)
		}
	}
	if len(manifest.ShapeCases) == 0 {
		t.Fatal("the capture holds no shape cases: the guard measured nothing")
	}
}
