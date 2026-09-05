# Negative-control fixture for scripts/docs/verify-mermaid.sh

Deliberately a MUST-NOT-EXTRACT case, not a must-fail case: CommonMark treats
a tab as 4+ columns of indentation, so a tab-indented fence marker is never
read as a fence at all -- the lines below are an ordinary indented code
block to any real Markdown renderer, never a rendered diagram. The check
must ignore this block entirely (not extract it, not attempt to render it,
not fail on it) -- extracting it would be a false-positive fence match.

	```mermaid
flowchart TB
    B -.->|Sum(weight_contributed) breaks the parser| A
	```
