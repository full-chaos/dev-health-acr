package devhealthfacts

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

func TestWorkItemRepositoryAuthorizationPreservesACRRequestSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		principalScopes []string
		requested       []string
		wantGranted     readers.RepositorySelectorSet
		wantRequested   *readers.RepositorySelectorSet
	}{
		{
			name:            "empty principal and absent request are organization wide",
			principalScopes: nil,
			requested:       nil,
			wantGranted:     readers.RepositorySelectorSet{All: true},
		},
		{
			name:            "empty principal and empty request stay unconstrained",
			principalScopes: []string{},
			requested:       []string{},
			wantGranted:     readers.RepositorySelectorSet{All: true},
		},
		{
			name:            "principal exact and owner selectors are normalized",
			principalScopes: []string{" ACME/TOOLS ", "Acme/*", "acme/tools", "bad/no/slash"},
			requested:       []string{" ACME/TOOLS ", "Acme/*", "not a selector"},
			wantGranted: readers.RepositorySelectorSet{
				ExactSlugs: []string{"acme/tools"},
				Owners:     []string{"acme"},
			},
			wantRequested: &readers.RepositorySelectorSet{
				ExactSlugs: []string{"acme/tools"},
				Owners:     []string{"acme"},
			},
		},
		{
			name:            "principal global wildcard is organization wide",
			principalScopes: []string{"acme/tools", "*"},
			requested:       nil,
			wantGranted:     readers.RepositorySelectorSet{All: true},
		},
		{
			name:            "requested global wildcard stays an explicit selector",
			principalScopes: []string{"acme/tools"},
			requested:       []string{"*"},
			wantGranted:     readers.RepositorySelectorSet{ExactSlugs: []string{"acme/tools"}},
			wantRequested:   &readers.RepositorySelectorSet{All: true},
		},
		{
			name:            "invalid nonempty requested list denies",
			principalScopes: []string{"acme/tools"},
			requested:       []string{"not a selector", "acme/"},
			wantGranted:     readers.RepositorySelectorSet{ExactSlugs: []string{"acme/tools"}},
			wantRequested:   &readers.RepositorySelectorSet{},
		},
		{
			name:            "invalid nonempty grant list fails closed",
			principalScopes: []string{"not a selector"},
			requested:       nil,
			wantGranted:     readers.RepositorySelectorSet{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := workItemRepositoryAuthorization(storage.Principal{RepositoryScopes: tt.principalScopes}, tt.requested)
			if got.RepositorySelectors == nil {
				t.Fatal("RepositorySelectors = nil, want the typed selector mode")
			}
			if !reflect.DeepEqual(got.RepositorySelectors.Granted, tt.wantGranted) {
				t.Fatalf("Granted = %#v, want %#v", got.RepositorySelectors.Granted, tt.wantGranted)
			}
			if !reflect.DeepEqual(got.RepositorySelectors.Requested, tt.wantRequested) {
				t.Fatalf("Requested = %#v, want %#v", got.RepositorySelectors.Requested, tt.wantRequested)
			}
		})
	}
}

func TestWorkItemRepositoryAuthorizationAgreesWithRepositoryScopeMatchers(t *testing.T) {
	t.Parallel()
	grants := []string{"ACME/tools", "other/*"}
	requested := []string{"ACME/tools", "other/*"}
	scope := workItemRepositoryAuthorization(storage.Principal{RepositoryScopes: grants}, requested)
	selectors := scope.RepositorySelectors
	if selectors == nil || selectors.Requested == nil {
		t.Fatal("selector scope is incomplete")
	}

	for _, slug := range []string{"acme/tools", "ACME/TOOLS", "other/service", "elsewhere/service"} {
		wantGrant := auth.RepositoryAllowed(grants, slug)
		if got := selectorSetMatchesRepository(selectors.Granted, slug); got != wantGrant {
			t.Errorf("grant selector match for %q = %v, want auth.RepositoryAllowed = %v", slug, got, wantGrant)
		}
		wantRequested := graphrank.ScopeMatch([]string{slug}, requested[0], graphrank.ScopeValueRepositoryName) ||
			graphrank.ScopeMatch([]string{slug}, requested[1], graphrank.ScopeValueRepositoryName)
		if got := selectorSetMatchesRepository(*selectors.Requested, slug); got != wantRequested {
			t.Errorf("requested selector match for %q = %v, want graphrank.ScopeMatch = %v", slug, got, wantRequested)
		}
	}
}

func selectorSetMatchesRepository(set readers.RepositorySelectorSet, slug string) bool {
	if set.All {
		return true
	}
	normalized, err := auth.NormalizeRepositorySlug(slug)
	if err != nil {
		return false
	}
	owner, _, _ := strings.Cut(normalized, "/")
	for _, exact := range set.ExactSlugs {
		if normalized == exact {
			return true
		}
	}
	for _, allowedOwner := range set.Owners {
		if owner == allowedOwner {
			return true
		}
	}
	return false
}

func TestWorkItemReaderSettingsUseThrowingCeilingsAndRespectDeadline(t *testing.T) {
	t.Parallel()
	background := workItemReaderSettings(context.Background())
	if background.MaxExecutionTimeSeconds != uint64(defaultTimeout/time.Second) {
		t.Fatalf("background MaxExecutionTimeSeconds = %d, want %d", background.MaxExecutionTimeSeconds, uint64(defaultTimeout/time.Second))
	}
	if background.MaxRowsToRead != workItemReaderMaxRowsToRead || background.MaxMemoryUsage != workItemReaderMaxMemoryUsage || background.MaxResultRows != uint64(maxFactRowsProbe) {
		t.Fatalf("background settings = %#v, want fixed row/memory/result ceilings", background)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(1500*time.Millisecond))
	defer cancel()
	bounded := workItemReaderSettings(ctx)
	if bounded.MaxExecutionTimeSeconds > 1 {
		t.Fatalf("bounded MaxExecutionTimeSeconds = %d, want no more than the remaining whole seconds", bounded.MaxExecutionTimeSeconds)
	}
	longCtx, longCancel := context.WithDeadline(context.Background(), time.Now().Add(3*time.Second))
	defer longCancel()
	long := workItemReaderSettings(longCtx)
	if long.MaxExecutionTimeSeconds == 0 || long.MaxExecutionTimeSeconds > 3 {
		t.Fatalf("long bounded MaxExecutionTimeSeconds = %d, want a positive value no more than the deadline", long.MaxExecutionTimeSeconds)
	}
	shortCtx, shortCancel := context.WithDeadline(context.Background(), time.Now().Add(750*time.Millisecond))
	defer shortCancel()
	short := workItemReaderSettings(shortCtx)
	if short.MaxExecutionTimeSeconds != 0 {
		t.Fatalf("sub-second MaxExecutionTimeSeconds = %d, want 0 because the context carries the remaining deadline", short.MaxExecutionTimeSeconds)
	}
	rendered := bounded.Render()
	if !strings.Contains(rendered, "timeout_overflow_mode = 'throw'") ||
		!strings.Contains(rendered, "read_overflow_mode = 'throw'") ||
		!strings.Contains(rendered, "result_overflow_mode = 'throw'") {
		t.Fatalf("bounded settings = %q, want every configured overflow mode to throw", rendered)
	}
}
