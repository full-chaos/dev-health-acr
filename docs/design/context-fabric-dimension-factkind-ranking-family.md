# Context Fabric: dimension ↔ FactKind ↔ ranking-family mapping (CHAOS-4468)

CHAOS-4468 asked for a mapping between the nine canonical
[`HealthDimension`](../../internal/contextfabric/chaos4632_question_family_registry.go)
values, the 21 registered `FactKind`s, and `cohort_ranking.go`'s five
`RankingSignal*` ranking families. Before CHAOS-4468 this mapping existed
nowhere: North Star §19 says so itself ("No such mapping exists as of
2026-08-28").

**This table is GENERATED, not hand-maintained.** Every row comes straight
from `FactCapability.Dimension` (CHAOS-4633) as declared on the live
provider registry (`devhealthfacts.NewProviders`), joined against
`cohort_ranking.go`'s five closed `RankingSignal*` constants. There is no
second, independently-authored copy of this mapping for either half to
drift from.

`internal/contextfabric/devhealthfacts/chaos4633_dimension_mapping_test.go`
(`TestGeneratedDimensionFactKindRankingFamilyTableMatchesDoc`) asserts this
file's table section is byte-for-byte what
`contextfabric.GenerateDimensionFactKindRankingFamilyTable` produces off the
current registry. A registry change that is not reflected here fails CI.

To regenerate after a registry change:

```bash
go run ./cmd/gendimtable > /tmp/table.md   # paste the table body back in below
```

(`cmd/gendimtable` is a throwaway generator kept only for this
regeneration step -- it is not a shipped binary.)

`ranking family` is `—` for every `FactKind` that does not feed
`RankCohort`'s five-signal formula (`cohort_ranking.go`'s
`weightInvestmentMix`/`weightHealthRisk`/`weightDeficiencySeverity`/
`weightReadinessGap`/`weightWorkloadPressure`) -- most `FactKind`s
contribute evidence to an answer without ever feeding the ranking formula,
and `—` says so honestly rather than leaving a placeholder.

| dimension | fact kind | capability | ranking family |
|---|---|---|---|
| execution_completion | actual_completion | devhealthfacts.actual_completion | — |
| dependencies_and_blockers | blockers | devhealthfacts.blockers | — |
| review_and_ci_pressure | continuous_integration | devhealthfacts.continuous_integration | — |
| reliability_and_release | deployments | devhealthfacts.deployments | — |
| delivery_flow | flow | devhealthfacts.flow | — |
| code_ownership_risk | health | devhealthfacts.health | health.compounding_risk |
| data_trust | identity | devhealthfacts.identity | — |
| reliability_and_release | incidents | devhealthfacts.incidents | — |
| investment_balance | investment | devhealthfacts.investment | investment_mix |
| cognitive_workload_pressure | landscape | devhealthfacts.landscape | — |
| data_trust | membership | devhealthfacts.membership | — |
| delivery_flow | metrics | devhealthfacts.metrics | — |
| cognitive_workload_pressure | operational_deficiencies | devhealthfacts.operational_deficiencies | operational_deficiencies.severity |
| review_and_ci_pressure | pull_requests | devhealthfacts.pull_requests | — |
| execution_completion | readiness | devhealthfacts.readiness | readiness.coverage_gap |
| dependencies_and_blockers | required_children | devhealthfacts.required_children | — |
| review_and_ci_pressure | reviews | devhealthfacts.reviews | — |
| data_trust | source_health | devhealthfacts.source_health | — |
| execution_completion | status | devhealthfacts.status | — |
| execution_completion | work | devhealthfacts.work | — |
| cognitive_workload_pressure | workload | devhealthfacts.workload | workload.forecast_pressure |
