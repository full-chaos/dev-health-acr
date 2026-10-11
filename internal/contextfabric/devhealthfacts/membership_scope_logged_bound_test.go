package devhealthfacts

import (
	"fmt"
	"testing"
)

func TestColdLoadReportSetIsBounded(t *testing.T) {
	cache := newMembershipScopeCache()
	for i := 0; i < membershipScopeMaxLogged*3; i++ {
		cache.firstLoad(fmt.Sprintf("org-%d", i))
	}
	if got := len(cache.logged); got > membershipScopeMaxLogged {
		t.Fatalf("logged set = %d, want <= %d", got, membershipScopeMaxLogged)
	}
	if !cache.firstLoad("org-last-new") {
		t.Fatal("a new key must report: measurement did not happen")
	}
}
