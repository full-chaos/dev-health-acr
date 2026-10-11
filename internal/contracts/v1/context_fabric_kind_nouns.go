package v1

// contextFabricSubjectKindNouns is the one source of the words a served
// sentence uses for a subject kind: the wire token (a snake_case identifier)
// never reaches prose a reader sees. Every kind in contextFabricSubjectKinds
// has an entry; a test pins that, and that no two nouns collide, so a sentence
// that interpolates a noun can be parsed back to exactly one kind.
var contextFabricSubjectKindNouns = map[ContextFabricSubjectKind][2]string{
	ContextFabricSubjectOrganization:      {"organization", "organizations"},
	ContextFabricSubjectTeam:              {"team", "teams"},
	ContextFabricSubjectProject:           {"project", "projects"},
	ContextFabricSubjectRepository:        {"repository", "repositories"},
	ContextFabricSubjectWorkItem:          {"work item", "work items"},
	ContextFabricSubjectPullRequest:       {"pull request", "pull requests"},
	ContextFabricSubjectDeployment:        {"deployment", "deployments"},
	ContextFabricSubjectIncident:          {"incident", "incidents"},
	ContextFabricSubjectDocument:          {"document", "documents"},
	ContextFabricSubjectDecision:          {"decision", "decisions"},
	ContextFabricSubjectEpisode:           {"episode", "episodes"},
	ContextFabricSubjectMetric:            {"metric", "metrics"},
	ContextFabricSubjectPullRequestReview: {"pull request review", "pull request reviews"},
	ContextFabricSubjectCIRun:             {"CI pipeline run", "CI pipeline runs"},
	ContextFabricSubjectWorkItemRef:       {"work item reference", "work item references"},
}

// ContextFabricSubjectKindNoun is the display noun for a kind: singular when
// count is 1, plural otherwise. A kind outside the vocabulary yields "".
func ContextFabricSubjectKindNoun(kind ContextFabricSubjectKind, count int) string {
	nouns, ok := contextFabricSubjectKindNouns[kind]
	if !ok {
		return ""
	}
	if count == 1 {
		return nouns[0]
	}
	return nouns[1]
}

// contextFabricSubjectKindFromNoun inverts ContextFabricSubjectKindNoun for the
// same count form, so a sentence parse recovers a vocabulary member or nothing.
func contextFabricSubjectKindFromNoun(noun string, count int) (ContextFabricSubjectKind, bool) {
	if noun == "" {
		return "", false
	}
	for kind := range contextFabricSubjectKindNouns {
		if ContextFabricSubjectKindNoun(kind, count) == noun {
			return kind, true
		}
	}
	return "", false
}
