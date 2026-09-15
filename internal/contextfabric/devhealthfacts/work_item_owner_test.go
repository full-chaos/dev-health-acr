package devhealthfacts

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type panicMembershipClient struct{ query func() }

func (c panicMembershipClient) Query(context.Context, string, []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.query()
	panic("S1 injected panic")
}

func TestWorkItemMembershipRegistersBeforeS1Panic(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name := "raw"
		if owned {
			name = "response_owner"
		}
		t.Run(name, func(t *testing.T) {
			gate, err := contextfabric.NewWorkItemMembershipGate(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			var owner *contextfabric.WorkItemResponseOwner
			if owned {
				ctx, owner = contextfabric.NewWorkItemResponseOwnerContext(ctx)
				defer owner.Complete()
			}
			reader, err := NewWorkItemMembershipReader(panicMembershipClient{query: func() {
				if gate.Stats().InFlight != 1 {
					t.Fatal("S1 ran without admission")
				}
			}}, WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Error("panic was not exercised")
					}
				}()
				_, _, _ = reader.BeginWorkItemMembership(ctx, storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{Anchor: workItemMembershipTestAnchor(t, "linear", "P1")})
			}()
			if owned {
				if gate.Stats().InFlight != 1 {
					t.Fatal("panic released before response completion")
				}
				owner.Complete()
			}
			if gate.Stats().InFlight != 0 {
				t.Fatal("panic leaked permit")
			}
		})
	}
}

func TestWorkItemMembershipClosedOwnerPreventsS1(t *testing.T) {
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	client := &workItemMembershipFakeClient{scanErrAt: -1}
	reader, err := NewWorkItemMembershipReader(client, WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, owner := contextfabric.NewWorkItemResponseOwnerContext(context.Background())
	owner.Complete()
	lease, _, err := reader.BeginWorkItemMembership(ctx, storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{Anchor: workItemMembershipTestAnchor(t, "linear", "P1")})
	if err == nil || lease != nil || len(client.queries) != 0 || gate.Stats().InFlight != 0 {
		t.Fatalf("closed owner ran/leaked S1: error=%v lease=%v queries=%d occupied=%d", err, lease, len(client.queries), gate.Stats().InFlight)
	}
}

type panicMembershipTelemetry struct {
	contextfabric.NoopWorkItemMembershipTelemetry
}

func (panicMembershipTelemetry) RecordWorkItemMembershipS1(context.Context, storage.Principal, contextfabric.WorkItemMembershipS1Event) {
	panic("injected telemetry panic")
}

func TestWorkItemMembershipRegistersBeforeOtherPanics(t *testing.T) {
	for _, phase := range []string{"clock", "telemetry"} {
		for _, owned := range []bool{false, true} {
			name := phase + "/raw"
			if owned {
				name = phase + "/response_owner"
			}
			t.Run(name, func(t *testing.T) {
				gate, err := contextfabric.NewWorkItemMembershipGate(1, 0)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				var owner *contextfabric.WorkItemResponseOwner
				if owned {
					ctx, owner = contextfabric.NewWorkItemResponseOwnerContext(ctx)
					defer owner.Complete()
				}
				client := &workItemMembershipFakeClient{scanErrAt: -1}
				options := WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}}
				if phase == "clock" {
					options.Now = func() time.Time { panic("injected clock panic") }
				} else {
					options.Telemetry = panicMembershipTelemetry{}
				}
				reader, err := NewWorkItemMembershipReader(client, options)
				if err != nil {
					t.Fatal(err)
				}
				func() {
					defer func() {
						if got := recover(); got != "injected "+phase+" panic" {
							t.Errorf("panic=%v, intended phase not measured", got)
						}
					}()
					_, _, _ = reader.BeginWorkItemMembership(ctx, storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{Anchor: workItemMembershipTestAnchor(t, "linear", "P1")})
				}()
				wantQueries := 0
				if phase == "telemetry" {
					wantQueries = 1
				}
				if len(client.queries) != wantQueries {
					t.Fatalf("queries=%d want=%d", len(client.queries), wantQueries)
				}
				if owned {
					if gate.Stats().InFlight != 1 {
						t.Fatal("panic released before response completion")
					}
					owner.Complete()
				}
				if gate.Stats().InFlight != 0 {
					t.Fatal("panic leaked permit")
				}
			})
		}
	}
}

func TestWorkItemMembershipTooShortDeadlineClearsOwnerSlot(t *testing.T) {
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	client := &workItemMembershipFakeClient{scanErrAt: -1}
	reader, err := NewWorkItemMembershipReader(client, WorkItemMembershipReaderOptions{Gate: gate, Telemetry: contextfabric.NoopWorkItemMembershipTelemetry{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	ctx, owner := contextfabric.NewWorkItemResponseOwnerContext(ctx)
	defer owner.Complete()
	lease, _, err := reader.BeginWorkItemMembership(ctx, storage.Principal{OrgID: workItemMembershipTestOrg}, contextfabric.WorkItemMembershipRequest{Anchor: workItemMembershipTestAnchor(t, "linear", "P1")})
	if err == nil || lease != nil || len(client.queries) != 0 || gate.Stats().InFlight != 0 {
		t.Fatalf("deadline refusal: err=%v lease=%v queries=%d occupied=%d", err, lease, len(client.queries), gate.Stats().InFlight)
	}
	fresh, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Retain(fresh); err != nil {
		t.Fatalf("refusal left owner slot occupied: %v", err)
	}
	owner.Complete()
	if gate.Stats().InFlight != 0 {
		t.Fatal("fresh completion leaked permit")
	}
}
