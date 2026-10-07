// Package hostedmetricstest builds hostedmetrics instruments over an in-memory
// reader and reads them back as flat "name{label=value,...}" cells. Tests only.
package hostedmetricstest

import (
	"context"
	"sort"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
)

// New returns instruments and a function that reads every recorded cell:
// counters as their sum, histograms as their sample count.
func New(t testing.TB, vocab hostedmetrics.Vocabularies) (*hostedmetrics.Instruments, func() map[string]int64) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	instruments, err := hostedmetrics.New(provider.Meter("test"), vocab)
	if err != nil {
		t.Fatal(err)
	}
	return instruments, func() map[string]int64 {
		var collected metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &collected); err != nil {
			t.Fatal(err)
		}
		cells := map[string]int64{}
		for _, scope := range collected.ScopeMetrics {
			for _, m := range scope.Metrics {
				switch data := m.Data.(type) {
				case metricdata.Sum[int64]:
					for _, point := range data.DataPoints {
						cells[cell(m.Name, point.Attributes.ToSlice())] += point.Value
					}
				case metricdata.Histogram[float64]:
					for _, point := range data.DataPoints {
						cells[cell(m.Name, point.Attributes.ToSlice())] += int64(point.Count)
					}
				}
			}
		}
		return cells
	}
}

func cell(name string, attributes []attribute.KeyValue) string {
	parts := make([]string, 0, len(attributes))
	for _, kv := range attributes {
		parts = append(parts, string(kv.Key)+"="+kv.Value.Emit())
	}
	sort.Strings(parts)
	return name + "{" + strings.Join(parts, ",") + "}"
}
