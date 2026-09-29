package directread

// RegistryWatch compares the ops registry acr vendors (the embedded
// operations.v1.json) with the registry the query service actually serves
// (CHAOS-7194).
//
//	GET <query service base URL>/registry     (ops server/registry_route.go)
//	{"schema_digest": "...", "operations": [{"operation": "...", "document_digest": "..."}]}
//
// The route is unauthenticated and in-cluster only: the request carries no
// Authorization header and no identity header. The check is telemetry, never
// a gate:
//
//   - a fetch failure is logged (Warn) and NEVER blocks serving; the stamped
//     digest falls back to the pinned one;
//   - the restricted-operation policy stays on the pinned catalogue; the watch
//     only chooses which digest label responses carry (Catalogue.StampedSchemaDigest);
//   - a stamped digest is the SERVED one only while the last fetch succeeded.
//
// Refresh is startup plus a lazy TTL: StampedDigest past the TTL starts one
// background refresh (single flight) and answers with the last known value, so
// no request waits on the network and no ticker outlives its owner.
// Warnings fire when the observed state changes, so a steady drift is one
// line per change, not one per call.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Registry watch limits.
const (
	// RegistryWatchTTL is how long a fetched state is trusted before the next
	// lazy refresh.
	RegistryWatchTTL = 10 * time.Minute
	// RegistryWatchTimeout bounds one /registry fetch.
	RegistryWatchTimeout = 5 * time.Second
	// MaxRegistryResponseBytes bounds how much of /registry is read.
	MaxRegistryResponseBytes = 1 << 20
)

// Registry check result classes (the "class" attribute of a failure line).
const (
	registryFailTransport  = "transport"
	registryFailHTTPStatus = "http_status"
	registryFailDecode     = "decode"
	registryFailTooLarge   = "too_large"
)

type registryServed struct {
	SchemaDigest string `json:"schema_digest"`
	Operations   []struct {
		Operation      string `json:"operation"`
		DocumentDigest string `json:"document_digest"`
	} `json:"operations"`
}

// RegistryWatchConfig configures NewRegistryWatch.
type RegistryWatchConfig struct {
	Catalogue *Catalogue
	// BaseURL is the query service base URL (config.DataQueryURL).
	BaseURL string
	// HTTPClient defaults to a client with no proxy and no redirects.
	HTTPClient *http.Client
	Logger     *slog.Logger
	// TTL defaults to RegistryWatchTTL; Timeout to RegistryWatchTimeout.
	TTL     time.Duration
	Timeout time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// RegistryWatch is the served-vs-pinned registry check.
type RegistryWatch struct {
	cat      *Catalogue
	endpoint string
	client   *http.Client
	logger   *slog.Logger
	ttl      time.Duration
	timeout  time.Duration
	now      func() time.Time

	mu        sync.Mutex
	served    string // last successfully fetched served digest; "" = unknown
	fetchedAt time.Time
	started   bool
	inflight  bool
	lastState string // dedupe key of the last logged outcome
	wg        sync.WaitGroup
}

// NewRegistryWatch builds the watch and attaches it to the catalogue.
func NewRegistryWatch(cfg RegistryWatchConfig) (*RegistryWatch, error) {
	if cfg.Catalogue == nil {
		return nil, ErrQueryClientConfig
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, ErrQueryClientConfig
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/registry"
	w := &RegistryWatch{
		cat: cfg.Catalogue, endpoint: u.String(), client: cfg.HTTPClient, logger: cfg.Logger,
		ttl: cfg.TTL, timeout: cfg.Timeout, now: cfg.Now,
	}
	if w.client == nil {
		w.client = &http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}
	} else {
		copied := *w.client
		w.client = &copied
	}
	w.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if w.logger == nil {
		w.logger = slog.Default()
	}
	if w.ttl <= 0 {
		w.ttl = RegistryWatchTTL
	}
	if w.timeout <= 0 {
		w.timeout = RegistryWatchTimeout
	}
	if w.now == nil {
		w.now = time.Now
	}
	cfg.Catalogue.attachRegistryWatch(w)
	return w, nil
}

// Start runs the startup check in the background; it never blocks the caller.
func (w *RegistryWatch) Start() {
	w.mu.Lock()
	if w.started || w.inflight {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.inflight = true
	w.mu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.refresh()
	}()
}

// Wait blocks until background refreshes finish (tests and shutdown).
func (w *RegistryWatch) Wait() { w.wg.Wait() }

// StampedDigest returns the digest responses should carry: the served digest
// while the last fetch succeeded, else the pinned one. Past the TTL it starts
// one background refresh; it never waits on the network.
func (w *RegistryWatch) StampedDigest() string {
	w.mu.Lock()
	served := w.served
	stale := w.started && !w.inflight && w.now().Sub(w.fetchedAt) >= w.ttl
	if stale {
		w.inflight = true
	}
	w.mu.Unlock()
	if stale {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.refresh()
		}()
	}
	if served == "" {
		return w.cat.SchemaDigest()
	}
	return served
}

