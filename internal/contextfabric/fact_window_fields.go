package contextfabric

// The fact fields a windowed fact uses to state the window it was read over,
// and the basis that names a window the provider chose from its own clock.
const (
	FactFieldWindowStart           = "window_start"
	FactFieldWindowEnd             = "window_end"
	FactFieldWindowBasis           = "window_basis"
	FactWindowBasisDefaultTrailing = "default_trailing"
)

// windowBoundsFromClock reports whether a window's bounds were resolved from
// this turn's clock: a relative window keyed as re-derivable and not carried
// from an earlier turn. It follows the reuse key's own rule (windowKeyEncoding).
func windowBoundsFromClock(window *EffectiveEvidenceWindow, encoding windowKeyEncoding, carried bool) bool {
	return window != nil && window.RelativeID != "" && window.RelativeID != RelativeWindowAllTime && encoding == windowKeyRederivable && !carried
}
