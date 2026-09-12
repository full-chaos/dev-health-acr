package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// TestTheProjectionCarriesTheServerClaimWithoutADriver pins the exemption.
//
// This projection copies a claim because some retained DRIVER cites it, which
// is right for claims that are evidence for a judgment: a judgment nobody kept
// needs no evidence. A server-computed claim has no driver and never will --
// it asserts something the server measured over the served member set rather
// than something a producer read -- so under the citation rule alone it was
// silently dropped from the surface every ordinary caller reads, while the
// canonical result carried it. The count reached the API and not the answer.
//
// AN ALLOW-LIST, NOT A WIDENING, and the negative arms are the half that says
// so: the citation rule is correct for producer-read claims and stays in force
// for them. A change that made uncited claims carry generally would pass the
// positive arm alone.
func TestTheProjectionCarriesTheServerClaimWithoutADriver(t *testing.T) {
	t.Parallel()

	if !projectionCarriesUncited(contractsv1.ContextFabricFactCardinality) {
		t.Error("the projection does not carry the server-computed cardinality claim -- it has no driver to cite it, so the citation rule alone drops it from the surface Ask Dev reads")
	}
	for _, kind := range []contractsv1.ContextFabricFactKind{
		contractsv1.ContextFabricFactPullRequests,
		contractsv1.ContextFabricFactReviews,
	} {
		if projectionCarriesUncited(kind) {
			t.Errorf("the projection carries %q uncited -- the citation rule must stay in force for producer-read claims; this allow-list names only what the SERVER mints", kind)
		}
	}
}
