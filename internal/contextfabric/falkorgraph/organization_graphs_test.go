package falkorgraph

import (
	"reflect"
	"testing"
)

func TestMatchOrganizationGraphKeysTakesEveryEpochAndNothingElse(t *testing.T) {
	base := graphKey("acr", "org-a")
	other := graphKey("acr", "org-b")
	names := []string{
		base, base + "-e1", base + "-e2", base + "-e7", base + "-e12",
		base + "-e", base + "-ex", base + "-e1x", base + "-e-1", base + "x",
		other, other + "-e1", "unrelated",
	}
	want := []string{base, base + "-e1", base + "-e2", base + "-e7", base + "-e12"}
	if got := matchOrganizationGraphKeys(names, base); !reflect.DeepEqual(got, want) {
		t.Fatalf("matchOrganizationGraphKeys() = %v, want %v", got, want)
	}
}
