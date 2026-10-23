package contextfabric

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type fanoutProvider struct {
	capability FactCapability
	behave     func(context.Context, FactQuery) (FactProviderResult, error)
}

func (p fanoutProvider) Capability() FactCapability { return p.capability }

func (p fanoutProvider) ReadFacts(ctx context.Context, _ storage.Principal, query FactQuery) (FactProviderResult, error) {
	return p.behave(ctx, query)
}

var fanoutKinds = []FactKind{FactStatus, FactWork, FactBlockers, FactPullRequests, FactReviews}

var fanoutProject = SubjectRef{Kind: SubjectProject, CanonicalID: "project_fanout", Label: "Fanout"}

func fanoutCapability(kind FactKind) FactCapability {
	return FactCapability{Kind: kind, Name: "fanout-" + string(kind), Version: "v1", SupportedSubjectKinds: []SubjectKind{SubjectProject}, Dimension: HealthDimensionExecutionCompletion, SubjectRoles: []FactRole{FactRoleSubject}}
}

func fanoutAvailable(kind FactKind) FactProviderResult {
	return FactProviderResult{State: SourceAvailable, Version: "v1", Facts: []CanonicalFact{{
		Kind: kind, Subject: fanoutProject, Fields: map[string]FactValue{"status": StringFactValue(string(kind))}, SourceState: SourceAvailable,
	}}}
}

func fanoutRegistry(t *testing.T, fanout int, behave func(index int, kind FactKind) func(context.Context, FactQuery) (FactProviderResult, error)) *FactCapabilityRegistry {
	t.Helper()
	providers := make([]FactProvider, 0, len(fanoutKinds))
	for index, kind := range fanoutKinds {
		providers = append(providers, fanoutProvider{capability: fanoutCapability(kind), behave: behave(index, kind)})
	}
	registry, err := NewFactCapabilityRegistry(providers, FactRegistryOptions{ReadFanout: fanout})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestFactReadFanoutRunsProvidersTogetherUpToTheNamedBound(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		fanout     int
		rendezvous int
		wantPeak   int32
	}{
		{name: "serial", fanout: 1, rendezvous: 0, wantPeak: 1},
		{name: "bounded", fanout: 100, rendezvous: FactReadFanout, wantPeak: FactReadFanout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var inflight, peak atomic.Int32
			var met atomic.Bool
			registry := fanoutRegistry(t, tc.fanout, func(_ int, kind FactKind) func(context.Context, FactQuery) (FactProviderResult, error) {
				return func(ctx context.Context, _ FactQuery) (FactProviderResult, error) {
					now := inflight.Add(1)
					defer inflight.Add(-1)
					for {
						old := peak.Load()
						if now <= old || peak.CompareAndSwap(old, now) {
							break
						}
					}
					deadline := time.Now().Add(5 * time.Second)
					for tc.rendezvous > 0 && inflight.Load() < int32(tc.rendezvous) && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if tc.rendezvous > 0 && inflight.Load() >= int32(tc.rendezvous) {
						met.Store(true)
					}
					time.Sleep(20 * time.Millisecond)
					return fanoutAvailable(kind), nil
				}
			})
			if _, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(fanoutProject, fanoutKinds...)); err != nil {
				t.Fatal(err)
			}
			if tc.rendezvous > 0 && !met.Load() {
				t.Fatalf("providers never ran %d at once", tc.rendezvous)
			}
			if got := peak.Load(); got != tc.wantPeak {
				t.Fatalf("peak concurrent provider reads = %d, want %d", got, tc.wantPeak)
			}
		})
	}
}

