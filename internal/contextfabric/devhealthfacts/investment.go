package devhealthfacts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// InvestmentProvider implements contextfabric.FactProvider for FactInvestment.
// A team's, a project's and a repository's investment is ONLY the canonical
// categorization (work_unit_investments, the Jev categorizer's five themes and
// fifteen subcategories): a team's mix over the repositories it owns (CHAOS-6559), a
// project's roll-up through its owning teams' repositories and its own attribution
// (CHAOS-5930, CHAOS-5935), a repository's own mix. The deprecated
// investment_metrics_daily (the legacy rule classifier's free-form investment_area /
// project_stream keys) is read for NO subject kind any more (CHAOS-9195, D5932: the
// categorization moved to the LLM/Jev path and the rule classifier is removed). A
// project that has no canonical mix gets no fact, never a fabricated one.
type InvestmentProvider struct {
	facts  clickhouseFacts
	scopes *membershipScopeCache
	// scopeLoadTimeout overrides membershipScopeLoadTimeout (tests only).
	scopeLoadTimeout time.Duration
}

func newInvestmentProvider(client contextpacket.ClickHouseQueryClient) *InvestmentProvider {
	return &InvestmentProvider{facts: clickhouseFacts{client: client}, scopes: newMembershipScopeCache()}
}

func (p *InvestmentProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactInvestment, "devhealthfacts.investment", []contextfabric.SubjectKind{
		contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectOrganization,
	})
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam:         {contextfabric.FactTableBreakdown},
		contextfabric.SubjectRepository:   {contextfabric.FactTableBreakdown},
		contextfabric.SubjectProject:      {contextfabric.FactTableBreakdown},
		contextfabric.SubjectOrganization: {contextfabric.FactTableBreakdown},
	}
	capability.EstimatedItems = 20
	return capability
}

// LoadPrerequisites loads the membership scope a project, repository or team
// read applies, so a cold load runs under its own bound and not inside the
// capability's budget. The returned context carries the resolved scope; the
// read's statements use it and never load it again.
func (p *InvestmentProvider) LoadPrerequisites(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (context.Context, error) {
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return ctx, err
	}
	_, unitListing := contextfabric.InvestmentUnitsFrom(ctx)
	for _, subject := range query.Subjects {
		// An organization reads the scope only for its unit listing.
		if subject.Kind == contextfabric.SubjectOrganization && !unitListing {
			continue
		}
		switch subject.Kind {
		case contextfabric.SubjectTeam, contextfabric.SubjectProject, contextfabric.SubjectRepository, contextfabric.SubjectOrganization:
			started := clock()
			scope, runID, err := p.resolveMembershipScopeRun(ctx, orgID)
			if err != nil {
				reason := "error"
				switch {
				case errors.Is(err, context.DeadlineExceeded):
					reason = "deadline_exceeded"
				case errors.Is(err, context.Canceled):
					reason = "canceled"
				}
				slog.WarnContext(ctx, "investment membership scope prerequisite load failed", "reason", contextfabric.SanitizeLogAttr(reason), "duration_ms", clock().Sub(started).Milliseconds())
				return ctx, err
			}
			return context.WithValue(ctx, loadedMembershipScopeKey{}, loadedMembershipScope{orgID: orgID, runID: runID, scope: scope}), nil
		}
	}
	return ctx, nil
}

