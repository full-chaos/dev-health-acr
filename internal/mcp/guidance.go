package mcp

// Guidance sentences shared by every surface that tells a client how teams,
// projects, repositories and investment relate. The server instructions, the
// generated guide and the catalog notes read these; the tool descriptions in
// contracts/mcp/tools.v1.json are literal JSON and a test pins that they carry
// the same sentences.
const (
	GuidanceTeamOwnership = "A team owns repositories and projects: team ownership = OWNED_BY_TEAM edges from both kinds to the team; for a team ownership question read `read_relationships` (when listed) on the team with types OWNED_BY_TEAM and direction in (or `find_subjects` (when listed) `owned_by`) first. Archived projects stay listed as owned; a team project cohort in an investigation answer flags them archived."

	GuidanceProjectRepository = "A project reaches repositories only through its issues' linked pull requests; no repository is mapped to a team by a project directly."

	GuidanceHomeRunOperation = "`home` is served by run_operation (when listed) only, by design."

	GuidanceInvestmentScopes = "Per-team, per-repository, per-project and organization investment is served by `read_facts` (when listed) kind `investment`; the organization subject is kind `organization` with canonical_id `organization:<your organization id>` (`find_subjects` (when listed) kind `organization` returns it): one fact over every repository once (never a sum of team facts), and a repository-bound credential gets a limitation instead. `investmentBreakdown` serves the organization only. Read the widest window one read takes for a kind from `data_catalog` (when listed; `facts.kinds[].max_window_days`) before choosing `days` (window {mode: trailing, days: N}); a read over several kinds is held to the smallest. Investment windows are additive: a work unit counts once, in the window that holds its end stamp (a unit that spans a window boundary counts whole in the window it ends in), so stitched windows add up to the whole period. A window that starts before the earliest stored work unit is served over the available span and says so in the coverage reason."
)
