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

	GuidanceInvestmentScopes = "Per-team, per-repository and per-project investment is served by `read_facts` kind `investment`; `investmentBreakdown` serves the organization only."
)
