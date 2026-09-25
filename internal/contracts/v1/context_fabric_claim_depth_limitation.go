package v1

import (
	"regexp"
	"strconv"
)

// CHAOS-6743: the claim-depth disclosure.
//
// WHAT IT CLOSES. An answer about a discovered cohort overran the ITEM budget
// because each member carried several claims, and stage 3's only item lever
// halved the cohort: "Which repositories does this organization have?" served
// 5 of its 11 repositories on prod. Members are the answer to such a question;
// per-member claims are depth. The allocation is now members before claims:
// claims are cut per member first, and this sentence says so -- from how many
// claims to how many, over how many subjects, and at what cap.
//
// Composed and recognised exactly as the fact-row truncation sentence beside
// it: a strict parse of a fixed grammar with bounded canonical integers, then
// a re-composition compared for equality.

const (
	contextFabricClaimDepthPrefix   = "This answer shows "
	contextFabricClaimDepthOf       = " of the "
	contextFabricClaimDepthAbout    = " facts it claimed about its "
	contextFabricClaimDepthSubjects = " subjects, because the assembled answer exceeded the item budget: each subject keeps at most "
	contextFabricClaimDepthFact     = " fact"
	contextFabricClaimDepthFacts    = " facts"
	contextFabricClaimDepthSuffix   = " (every fact the answer cites is kept, and a subject's other facts are kept in the order they were claimed). Ask a narrower question or allow a larger response budget to see the rest."
	contextFabricClaimDepthNumber   = `(0|[1-9][0-9]{0,8})`
	contextFabricClaimDepthMaxCount = 999999999
)

// ContextFabricClaimDepthLimitation composes the disclosure. THE SOLE
// COMPOSER. served and declared count the claims about cohort members before
// and after the cut; subjects is the member count; perMember is the cap. The
// second return is false unless the numbers describe a real cut.
func ContextFabricClaimDepthLimitation(served, declared, subjects, perMember int) (string, bool) {
	if !contextFabricClaimDepthValid(served, declared, subjects, perMember) {
		return "", false
	}
	unit := contextFabricClaimDepthFacts
	if perMember == 1 {
		unit = contextFabricClaimDepthFact
	}
	return contextFabricClaimDepthPrefix + strconv.Itoa(served) +
		contextFabricClaimDepthOf + strconv.Itoa(declared) +
		contextFabricClaimDepthAbout + strconv.Itoa(subjects) +
		contextFabricClaimDepthSubjects + strconv.Itoa(perMember) + unit +
		contextFabricClaimDepthSuffix, true
}

func contextFabricClaimDepthValid(served, declared, subjects, perMember int) bool {
	for _, value := range []int{served, declared, subjects, perMember} {
		if value < 0 || value > contextFabricClaimDepthMaxCount {
			return false
		}
	}
	return perMember >= 1 && subjects >= 1 && served < declared
}

var contextFabricClaimDepthPattern = func() *regexp.Regexp {
	q := regexp.QuoteMeta
	number := contextFabricClaimDepthNumber
	return regexp.MustCompile(`^` + q(contextFabricClaimDepthPrefix) + number +
		q(contextFabricClaimDepthOf) + number +
		q(contextFabricClaimDepthAbout) + number +
		q(contextFabricClaimDepthSubjects) + number +
		`(` + q(contextFabricClaimDepthFacts) + `|` + q(contextFabricClaimDepthFact) + `)` +
		q(contextFabricClaimDepthSuffix) + `$`)
}()

// IsContextFabricClaimDepthLimitation reports whether a limitation is one
// ContextFabricClaimDepthLimitation could have composed.
func IsContextFabricClaimDepthLimitation(limitation string) bool {
	match := contextFabricClaimDepthPattern.FindStringSubmatch(limitation)
	if match == nil {
		return false
	}
	values := make([]int, 4)
	for index := range values {
		value, err := strconv.Atoi(match[index+1])
		if err != nil {
			return false
		}
		values[index] = value
	}
	recomposed, ok := ContextFabricClaimDepthLimitation(values[0], values[1], values[2], values[3])
	return ok && recomposed == limitation
}
