package contextfabric

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func TestWorkItemEngineOwnsOnlyItsFallback(t *testing.T) {
	for _, borrowed := range []bool{false, true} {
		for _, panics := range []bool{false, true} {
			name := "fallback"
			if borrowed {
				name = "borrowed"
			}
			if panics {
				name += "_panic"
			} else {
				name += "_error"
			}
			t.Run(name, func(t *testing.T) {
				gate, err := NewWorkItemMembershipGate(1, 0)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				var outer *WorkItemResponseOwner
				if borrowed {
					ctx, outer = NewWorkItemResponseOwnerContext(ctx)
					defer outer.Complete()
				}
				called := false
				engine := mustReuseTestEngine(t, EngineDependencies{Interpreter: interpreterFunc(func(c context.Context, _ storage.Principal, _ InvestigationRequest) (InterpretedQuestion, error) {
					called = true
					owner, ok := WorkItemResponseOwnerFromContext(c)
					if !ok {
						t.Fatal("Engine supplied no response owner")
					}
					if borrowed && owner != outer {
						t.Fatal("Engine replaced caller owner")
					}
					lease, err := gate.Acquire(c)
					if err != nil {
						t.Fatal(err)
					}
					if err := owner.Retain(lease); err != nil {
						t.Fatal(err)
					}
					if panics {
						panic("injected owner scope panic")
					}
					return InterpretedQuestion{}, errors.New("injected owner scope error")
				})})
				func() {
					defer func() {
						value := recover()
						if (value != nil) != panics {
							t.Errorf("panic=%v want=%t", value, panics)
						}
					}()
					_, err := engine.Investigate(ctx, reusePrincipal(), validInvestigationRequest())
					if !panics && err == nil {
						t.Error("expected error")
					}
				}()
				if !called {
					t.Fatal("owner scope never exercised")
				}
				want := 0
				if borrowed {
					want = 1
				}
				if gate.Stats().InFlight != want {
					t.Fatalf("after Engine exit occupancy=%d want=%d", gate.Stats().InFlight, want)
				}
				if borrowed {
					outer.Complete()
				}
				if gate.Stats().InFlight != 0 {
					t.Fatal("completion leaked permit")
				}
			})
		}
	}
}
