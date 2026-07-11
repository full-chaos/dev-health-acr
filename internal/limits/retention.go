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
		window = &usageWindow{started: now, quotaStarted: now, lastTouched: now, quotaCredential: make(map[string]int64), credentials: make(map[string]UsageCounters)}
		byOrg[subject.OrgID] = window
	}
	if m.quotaExpired(window, policy, now) {
		window.quotaStarted = now
		window.quotaOrg = 0
		for credentialID := range window.quotaCredential {
			delete(window.quotaCredential, credentialID)
		}
	}
	if _, exists := window.credentials[subject.CredentialID]; !exists && len(window.credentials) >= m.maxCredentials {
		return nil, m.capacityDenied()
	}
	return window, Decision{Allowed: true}
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
	retention := max(m.retention, policy.Window)
	return !now.Before(window.lastTouched.Add(retention))
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
}