func TestFactReadFanoutServesTheSameBundleAsSerialWhateverOrderReadsFinish(t *testing.T) {
	t.Parallel()
	serve := func(fanout int) []byte {
		registry := fanoutRegistry(t, fanout, func(index int, kind FactKind) func(context.Context, FactQuery) (FactProviderResult, error) {
			return func(context.Context, FactQuery) (FactProviderResult, error) {
				time.Sleep(time.Duration(len(fanoutKinds)-index) * 15 * time.Millisecond)
				switch index {
				case 1:
					return FactProviderResult{}, errors.New("provider failed")
				case 3:
					return FactProviderResult{State: SourceNoData, Reason: "no rows", Version: "v1"}, nil
				}
				return fanoutAvailable(kind), nil
			}
		})
		bundle, err := registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(fanoutProject, fanoutKinds...))
		if err != nil {
			t.Fatal(err)
		}
		if got := bundle.Outcomes[FactWork].Branch; got != string(factReadFailed) {
			t.Fatalf("a failing provider read was not recorded as failed: branch = %q", got)
		}
		encoded, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	serial, parallel := serve(1), serve(FactReadFanout)
	if string(serial) != string(parallel) {
		t.Fatalf("bundle differs between fan-out 1 and %d\nserial:   %s\nparallel: %s", FactReadFanout, serial, parallel)
	}
}

func TestFactReadFanoutCancelServesTheSerialErrorAndCancelsEveryRead(t *testing.T) {
	t.Parallel()
	run := func(fanout int) (CanonicalFactBundle, error, int32) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var cancelled atomic.Int32
		var started sync.WaitGroup
		started.Add(len(fanoutKinds) - 1)
		registry := fanoutRegistry(t, fanout, func(index int, kind FactKind) func(context.Context, FactQuery) (FactProviderResult, error) {
			return func(readCtx context.Context, _ FactQuery) (FactProviderResult, error) {
				if index == 0 {
					return fanoutAvailable(kind), nil
				}
				if fanout > 1 {
					started.Done()
				}
				if index == 1 {
					if fanout > 1 {
						started.Wait()
					}
					cancel()
				}
				<-readCtx.Done()
				cancelled.Add(1)
				return FactProviderResult{}, readCtx.Err()
			}
		})
		bundle, err := registry.ReadFacts(ctx, storage.Principal{OrgID: "org_1"}, canonicalFactRequest(fanoutProject, fanoutKinds...))
		return bundle, err, cancelled.Load()
	}
	serialBundle, serialErr, _ := run(1)
	parallelBundle, parallelErr, cancelled := run(FactReadFanout)
	if !errors.Is(serialErr, context.Canceled) || !errors.Is(parallelErr, context.Canceled) {
		t.Fatalf("errors = %v / %v, want context.Canceled from both", serialErr, parallelErr)
	}
	if parallelBundle.Scope == nil {
		t.Fatal("cancelled read dropped the resolved scope")
	}
	serialJSON, _ := json.Marshal(serialBundle)
	parallelJSON, _ := json.Marshal(parallelBundle)
	if string(serialJSON) != string(parallelJSON) {
		t.Fatalf("cancelled bundle differs\nserial:   %s\nparallel: %s", serialJSON, parallelJSON)
	}
	if want := int32(len(fanoutKinds) - 1); cancelled != want {
		t.Fatalf("reads that saw the cancel = %d, want %d", cancelled, want)
	}
}

func TestFactReadFanoutRaisesAProviderPanicInTheCallerAndCancelsTheRest(t *testing.T) {
	t.Parallel()
	var sawCancel atomic.Int32
	registry := fanoutRegistry(t, FactReadFanout, func(index int, kind FactKind) func(context.Context, FactQuery) (FactProviderResult, error) {
		return func(ctx context.Context, _ FactQuery) (FactProviderResult, error) {
			if index == 2 {
				panic("provider exploded")
			}
			select {
			case <-ctx.Done():
				sawCancel.Add(1)
				return FactProviderResult{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return fanoutAvailable(kind), nil
			}
		}
	})
	defer func() {
		if recovered := recover(); recovered != "provider exploded" {
			t.Fatalf("recovered = %v, want the provider's panic in the caller's goroutine", recovered)
		}
		if sawCancel.Load() == 0 {
			t.Fatal("no sibling read saw the cancel")
		}
	}()
	_, _ = registry.ReadFacts(context.Background(), storage.Principal{OrgID: "org_1"}, canonicalFactRequest(fanoutProject, fanoutKinds...))
	t.Fatal("ReadFacts returned after a provider panic")
}
