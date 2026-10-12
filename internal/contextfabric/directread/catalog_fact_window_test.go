package directread

import (
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestCatalogFactKindMaxWindowEqualsWhatReadFactsEnforces(t *testing.T) {
	capabilities := productionCapabilities()
	byKind := map[contextfabric.FactKind]contextfabric.FactCapability{}
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	catalog := factsCatalogFor(ClassUnrestricted, capabilities, true)
	if catalog.Facts == nil || len(catalog.Facts.Kinds) == 0 {
		t.Fatal("catalog lists no fact kind: the test measured nothing")
	}
	reader := &FactsReader{now: func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }}
	for _, entry := range catalog.Facts.Kinds {
		if entry.MaxWindowDays < 1 {
			t.Fatalf("kind %s lists max_window_days %d", entry.Kind, entry.MaxWindowDays)
		}
		read := func(days int) error {
			_, err := reader.validate(FactsRequest{
				Kinds:    []string{entry.Kind},
				Subjects: []RequestSubject{{Kind: "repository", CanonicalID: "repository:x"}},
				Window:   &RequestWindow{Mode: WindowTrailing, Days: days},
			}, byKind)
			return err
		}
		if err := read(entry.MaxWindowDays); err != nil {
			t.Fatalf("kind %s refuses its own listed maximum %d: %v", entry.Kind, entry.MaxWindowDays, err)
		}
		err := read(entry.MaxWindowDays + 1)
		var requestErr *RequestError
		if !errors.As(err, &requestErr) || requestErr.Reason != contractsv1.WindowBeyondKindMaxReason || requestErr.MaxDays != entry.MaxWindowDays {
			t.Fatalf("kind %s accepts or mislabels %d days (listed maximum %d): %v", entry.Kind, entry.MaxWindowDays+1, entry.MaxWindowDays, err)
		}
	}
}

func TestReadFactsMixedKindsAreHeldToTheSmallestCatalogMaximum(t *testing.T) {
	byKind := map[contextfabric.FactKind]contextfabric.FactCapability{}
	for _, capability := range productionCapabilities() {
		byKind[capability.Kind] = capability
	}
	catalog := factsCatalogFor(ClassUnrestricted, productionCapabilities(), true)
	smallest, widest := 0, 0
	for _, entry := range catalog.Facts.Kinds {
		if smallest == 0 || entry.MaxWindowDays < smallest {
			smallest = entry.MaxWindowDays
		}
		widest = max(widest, entry.MaxWindowDays)
	}
	if widest <= smallest {
		t.Fatalf("catalog has one maximum (%d) for every kind: the mixed read measured nothing", widest)
	}
	var kinds []string
	for _, want := range []int{smallest, widest} {
		for _, entry := range catalog.Facts.Kinds {
			if entry.MaxWindowDays == want {
				kinds = append(kinds, entry.Kind)
				break
			}
		}
	}
	reader := &FactsReader{now: func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }}
	_, err := reader.validate(FactsRequest{
		Kinds:    kinds,
		Subjects: []RequestSubject{{Kind: "repository", CanonicalID: "repository:x"}},
		Window:   &RequestWindow{Mode: WindowTrailing, Days: smallest + 1},
	}, byKind)
	var requestErr *RequestError
	if !errors.As(err, &requestErr) || requestErr.MaxDays != smallest {
		t.Fatalf("mixed read of %v not held to the smallest listed maximum %d: %v", kinds, smallest, err)
	}
}
