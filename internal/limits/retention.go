package limits

import "time"

func (m *Manager) windowFor(class RequestClass, subject Subject, policy quotaPolicy, now time.Time) (*usageWindow, Decision) {
	byOrg := m.windows[class]
	if byOrg == nil {
		byOrg = make(map[string]*usageWindow)
		m.windows[class] = byOrg
	}
	window := byOrg[subject.OrgID]
	if window == nil {
		if len(byOrg) >= m.maxOrganizations {
			return nil, m.capacityDenied()
		}
		window = &usageWindow{started: now, quotaStarted: now, lastTouched: now, quotaCredential: make(map[string]int64), credentials: make(map[string]UsageCounters), touched: make(map[string]time.Time), running: make(map[string]int)}
		byOrg[subject.OrgID] = window
	}
	if m.quotaExpired(window, policy, now) {
		window.quotaStarted = now
		window.quotaOrg = 0
		for credentialID := range window.quotaCredential {
			delete(window.quotaCredential, credentialID)
		}
	}
	if _, exists := window.credentials[subject.key()]; !exists && len(window.credentials) >= m.maxCredentials {
		m.forgetIdle(window, policy, now)
		if len(window.credentials) >= m.maxCredentials {
			return nil, m.capacityDenied()
		}
	}
	return window, Decision{Allowed: true}
}

// forgetIdle removes the subjects a full set no longer needs, so the cap counts
// live subjects and not every subject since the last sweep: one steady caller
// stops that sweep. A subject goes when no claim of it is running and it has not
// been seen for a retention. A retention is at least the quota window, so a
// removed subject's quota count has already expired: waiting is no way around a
// quota. A removed subject's Usage totals go with it.
//
// It holds the manager's lock, and the cap is configurable, so a full set of
// live subjects must not cost a scan per refused newcomer: a scan records the
// first moment a subject can leave, and it does not run again before that. A
// subject's last-seen time only moves forward, and a running subject sets it to
// the moment it finishes, a retention before it can leave, so nothing can leave
// earlier than that moment.
func (m *Manager) forgetIdle(window *usageWindow, policy quotaPolicy, now time.Time) {
	if now.Before(window.nextForget) {
		return
	}
	window.forgetPasses++
	retention := m.retentionOf(policy)
	next := now.Add(retention)
	for key, touched := range window.touched {
		if window.running[key] > 0 {
			continue
		}
		expires := touched.Add(retention)
		if !now.Before(expires) {
			delete(window.credentials, key)
			delete(window.quotaCredential, key)
			delete(window.touched, key)
			continue
		}
		if expires.Before(next) {
			next = expires
		}
	}
	window.nextForget = next
}

// retentionOf is how long state of a class is kept with no activity.
func (m *Manager) retentionOf(policy quotaPolicy) time.Duration {
	return max(m.retention, policy.Window)
}

func (m *Manager) sweep(now time.Time) {
	for class, byOrg := range m.windows {
		policy, err := m.policies.policy(class)
		if err != nil {
			continue
		}
		for orgID, window := range byOrg {
			if m.inflight[orgID] == 0 && m.expired(window, policy, now) {
				delete(byOrg, orgID)
			}
		}
	}
}

func (m *Manager) expired(window *usageWindow, policy quotaPolicy, now time.Time) bool {
	return !now.Before(window.lastTouched.Add(m.retentionOf(policy)))
}

func (m *Manager) quotaExpired(window *usageWindow, policy quotaPolicy, now time.Time) bool {
	return policy.Window > 0 && !now.Before(window.quotaStarted.Add(policy.Window))
}

func (m *Manager) capacityDenied() Decision {
	return Decision{Reason: DenialTrackingCapacity, RetryAfter: m.maxRetry}
}

func (m *Manager) denied(reason DenialReason, started time.Time, window time.Duration, now time.Time) Decision {
	retryAfter := started.Add(window).Sub(now)
	if retryAfter <= 0 {
		return Decision{Reason: reason}
	}
	retryAfter = ((retryAfter + time.Second - 1) / time.Second) * time.Second
	retryAfter = min(retryAfter, m.maxRetry)
	return Decision{Reason: reason, RetryAfter: retryAfter}
}

func (window *usageWindow) denied(credentialID string, now time.Time) {
	window.org.Denied++
	credential := window.credentials[credentialID]
	credential.Denied++
	window.credentials[credentialID] = credential
	window.lastTouched = now
	window.touched[credentialID] = now
}
