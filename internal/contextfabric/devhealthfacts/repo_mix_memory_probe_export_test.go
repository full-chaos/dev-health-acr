package devhealthfacts

func ProbeRepoMixStatement(limit int) string {
	return repoMixStatementScoped([]factTimeBound{{}}, membershipScope{mode: membershipScopeNone}, limit)
}

func ProbeRepoSplitCore() string {
	return repoSplitCoreScoped([]string{"if(1, 0, -1)"}, "", "", true, membershipScope{mode: membershipScopeNone})
}
