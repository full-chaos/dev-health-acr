package contextfabric

// The fact fields a windowed fact uses to state the window it was read over,
// and the basis that names a window the provider chose from its own clock.
const (
	FactFieldWindowStart           = "window_start"
	FactFieldWindowEnd             = "window_end"
	FactFieldWindowBasis           = "window_basis"
	FactWindowBasisDefaultTrailing = "default_trailing"
)
