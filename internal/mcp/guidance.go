package mcp

// Guidance sentences shared by every surface that tells a client how teams,
// projects, repositories and investment relate. The server instructions, the
// generated guide and the catalog notes read these; the tool descriptions in
// contracts/mcp/tools.v1.json are literal JSON and a test pins that they carry
// the same sentences.
const (
	GuidanceTeamOwnership = "A team owns repositories and projects: team ownership = OWNED_BY_TEAM edges from both kinds to the team; for a team ownership question read `read_relationships` on the team with types OWNED_BY_TEAM and direction in (or `find_subjects` `owned_by`) first. Archived projects stay listed as owned; a team project cohort in an investigation answer flags them archived."

	GuidanceProjectRepository = "A project reaches repositories only through its issues' linked pull requests; no repository is mapped to a team by a project directly."

	GuidanceHomeRunOperation = "`home` is served by run_operation only, by design."

	GuidanceInvestmentScopes = "Per-team, per-repository, per-project and organization investment is served by `read_facts` kind `investment`; the organization subject is kind `organization` with canonical_id `organization:<your organization id>`: one fact over every repository once (never a sum of team facts), and a repository-bound credential gets a limitation instead. `investmentBreakdown` serves the organization only. `read_facts` kind `investment` takes a trailing window of up to 365 days in one read (window {mode: trailing, days: N}; other kinds stay at 60); read one long window, do not add up shorter ones: a work unit that spans a window boundary counts in each of them, so a sum of stitched windows overcounts it. A window that starts before the earliest stored work unit is served over the available span and says so in the coverage reason."
)
