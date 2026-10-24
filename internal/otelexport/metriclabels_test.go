package otelexport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// libraryMetricLabelKeys are the label keys the otelhttp server instruments may carry. They are chosen by the library, not
// by this repository: a library bump that adds a key (a client address, a user agent, a raw path) fails this test until the
// key is reviewed and listed here.
var libraryMetricLabelKeys = map[string]bool{
	"http.request.method": true, "http.response.status_code": true, "http.route": true,
	"network.protocol.name": true, "network.protocol.version": true, "url.scheme": true,
	"server.address": true, "server.port": true, "error.type": true,
}

func metricAttributeSets(rm metricdata.ResourceMetrics) [][]attribute.KeyValue {
	var sets [][]attribute.KeyValue
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Histogram[float64]:
				for _, p := range data.DataPoints {
					sets = append(sets, p.Attributes.ToSlice())
				}
			case metricdata.Histogram[int64]:
				for _, p := range data.DataPoints {
					sets = append(sets, p.Attributes.ToSlice())
				}
			case metricdata.Sum[int64]:
				for _, p := range data.DataPoints {
					sets = append(sets, p.Attributes.ToSlice())
				}
			case metricdata.Sum[float64]:
				for _, p := range data.DataPoints {
					sets = append(sets, p.Attributes.ToSlice())
				}
			}
		}
	}
	return sets
}

func TestHTTPServerMetricLabelsStayInTheReviewedSet(t *testing.T) {
	exporter, s := newTestExporter(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	req := httptest.NewRequest(http.MethodGet, "/v1/things/secret-id-4711", nil)
	req.Header.Set("User-Agent", "probe-agent/9.9")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.RemoteAddr = "198.51.100.9:4455"
	exporter.HTTPHandler(mux).ServeHTTP(httptest.NewRecorder(), req)

	var rm metricdata.ResourceMetrics
	if err := s.metrics.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	sets := metricAttributeSets(rm)
	if len(sets) == 0 {
		t.Fatal("no metric data point was recorded: the test would pass vacuously")
	}
	seen := map[string]bool{}
	for _, set := range sets {
		for _, kv := range set {
			seen[string(kv.Key)] = true
		}
	}
	var unlisted []string
	for key := range seen {
		if !libraryMetricLabelKeys[key] {
			unlisted = append(unlisted, key)
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) != 0 {
		t.Fatalf("the http server instruments carry label keys outside the reviewed set: %v", unlisted)
	}
	if !seen["http.request.method"] || !seen["http.response.status_code"] {
		t.Fatalf("control: the request labels are missing (%v), so the check read nothing real", seen)
	}
	for _, secret := range []string{"secret-id-4711", "probe-agent", "203.0.113.7", "198.51.100.9"} {
		for _, set := range sets {
			for _, kv := range set {
				if kv.Value.Emit() == secret {
					t.Errorf("label %s carries %q", kv.Key, secret)
				}
			}
		}
	}
}
