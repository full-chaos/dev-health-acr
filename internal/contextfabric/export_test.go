package contextfabric

// Test-only exports, the standard Go pattern: this file is compiled into
// the package for the test binary and nothing else, so the external
// contextfabric_test package can reach package internals WITHOUT widening
// the production surface.
//
// It exists because the N2 parity property has to compare a GENERATED seed
// against the CHAOS-4347 status composition, and that composition is
// unexported package state. Exporting it for real would invite a
// production reader, and the whole point of the parity property is that
// the composition is the thing being RETIRED -- it is a reference for the
// proof, not an API.

// StatusCategoryCompositionForTest returns a copy of the CHAOS-4347
// status-category composition: the ruled subject-kind -> fact-kind set
// that a bare `status` requirement expands into today.
//
// A COPY, not the map: a test that sorted or appended in place would
// corrupt the composition for the engine's own tests in the same binary,
// and the resulting failure would point anywhere but here.
func StatusCategoryCompositionForTest() map[SubjectKind][]FactKind {
	out := make(map[SubjectKind][]FactKind, len(statusCategoryFactKindComposition))
	for subject, kinds := range statusCategoryFactKindComposition {
		out[subject] = append([]FactKind(nil), kinds...)
	}
	return out
}
