package auth

import "time"

type usageTelemetryState uint8

const (
	usageTelemetryOpen usageTelemetryState = iota
	usageTelemetryStopping
	usageTelemetryStopped
)

// Close rejects new records, cancels ordinary delivery, and waits only until
// its first absolute shutdown deadline. A nil result proves the worker has
// stopped, not that every best-effort record reached durable storage.
func (u *UsageTelemetry) Close() error {
	if u == nil {
		return nil
	}
	u.lifecycleMu.Lock()
	if u.state == usageTelemetryOpen {
		u.state = usageTelemetryStopping
		u.shutdownBy = time.Now().Add(u.shutdownTimeout)
		close(u.stop)
		u.cancelWorker()
	}
	if u.state == usageTelemetryStopped {
		err := u.terminalErr
		u.lifecycleMu.Unlock()
		return err
	}
	deadline := u.shutdownBy
	u.lifecycleMu.Unlock()

	delay := time.Until(deadline)
	if delay <= 0 {
		select {
		case <-u.done:
			return u.resultAfterDone()
		default:
			return ErrUsageTelemetryShutdownTimeout
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-u.done:
		return u.resultAfterDone()
	case <-timer.C:
		select {
		case <-u.done:
			return u.resultAfterDone()
		default:
			return ErrUsageTelemetryShutdownTimeout
		}
	}
}

func (u *UsageTelemetry) shutdownDeadline() time.Time {
	u.lifecycleMu.RLock()
	defer u.lifecycleMu.RUnlock()
	return u.shutdownBy
}

func (u *UsageTelemetry) finish(err error) {
	u.lifecycleMu.Lock()
	u.terminalErr = err
	u.state = usageTelemetryStopped
	u.lifecycleMu.Unlock()
	close(u.done)
}

func (u *UsageTelemetry) resultAfterDone() error {
	u.lifecycleMu.RLock()
	defer u.lifecycleMu.RUnlock()
	return u.terminalErr
}

func (u *UsageTelemetry) Done() <-chan struct{} {
	if u == nil {
		return nil
	}
	return u.done
}
