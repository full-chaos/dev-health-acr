package factoracle

import (
	"fmt"
	"math"
	"sort"
)

// The owner-share pair (CHAOS-12691) is an ops-only check: it holds the ops
// TEAM-scoped investment breakdown of every owning team against the ops
// REPO-scoped breakdowns of the repositories it owns, weighed by the owner
// shares of the store (D5988; Store.OwnerShares). It is what acr needs to see
// before the run_operation policy may serve the TEAM and REPO scopes of the
// investment operations (cmd/operationpolicy investmentDecl, K14-A).
//
// It is a pure comparator. The policy refuses the TEAM and REPO scopes, and
// the recorded capture holds no such reply, so the pair is not in rootPairs:
// its ops answers come from the ops public GraphQL API through its own live
// plane (owner_share_live.go, OpsGraphQLPlane), run by the o4venue-tagged
// TestVenueOwnerShareLive (`make o4-owner-share-live`).
const pairOwnerShare = "investment_owner_share"

// OwnerShareRule names the rule an owner-share finding failed.
type OwnerShareRule string

const (
	// RuleTeamIsShareWeightedSum: TEAM(t)[k] = sum over r of
	// share(t, r) x REPO(r)[k].
	RuleTeamIsShareWeightedSum OwnerShareRule = "team_is_share_weighted_repo_sum"
	// RuleConservation: sum over t of TEAM(t)[k] + sum over unowned r of
	// REPO(r)[k] = sum over r of REPO(r)[k].
	RuleConservation OwnerShareRule = "conservation"
	// RuleTeamAnswerMissing: an owning team has no ops TEAM answer.
	RuleTeamAnswerMissing OwnerShareRule = "team_answer_missing"
	// RuleRepoAnswerMissing: a repository has no ops REPO answer.
	RuleRepoAnswerMissing OwnerShareRule = "repo_answer_missing"
	// RuleShareContract: the stored ownership rows breach the share contract
	// (Store.OwnerShares breaches).
	RuleShareContract OwnerShareRule = "share_contract"
)

// ownerShareMeasures are the measures the pair compares: the additive ones,
// whose value over a set of repositories is the sum of their values (ops
// analytics ownershipDbExpression: SUM(... x share)). A ratio or a percentile
// does not add over repositories, so a share-weighted sum of it means nothing.
var ownerShareMeasures = map[string]bool{"COUNT": true, "ISSUE_SIZE": true}

// OwnerShareInput is what the pair compares.
type OwnerShareInput struct {
	// Measure is the breakdown measure of every answer.
	Measure string
	// Team is the ops answer of a scope of one team, per team: key (theme,
	// subcategory) -> value. A team with no key here has no answer; an
	// answer with no item is an empty map.
	Team map[string]map[string]float64
	// Repo is the ops answer of a scope of one repository, per repository,
	// in the same form.
	Repo map[string]map[string]float64
	// Shares is the store reading of the owners, at the instant ops read
	// them.
	Shares *OwnerShareReading
	// Repositories is the organization's repositories; each must have an
	// answer, as must every repository a team owns.
	Repositories []string
}

// OwnerShareFinding is one failed rule of the pair.
type OwnerShareFinding struct {
	Rule OwnerShareRule
	Team string
	Repo string
	Key  string
	// Got is the ops value, Want what the rule gives for it.
	Got, Want float64
	// Breach is the kind of a share_contract finding.
	Breach OwnerShareBreachKind
	Detail string
}

// Finding is the finding in the report form of the oracle.
func (f OwnerShareFinding) Finding() Finding {
	key := f.Key
	switch {
	case f.Team != "" && key != "":
		key = "team " + f.Team + " " + key
	case f.Team != "":
		key = "team " + f.Team
	case f.Repo != "":
		key = "repository " + f.Repo
	}
	return Finding{Pair: pairOwnerShare, Key: key, Path: string(f.Rule), Detail: f.Detail}
}

// OwnerShareVerdict is the outcome of the pair.
type OwnerShareVerdict struct {
	Compared int
	Matches  int
	Findings []OwnerShareFinding
	// Notes name what the reading could not measure: repositories split
	// equally for want of a stored share, repositories read at their first
	// record. They are not findings and not matches.
	Notes []string
}