func (p *InvestmentProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ctx, span := withInvestmentSpan(ctx)
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	truncated := false
	omittedUnrepresentableCount := 0
	rejectedCount := 0
	defer func() {
		attachOwnershipNoteToFailure(ctx, p.facts.client, orgID, query.Subjects, p.ownershipForFailureNote, timeBound, err)
	}()
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note. A team/project subject
	// named without its "team:"/project v2 shape reads no differently from
	// a subject with genuinely no investment data unless this runs -- see
	// subjectIndex/v2Index's own doc comments.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.investment", contextfabric.FactInvestment, rejectedCount)
		}
	}()

	var mixUnavailable string
	var projectsNoMix []contextfabric.SubjectRef
	var teamsRequested int
	var teamsOwningNothing, teamsNoUnit []contextfabric.SubjectRef
	var teamsZeroEffort []teamZeroEffort
	var teamsNoLinked []teamNoLinkedWork
	var teamLinksUnwindowed bool
	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		// CHAOS-6559: a team's investment is ONLY the canonical theme mix
		// (work_unit_investments via owned repositories). The deprecated
		// investment_metrics_daily per-day rows are never served for a team:
		// beside the mix they buried it (the model answered from the day rows
		// and said no shares existed), and without a mix they stood in for
		// it. A team with no mix is disclosed as unavailable instead.
		teamRejected, unavailable, noOwned, noUnit, noEffort, noLinked, linksUnwindowed, scanErr := p.readTeamThemeMix(ctx, orgID, teamSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team theme mix", scanErr)
		}
		rejectedCount += teamRejected
		teamLinksUnwindowed = linksUnwindowed
		teamsOwningNothing, teamsNoUnit, teamsZeroEffort, teamsNoLinked = noOwned, noUnit, noEffort, noLinked
		teamsRequested = len(teamSubjects) - teamRejected
		if unavailable > 0 {
			watermark, watermarkErr := p.readInvestmentWatermark(ctx, orgID)
			if watermarkErr != nil {
				return contextfabric.FactProviderResult{}, readFailure("query investment watermark", watermarkErr)
			}
			mixUnavailable = investmentMixUnavailableReason(unavailable, len(noOwned), noEffort, noLinked, len(teamSubjects)-teamRejected, watermark)
		}
	}

	if projectSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectProject); len(projectSubjects) > 0 {
		// A project's investment is ONLY the canonical theme mix (work_unit_investments,
		// the Jev categorization; CHAOS-9195, D5932). The legacy per-team
		// (investment_area, project_stream, day) rows of investment_metrics_daily (the
		// deprecated rule classifier's keys) are no longer read for a project, as they
		// have not been for a team since CHAOS-6559. The shape rejection of the subjects
		// is counted here, once.
		_, _, projectRejected := v2Index(projectSubjects, identity.KindProject)
		rejectedCount += projectRejected
		// unusableRollup carries the unit population of a roll-up that had
		// units but no positive effort, so a native mix that replaces it can
		// still disclose that population.
		unusableRollup := map[string]int64{}
		themeRowCount, themeScanErr := p.readProjectThemeMix(ctx, orgID, projectSubjects, &facts, timeBound, unusableRollup)
		if themeScanErr != nil {
			return contextfabric.FactProviderResult{}, mixReadFailure("query project theme mix", themeScanErr)
		}
		truncated = truncated || themeRowCount > maxFactRowsPerQuery
		// The project's OWN attribution replaces the roll-up's shares for a
		// project that has any, and leaves the roll-up in place for one that
		// has none. It reads after the roll-up so it can merge onto the same
		// fact and move the roll-up's population beside it.
		nativeRowCount, nativeScanErr := p.readProjectNativeThemeMix(ctx, orgID, projectSubjects, &facts, timeBound, unusableRollup)
		if nativeScanErr != nil {
			return contextfabric.FactProviderResult{}, mixReadFailure("query project native theme mix", nativeScanErr)
		}
		truncated = truncated || nativeRowCount > maxFactRowsPerQuery
		// Missing is not healthy: a project with no canonical mix serves no
		// fact, and says so by a NAMED reason instead of a silent absence.
		projectsNoMix = projectsWithoutInvestmentFact(projectSubjects, &facts)
	}

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		rejected, scanErr := p.readRepositoryThemeMix(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository theme mix", scanErr)
		}
		rejectedCount += rejected
	}

	orgRestricted, orgUnattributedOnly := false, false
	if orgSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectOrganization); len(orgSubjects) > 0 {
		rejected, restricted, unattributedOnly, scanErr := p.readOrganizationThemeMix(ctx, principal, orgID, orgSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query organization theme mix", scanErr)
		}
		rejectedCount += rejected
		orgRestricted = restricted
		orgUnattributedOnly = unattributedOnly
	}

	var cutSubjects []contextfabric.SubjectRef
	if unitsRequest, wanted := contextfabric.InvestmentUnitsFrom(ctx); wanted {
		ownOrganization, _ := organizationSubjectsOfCaller(query.Subjects, orgID)
		for _, subject := range query.Subjects {
			prefix := teamPrefix
			if subject.Kind == contextfabric.SubjectOrganization {
				// Only the caller's own organization, one listing, and never for
				// a repository-bound grant (the organization fact is withheld
				// for it too: a partial total is not an organization's).
				if len(ownOrganization) == 0 || subject.CanonicalID != ownOrganization[0].CanonicalID || sourceHealthRestricted(principal) {
					continue
				}
				unitFacts, more, unitErr := p.readInvestmentUnits(ctx, orgID, subject, orgID, unitsRequest, timeBound)
				if unitErr != nil {
					return contextfabric.FactProviderResult{}, readFailure("query investment units", unitErr)
				}
				facts = append(facts, unitFacts...)
				if more {
					cutSubjects = append(cutSubjects, subject)
				}
				continue
			}
			if subject.Kind == contextfabric.SubjectRepository {
				prefix = repositoryPrefix
			} else if subject.Kind != contextfabric.SubjectTeam {
				continue
			}
			raw := strings.TrimPrefix(subject.CanonicalID, prefix)
			if raw == "" || raw == subject.CanonicalID || !bindingSafeKey(raw) {
				continue
			}
			unitFacts, more, unitErr := p.readInvestmentUnits(ctx, orgID, subject, raw, unitsRequest, timeBound)
			if unitErr != nil {
				return contextfabric.FactProviderResult{}, readFailure("query investment units", unitErr)
			}
			facts = append(facts, unitFacts...)
			if more {
				cutSubjects = append(cutSubjects, subject)
			}
		}
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	// CHAOS-4521b: this source has no project dimension, so an all-project
	// read that came back empty says something more specific than "no rows".
	retentionReason = explainTeamScopedProjectAbsence(timeBound, state, retentionReason, query.Subjects)
	if omittedUnrepresentableCount > 0 && retentionReason == "" {
		retentionReason = unrepresentableValueReason
	}
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainDaily), Truncated: truncated || omittedUnrepresentableCount > 0, OmittedCount: omittedUnrepresentableCount}
	applyTeamsOwningNothing(&result, query.Subjects, teamsRequested, teamsOwningNothing)
	if mixUnavailable != "" {
		addRequestNote(&result, mixUnavailable)
		addSubjectReasons(&result, teamsNoUnit, investmentTeamNoUnitReason)
		for _, n := range teamsNoLinked {
			addSubjectReasons(&result, []contextfabric.SubjectRef{n.subject}, investmentTeamPRsWithoutLinkedWorkOwnReason(n.pullRequests))
		}
		for _, z := range teamsZeroEffort {
			addSubjectReasons(&result, []contextfabric.SubjectRef{z.subject}, investmentTeamEffortNotMeasurableOwnReason(z.units))
		}
	}
	if len(projectsNoMix) > 0 {
		addRequestNote(&result, investmentProjectNoCanonicalMixReason(len(projectsNoMix), len(subjectsOfKind(query.Subjects, contextfabric.SubjectProject))-rejectedProjectCount(query.Subjects)))
		addSubjectReasons(&result, projectsNoMix, investmentProjectNoCanonicalMixOwnReason)
	}
	if timeBound.active {
		addScopedReason(&result, query.Subjects, subjectsOfKind(query.Subjects, contextfabric.SubjectTeam), investmentOwnershipAsSynced)
	}
	if teamLinksUnwindowed {
		addScopedReason(&result, query.Subjects, subjectsOfKind(query.Subjects, contextfabric.SubjectTeam), teamProjectLinksUnwindowedReason)
	}
	// Every requested subject is checked, with or without a fact: a window
	// with no overlapping unit serves no fact and must still say the window
	// starts before the stored history.
	for _, subject := range query.Subjects {
		if reason := span.reasonFor(subject.CanonicalID, projectSpanKey(subject), subject.Kind == contextfabric.SubjectProject, timeBound); reason != "" {
			addScopedReason(&result, query.Subjects, []contextfabric.SubjectRef{subject}, reason)
		}
	}
	orgSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectOrganization)
	if orgUnattributedOnly {
		addScopedReason(&result, query.Subjects, orgSubjects, investmentOrgUnattributedOnlyReason)
	}
	if orgRestricted {
		if len(facts) == 0 && coversEveryQueriedSubject(query.Subjects, orgSubjects) {
			result.State = contextfabric.SourceNotApplicable
		}
		addScopedReason(&result, query.Subjects, orgSubjects, investmentOrgRestrictedReason)
	}
	if len(cutSubjects) > 0 {
		result.Truncated = true
		addScopedReason(&result, query.Subjects, cutSubjects, unitFactReasonCut)
	}
	return result, nil
}