// refresh performs one fetch and comparison. The caller has set inflight.
func (w *RegistryWatch) refresh() {
	served, class := w.fetch()
	w.mu.Lock()
	w.inflight = false
	w.fetchedAt = w.now()
	if class != "" {
		w.served = ""
	} else {
		w.served = served.SchemaDigest
	}
	w.mu.Unlock()
	if class != "" {
		w.logOnChange("failed:"+class, func() {
			w.logger.Warn("context fabric registry check failed",
				"class", class, "fallback", "pinned", "pinned_digest", w.cat.SchemaDigest())
		})
		return
	}
	w.compare(served)
}

func (w *RegistryWatch) logOnChange(key string, emit func()) {
	w.mu.Lock()
	same := w.lastState == key
	w.lastState = key
	w.mu.Unlock()
	if !same {
		emit()
	}
}

func (w *RegistryWatch) fetch() (registryServed, string) {
	var out registryServed
	ctx, cancel := context.WithTimeout(context.Background(), w.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.endpoint, nil)
	if err != nil {
		return out, registryFailTransport
	}
	req.Header.Set("User-Agent", queryClientUserAgent)
	resp, err := w.client.Do(req)
	if err != nil {
		return out, registryFailTransport
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return out, registryFailHTTPStatus
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRegistryResponseBytes+1))
	if err != nil {
		return out, registryFailTransport
	}
	if len(body) > MaxRegistryResponseBytes {
		return out, registryFailTooLarge
	}
	if err := json.Unmarshal(body, &out); err != nil || strings.TrimSpace(out.SchemaDigest) == "" {
		return registryServed{}, registryFailDecode
	}
	return out, ""
}

func normalizeDigest(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "sha256:"))
}

// pinnedDocumentDigests is every registry row the artifact carries, served or
// not: the ops registry lists both.
func (c *Catalogue) pinnedDocumentDigests() map[string]string {
	out := map[string]string{}
	for i := range c.file.Operations {
		out[c.file.Operations[i].Name] = normalizeDigest(c.file.Operations[i].Digest)
	}
	for _, ns := range c.file.NotServed {
		out[ns.Name] = normalizeDigest(ns.Digest)
	}
	return out
}

type opDrift struct{ op, pinned, served, reason string }

func (w *RegistryWatch) compare(served registryServed) {
	pinned := w.cat.pinnedDocumentDigests()
	servedOps := map[string]string{}
	for _, o := range served.Operations {
		servedOps[o.Operation] = normalizeDigest(o.DocumentDigest)
	}
	var drift []opDrift
	for name, p := range pinned {
		s, ok := servedOps[name]
		switch {
		case !ok:
			drift = append(drift, opDrift{name, p, "", "missing_in_served"})
		case s != p:
			drift = append(drift, opDrift{name, p, s, "changed"})
		}
	}
	for name, s := range servedOps {
		if _, ok := pinned[name]; !ok {
			drift = append(drift, opDrift{name, "", s, "missing_in_pinned"})
		}
	}
	sort.Slice(drift, func(i, j int) bool { return drift[i].op < drift[j].op })
	pinnedDigest := w.cat.SchemaDigest()
	digestMatch := normalizeDigest(served.SchemaDigest) == normalizeDigest(pinnedDigest)
	key := "ok"
	if !digestMatch || len(drift) > 0 {
		var b strings.Builder
		b.WriteString("drift:" + served.SchemaDigest)
		for _, d := range drift {
			b.WriteString("|" + d.op + "=" + d.reason + ":" + d.served)
		}
		key = b.String()
	}
	w.logOnChange(key, func() {
		if digestMatch && len(drift) == 0 {
			w.logger.Info("context fabric registry digest match",
				"pinned_digest", pinnedDigest, "served_digest", served.SchemaDigest, "operations", len(pinned))
			return
		}
		if !digestMatch {
			w.logger.Warn("context fabric registry digest drift",
				"pinned_digest", pinnedDigest, "served_digest", served.SchemaDigest,
				"pinned_ops", len(pinned), "served_ops", len(servedOps), "drifted_ops", len(drift))
		}
		for _, d := range drift {
			w.logger.Warn("context fabric registry operation drift",
				"operation", contextfabric.SanitizeLogAttr(d.op), "pinned_document_digest", d.pinned,
				"served_document_digest", d.served, "reason", d.reason)
		}
	})
}

func (c *Catalogue) attachRegistryWatch(w *RegistryWatch) { c.watch.Store(w) }

// StampedSchemaDigest is the digest responses carry: the served digest when a
// RegistryWatch knows it, else the pinned one (SchemaDigest).
func (c *Catalogue) StampedSchemaDigest() string {
	if w := c.watch.Load(); w != nil {
		return w.StampedDigest()
	}
	return c.SchemaDigest()
}