// CompareOwnerShares holds the ops TEAM answers against the share-weighted
// sums of the ops REPO answers. It refuses a measure that is not additive. A
// team or repository with no answer, a value off its rule by more than the
// Float32 tolerance of an ops theme value (themeTolerance), and every breach of
// the share contract in the store reading are findings.
func CompareOwnerShares(in OwnerShareInput) (OwnerShareVerdict, error) {
	var v OwnerShareVerdict
	if !ownerShareMeasures[in.Measure] {
		return v, fmt.Errorf("measure %q is refused: the owner-share pair compares share-weighted sums, which only an additive measure (COUNT, ISSUE_SIZE) has; a ratio or a percentile does not add over repositories", in.Measure)
	}
	if in.Shares == nil {
		return v, fmt.Errorf("the owner-share pair has no owner shares")
	}
	for _, b := range in.Shares.Breaches {
		v.Compared++
		v.Findings = append(v.Findings, OwnerShareFinding{Rule: RuleShareContract, Repo: b.Repo, Breach: b.Kind,
			Detail: fmt.Sprintf("%s: %s (teams %v, values %s)", b.Kind, b.Detail, b.Teams, formatThemes(b.Values))})
	}
	for _, repo := range in.Shares.EqualSplit {
		v.Notes = append(v.Notes, "repository "+repo+": its owners split it equally, an owner has no stored share")
	}
	for _, repo := range in.Shares.Clamped {
		v.Notes = append(v.Notes, "repository "+repo+": read at its first record, which is after the instant")
	}

	// Every repository of the organization and every owned repository.
	repos := map[string]bool{}
	for _, repo := range in.Repositories {
		repos[repo] = true
	}
	for repo := range in.Shares.Owners {
		repos[repo] = true
	}
	for repo := range in.Repo {
		repos[repo] = true
	}
	missingRepo := map[string]bool{}
	for _, repo := range sortedKeys(repos) {
		if _, ok := in.Repo[repo]; !ok {
			missingRepo[repo] = true
			v.Compared++
			v.Findings = append(v.Findings, OwnerShareFinding{Rule: RuleRepoAnswerMissing, Repo: repo,
				Detail: "the repository has no ops REPO answer; it is not a zero"})
		}
	}

	// Every owning team and every team ops answered for.
	shares := in.Shares.ForTeams(append(in.Shares.OwningTeams(), mapKeys(in.Team)...))
	teams := map[string]bool{}
	for _, team := range in.Shares.OwningTeams() {
		teams[team] = true
	}
	for team := range in.Team {
		teams[team] = true
	}
	missingTeam := false
	for _, team := range sortedKeys(teams) {
		got, answered := in.Team[team]
		if !answered {
			missingTeam = true
			v.Compared++
			v.Findings = append(v.Findings, OwnerShareFinding{Rule: RuleTeamAnswerMissing, Team: team,
				Detail: "the owning team has no ops TEAM answer; it is not a zero"})
			continue
		}
		want := map[string]float64{}
		whole := true
		for repo, share := range shares[team] {
			if missingRepo[repo] {
				whole = false
				continue
			}
			for key, value := range in.Repo[repo] {
				want[key] += share * value
			}
		}
		if !whole {
			// The repository finding stands; a sum with a part missing is
			// not compared.
			continue
		}
		for _, key := range themeKeys(got, want) {
			v.Compared++
			if math.Abs(got[key]-want[key]) <= themeTolerance(got[key], want[key]) {
				v.Matches++
				continue
			}
			v.Findings = append(v.Findings, OwnerShareFinding{Rule: RuleTeamIsShareWeightedSum, Team: team, Key: key, Got: got[key], Want: want[key],
				Detail: fmt.Sprintf("ops TEAM answered %s; the owner-share-weighted sum of its repositories' ops REPO answers is %s",
					formatFloat(got[key]), formatFloat(want[key]))})
		}
	}

	if missingTeam || len(missingRepo) > 0 {
		v.Notes = append(v.Notes, "conservation was not compared: an answer is missing (a finding above)")
		return v, nil
	}
	teamTotal, unowned, all := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, values := range in.Team {
		for key, value := range values {
			teamTotal[key] += value
		}
	}
	for repo, values := range in.Repo {
		_, owned := in.Shares.Owners[repo]
		for key, value := range values {
			all[key] += value
			if !owned {
				unowned[key] += value
			}
		}
	}
	for _, key := range themeKeys(teamTotal, unowned, all) {
		v.Compared++
		got := teamTotal[key] + unowned[key]
		if math.Abs(got-all[key]) <= themeTolerance(got, all[key]) {
			v.Matches++
			continue
		}
		v.Findings = append(v.Findings, OwnerShareFinding{Rule: RuleConservation, Key: key, Got: got, Want: all[key],
			Detail: fmt.Sprintf("the TEAM answers plus the unowned repositories give %s; the REPO answers sum to %s",
				formatFloat(got), formatFloat(all[key]))})
	}
	return v, nil
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
