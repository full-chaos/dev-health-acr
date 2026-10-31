package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// projectOnlyWithheld reports whether a project-only caller is withheld a
// project or organization fact this package cannot prove intrinsic
// (CHAOS-12299 F5/D3): yes for a project-only caller, no for every other
// class. A withheld path reads none of its rows and returns no fact for the
// subject. The subject carries no reason of its own yet; its typed label is
// CHAOS-12330.
//
// Guarded paths, and why each is not intrinsic to the project:
//   - health, flow, landscape, metrics, readiness, workload (project): built
//     only from the rows of the teams that own the project and, for health,
//     those teams' repositories.
//   - investment (project theme and native mixes, the project roll-up mix):
//     work units placed by repository and membership scope, not by the
//     work item's stated project membership. LoadPrerequisites loads no
//     membership scope for this caller either.
//   - investment (organization mix and unit listing), source_health
//     (organization): organization-wide figures (D3).
//   - pull_requests (project roll-up): pull requests are denied (CHAOS-12308).
//   - blockers (project roll-up): its rows can be non-member or unstated
//     work items.
//   - commitment and actual completion (project): items joined by the work
//     item's own project_id through the projects join keys, never by the
//     exact membership the graph admits; not proven member-exact.
//
// The linked pull request mix is read only for team subjects, which the graph
// denies this caller, so it needs no guard.
func projectOnlyWithheld(principal storage.Principal) bool {
	return auth.ClassifyGrant(principal) == auth.GrantProjectOnly
}