// canonicalInvestmentThemes is the fixed 5-theme taxonomy
// (ops/src/dev_health_ops/investment_taxonomy.py's THEMES; AGENTS.md: "no
// synonyms/overrides") in a stable iteration order, so readTeamThemeMix's
// normalization is deterministic regardless of map iteration order.
var canonicalInvestmentThemes = [...]string{
	contextfabric.ThemeFeatureDelivery, contextfabric.ThemeOperational,
	contextfabric.ThemeMaintenance, contextfabric.ThemeQuality, contextfabric.ThemeRisk,
}

// readTeamThemeMix reads the CANONICAL investment theme/subcategory
// distribution (CHAOS-4398 §0: work_unit_investments) for the given team
// subjects -- NEVER investment_metrics_daily, the deprecated legacy rule set.
// A team's mix is the sum of the mixes of the repositories the team owns
// (CHAOS-6559, chris ruling 2026-09-24; see investment_repo_mix.go).
//
// Each team with a mix gets ONE standalone FactInvestment fact carrying the
// theme_*/prior_theme_*/theme_quality_bugfix scalars and the theme_breakdown
// table. It is never merged onto another fact: a team's window mix is not an
// attribute of any single day row, and a mix riding on a per-day fact was read
// by the model as that day's attribute and reported as "no shares".
// internal/contextfabric/cohort_ranking.go's investmentMixSignal finds the
// fact by field PRESENCE (theme_feature_delivery), never by position.
//
// timeBound.neutral() bounds the CURRENT window read. When timeBound also
// carries an explicit start (never inferred -- CHAOS-4040: a window this
// producer invented on its own would be exactly the "commit under an
// inferred window" the ticket forbids), the same statement also reads the
// prior comparable window [start-duration, start) for RankCohort's mix-shift
// sub-signal. A team with no prior-window data gets NO prior_theme_* fields
// at all -- omitted, never zero-filled.
//
// A team with zero current-window weighted effort (no owned repository, or
// owned repositories with no persisted work) gets NO fact: a fabricated 0.0
// share across all five themes would read as "we know this team's mix is
// exactly nothing" rather than "we have no mix to report". Such teams are
// counted in unavailable so the caller can say so; shapeRejected counts
// subjects whose id did not have the team shape (CHAOS-5026).
func (p *InvestmentProvider) readTeamThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (shapeRejected, unavailable int, noOwned, noUnit []contextfabric.SubjectRef, noEffort []teamZeroEffort, noLinked []teamNoLinkedWork, linksUnwindowed bool, err error) {
	ids, bySubject, shapeRejected := subjectIndex(subjects, teamPrefix)
	if len(ids) == 0 {
		return shapeRejected, 0, nil, nil, nil, nil, false, nil
	}
	// CHAOS-6559 (chris ruling 2026-09-24): a team's mix is the SUM of the
	// mixes of the repositories the team OWNS (team_repo_ownership), each
	// repository's mix being the PR-ref-share partition of persisted work
	// unit distributions (investment_repo_mix.go). Never a member vote and
	// never a work-item majority vote.
	var priorBound *factTimeBound
	if timeBound.active && timeBound.hasStart {
		duration := timeBound.end.Sub(timeBound.start)
		priorBound = &factTimeBound{active: true, hasStart: true, start: timeBound.start.Add(-duration), end: timeBound.start}
	}
	current, prior, owned, ownedUnits, linked, projectTeams, err := p.teamOwnedRepoMix(ctx, orgID, ids, timeBound, priorBound)
	if err != nil {
		return shapeRejected, 0, nil, nil, nil, nil, false, err
	}
	// A team also reaches repositories through its owned projects' linked
	// pull requests: those count only their linked pull requests (linked), and
	// a team that reaches any is not a team that "owns no repository".
	ownedRepos := make(map[string][]teamOwnedRepo, len(owned))
	for teamID, keys := range owned {
		for _, key := range keys {
			ownedRepos[teamID] = append(ownedRepos[teamID], teamOwnedRepo{key: key})
		}
	}
	var reachedOnly map[string][]teamReachedRepo
	if len(projectTeams) > 0 {
		reachedRepos, reachErr := teamProjectReachedRepositories(ctx, p.facts.client, orgID, projectTeams, timeBound)
		if reachErr != nil {
			return shapeRejected, 0, nil, nil, nil, nil, false, reachErr
		}
		reachedOnly = reachedOnlyRepositories(reachedRepos, ownedRepos)
	}

	teamIDs := make([]string, 0, len(ids))
	for _, teamID := range ids {
		if _, ok := bySubject[teamID]; ok {
			teamIDs = append(teamIDs, teamID)
		}
	}
	sort.Strings(teamIDs)
	for _, teamID := range teamIDs {
		subject := bySubject[teamID]
		m, ok := current[teamID]
		if !ok || m.total() <= 0 {
			unavailable++
			if len(owned[teamID]) == 0 && len(reachedOnly[teamID]) == 0 {
				noOwned = append(noOwned, subject)
			} else if units := ownedUnits[teamID]; units > 0 {
				noEffort = append(noEffort, teamZeroEffort{subject: subject, units: units})
			} else {
				noUnit = append(noUnit, subject)
			}
			continue
		}
		currentTotal := m.total()
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+5)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(m.theme[theme] / currentTotal))
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(m.bugfix / currentTotal))
		fields["theme_breakdown"] = themeBreakdownTable(m, timeBound.effectiveGrain(grainDaily))
		fields["owned_repository_count"] = contextfabric.IntegerFactValue(m.repos)
		fields["mix_source"] = contextfabric.StringFactValue(repoMixSource)
		fields["attribution_basis"] = contextfabric.StringFactValue(repoMixBasis + "_over_team_repo_ownership")
		if l, ok := linked[teamID]; ok && l.total() > 0 {
			fields["attribution_basis"] = contextfabric.StringFactValue(repoMixBasis + "_over_team_repo_ownership_and_project_links")
			fields["project_reached_repository_count"] = contextfabric.IntegerFactValue(int64(len(reachedOnly[teamID])))
			if timeBound.active {
				linksUnwindowed = true
			}
			if teamRollupTablesIncluded(len(ids)) {
				if table, omitted, ok := projectReachedRepositoriesFactValue(reachedOnly[teamID]); ok {
					fields["project_reached_repositories"] = table
					if omitted > 0 {
						fields["project_reached_repositories_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
					}
				}
			}
			weakest := int64(0)
			for _, repo := range reachedOnly[teamID] {
				if weakest == 0 || repo.linkRank < weakest {
					weakest = repo.linkRank
				}
			}
			if tier := blockerLinkTierName(weakest); tier != "" {
				fields["repository_link_basis"] = contextfabric.StringFactValue(tier)
			}
		}

		if priorMix, ok := prior[teamID]; ok && priorMix.total() > 0 {
			priorTotal := priorMix.total()
			for _, theme := range canonicalInvestmentThemes {
				fields[contextfabric.FactFieldPriorTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(priorMix.theme[theme] / priorTotal))
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	if len(noUnit) > 0 {
		noUnit, noLinked, err = p.splitTeamsWithoutLinkedWork(ctx, orgID, noUnit, nil, bySubject, owned, reachedOnly, timeBound)
		if err != nil {
			return shapeRejected, 0, nil, nil, nil, nil, false, err
		}
	}
	return shapeRejected, unavailable, noOwned, noUnit, noEffort, noLinked, linksUnwindowed, nil
}

// investmentWatermarkStatement reads the newest computed_at of the org's
// persisted work unit distributions: how fresh the data behind a mix is.
const investmentWatermarkStatement = `SELECT toString(max(computed_at)), count() FROM work_unit_investments WHERE org_id = {org_id:String}`

// readInvestmentWatermark returns the newest computed_at of the org's
// work_unit_investments, or "" when the table holds no row for the org.
func (p *InvestmentProvider) readInvestmentWatermark(ctx context.Context, orgID string) (string, error) {
	watermark := ""
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadInvestmentWatermark", withRowLimit(investmentWatermarkStatement), orgID, []string{}, func(row contextpacket.ClickHouseRowScanner) error {
		var maxComputedAt string
		var rows uint64
		if err := row.Scan(&maxComputedAt, &rows); err != nil {
			return err
		}
		if rows > 0 {
			watermark = maxComputedAt
		}
		return nil
	})
	return watermark, err
}

