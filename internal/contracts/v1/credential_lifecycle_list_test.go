package v1

import (
	"strings"
	"testing"
)

func TestCredentialLifecycleListValidate(t *testing.T) {
	last := "cred_b"
	good := CredentialLifecycleList{SchemaVersion: CredentialLifecycleListSchema, NextAfter: &last, Items: []CredentialLifecycleItem{
		{CredentialID: "cred_a", Revision: 1, Status: CredentialLifecycleStatusActive},
		{CredentialID: "cred_b", Revision: 3, Status: CredentialLifecycleStatusRevoked},
	}}
	if err := good.Validate(); err != nil {
		t.Fatalf("good page: %v", err)
	}
	empty := CredentialLifecycleList{SchemaVersion: CredentialLifecycleListSchema, Items: []CredentialLifecycleItem{}}
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty last page: %v", err)
	}
	wrongCursor := "cred_a"
	cases := map[string]func(*CredentialLifecycleList){
		"schema":         func(l *CredentialLifecycleList) { l.SchemaVersion = "x" },
		"nil items":      func(l *CredentialLifecycleList) { l.Items = nil },
		"revision zero":  func(l *CredentialLifecycleList) { l.Items[0].Revision = 0 },
		"status":         func(l *CredentialLifecycleList) { l.Items[0].Status = "live" },
		"id alphabet":    func(l *CredentialLifecycleList) { l.Items[0].CredentialID = "cred a" },
		"id too long":    func(l *CredentialLifecycleList) { l.Items[0].CredentialID = strings.Repeat("a", 129) },
		"unordered":      func(l *CredentialLifecycleList) { l.Items[0].CredentialID = "cred_c" },
		"cursor not end": func(l *CredentialLifecycleList) { l.NextAfter = &wrongCursor },
	}
	for name, mutate := range cases {
		page := CredentialLifecycleList{SchemaVersion: good.SchemaVersion, NextAfter: good.NextAfter, Items: append([]CredentialLifecycleItem(nil), good.Items...)}
		mutate(&page)
		if err := page.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
