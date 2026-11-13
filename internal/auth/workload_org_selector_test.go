package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/authverify"
)

const (
	selectorOrgA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	selectorOrgB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	selectorOrgC = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func selectorResolver(t *testing.T, bindings ...storage.WorkloadBinding) authverify.GrantResolver {
	t.Helper()
	resolver, err := NewGrantResolver(&fakeWorkloadBindingStore{bindings: bindings}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func selectorBinding(id, org string) storage.WorkloadBinding {
	return storage.WorkloadBinding{BindingID: id, OrgID: org, Role: "read", RepositoryScopes: []string{"*"}}
}

func TestWorkloadSelector_singleBindingNoSelectorAnswersAsBefore(t *testing.T) {
	resolver := selectorResolver(t, selectorBinding("wlb_a", selectorOrgA))
	got, err := resolver.Resolve(context.Background(), authverify.SubjectIdentity{})
	if err != nil || got.BindingID != "wlb_a" || got.OrgID != selectorOrgA {
		t.Fatalf("binding = %#v err = %v, want wlb_a", got, err)
	}
}

func TestWorkloadSelector_severalBindingsNoSelectorIsRefused(t *testing.T) {
	resolver := selectorResolver(t, selectorBinding("wlb_a", selectorOrgA), selectorBinding("wlb_b", selectorOrgB))
	got, err := resolver.Resolve(context.Background(), authverify.SubjectIdentity{})
	if !errors.Is(err, ErrWorkloadOrgSelectorRequired) || got.BindingID != "" {
		t.Fatalf("binding = %#v err = %v, want ErrWorkloadOrgSelectorRequired and no binding", got, err)
	}
}

func TestWorkloadSelector_selectorForAnUnboundOrgIsRefusedWithoutFallback(t *testing.T) {
	for name, bindings := range map[string][]storage.WorkloadBinding{
		"one binding":      {selectorBinding("wlb_a", selectorOrgA)},
		"several bindings": {selectorBinding("wlb_a", selectorOrgA), selectorBinding("wlb_b", selectorOrgB)},
	} {
		resolver := selectorResolver(t, bindings...)
		ctx := WithWorkloadOrgSelector(context.Background(), selectorOrgC)
		got, err := resolver.Resolve(ctx, authverify.SubjectIdentity{})
		if !errors.Is(err, ErrWorkloadOrgNotBound) || got.BindingID != "" {
			t.Fatalf("%s: binding = %#v err = %v, want ErrWorkloadOrgNotBound and no binding (no fallback)", name, got, err)
		}
	}
}

func TestWorkloadSelector_selectorForABoundOrgIssuesThatBindingOnly(t *testing.T) {
	resolver := selectorResolver(t, selectorBinding("wlb_a", selectorOrgA), selectorBinding("wlb_b", selectorOrgB))
	for org, want := range map[string]string{selectorOrgA: "wlb_a", selectorOrgB: "wlb_b"} {
		got, err := resolver.Resolve(WithWorkloadOrgSelector(context.Background(), org), authverify.SubjectIdentity{})
		if err != nil || got.BindingID != want || got.OrgID != org {
			t.Fatalf("selector %s: binding = %#v err = %v, want %s", org, got, err, want)
		}
	}
}

func TestWorkloadSelector_disabledBindingsStayInvisible(t *testing.T) {
	disabledAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	disabledB := selectorBinding("wlb_b", selectorOrgB)
	disabledB.DisabledAt = &disabledAt

	// One enabled + one disabled counts as one binding: no selector needed.
	resolver := selectorResolver(t, selectorBinding("wlb_a", selectorOrgA), disabledB)
	got, err := resolver.Resolve(context.Background(), authverify.SubjectIdentity{})
	if err != nil || got.BindingID != "wlb_a" {
		t.Fatalf("binding = %#v err = %v, want wlb_a", got, err)
	}
	// A selector naming the disabled org reads as not bound, never as disabled.
	_, err = resolver.Resolve(WithWorkloadOrgSelector(context.Background(), selectorOrgB), authverify.SubjectIdentity{})
	if !errors.Is(err, ErrWorkloadOrgNotBound) {
		t.Fatalf("err = %v, want ErrWorkloadOrgNotBound for a disabled org", err)
	}
	// Every binding disabled: invalid_grant path, with or without selector.
	allDisabled := selectorResolver(t, disabledB)
	for _, ctx := range []context.Context{context.Background(), WithWorkloadOrgSelector(context.Background(), selectorOrgB)} {
		if _, err := allDisabled.Resolve(ctx, authverify.SubjectIdentity{}); !errors.Is(err, authverify.ErrWorkloadBindingNotFound) {
			t.Fatalf("err = %v, want ErrWorkloadBindingNotFound when every binding is disabled", err)
		}
	}
}