// Closed reason set of an empty team window.
const (
	investmentTeamNoOwnedRepository   = "investment_team_no_owned_repository"
	investmentTeamNoUnitInWindow      = "investment_team_no_unit_in_window"
	investmentTeamEffortNotMeasurable = "investment_team_effort_not_measurable"
	investmentTeamNoUnitReason        = "the team owns repositories, and none of them has persisted work unit effort in the requested window"
	investmentOwnershipAsSynced       = "investment_ownership_as_synced: team ownership is read from the synced ownership assertions (not historical); a past window includes every assertion not ended before the window start"
)

// investmentProjectNoCanonicalMix is the closed reason token of a project that has no
// canonical theme mix in the requested window (CHAOS-9195): no owning-team roll-up and no
// native attribution carries positive effort. It is not a zero mix and not a healthy project.
const investmentProjectNoCanonicalMix = "investment_project_no_canonical_mix"

const investmentProjectNoCanonicalMixOwnReason = investmentProjectNoCanonicalMix + ": no canonical theme mix (work_unit_investments) for the project in the requested window"

func investmentProjectNoCanonicalMixReason(noMix, requested int) string {
	return fmt.Sprintf("%s for %d of %d requested projects: no canonical theme mix (work_unit_investments) for the project in the requested window; the deprecated per-team investment_area breakdown is not served", investmentProjectNoCanonicalMix, noMix, requested)
}

