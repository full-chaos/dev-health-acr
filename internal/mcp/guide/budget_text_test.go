package guide

import (
	"strings"
	"testing"
)

func TestTheDataGuideSaysHowAMultiListOperationIsCut(t *testing.T) {
	text, err := Text(URIData)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"`primary_list`", "carried whole", "named in `page.cut`"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the data guide lacks %q", want)
		}
	}
}
