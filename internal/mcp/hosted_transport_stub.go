package mcp

// Placeholder for the transport signal the http transport change owns. The
// names below are the ones that change fixes (TransportSTDIO, TransportHTTP,
// ProcessConfig.Transport, ProcessConfig.Hosted), so removing this file when
// that change lands is a deletion, not a rename.
const (
	// TransportSTDIO serves one local caller over STDIO: workspace discovery
	// is available.
	TransportSTDIO = "stdio"
	// TransportHTTP serves many remote callers whose workspace this process
	// cannot see.
	TransportHTTP = "http"
)

// Transport reports how this process serves callers; nil and unset read as
// STDIO.
func (p *ProcessConfig) Transport() string {
	if p == nil || p.transport == "" {
		return TransportSTDIO
	}
	return p.transport
}

// Hosted reports whether this process serves callers whose workspace it
// cannot see. It is plain process configuration, never derived from a
// request or a session.
func (p *ProcessConfig) Hosted() bool {
	return p.Transport() == TransportHTTP
}
