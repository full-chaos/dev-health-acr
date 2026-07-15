package auth

func (a *Authenticator) Close() error {
	if a == nil || !a.ownsTelemetry || a.usageTelemetry == nil {
		return nil
	}
	return a.usageTelemetry.Close()
}

func (a *Authenticator) UsageTelemetry() *UsageTelemetry {
	if a == nil {
		return nil
	}
	return a.usageTelemetry
}
