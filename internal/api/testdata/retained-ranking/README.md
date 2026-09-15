These whole stored-result snapshots were emitted by Engine.Investigate,
RankCohort and the memory codec at fc75581e377aaef8f7807a37f819272998b484df.
The fixture uses the real capability registry and controlled graph/fact/model
adapters in captureRetainedRankingFromProduction. No result fields were edited.

The qualified snapshot SHA256 is
aac27419ef7490cbc87c823a06ce2ebe1b4dedf193161ea1ddad930b2a4a0dcf;
the insufficient-evidence snapshot SHA256 is
d8a2b5022641a30bdd04de95e894c2fa36ba92338bc234a622a7a6c031f675f4.
Both original results are degraded and lack assembled ranking accounting.
Current controls execute the producer rather than treating these old versions
as newly produced reusable results.

The capped snapshot additionally invokes graphrank.DiscoveredCohort through
the engine graph adapter: three authorized candidate nodes, two retained
members, complete=false and truncated=true. Its SHA256 is
8c3308c58ab0d79fc987d5aca73da9ae59890b9cd3672d128900783e3ed6c999.

The zero-signal snapshot uses an empty fact bundle with the operational-
deficiency batch pruned. Actual RankCohort emits not_applicable with no score;
fresh accounting reports unavailable. The fc capture SHA256 is
7b59258a5c43cef25828c9bc8c42a36b5b1ffc6183460ce023bd8d3e24e343ac.