// projectsWithoutInvestmentFact lists the project subjects of valid shape that have no
// FactInvestment fact after the project reads.
func projectsWithoutInvestmentFact(subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact) []contextfabric.SubjectRef {
	have := map[string]bool{}
	for _, f := range *facts {
		if f.Kind == contextfabric.FactInvestment {
			have[contextfabric.FactSubjectKey(f.Subject)] = true
		}
	}
	var missing []contextfabric.SubjectRef
	for _, subject := range subjects {
		if projectSpanKey(subject) == "" {
			continue
		}
		if !have[contextfabric.FactSubjectKey(subject)] {
			missing = append(missing, subject)
		}
	}
	return missing
}

// rejectedProjectCount: project subjects whose id has no project v2 shape.
func rejectedProjectCount(subjects []contextfabric.SubjectRef) int {
	n := 0
	for _, subject := range subjects {
		if subject.Kind == contextfabric.SubjectProject && projectSpanKey(subject) == "" {
			n++
		}
	}
	return n
}

// teamZeroEffort is a team whose owned repositories hold work units in the
// window, all with zero persisted effort.
type teamZeroEffort struct {
	subject contextfabric.SubjectRef
	units   int64
}

// investmentTeamEffortNotMeasurableOwnReason is the per-team reason of a team
// whose units all carry zero effort. Effort is not measurable from this data
// (no line counts, no linked commits, no cycle times). A unit whose commit
// statistics were capped or never synced reads as effort 0, the same as a
// measured zero: no partial marker is persisted, so the two are not told apart.
func investmentTeamEffortNotMeasurableOwnReason(units int64) string {
	return fmt.Sprintf("%s: %d work unit(s) attributed to the team's owned repositories in the requested window, all with zero persisted effort; effort not measurable (no line counts, no linked commits, no cycle times); an unmeasured or capped unit is not told apart from a measured zero, and no mix is served", investmentTeamEffortNotMeasurable, units)
}

// investmentMixUnavailableReason is the disclosure for teams that have no
// canonical mix: unknown is not healthy and is not a zero mix (North Star
// check 12), and the watermark says how fresh the data that was searched is.
func investmentMixUnavailableReason(unavailable, noOwned int, noEffort []teamZeroEffort, noLinked []teamNoLinkedWork, requested int, watermark string) string {
	reason := fmt.Sprintf("investment mix unavailable for %d of %d requested teams", unavailable, requested)
	if noOwned > 0 {
		reason += fmt.Sprintf("; %s for %d: the team owns no repository", investmentTeamNoOwnedRepository, noOwned)
	}
	if len(noEffort) > 0 {
		units := int64(0)
		for _, z := range noEffort {
			units += z.units
		}
		reason += fmt.Sprintf("; %s for %d: the team's owned repositories hold %d work unit(s) in the requested window, all with zero persisted effort; effort not measurable (no line counts, no linked commits, no cycle times); an unmeasured or capped unit is not told apart from a measured zero", investmentTeamEffortNotMeasurable, len(noEffort), units)
	}
	if len(noLinked) > 0 {
		prs := int64(0)
		for _, n := range noLinked {
			prs += n.pullRequests
		}
		reason += fmt.Sprintf("; %s for %d: %d pull request(s) in the team's repositories in the requested window; %s", investmentTeamPRsWithoutLinkedWork, len(noLinked), prs, investmentTeamPRsWithoutLinkedWorkRule)
	}
	if noUnit := unavailable - noOwned - len(noEffort) - len(noLinked); noUnit > 0 {
		reason += fmt.Sprintf("; %s for %d: no repository the team owns and no pull request linked to its owned projects has persisted work unit effort in the requested window", investmentTeamNoUnitInWindow, noUnit)
	}
	if watermark == "" {
		return reason + "; work_unit_investments holds no rows for this organization"
	}
	return reason + "; work_unit_investments last computed at " + watermark
}

// evidenceVoteAttributedPredicate is the ONE "does this evidence-vote row
// carry a real team attribution" check for a nullable/empty-able join key --
// a LEFT JOIN miss against wita leaves t.team_id NULL, and this expression
// must stay NULL (not fall back to a concrete value) for that row, or an
// unmatched row reads as attributed. nullIf(t.team_id, ”) alone has this
// property: NULL stays NULL, an empty-string team_id also collapses to
// NULL, and only a real, non-empty team_id survives to compare NOT NULL.
// readers.ReadTeamThemeMix's own analogous count expression has this same
// no-fallback shape; this is the one definition for this producer's
// evidence vote, so a second call site reuses it rather than re-deriving
// an equivalent-looking expression that quietly adds a fallback and stops
// discriminating.
const evidenceVoteAttributedPredicate = "nullIf(t.team_id, '') IS NOT NULL"

// themeInvestmentRangePredicate mirrors dev-health-go's
// readers.TimeBound.rangePredicate (investment_theme.go, unexported there):
// a work_unit_investments row is included whenever any part of its own
// [from_ts, to_ts] evidence window overlaps the requested [start, end) --
// to_ts is the INCLUSIVE last-evidence instant, so a unit whose to_ts equals
// the window start is inside it (ops api/queries/investment.py:518,555,593
// use the same `to_ts >= start_ts`; ops is the authority for investment
// semantics) --
// an overlap test against a row's own two-sided range, unlike
// factTimeBound's dayPredicate/timestampPredicate (a single row-level
// column compared to one instant or a half-open range). An inactive bound
// (the current axis) applies no predicate.
func themeInvestmentRangePredicate(b factTimeBound, fromColumn, toColumn string) string {
	if !b.active {
		return ""
	}
	predicate := " AND " + fromColumn + " < {" + boundEndParam + ":DateTime64(6,'UTC')}"
	if b.hasStart {
		predicate += " AND " + toColumn + " >= {" + boundStartParam + ":DateTime64(6,'UTC')}"
	}
	return predicate
}

