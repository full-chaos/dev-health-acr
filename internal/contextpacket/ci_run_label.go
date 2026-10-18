package contextpacket

import (
	"strconv"
	"strings"
)

// ciRunLabelNameMaxRunes bounds the pipeline name in a CI run's display label.
const ciRunLabelNameMaxRunes = 80

// ciRunLabelSpace is the whitespace the label rule trims from a pipeline name:
// the ASCII set, which ClickHouse's [[:space:]] matches too, so the Go and SQL
// forms of the rule cannot disagree.
const ciRunLabelSpace = " \t\n\v\f\r"

// CIRunLabel is the one rule that names a CI run: "CI <pipeline_name> #<run_id>"
// when a pipeline name is stored, "CI <run_id>" otherwise. When the name's
// first token is already "CI" (any case), the fixed prefix is dropped so the
// word is not doubled. No job name is stored, so none is shown. CIRunLabelSQL is the same rule as a ClickHouse
// expression; both read the constants above.
func CIRunLabel(pipelineName, runID string) string {
	name := strings.Trim(pipelineName, ciRunLabelSpace)
	if runes := []rune(name); len(runes) > ciRunLabelNameMaxRunes {
		name = string(runes[:ciRunLabelNameMaxRunes])
	}
	if name == "" {
		return "CI " + runID
	}
	token := name
	if i := strings.IndexAny(name, ciRunLabelSpace); i >= 0 {
		token = name[:i]
	}
	if strings.EqualFold(token, "CI") {
		return name + " #" + runID
	}
	return "CI " + name + " #" + runID
}

// CIRunLabelSQL returns CIRunLabel as a ClickHouse expression over the given
// pipeline name and run id column expressions.
func CIRunLabelSQL(pipelineNameExpr, runIDExpr string) string {
	trimmed := "replaceRegexpAll(ifNull(" + pipelineNameExpr + ", ''), '^[[:space:]]+|[[:space:]]+$', '')"
	prefixed := "concat('CI ', leftUTF8(" + trimmed + ", " + strconv.Itoa(ciRunLabelNameMaxRunes) + "), ' #', " + runIDExpr + ")"
	bare := "concat(leftUTF8(" + trimmed + ", " + strconv.Itoa(ciRunLabelNameMaxRunes) + "), ' #', " + runIDExpr + ")"
	return "multiIf(lengthUTF8(" + trimmed + ") = 0, concat('CI ', " + runIDExpr + "), match(" + trimmed + ", '^[Cc][Ii]([[:space:]]|$)'), " + bare + ", " + prefixed + ")"
}
