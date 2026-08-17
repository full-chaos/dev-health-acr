package graphrank

import "sync"

// captureResolutionTracer is the test-side ResolutionTracer: accumulates
// every event in memory (content-free by construction, same as the events
// themselves), safe to inspect directly in a test's own assertions.
// Concurrency-safe (mutex-guarded) even though no current caller needs
// that, matching trialRawSignalCollector's own precedent for a shared
// capture sink.
type captureResolutionTracer struct {
	mu     sync.Mutex
	events []ResolutionTraceEvent
}

func (c *captureResolutionTracer) Trace(event ResolutionTraceEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *captureResolutionTracer) snapshot() []ResolutionTraceEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ResolutionTraceEvent(nil), c.events...)
}

func (c *captureResolutionTracer) eventsForStage(stage string) []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, e := range c.snapshot() {
		if e.Stage == stage {
			out = append(out, e)
		}
	}
	return out
}
