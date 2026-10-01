package factoracle

import (
	"fmt"
	"sort"
	"strings"
)

// Table renders the per-root outcome of a run as a Markdown table.
func (r *Report) Table() string {
	var b strings.Builder
	b.WriteString("| root | mode | listener | shapes run | leaves | compared | matches | differences by class | findings |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, rr := range r.Roots {
		classes := make([]string, 0, len(rr.ByClass))
		for class, n := range rr.ByClass {
			classes = append(classes, fmt.Sprintf("%s=%d", class, n))
		}
		sort.Strings(classes)
		byClass := strings.Join(classes, " ")
		if byClass == "" {
			byClass = "none"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %d | %s | %d |\n",
			rr.Root, rr.Mode, rr.Listener, rr.ShapesRun, rr.Leaves, rr.Compared, rr.Matches, byClass, len(rr.Findings))
	}
	return b.String()
}

// Details lists the differences, the findings and the unjoined subjects.
func (r *Report) Details() string {
	var b strings.Builder
	for _, rr := range r.Roots {
		if len(rr.Residual) > 0 {
			fmt.Fprintf(&b, "RESIDUAL %s (ops organization value minus the acr repository sum): %s\n", rr.Root, formatThemes(rr.Residual))
		}
		for _, d := range rr.Differences {
			exact := "bounded"
			if d.Exact {
				exact = "exact"
			}
			fmt.Fprintf(&b, "DIFFERENCE %s %s [%s] class=%s (%s): %s", rr.Root, d.Pair, d.Key, d.Class, exact, d.Detail)
			if len(d.Values) > 0 {
				fmt.Fprintf(&b, " {%s}", formatThemes(d.Values))
			}
			b.WriteString("\n")
		}
		for _, f := range rr.Findings {
			fmt.Fprintf(&b, "FINDING %s %s [%s] %s: %s\n", rr.Root, f.Pair, f.Key, f.Path, f.Detail)
		}
		for _, n := range rr.NotJoined {
			fmt.Fprintf(&b, "NOT-JOINED %s: %s\n", rr.Root, n)
		}
	}
	return b.String()
}
