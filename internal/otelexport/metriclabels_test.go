package otelexport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

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

// thirdPartyLabelKeys reads the reviewed label keys of the otelhttp server instruments from the census table.
func thirdPartyLabelKeys(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "cigates", "emitscan", "labels.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 4 && fields[0] == "third-party:otelhttp" {
			for _, key := range strings.Split(fields[2], ",") {
				keys[key] = true
			}
		}
	}
	if len(keys) == 0 {
		t.Fatal("labels.tsv has no third-party:otelhttp row")
	}
	return keys
}

func collectMetrics(t *testing.T, s sinks) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := s.metrics.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	return rm
}

func durationSets(rm metricdata.ResourceMetrics) int {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok && m.Name == "http.server.request.duration" {
				return len(h.DataPoints)
			}
		}
	}
	return -1
}

func routedHandler(exporter *Exporter) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	mux.HandleFunc("GET /v1/boom", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	return exporter.HTTPHandler(mux)
}

// The Host header is chosen by the caller: no label may carry it, and the label set of a route stays one set however many
// hosts and ports a caller tries.
func TestHTTPHandlerMetricsCarryNoHostDerivedLabels(t *testing.T) {
	exporter, s := newTestExporter(t)
	handler := routedHandler(exporter)
	hosts := []string{"ATTACKER-UPPER.EXAMPLE", "[::1]:4455", strings.Repeat("attacker-long", 24) + ".example", "attacker space.example", "attacker\tcontrol.example"}
	for i := 0; i < 25; i++ {
		hosts = append(hosts, "attacker-"+strings.Repeat("x", i)+".example", "attacker-"+strings.Repeat("y", i)+".example:"+strings.Repeat("1", 1+i%4)+"234")
	}
	for _, host := range hosts {
		req := httptest.NewRequest(http.MethodGet, "/v1/things/7", nil)
		req.Host = host
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	rm := collectMetrics(t, s)
	sets := metricAttributeSets(rm)
	if len(sets) == 0 {
		t.Fatal("no metric data point was recorded: the test would pass vacuously")
	}
	for _, set := range sets {
		for _, kv := range set {
			if kv.Key == "server.address" || kv.Key == "server.port" {
				t.Errorf("label %s is exported", kv.Key)
			}
			if strings.Contains(strings.ToLower(kv.Value.Emit()), "attacker") {
				t.Errorf("label %s carries a Host value: %q", kv.Key, kv.Value.Emit())
			}
		}
	}
	if got := durationSets(rm); got != 1 {
		t.Errorf("http.server.request.duration has %d attribute sets after %d hostile hosts, want 1", got, len(hosts))
	}
}

// The census: whatever the library emits for a request with a body, a 404, a 500, an odd method and HTTP/1.0, every label
// key is one of the reviewed keys of the labels.tsv row, and the reviewed set is the shared allow-list.
func TestHTTPServerMetricLabelSetIsClosed(t *testing.T) {
	allowed := thirdPartyLabelKeys(t)
	if len(allowed) != len(httpSafeKeys) {
		t.Fatalf("labels.tsv lists %d otelhttp keys, the allow-list has %d", len(allowed), len(httpSafeKeys))
	}
	for key := range httpSafeKeys {
		if !allowed[key] {
			t.Errorf("the allow-list key %s is missing from the labels.tsv third-party row", key)
		}
	}
	exporter, s := newTestExporter(t)
	handler := routedHandler(exporter)
	probes := []*http.Request{
		httptest.NewRequest(http.MethodPost, "/v1/things/7", strings.NewReader("body")),
		httptest.NewRequest(http.MethodGet, "/v1/missing", nil),
		httptest.NewRequest(http.MethodGet, "/v1/boom", nil),
		httptest.NewRequest("BREW", "/v1/things/7", nil),
	}
	old := httptest.NewRequest(http.MethodGet, "/v1/things/7", nil)
	old.Proto, old.ProtoMajor, old.ProtoMinor = "HTTP/1.0", 1, 0
	probes = append(probes, old)
	for _, req := range probes {
		req.Host = "probe-host.example:9090"
		req.Header.Set("User-Agent", "probe-agent/9.9")
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		req.RemoteAddr = "198.51.100.9:4455"
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	rm := collectMetrics(t, s)
	seen := map[string]bool{}
	for _, set := range metricAttributeSets(rm) {
		for _, kv := range set {
			seen[string(kv.Key)] = true
		}
	}
	var unlisted []string
	for key := range seen {
		if !allowed[key] {
			unlisted = append(unlisted, key)
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) != 0 {
		t.Fatalf("the http server instruments export label keys outside the reviewed set: %v", unlisted)
	}
	if !seen["http.request.method"] || !seen["http.response.status_code"] || !seen["http.route"] {
		t.Fatalf("control: the request labels are missing (%v), so the check read nothing real", seen)
	}
}

// An attribute the handler adds that is not on the allow-list is dropped: the view is an allow-list, not a deny-list of the
// two keys that were found.
func TestViewDropsAnyUnlistedMetricAttribute(t *testing.T) {
	exporter, s := newTestExporter(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	handler := otelhttp.NewHandler(mux, "probe",
		otelhttp.WithMeterProvider(exporter.metrics),
		otelhttp.WithMetricAttributesFn(func(*http.Request) []attribute.KeyValue {
			return []attribute.KeyValue{attribute.String("user_agent.original", "probe-agent"), attribute.String("client.address", "203.0.113.7")}
		}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
	sets := metricAttributeSets(collectMetrics(t, s))
	if len(sets) == 0 {
		t.Fatal("no metric data point was recorded: the test would pass vacuously")
	}
	for _, set := range sets {
		for _, kv := range set {
			if kv.Key == "user_agent.original" || kv.Key == "client.address" {
				t.Errorf("the unlisted label %s was exported", kv.Key)
			}
		}
	}
}

// A sampled measurement keeps the attributes the view filtered out as exemplar attributes, and the OTLP exporter serialises
// them: the caller's Host must not leave that way either. The exemplar itself (value, time, trace link) is kept.
func TestHTTPServerMetricExemplarsCarryNoFilteredAttributes(t *testing.T) {
	exporter, s := newTestExporter(t)
	handler := routedHandler(exporter)
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/things/7", nil)
		req.Host = "attacker-exemplar-" + strings.Repeat("x", i) + ".example:4" + strings.Repeat("3", i%4) + "21"
		req.Header.Set("User-Agent", "attacker-agent/9.9")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	rm := collectMetrics(t, s)
	allowed := thirdPartyLabelKeys(t)
	exemplars := 0
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				continue
			}
			for _, p := range h.DataPoints {
				for _, ex := range p.Exemplars {
					exemplars++
					if len(ex.TraceID) == 0 {
						t.Errorf("%s: an exemplar lost its trace link", m.Name)
					}
					for _, kv := range ex.FilteredAttributes {
						if !allowed[string(kv.Key)] || strings.Contains(strings.ToLower(kv.Value.Emit()), "attacker") {
							t.Errorf("%s: exemplar attribute %s=%q left the process", m.Name, kv.Key, kv.Value.Emit())
						}
					}
				}
			}
		}
	}
	if exemplars == 0 {
		t.Fatal("no exemplar was recorded: the test would pass vacuously")
	}
}

// The census over every scope of the exporter's meter provider: no exemplar carries a filtered attribute outside the
// reviewed set, whichever instrument recorded it (a sampled measurement of a first-party instrument is included).
func TestNoExemplarCarriesAnUnreviewedFilteredAttribute(t *testing.T) {
	exporter, s := newTestExporter(t)
	handler := routedHandler(exporter)
	req := httptest.NewRequest(http.MethodGet, "/v1/things/7", nil)
	req.Host = "attacker-census.example:4321"
	handler.ServeHTTP(httptest.NewRecorder(), req)
	counter, err := exporter.Meter().Int64Counter("census_probe_total")
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := exporter.Tracer().Start(context.Background(), "projection tick")
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "ok")))
	span.End()
	allowed := thirdPartyLabelKeys(t)
	rm := collectMetrics(t, s)
	exemplars := 0
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			var all [][]metricdata.Exemplar[float64]
			var ints [][]metricdata.Exemplar[int64]
			switch data := m.Data.(type) {
			case metricdata.Histogram[float64]:
				for _, p := range data.DataPoints {
					all = append(all, p.Exemplars)
				}
			case metricdata.Sum[int64]:
				for _, p := range data.DataPoints {
					ints = append(ints, p.Exemplars)
				}
			}
			for _, group := range all {
				for _, ex := range group {
					exemplars++
					checkFiltered(t, scope.Scope.Name, m.Name, ex.FilteredAttributes, allowed)
				}
			}
			for _, group := range ints {
				for _, ex := range group {
					exemplars++
					checkFiltered(t, scope.Scope.Name, m.Name, ex.FilteredAttributes, allowed)
				}
			}
		}
	}
	if exemplars == 0 {
		t.Fatal("no exemplar was recorded in any scope: the census would pass vacuously")
	}
}

func checkFiltered(t *testing.T, scope, metric string, attrs []attribute.KeyValue, allowed map[string]bool) {
	t.Helper()
	for _, kv := range attrs {
		if !allowed[string(kv.Key)] || strings.Contains(strings.ToLower(kv.Value.Emit()), "attacker") {
			t.Errorf("%s %s: exemplar attribute %s=%q is outside the reviewed set", scope, metric, kv.Key, kv.Value.Emit())
		}
	}
}
