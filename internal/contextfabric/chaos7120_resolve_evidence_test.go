package contextfabric

import (
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-7120's resolveEvidence mapping table, moved here with the
// embedded-subject gate core (CHAOS-7127): the function lives in this
// package now, shared by the direct read tools and the engine.

func derived7120(kind string, values ...string) string {
	id, omitted, err := identity.Derive(kind, values, nil)
	if err != nil || omitted {
		panic(fmt.Sprintf("derive %s %v: %v", kind, values, err))
	}
	return id
}

func evidence7120(entity contractsv1.ContextFabricEvidenceEntityType, raw string) string {
	return contractsv1.EvidenceRefID(entity, raw)
}

var (
	workA1_7120     = SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: derived7120(identity.KindWorkItem, "a", "WA-1")}
	workB9_7120     = SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: derived7120(identity.KindWorkItem, "b", "WB-9")}
	ciA1_7120       = SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: derived7120(identity.KindCIPipelineRun, "a", "run-1")}
	deployA1_7120   = SubjectRef{Kind: contractsv1.ContextFabricSubjectDeployment, CanonicalID: derived7120(identity.KindDeployment, "a", "dep-1")}
	pullA1_7120     = SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequest, CanonicalID: "pull_request:a:1"}
	reviewA1_7120   = SubjectRef{Kind: contractsv1.ContextFabricSubjectPullRequestReview, CanonicalID: derived7120(identity.KindPullRequestReview, "a", "1", "rev-1")}
	incidentA1_7120 = SubjectRef{Kind: contractsv1.ContextFabricSubjectIncident, CanonicalID: "incident:inc-a"}
	incidentB9_7120 = SubjectRef{Kind: contractsv1.ContextFabricSubjectIncident, CanonicalID: "incident:inc-b"}
	repoA_7120      = SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:a"}
)

// resolveEvidence directly: the mapping is exact, a malformed value is
// unresolvable, and a matching KIND alone never makes a reference own.
func TestChaos7120ResolveEvidenceMapsEntityFormsExactly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		subject   SubjectRef
		id        string
		wantOK    bool
		wantOwn   bool
		wantKind  SubjectKind
		wantCanon string
	}{
		{"own work item", workA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:WA-1"), true, true, "", ""},
		{"other work item, same kind", workA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityWorkItem, "b:WB-9"), true, false, contractsv1.ContextFabricSubjectWorkItem, workB9_7120.CanonicalID},
		{"work item id with a colon", workA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:linear:X"), true, false, contractsv1.ContextFabricSubjectWorkItem, derived7120(identity.KindWorkItem, "a", "linear:X")},
		{"work item without repository", workA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityWorkItem, "WA-1"), false, false, "", ""},
		{"own ci run", ciA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityCI, "a:run-1"), true, true, "", ""},
		{"own deployment", deployA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityDeployment, "a:dep-1"), true, true, "", ""},
		{"own pull request", pullA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityPullRequest, "a:1"), true, true, "", ""},
		{"pull request with a non-numeric number", pullA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityPullRequest, "a:x"), false, false, "", ""},
		{"own incident", incidentA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityIncident, "inc-a"), true, true, "", ""},
		{"other incident", incidentA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityIncident, "inc-b"), true, false, contractsv1.ContextFabricSubjectIncident, incidentB9_7120.CanonicalID},
		{"own review", reviewA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityReview, "a:rev-1"), true, true, "", ""},
		{"other review", reviewA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityReview, "a:rev-2"), false, false, "", ""},
		{"work item evidence on a repository fact", repoA_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityWorkItem, "a:WA-1"), true, false, contractsv1.ContextFabricSubjectWorkItem, workA1_7120.CanonicalID},
	} {
		ref, ok, own := resolveEvidence(tc.subject, tc.id)
		if ok != tc.wantOK || own != tc.wantOwn {
			t.Errorf("%s: ok=%v own=%v, want ok=%v own=%v", tc.name, ok, own, tc.wantOK, tc.wantOwn)
			continue
		}
		if ok && !own && (ref.opaque || ref.kind != tc.wantKind || ref.canonical != tc.wantCanon) {
			t.Errorf("%s: ref = %+v, want %s %s", tc.name, ref, tc.wantKind, tc.wantCanon)
		}
	}
	// A dependency edge names no subject: opaque.
	if ref, ok, own := resolveEvidence(workA1_7120, evidence7120(contractsv1.ContextFabricEvidenceEntityWorkItemDependency, "WB-9:WA-1")); !ok || own || !ref.opaque {
		t.Errorf("dependency evidence = %+v ok=%v own=%v, want opaque", ref, ok, own)
	}
}
