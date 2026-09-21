package mcp

// Transport says how a process serves callers.
//
// Placeholder for the transport enumeration the http transport change owns:
// the names below are the ones that change fixes, so removing this file when
// that change lands is a deletion, not a rename.
type Transport string

const (
	// TransportStdio serves one local caller over STDIO. It is the zero
	// behaviour: workspace discovery is available.
	TransportStdio Transport = "stdio"
	// TransportHTTP serves many remote callers whose workspace this process
	// cannot see.
	TransportHTTP Transport = "http"
)

// Hosted reports whether this process serves callers whose workspace it
// cannot see. It is plain process configuration, never derived from a
// request or a session.
func (p *ProcessConfig) Hosted() bool {
	return p != nil && p.Transport == TransportHTTP
}
