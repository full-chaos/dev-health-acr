package main

import (
	"reflect"
	"testing"
)

func TestStringSetMissingAndPresent(t *testing.T) {
	s := newStringSet("acr:v1:commit:a1b2", "acr:v1:ci:run-4821")

	if got := s.missing([]string{"acr:v1:commit:a1b2", "acr:v1:pull-request:1042"}); !reflect.DeepEqual(got, []string{"acr:v1:pull-request:1042"}) {
		t.Fatalf("missing() = %#v", got)
	}
	if got := s.missing([]string{"acr:v1:commit:a1b2", "acr:v1:ci:run-4821"}); got != nil {
		t.Fatalf("missing() = %#v, want nil when everything required is present", got)
	}
	if got := s.present([]string{"acr:v1:commit:a1b2", "acr:v1:other:x"}); !reflect.DeepEqual(got, []string{"acr:v1:commit:a1b2"}) {
		t.Fatalf("present() = %#v", got)
	}
}

func TestStringSetUnion(t *testing.T) {
	a := newStringSet("x", "y")
	b := newStringSet("y", "z")
	got := a.union(b).sorted()
	want := []string{"x", "y", "z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("union() = %#v, want %#v", got, want)
	}
	// a itself must be unmodified by union.
	if got := a.sorted(); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Fatalf("union() mutated its receiver: %#v", got)
	}
}

func TestEqualSortedIgnoresOrderAndDuplicates(t *testing.T) {
	if !equalSorted([]string{"b", "a", "a"}, []string{"a", "b"}) {
		t.Fatal("equalSorted should ignore order and duplicates")
	}
	if equalSorted([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("equalSorted should distinguish differing sets")
	}
}
