package devhealthfacts

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type c11165PanicRows struct{ closed *int }

func (r c11165PanicRows) Next() bool        { return true }
func (r c11165PanicRows) Scan(...any) error { panic("scan panic") }
func (r c11165PanicRows) Err() error        { return nil }
func (r c11165PanicRows) Close() error      { *r.closed++; return nil }

type c11165Client struct{ closed *int }

func (c c11165Client) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	return c11165PanicRows{closed: c.closed}, nil
}

func TestCHAOS11165ProjectOwningTeamsClosesRowsWhenScanPanics(t *testing.T) {
	t.Parallel()
	canonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"linear", "p1"}, nil)
	if err != nil || omitted {
		t.Fatalf("project id: %v %v", err, omitted)
	}
	closed := 0
	expander := NewScopeExpander(c11165Client{&closed})
	defer func() {
		if recover() == nil {
			t.Fatal("the scan panic did not reach the test")
		}
		if closed != 1 {
			t.Fatalf("rows closed %d times after a panic in Scan, want 1", closed)
		}
	}()
	_, _, _, _ = expander.projectOwningTeams(context.Background(), storage.Principal{}, "org",
		[]contextfabric.SubjectRef{{Kind: contextfabric.SubjectProject, CanonicalID: canonicalID}}, 10)
}
