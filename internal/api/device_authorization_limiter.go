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

func (l *deviceAuthorizationLimiter) allow(key string, limit int) DeviceAuthorizationLimitDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now().UTC()
	l.cleanup(now)
	window, exists := l.windows[key]
	if !exists {
		if len(l.windows) >= l.maxKeys {
			return DeviceAuthorizationLimitDecision{RetryAfter: deviceAuthorizationLimitWindow}
		}
		window = deviceAuthorizationWindow{started: now}
	}
	if window.count >= limit {
		return DeviceAuthorizationLimitDecision{RetryAfter: window.started.Add(deviceAuthorizationLimitWindow).Sub(now)}
	}
	window.count++
	l.windows[key] = window
	return DeviceAuthorizationLimitDecision{Allowed: true}
}

func (l *deviceAuthorizationLimiter) cleanup(now time.Time) {
	for key, window := range l.windows {
		if !now.Before(window.started.Add(deviceAuthorizationLimitWindow)) {
			delete(l.windows, key)
		}
	}
}