// readProjectThemeMix rolls FactInvestment's canonical theme_*/
// theme_quality_bugfix fields up for a PROJECT (CHAOS-5930): the top-level
// scalar cohort_ranking.go's investmentMixSignal reads
// (theme_feature_delivery, subject-kind-blind) off ANY FactInvestment fact,
// promoted here for project subjects.
//
// Population/join: a team-weighted roll-up via the project's owning teams'
// repositories, into work_unit_investments -- projects ->
// team_project_ownership (the project's owning teams,
// deduplicated the same way readProjectHealth/readProjectInvestment already
// dedupe a team owning a project through more than one ownership `source`
// row) -> team_repo_ownership (those teams' owned repos, deduplicated per
// (project, repo) so a repo owned through more than one source, or by more
// than one of the project's owning teams, is never counted twice) ->
// work_unit_investments, joined DIRECTLY on its own repo_id column.
//
// This is deliberately NOT readTeamThemeMix's join. That reader resolves a
// work unit's structural_evidence_json (issue/PR refs) to a repo and then
// to ONE team via work_item_team_attributions' majority vote, because it
// must decide which single team a work unit belongs to. A project's
// roll-up asks a narrower, repo-membership question instead -- "is this
// work unit's own repo one this project's owning teams own" -- answerable
// directly off work_unit_investments.repo_id (declared in devhealthschema's
// read set; live-schema verified against the trial ClickHouse,
// acr-trial-data/dh_0906), without parsing structural_evidence_json or the
// team-attribution vote at all. A work unit whose repo_id IS NULL is
// invisible to this roll-up -- disclosed by omission (work_unit_count
// counts only what was reachable), never fabricated into a share.
//
// One row per (project, work_unit_id) by construction: a work unit carries
// exactly one repo_id, and project_repo below is already deduplicated to
// one row per (project, repo), so the join cannot fan a work unit out more
// than once for the same project. A repo (or team) belonging to more than
// one project legitimately contributes to EACH project's own roll-up -- a
// shared repo or team is a coverage filter, never a weight -- and a work
// unit is never double-attributed WITHIN one project's own total.
//
// A project with zero attributed work units, or whose attributed work
// units sum to zero effort across the five canonical themes, gets NO theme
// fields at all (never a fabricated 0.0 share) -- the same
// degrade-not-fabricate rule readTeamThemeMix's own doc comment states for
// the team subject.
//
// Fields MERGE onto the project's existing FactInvestment fact
// (readProjectInvestment's team_breakdown fact, appended BEFORE this call in
// ReadFacts) when one exists. That fact is ONE per project and carries only
// the Rows-shaped team_breakdown table (which the model-facing projection
// drops) plus rollup_basis/team_count scalars, so the merged fact stays the
// project's single investment fact and the theme scalars are model-visible
// on it. A project with no such fact still gets a standalone one. (A team is
// different: it never merges, see readTeamThemeMix.)
//
// The statement returns at most ONE row per requested project (every
// aggregate collapses to project_key), probed at maxFactRowsProbe
// (withRowProbeLimit) rather than a plain LIMIT, so a project population
// sitting exactly at the served cap is distinguishable from one that
// overflowed it -- the same discipline workItemProjectCompletionStatement
// documents for its own project-grain aggregate.
func (p *InvestmentProvider) readProjectThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, unusableRollup map[string]int64) (rowCount int, err error) {
	ids, bySubject, _ := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	// readers.QueryOrgScopedNamed, never p.facts.query: this is genuinely
	// raw SQL (not a readers.ReadXxx call), but it must still report
	// through the SAME readers.Instrumentation hook NewInstrumentedProviders
	// wires into ctx (instrumentation.go's own doc comment) -- p.facts.query
	// is devhealthfacts's own acr-side mirror of this exact function,
	// deliberately without the readers-package instrumentation piece, so
	// using it here would silently drop this read out of the slog/span/
	// counter coverage every other reader-backed read in this package
	// carries (readRepositoryMetricsSeries's identical raw-SQL shape
	// already makes this same choice, for the same reason).
	// contextpacket.ClickHouseQueryClient and readers.QueryClient share the
	// identical underlying method signature (both alias dev-health-go/
	// clickhouse's own Binding/RowScanner types), so p.facts.client
	// satisfies readers.QueryClient directly, no adapter needed.
	// "ReadProjectThemeMix" names the reader for attribution -- distinct
	// from "ReadTeamThemeMix" (dev-health-go's own reader for the team
	// subject), never conflated with that reader's own instrumentation.
	// CHAOS-7271: phased reads (investment_project_mix_phased.go); every
	// phase reports through readers.QueryOrgScopedNamed.
	mixRows, readErr := readProjectRollupMixRows(ctx, p.facts.client, orgID, ids, timeBound, p)
	if readErr != nil {
		return 0, readErr
	}
	serve := func(mix projectRollupMixRow) error {
		rowCount++
		if mix.WorkUnits > 0 {
			recordInvestmentProjectSpan(ctx, mix.ProjectKey, mix.FirstUnitFrom)
		}
		projectKey, featureDelivery, operational, maintenance, quality, risk, bugfixWeighted := mix.ProjectKey, mix.FeatureDelivery, mix.Operational, mix.Maintenance, mix.Quality, mix.Risk, mix.BugfixWeighted
		workUnits, repoCount, teamCount, excludedNoRepoLink := mix.WorkUnits, mix.Repos, mix.Teams, mix.ExcludedNoRepoLink
		// The probe row (maxFactRowsProbe = maxFactRowsPerQuery+1) is
		// counted toward rowCount, so ReadFacts' rowCount>maxFactRowsPerQuery
		// check reports Truncated, but it must not itself be served -- an
		// unbounded LIMIT+1 read still needs the SAME served-output cap
		// every other provider here enforces.
		if rowCount > maxFactRowsPerQuery {
			return nil
		}
		subject, ok := bySubject[projectKey]
		if !ok {
			return nil
		}
		currentTotal := featureDelivery + operational + maintenance + quality + risk
		if currentTotal <= 0 {
			unusableRollup[projectKey] = int64(workUnits)
			return nil
		}
		themeValues := map[string]float64{
			contextfabric.ThemeFeatureDelivery: featureDelivery,
			contextfabric.ThemeOperational:     operational,
			contextfabric.ThemeMaintenance:     maintenance,
			contextfabric.ThemeQuality:         quality,
			contextfabric.ThemeRisk:            risk,
		}
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+4)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(themeValues[theme] / currentTotal))
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(bugfixWeighted / currentTotal))
		// rollup_basis names both hops (owning teams, then their owned
		// repos) so a synthesizer never presents this as a project-native
		// attribution computed directly from the project's own work items.
		fields["rollup_basis"] = contextfabric.StringFactValue("team_project_ownership_via_owned_repos_work_unit_investments")
		fields[contextfabric.FactFieldInvestmentMixSource] = contextfabric.StringFactValue(contextfabric.InvestmentMixSourceOwningTeamRollup)
		fields["team_count"] = contextfabric.IntegerFactValue(int64(teamCount))
		fields["repo_count"] = contextfabric.IntegerFactValue(int64(repoCount))
		fields["work_unit_count"] = contextfabric.IntegerFactValue(int64(workUnits))
		// work_units_without_repo_link is the SAME population partition as
		// work_unit_count, not a separate estimate: it counts work units the
		// project's owning teams' own evidence-attribution vote (the SAME
		// mechanism readTeamThemeMix uses for the team subject) assigns to
		// one of those teams, but whose work_unit_investments row carries no
		// repo_id at all -- so this producer's repo-keyed join (this
		// function's own doc comment) never reaches them. counted
		// (work_unit_count) and excluded (work_units_without_repo_link) are
		// disjoint by construction (repo_id IS NOT NULL vs IS NULL on the
		// SAME row) and their sum is this project's disclosed work-unit
		// population for the theme mix -- never silently folded into the
		// counted share. A work unit reachable by neither path (evidence
		// vote assigns it to a DIFFERENT team than any repo-ownership match,
		// a rarer disagreement between the two attribution mechanisms) is
		// outside this roll-up's disclosed partition; not claimed as counted
		// or excluded.
		fields["work_units_without_repo_link"] = contextfabric.IntegerFactValue(int64(excludedNoRepoLink))
		// population_window states, in the fact's own structure, which axis
		// the disclosed population/basis fields above were computed over --
		// this producer supports both current and an explicit requested
		// range (themeInvestmentRangePredicate), unlike the sibling
		// completion roll-up's current-only scope.
		if timeBound.active {
			fields["population_window"] = contextfabric.StringFactValue("requested_range")
		} else {
			fields["population_window"] = contextfabric.StringFactValue("current")
		}

		// Merge onto the project's existing FactInvestment fact when one
		// exists -- see this function's own doc comment for why.
		mergeProjectInvestmentFact(facts, subject, projectKey, fields, nil)
		return nil
	}
	for _, mix := range mixRows {
		if serveErr := serve(mix); serveErr != nil {
			return rowCount, serveErr
		}
	}
	return rowCount, nil
}

