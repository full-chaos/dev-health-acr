package contextfabric

// BlockerStatusParameter is the one fact parameter a blockers requirement
// carries when the question's frame names a work-item status: the team
// blockers rollup then counts blockers in that status instead of open ones.
const BlockerStatusParameter = "blocker_status"

// withBlockerStatusQualifier copies the frame's status qualifier onto every
// blockers requirement that does not already carry one. Only a qualifier whose
// value is in the closed work-item status set is carried; anything else leaves
// the default (open blockers) untouched.
func withBlockerStatusQualifier(requirements []FactRequirement, frame *QuestionFrame) []FactRequirement {
	if frame == nil || frame.SubjectExpression.Scoped == nil {
		return requirements
	}
	scoped := frame.SubjectExpression.Scoped
	if scoped.MemberQualifier != MemberQualifierStatus || !InWorkItemStatusVocabulary(scoped.MemberQualifierValue) {
		return requirements
	}
	out := make([]FactRequirement, len(requirements))
	copy(out, requirements)
	for i, requirement := range out {
		if requirement.Kind != FactBlockers || requirement.Parameters[BlockerStatusParameter] != "" {
			continue
		}
		parameters := make(map[string]string, len(requirement.Parameters)+1)
		for key, value := range requirement.Parameters {
			parameters[key] = value
		}
		parameters[BlockerStatusParameter] = scoped.MemberQualifierValue
		out[i].Parameters = parameters
	}
	return out
}
