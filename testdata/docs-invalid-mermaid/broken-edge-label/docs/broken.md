# Negative-control fixture for scripts/docs/verify-mermaid.sh

Deliberately broken: an unquoted flowchart edge label containing a literal
parenthesis, the same defect shape `test-verify-mermaid.sh` proves the real
render check catches (see `context-fabric-architecture-diagrams.md`'s fixed
`Sum(weight_contributed)` edge label for the real-world instance).

```mermaid
flowchart TB
    A["start"] --> B["end"]
    B -.->|Sum(weight_contributed) breaks the parser| A
```
