package devhealthfacts

import "strings"

func ProbeRepoMixStatement(limit int) string {
	return repoMixStatementScoped([]factTimeBound{{}}, membershipScope{mode: membershipScopeNone}, limit)
}

func ProbeRepoSplitCore() string {
	return repoSplitCoreScoped([]string{"if(1, 0, -1)"}, "", "", true, membershipScope{mode: membershipScopeNone})
}

func ProbeRepoSplitParsed() string {
	core := ProbeRepoSplitCore()
	start := strings.Index(core, "FROM (") + len("FROM (")
	end := strings.Index(core, ") AS parsed")
	return core[start:end]
}
