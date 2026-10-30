package api

import (
	"sync"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const (
	deviceAuthorizationLimitWindow = time.Minute
	deviceCreationLimit            = 10
	tokenRequestLimit              = 60
	approvalAttemptLimit           = 5
	// oauthConsentRequestLimit bounds the web consent page's requests per
	// OAuth request handle (a login makes a preview and one decision). It is
	// keyed by the handle, never the peer address: every consent request
	// comes from the web server, and behind the ingress every public caller
	// shares one peer address, so an address key throttles all users at once.
	oauthConsentRequestLimit = 20
	// browserAuthorizeClientLimit is the per-process ceiling on /authorize
	// requests of one first-party browser client in a window, across every
	// source (CHAOS-12156). Every user of the web app shares the browser
	// client's public id, so its /authorize is limited per source at
	// deviceCreationLimit; this ceiling bounds the client as a whole.
	browserAuthorizeClientLimit    = 600
	defaultDeviceAuthorizationKeys = 4096
)

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type DeviceAuthorizationLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type DeviceAuthorizationLimiter interface {
	AllowDeviceCreation(string) DeviceAuthorizationLimitDecision
	AllowTokenRequest(string) DeviceAuthorizationLimitDecision
	AllowApprovalAttempt(string, storage.UserCodeHash) DeviceAuthorizationLimitDecision
	// AllowOAuthConsentRequest takes the SHA-256 of the request handle.
	AllowOAuthConsentRequest(storage.OAuthSecretHash) DeviceAuthorizationLimitDecision
	// AllowBrowserAuthorize admits one /authorize of a first-party browser
	// client (its limiter key) from source: deviceCreationLimit per source
	// and browserAuthorizeClientLimit for the client, counted only when both
	// allow it (CHAOS-12156).
	AllowBrowserAuthorize(client, source string) DeviceAuthorizationLimitDecision
}

type DeviceAuthorizationLimiterOptions struct {
	Clock          Clock
	MaxTrackedKeys int
}

type deviceAuthorizationLimiter struct {
	mu      sync.Mutex
	clock   Clock
	maxKeys int
	windows map[string]deviceAuthorizationWindow
}

type deviceAuthorizationWindow struct {
	started time.Time
	count   int
}

func NewDeviceAuthorizationLimiter(clock Clock) DeviceAuthorizationLimiter {
	return NewBoundedDeviceAuthorizationLimiter(DeviceAuthorizationLimiterOptions{Clock: clock, MaxTrackedKeys: defaultDeviceAuthorizationKeys})
}

func NewBoundedDeviceAuthorizationLimiter(options DeviceAuthorizationLimiterOptions) DeviceAuthorizationLimiter {
	if options.Clock == nil {
		options.Clock = ClockFunc(time.Now)
	}
	if options.MaxTrackedKeys < 1 {
		options.MaxTrackedKeys = 1
	}
	return &deviceAuthorizationLimiter{clock: options.Clock, maxKeys: options.MaxTrackedKeys, windows: make(map[string]deviceAuthorizationWindow)}
}

func (l *deviceAuthorizationLimiter) AllowDeviceCreation(ip string) DeviceAuthorizationLimitDecision {
	return l.allow("device:create\x00"+ip, deviceCreationLimit)
}

func (l *deviceAuthorizationLimiter) AllowTokenRequest(ip string) DeviceAuthorizationLimitDecision {
	return l.allow("device:token\x00"+ip, tokenRequestLimit)
}

func (l *deviceAuthorizationLimiter) AllowApprovalAttempt(ip string, userCode storage.UserCodeHash) DeviceAuthorizationLimitDecision {
	return l.allow("device:approve\x00"+ip+"\x00"+userCode.String(), approvalAttemptLimit)
}

func (l *deviceAuthorizationLimiter) AllowOAuthConsentRequest(handle storage.OAuthSecretHash) DeviceAuthorizationLimitDecision {
	return l.allow("oauth:consent\x00"+handle.String(), oauthConsentRequestLimit)
}

func (l *deviceAuthorizationLimiter) AllowBrowserAuthorize(client, source string) DeviceAuthorizationLimitDecision {
	return l.allowAll(
		keyLimit{key: "oauth:browser-authorize:source\x00" + client + "\x00" + source, limit: deviceCreationLimit},
		keyLimit{key: "oauth:browser-authorize:client\x00" + client, limit: browserAuthorizeClientLimit},
	)
}

type keyLimit struct {
	key   string
	limit int
}

func (l *deviceAuthorizationLimiter) allow(key string, limit int) DeviceAuthorizationLimitDecision {
	return l.allowAll(keyLimit{key: key, limit: limit})
}

// allowAll admits one request only when every window is under its limit, and
// then counts it in every window; a refused request counts in none.
func (l *deviceAuthorizationLimiter) allowAll(limits ...keyLimit) DeviceAuthorizationLimitDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now().UTC()
	l.cleanup(now)
	windows := make([]deviceAuthorizationWindow, len(limits))
	for index, limit := range limits {
		window, exists := l.windows[limit.key]
		if !exists {
			window = deviceAuthorizationWindow{started: now}
		}
		if window.count >= limit.limit {
			return DeviceAuthorizationLimitDecision{RetryAfter: window.started.Add(deviceAuthorizationLimitWindow).Sub(now)}
		}
		windows[index] = window
	}
	for index, limit := range limits {
		if _, exists := l.windows[limit.key]; !exists && len(l.windows) >= l.maxKeys {
			// Never a hard lock-out: keys are attacker-choosable (a bogus
			// client_id per request), so a full map would let one source
			// refuse every new legitimate client. Evict the oldest window.
			l.evictOldest()
		}
		windows[index].count++
		l.windows[limit.key] = windows[index]
	}
	return DeviceAuthorizationLimitDecision{Allowed: true}
}

func (l *deviceAuthorizationLimiter) cleanup(now time.Time) {
	for key, window := range l.windows {
		if !now.Before(window.started.Add(deviceAuthorizationLimitWindow)) {
			delete(l.windows, key)
		}
	}
}

func (l *deviceAuthorizationLimiter) evictOldest() {
	var oldestKey string
	var oldest time.Time
	first := true
	for key, window := range l.windows {
		if first || window.started.Before(oldest) {
			oldestKey, oldest, first = key, window.started, false
		}
	}
	if !first {
		delete(l.windows, oldestKey)
	}
}