// mergeProjectInvestmentFact merges fields onto the project's existing
// FactInvestment fact, or appends a standalone one when none exists. A field
// named in remove is deleted from an existing fact first, so a project keeps
// ONE investment fact rather than a mix fact beside a breakdown fact.
func mergeProjectInvestmentFact(facts *[]contextfabric.CanonicalFact, subject contextfabric.SubjectRef, projectKey string, fields map[string]contextfabric.FactValue, remove []string) {
	targetKey := contextfabric.FactSubjectKey(subject)
	for i := range *facts {
		if (*facts)[i].Kind != contextfabric.FactInvestment || contextfabric.FactSubjectKey((*facts)[i].Subject) != targetKey {
			continue
		}
		for _, field := range remove {
			delete((*facts)[i].Fields, field)
		}
		for field, value := range fields {
			(*facts)[i].Fields[field] = value
		}
		return
	}
	*facts = append(*facts, contextfabric.CanonicalFact{
		Kind: contextfabric.FactInvestment, Subject: subject, Fields: fields,
		EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, projectKey)},
	})
}

// projectNativeMixBasis names the attribution behind a project_native mix:
// the project's own issue evidence, never pull requests and never the owning
// teams' repositories.
const projectNativeMixBasis = "project_work_items_issue_evidence_work_unit_investments"

// readProjectNativeThemeMix attributes FactInvestment's canonical theme
// fields to a project through the project's OWN work items
// (readers.ReadProjectThemeMix), and makes that the project's mix when it has
// any weight. The owning-team roll-up (readProjectThemeMix) stays for a
// project with no native weight; the two are never blended, and the fact says
// which one it carries in investment_mix_source.
//
// A project with native work units but no positive effort keeps the roll-up
// and gains nothing: a zero total is not a mix. When the native mix replaces
// the roll-up, the roll-up's population is not carried as the native
// population: team_count, repo_count and work_units_without_repo_link are
// removed because they describe a different attribution, and the roll-up's
// work_unit_count moves to owning_team_rollup_work_unit_count so both
// populations stay visible.
//
// A work unit that touches several projects counts in full for each;
// spanning_unit_count discloses how many of the project's units do. A work
// item id that project membership places under more than one repository
// counts for each project it is placed in, like a spanning unit;
// native_multi_placed_unit_count discloses how many of the project's units
// were counted through such an id (a data defect shows as a non-zero count),
// on the mix fact when there is a native mix and on the project's fact (the
// roll-up's, or a fact carrying only that count) when there is not.
func (p *InvestmentProvider) readProjectNativeThemeMix(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, unusableRollup map[string]int64) (rowCount int, err error) {
	ids, bySubject, _ := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := readProjectNativeThemeMixRows(ctx, p.facts.client, orgID, ids, timeBound, maxFactRowsProbe, p)
	if err != nil {
		return 0, err
	}
	rowCount = len(rows)
	for i, row := range rows {
		// The probe row is evidence of truncation, never served.
		if i >= maxFactRowsPerQuery {
			break
		}
		subject, ok := bySubject[row.ProjectSubjectKey]
		if !ok {
			continue
		}
		currentTotal := row.FeatureDelivery + row.Operational + row.Maintenance + row.Quality + row.Risk
		if row.EffortUnits == 0 || currentTotal <= 0 {
			// No native mix. A project whose only counted units carry no
			// effort still discloses its multi-placed units.
			if row.MultiPlacedUnits > 0 {
				mergeProjectInvestmentFact(facts, subject, row.ProjectSubjectKey, map[string]contextfabric.FactValue{
					"native_multi_placed_unit_count": contextfabric.IntegerFactValue(int64(row.MultiPlacedUnits)),
				}, nil)
			}
			continue
		}
		themeValues := map[string]float64{
			contextfabric.ThemeFeatureDelivery: row.FeatureDelivery,
			contextfabric.ThemeOperational:     row.Operational,
			contextfabric.ThemeMaintenance:     row.Maintenance,
			contextfabric.ThemeQuality:         row.Quality,
			contextfabric.ThemeRisk:            row.Risk,
		}
		fields := make(map[string]contextfabric.FactValue, 2*len(canonicalInvestmentThemes)+8)
		for _, theme := range canonicalInvestmentThemes {
			fields[contextfabric.FactFieldTheme(theme)] = contextfabric.NumberFactValue(roundMixEffort(themeValues[theme] / currentTotal))
		}
		fields[contextfabric.FactFieldThemeQualityBugfix] = contextfabric.NumberFactValue(roundMixEffort(row.BugfixWeighted / currentTotal))
		fields[contextfabric.FactFieldInvestmentMixSource] = contextfabric.StringFactValue(contextfabric.InvestmentMixSourceProjectNative)
		fields["rollup_basis"] = contextfabric.StringFactValue(projectNativeMixBasis)
		fields["work_unit_count"] = contextfabric.IntegerFactValue(int64(row.WorkUnits))
		fields["effort_unit_count"] = contextfabric.IntegerFactValue(int64(row.EffortUnits))
		fields["spanning_unit_count"] = contextfabric.IntegerFactValue(int64(row.SpanningUnits))
		fields["native_multi_placed_unit_count"] = contextfabric.IntegerFactValue(int64(row.MultiPlacedUnits))
		if timeBound.active {
			fields["population_window"] = contextfabric.StringFactValue("requested_range")
		} else {
			fields["population_window"] = contextfabric.StringFactValue("current")
		}
		// The roll-up's unit population moves aside rather than being
		// overwritten by a count of a different population. A roll-up with no
		// positive effort served no fact, so its population comes from the
		// side channel instead.
		if count, unusable := unusableRollup[row.ProjectSubjectKey]; unusable {
			fields["owning_team_rollup_work_unit_count"] = contextfabric.IntegerFactValue(count)
		}
		targetKey := contextfabric.FactSubjectKey(subject)
		for j := range *facts {
			existing := &(*facts)[j]
			if existing.Kind != contextfabric.FactInvestment || contextfabric.FactSubjectKey(existing.Subject) != targetKey {
				continue
			}
			// Only the roll-up writes work_unit_count onto the project's fact.
			if count, hasCount := existing.Fields["work_unit_count"]; hasCount {
				fields["owning_team_rollup_work_unit_count"] = count
			}
			break
		}
		mergeProjectInvestmentFact(facts, subject, row.ProjectSubjectKey, fields, []string{"team_count", "repo_count", "work_units_without_repo_link"})
	}
	return rowCount, nil
}

// projectSpanKey is the "provider:id" key the project mixes name a project by
// (v2Index's key), empty for a subject that is not a project v2 id.
func projectSpanKey(subject contextfabric.SubjectRef) string {
	if subject.Kind != contextfabric.SubjectProject {
		return ""
	}
	segments, ok := identity.Segments(identity.KindProject, subject.CanonicalID)
	if !ok || len(segments) < 2 {
		return ""
	}
	return segments[0] + ":" + segments[len(segments)-1]
}

// ownershipForFailureNote is teamOwnedOrReachedRepositories over the readers
// this kind's success path uses, so a failed mix read still knows which
// requested teams own no repository.
func (p *InvestmentProvider) ownershipForFailureNote(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (map[string][]teamOwnedRepo, error) {
	owned, err := p.readTeamOwnedRepositories(ctx, orgID, teamIDs, bound)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]teamOwnedRepo, len(owned))
	for teamID, keys := range owned {
		for _, key := range keys {
			out[teamID] = append(out[teamID], teamOwnedRepo{key: key})
		}
	}
	projectTeams, err := teamsOwningAnyProject(ctx, client, orgID, teamIDs)
	if err != nil || len(projectTeams) == 0 {
		return out, err
	}
	reached, err := teamProjectReachedRepositories(ctx, client, orgID, projectTeams, bound)
	if err != nil {
		return nil, err
	}
	for teamID, repos := range reachedOnlyRepositories(reached, out) {
		if len(out[teamID]) == 0 && len(repos) > 0 {
			out[teamID] = []teamOwnedRepo{{key: "reached"}}
		}
	}
	return out, nil
}
